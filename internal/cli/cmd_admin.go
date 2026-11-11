package cli

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"time"

	"github.com/hackerick4/botobot/internal/prompts"
	"github.com/hackerick4/botobot/internal/protocol"
)

func init() {
	commands["kick"] = cmdKick
	commands["leave"] = cmdLeave
	commands["close"] = cmdClose
	commands["rekey"] = cmdRekey
	commands["rotate-key"] = cmdRotateKey
	commands["status"] = cmdStatus
}

// cmdKick implements `kick HANDLE` (admin) (DEV_SPEC §12.3): resolve the handle
// to a land id via the refreshed land set and remove it.
func cmdKick(a *app, args []string) *cliError {
	g, fs, e := a.parse("kick", args, nil)
	if e != nil {
		return e
	}
	handle := fs.Arg(0)
	if handle == "" {
		return usageErrorf("kick: HANDLE is required")
	}
	sess, e := a.newSession(g)
	if e != nil {
		return e
	}
	bs, e := sess.resolveBridge()
	if e != nil {
		return e
	}
	if bs.isPair() {
		return useCloseErr(bs.bc.BridgeID)
	}
	lc, e := bs.refreshLands(context.Background(), sess.ks)
	if e != nil {
		return e
	}
	target := lc.findByHandle(handle)
	if target == nil {
		return usageErrorf("kick: no land with handle %q", handle)
	}
	var resp removedResp
	if ce := bs.cli.do(context.Background(), http.MethodDelete,
		"/v1/bridges/"+bs.bc.BridgeID+"/lands/"+target.LandID, nil, &resp); ce != nil {
		return ce
	}
	if resp.BridgeClosed {
		// A two-bot bridge closes with the peer gone (v0.5.0): this side goes
		// quiet like after its own close.
		sess.markEndedSelf(bs.bc.BridgeID)
	}
	sess.pr.emit(map[string]any{"type": "kicked", "land_id": resp.Removed, "bridge_closed": resp.BridgeClosed})
	if resp.BridgeClosed {
		sess.pr.raw(removalNext(bs.bc.BridgeID, sess.cliCmd()))
	}
	return sess.pr.done()
}

// cmdLeave implements `leave [--yes]` (DEV_SPEC §12.3): the land leaves the
// bridge. An admin leaving closes the bridge, so it is refused without --yes.
func cmdLeave(a *app, args []string) *cliError {
	var yes bool
	g, _, e := a.parse("leave", args, func(fs *flag.FlagSet) {
		fs.BoolVar(&yes, "yes", false, "confirm (required for an admin, which closes the bridge)")
		fs.Bool("owner-picked", false, "the owner picked this bridge on botobot.app/delete (no extra question)")
	})
	if e != nil {
		return e
	}
	sess, e := a.newSession(g)
	if e != nil {
		return e
	}
	if g.bridge == "" {
		return requireExplicitBridge("leave", nil)
	}
	bs, e := sess.resolveBridge()
	if e != nil {
		return e
	}
	if bs.bc.Role == "admin" && !yes {
		return usageErrorf("leave: you are the admin; leaving closes the bridge. Re-run with --yes to confirm")
	}
	var resp removedResp
	if ce := bs.cli.do(context.Background(), http.MethodDelete,
		"/v1/bridges/"+bs.bc.BridgeID+"/lands/"+bs.bc.LandID, nil, &resp); ce != nil {
		if sess.alreadyClosed(bs, ce) {
			return sess.pr.done()
		}
		return ce
	}
	sess.pr.emit(sess.withReleaseConfirmed(resp.closeOutcome.addTo(map[string]any{"type": "left", "land_id": resp.Removed, "bridge_closed": resp.BridgeClosed}), "離開"))
	sess.undeliveredNotice(resp.closeOutcome, bs.bc.Lang)
	if bs.bc.EntryID != "" {
		sess.guestLineClosed(bs.bc)
		return sess.pr.done()
	}
	sess.pr.raw(pairEndedNotice(bs.bc, resp.BridgeClosed))
	sess.markEndedSelf(bs.bc.BridgeID)
	sess.pr.raw(removalNext(bs.bc.BridgeID, sess.cliCmd()))
	return sess.pr.done()
}

// cmdClose implements `close --yes` (admin) (DEV_SPEC §12.3): immediately close
// the bridge.
func cmdClose(a *app, args []string) *cliError {
	var yes bool
	g, _, e := a.parse("close", args, func(fs *flag.FlagSet) {
		fs.BoolVar(&yes, "yes", false, "confirm closing the bridge (required)")
		fs.Bool("owner-picked", false, "the owner picked this bridge on botobot.app/delete (no extra question)")
	})
	if e != nil {
		return e
	}
	if !yes {
		return usageErrorf("close: this permanently closes the bridge. Re-run with --yes to confirm")
	}
	if g.bridge == "" {
		return requireExplicitBridge("close", nil)
	}
	sess, e := a.newSession(g)
	if e != nil {
		return e
	}
	bs, e := sess.resolveBridge()
	if e != nil {
		return e
	}
	var resp struct {
		Closed bool `json:"closed"`
		closeOutcome
	}
	if ce := bs.cli.do(context.Background(), http.MethodDelete, "/v1/bridges/"+bs.bc.BridgeID, nil, &resp); ce != nil {
		if sess.alreadyClosed(bs, ce) {
			return sess.pr.done()
		}
		return ce
	}
	sess.pr.emit(sess.withReleaseConfirmed(resp.closeOutcome.addTo(map[string]any{"type": "closed"}), "關閉"))
	sess.undeliveredNotice(resp.closeOutcome, bs.bc.Lang)
	if bs.bc.EntryID != "" {
		sess.guestLineClosed(bs.bc)
		return sess.pr.done()
	}
	sess.pr.raw(pairEndedNotice(bs.bc, true))
	sess.markEndedSelf(bs.bc.BridgeID)
	sess.pr.raw(removalNext(bs.bc.BridgeID, sess.cliCmd()))
	return sess.pr.done()
}

// cmdRekey implements `rekey` (DEV_SPEC §12.3): generate a new keypair, publish
// it, and keep the old private key for 24h so in-flight messages still decrypt.
func cmdRekey(a *app, args []string) *cliError {
	g, _, e := a.parse("rekey", args, nil)
	if e != nil {
		return e
	}
	sess, e := a.newSession(g)
	if e != nil {
		return e
	}
	bs, e := sess.resolveBridge()
	if e != nil {
		return e
	}
	if bs.bc.EntryID != "" {
		return entryRefuse("rekey")
	}
	km, err := sess.ks.loadKeys(bs.bc.BridgeID)
	if err != nil {
		return generalErrorf("read keys: %v", err)
	}

	encPriv, encPub, signPriv, signPub, bindingSig, err := newKeypair()
	if err != nil {
		return generalErrorf("%v", err)
	}
	body := keyInputBody{EncPub: encPub, SignPub: signPub, BindingSig: bindingSig}
	var resp landResp
	if ce := bs.cli.do(context.Background(), http.MethodPut, "/v1/me/public-key", body, &resp); ce != nil {
		return ce
	}

	// Retain the superseded key for the 24h decrypt window, then install the new
	// one (DEV_SPEC §6.4 PUT public-key, §12.3 rekey).
	nowMS := sess.app.now().UnixMilli()
	if km.KeyID != "" {
		km.Previous = append(km.Previous, retiredKey{
			EncPriv:   km.EncPriv,
			SignPriv:  km.SignPriv,
			KeyID:     km.KeyID,
			CreatedAt: km.CreatedAt,
			RetiredAt: nowMS,
		})
	}
	km.EncPriv = encPriv
	km.SignPriv = signPriv
	km.KeyID = resp.Land.KeyID
	km.CreatedAt = nowMS
	km.pruneRetained(nowMS)
	if err := sess.ks.saveKeys(bs.bc.BridgeID, km); err != nil {
		return generalErrorf("save keys: %v", err)
	}

	sess.pr.emit(map[string]any{
		"type":        "rekeyed",
		"key_id":      resp.Land.KeyID,
		"fingerprint": protocol.Fingerprint(signPub, encPub),
	})
	return sess.pr.done()
}

// cmdRotateKey implements `rotate-key` (DEV_SPEC §12.3): issue a new API key
// (revoking the others) and store it.
func cmdRotateKey(a *app, args []string) *cliError {
	g, _, e := a.parse("rotate-key", args, nil)
	if e != nil {
		return e
	}
	sess, e := a.newSession(g)
	if e != nil {
		return e
	}
	bs, e := sess.resolveBridge()
	if e != nil {
		return e
	}
	if bs.bc.EntryID != "" {
		return entryRefuse("rotate-key")
	}
	var resp apiKeyResp
	if ce := bs.cli.do(context.Background(), http.MethodPost, "/v1/me/api-keys",
		map[string]any{"revoke_others": true}, &resp); ce != nil {
		return ce
	}
	if err := sess.ks.saveAPIKey(bs.bc.BridgeID, resp.APIKey); err != nil {
		return generalErrorf("save api key: %v", err)
	}
	sess.pr.emit(map[string]any{"type": "rotated", "key_id": resp.KeyID})
	return sess.pr.done()
}

// cmdStatus implements `status --bridge ID [MSG_ID]` (DEV_SPEC §12.3). With
// MSG_ID: per-recipient delivery status of a message this land sent. Without:
// this bot's state on the bridge (read-only; it never changes trust or pins).
func cmdStatus(a *app, args []string) *cliError {
	g, fs, e := a.parse("status", args, nil)
	if e != nil {
		return e
	}
	msgID := fs.Arg(0)
	sess, e := a.newSession(g)
	if e != nil {
		return e
	}
	bs, e := sess.resolveBridge()
	if e != nil {
		return e
	}
	if msgID == "" {
		return sess.bridgeStatus(bs)
	}
	var resp statusResp
	if ce := bs.cli.do(context.Background(), http.MethodGet,
		"/v1/bridges/"+bs.bc.BridgeID+"/messages/"+msgID+"/status", nil, &resp); ce != nil {
		return ce
	}
	sess.pr.emit(map[string]any{
		"type":       "status",
		"msg_id":     resp.MsgID,
		"deliveries": resp.Deliveries,
	})
	return sess.pr.done()
}

// removalNext is the follow-up printed after leave/close (removal manual at
// /delete, steps 4 and 5). Since v0.5.1 one routine serves all of a bot's
// bridges, so forget (which knows how many remain) says what to do with it.
// alreadyClosed (v0.6.8, round-15 finding): close or leave on a two-bot
// bridge the other side already closed is not an error. It prints that there
// is nothing left to do and points to forget, and reports true; any other
// error (or a public-entry line) reports false.
func (s *session) alreadyClosed(bs *bridgeState, ce *cliError) bool {
	if ce == nil || ce.Code != "bridge_closed" || bs.bc.EntryID != "" {
		return false
	}
	s.pr.emit(map[string]any{"type": "already_closed", "bridge_id": bs.bc.BridgeID})
	s.pr.raw(alreadyClosedNotice(bs.bc, s.endedBySelf(bs.bc.BridgeID)))
	s.pr.raw(removalNext(bs.bc.BridgeID, s.cliCmd()))
	return true
}

// endedBySelf reports whether this bot itself closed or left the bridge
// earlier (so the notice does not blame the other side).
func (s *session) endedBySelf(bid string) bool {
	ts, err := s.ks.loadTickState(bid)
	return err == nil && ts.Ended == endedSelf
}

func alreadyClosedNotice(bc *bridgeConfig, self bool) string {
	n := displayName(bc)
	if n == "" {
		n = bc.BridgeID
	}
	en := prompts.IsEnglish(bc.Lang)
	if self {
		return "NOTIFY_OWNER: " + pick(en, "\""+n+"\" is already closed. Nothing more to do.", "「"+n+"」已經關閉，不必再做任何事。")
	}
	return "NOTIFY_OWNER: " + pick(en, "The other side already closed \""+n+"\". Nothing more to do.", "對方已經關閉「"+n+"」，不必再做任何事。")
}

func removalNext(bridgeID, cli string) string {
	return "NEXT: 執行 " + cli + " forget --bridge " + bridgeID + " --yes 刪除本機金鑰；forget 會說明 routine 是否要刪。"
}

// bridgeStatus prints this bot's state on one bridge: open/left/closed, role,
// handle, own fingerprint, members, who is not trusted (and why), held items
// for this bridge and the poll interval. It only reads local pins; trust
// decisions stay with tick/send.
func (s *session) bridgeStatus(bs *bridgeState) *cliError {
	bc := bs.bc
	out := s.describeBridge(bc, false)
	out["type"] = "bridge_status"
	out["role"] = bc.Role
	out["handle"] = bc.Handle
	out["land_id"] = bc.LandID
	if lc, err := s.ks.loadLands(bc.BridgeID); err == nil && lc != nil {
		if me := lc.findByID(bc.LandID); me != nil {
			out["fingerprint"] = me.Fingerprint
		}
		if ps, err := s.ks.loadPins(bc.BridgeID); err == nil && ps != nil {
			waiting := []string{}
			for i := range lc.Lands {
				l := &lc.Lands[i]
				if l.LandID == bc.LandID {
					continue
				}
				if !ps.isTrusted(l) {
					waiting = append(waiting, l.Handle)
				}
			}
			trust := "ok"
			switch {
			case ps.Locked != "":
				trust = "locked: " + ps.Locked
			case ps.ExpectAdminFP != "":
				trust = "checking_admin"
			case len(waiting) > 0:
				trust = "paused"
			}
			out["trust"] = trust
			out["not_trusted"] = waiting
		}
	}
	held := 0
	for _, it := range s.ks.listHeld(s.app.now().UnixMilli()) {
		if it.Bridge == bc.BridgeID {
			held++
		}
	}
	out["held"] = held
	if out["status"] == "open" {
		var me landResp
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		if bs.cli.do(ctx, http.MethodGet, "/v1/me", nil, &me) == nil {
			out["poll_interval_sec"] = me.Land.PollIntervalSec
		}
		cancel()
	}
	s.pr.emit(out)
	return s.pr.done()
}

// pairEndedNotice is the owner line right after this bot closed or left a
// bridge that is not a public-entry line (v0.6.5; entry lines print
// guestLineClosed's line).
func pairEndedNotice(bc *bridgeConfig, closed bool) string {
	n := displayName(bc)
	if n == "" {
		n = bc.BridgeID
	}
	en := prompts.IsEnglish(bc.Lang)
	if closed {
		return "NOTIFY_OWNER: " + pick(en,
			"\""+n+"\" is closed. Neither side can send messages on it any more. Other lines are not affected.",
			"「"+n+"」已關閉，雙方都無法再傳訊息。其他連線不受影響。")
	}
	return "NOTIFY_OWNER: " + pick(en,
		"Left \""+n+"\". Other lines are not affected.",
		"已離開「"+n+"」，其他連線不受影響。")
}

// withReleaseConfirmed keeps the release time and confirmation code in the
// bot-facing JSON when this run is an owner-approved release (v0.6.5: the
// release is reported at once, so forget no longer carries it).
func (s *session) withReleaseConfirmed(line map[string]any, what string) map[string]any {
	if it := s.app.releasingItem; it != nil {
		line["release_confirmed"] = fmt.Sprintf("%s已於 %s 確認，確認碼 %s", what, s.app.now().Format("01/02 15:04"), shortHash(it.Hash))
	}
	return line
}
