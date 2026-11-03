package cli

import (
	"github.com/hackerick4/botobot/internal/update"

	"context"
	"flag"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/hackerick4/botobot/internal/prompts"
	"github.com/hackerick4/botobot/internal/protocol"
)

func init() {
	commands["lands"] = cmdLands
	commands["fingerprint"] = cmdFingerprint
	commands["invite-code"] = cmdInviteCode
	commands["config"] = cmdConfig
	commands["ping"] = cmdPing
	commands["version"] = cmdVersion
	commands["bridges"] = cmdBridges
}

// cmdLands implements `lands` (DEV_SPEC §12.3): refresh the land set and print
// one line per land.
func cmdLands(a *app, args []string) *cliError {
	g, _, e := a.parse("lands", args, nil)
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
	lc, e := bs.refreshLands(context.Background(), sess.ks)
	if e != nil {
		return e
	}
	for _, l := range lc.Lands {
		sess.pr.emit(map[string]any{
			"type":        "land",
			"land_id":     l.LandID,
			"handle":      l.Handle,
			"role":        l.Role,
			"fingerprint": l.Fingerprint,
			"owner_name":  l.OwnerName,
		})
	}
	return sess.pr.done()
}

// cmdFingerprint implements `fingerprint [HANDLE]` (DEV_SPEC §12.3): the
// fingerprint of this land (default) or of the named land, from the refreshed
// land set.
func cmdFingerprint(a *app, args []string) *cliError {
	g, fs, e := a.parse("fingerprint", args, nil)
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
	lc, e := bs.refreshLands(context.Background(), sess.ks)
	if e != nil {
		return e
	}

	var target *cachedLand
	handle := fs.Arg(0)
	if handle == "" {
		target = lc.findByID(bs.bc.LandID)
	} else {
		target = lc.findByHandle(handle)
	}
	if target == nil {
		return generalErrorf("fingerprint: land not found")
	}
	sess.pr.emit(map[string]any{
		"type":        "fingerprint",
		"land_id":     target.LandID,
		"handle":      target.Handle,
		"fingerprint": target.Fingerprint,
	})
	return sess.pr.done()
}

// cmdInviteCode implements `invite-code [--rotate]` (DEV_SPEC §12.3).
func cmdInviteCode(a *app, args []string) *cliError {
	var rotate bool
	g, _, e := a.parse("invite-code", args, func(fs *flag.FlagSet) {
		fs.BoolVar(&rotate, "rotate", false, "rotate to a new invite code (admin)")
	})
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
		return entryRefuse("invite-code")
	}

	var resp inviteCodeResp
	if rotate {
		if ce := bs.cli.do(context.Background(), http.MethodPost,
			"/v1/bridges/"+bs.bc.BridgeID+"/invite-code/rotate", nil, &resp); ce != nil {
			return ce
		}
	} else {
		if ce := bs.cli.do(context.Background(), http.MethodGet,
			"/v1/bridges/"+bs.bc.BridgeID+"/invite-code", nil, &resp); ce != nil {
			return ce
		}
	}
	link := resp.InviteLink
	if link == "" {
		link = bs.bc.Server + "/b/" + bs.bc.BridgeID
	}
	out := map[string]any{
		"type":        "invite_code",
		"invite_code": resp.InviteCode,
		"invite_link": link,
	}
	// An admin also gets the complete, ready-to-forward invite with its own
	// fingerprint computed from local keys (never from the server).
	var msg string
	usable := resp.Usable == nil || *resp.Usable
	out["usable"] = usable
	if bs.bc.Role == "admin" && resp.InviteCode != "" && usable {
		if km, err := sess.ks.loadKeys(bs.bc.BridgeID); err == nil {
			if fp, err := localFingerprint(km); err == nil {
				msg = inviteMessage(bs.bc.Server, link, resp.InviteCode, fp, bs.bc.Lang)
				// Printed once, in the INVITE_MESSAGE block (v0.5.1).
				delete(out, "invite_code")
				out["invite"] = "INVITE_MESSAGE_BEGIN … INVITE_MESSAGE_END below"
			}
		}
	}
	sess.pr.emit(out)
	if msg != "" {
		if rotate {
			// v0.5.5: name the bridge, as in the claim notice.
			if n := claimNoticeName(bs.bc.Name); n != "" {
				sess.pr.raw("NOTIFY_OWNER: 「" + n + "」已產生新邀請，舊邀請已失效。下一則是邀請，請整段私下傳給對方，請對方直接貼給自己的 Grok Bot。")
			} else {
				sess.pr.raw("NOTIFY_OWNER: 已產生新邀請，舊邀請已失效。下一則是邀請，請整段私下傳給對方，請對方直接貼給自己的 Grok Bot。")
			}
		}
		sess.pr.raw(inviteBlock(msg))
	} else if bs.bc.Role == "admin" && !usable && bs.pairFull(sess) {
		name := sanitizeDisplayName(bs.bc.Name)
		if name == "" || name == "?" {
			name = bs.bc.BridgeID
		}
		if prompts.IsEnglish(bs.bc.Lang) {
			sess.pr.raw("NOTIFY_OWNER: \"" + name + "\" already connects two bots and takes no one else. To connect another bot, create a new bridge.")
		} else {
			sess.pr.raw("NOTIFY_OWNER: 「" + name + "」已連接兩個 bot，無法再加入。要連接其他 bot，請另建一個 bridge。")
		}
	} else if bs.bc.Role == "admin" && !usable {
		sess.pr.raw("NOTIFY_OWNER: bridge " + bs.bc.BridgeID + " 的邀請碼已使用或已過期。如需新邀請，在私人對話中提出即可產生。")
		sess.pr.raw("NEXT: " + sess.cliCmd() + " invite-code --bridge " + bs.bc.BridgeID + " --rotate   (only after the owner asked, in the private chat, for a new invite)")
	}
	return sess.pr.done()
}

// cmdConfig implements `config` (DEV_SPEC §12.3): PATCH /me with any of the
// poll interval, display name, or owner name.
func cmdConfig(a *app, args []string) *cliError {
	var pollInterval time.Duration
	var displayName, owner string
	var setDisplay, setOwner bool
	g, fs, e := a.parse("config", args, func(fs *flag.FlagSet) {
		fs.DurationVar(&pollInterval, "poll-interval", 0, "poll interval, e.g. 30m (60s..24h)")
		fs.StringVar(&displayName, "display-name", "", "display name")
		fs.StringVar(&owner, "owner", "", "owner name (unverified)")
	})
	if e != nil {
		return e
	}
	// Distinguish "flag present" from "empty value" so empty strings can clear.
	fs.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "display-name":
			setDisplay = true
		case "owner":
			setOwner = true
		}
	})

	body := map[string]any{}
	if pollInterval != 0 {
		secs := int(pollInterval / time.Second)
		if secs < 60 || secs > 86400 {
			return usageErrorf("config: --poll-interval must be 60s..86400s (24h)")
		}
		body["poll_interval_sec"] = secs
	}
	if e := checkProfileNames(displayName, owner); e != nil {
		return e
	}
	if setDisplay {
		body["display_name"] = displayName
	}
	if setOwner {
		body["owner_name"] = owner
	}
	sess, e := a.newSession(g)
	if e != nil {
		return e
	}
	bs, e := sess.resolveBridge()
	if e != nil {
		return e
	}
	var resp landResp
	// No flags: show the current settings (help says "show or change").
	method, reqBody := http.MethodPatch, any(body)
	if len(body) == 0 {
		method, reqBody = http.MethodGet, nil
	}
	if ce := bs.cli.do(context.Background(), method, "/v1/me", reqBody, &resp); ce != nil {
		return ce
	}
	sess.pr.emit(map[string]any{
		"type":              "config",
		"display_name":      resp.Land.DisplayName,
		"owner_name":        resp.Land.OwnerName,
		"poll_interval_sec": resp.Land.PollIntervalSec,
	})
	return sess.pr.done()
}

// cmdPing implements `ping` (DEV_SPEC §12.3).
func cmdPing(a *app, args []string) *cliError {
	g, _, e := a.parse("ping", args, nil)
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
	var resp pingResp
	if ce := bs.cli.do(context.Background(), http.MethodPost, "/v1/me/ping", map[string]any{}, &resp); ce != nil {
		return ce
	}
	sess.pr.emit(map[string]any{"type": "pong", "server_time": resp.ServerTime})
	return sess.pr.done()
}

// cmdVersion implements `version` (DEV_SPEC §12.3). The version is injected via
// ldflags into cmd/bot-bridge-cli and passed to Run.
func cmdVersion(a *app, args []string) *cliError {
	g, _, e := a.parse("version", args, nil)
	if e != nil {
		return e
	}
	pr := newPrinter(a.stdout, g.pretty)
	pr.emit(map[string]any{"type": "version", "version": a.version, "seq": buildSeq(), "protocol": protocol.ProtocolVersion, "update_keys": update.NewKeyring(releasePubKeys...).IDs()})
	return pr.done()
}

// cmdBridges implements `bridges [--offline]`: list every joined bridge with
// what an owner needs to pick one (name, peers, joined, last activity, status).
// The removal manual (/delete) shows this list and asks the owner which one.
// Unless --offline, it asks the server for the name, status and peers
// (read-only; nothing is saved).
func cmdBridges(a *app, args []string) *cliError {
	var offline, menu, all, picked bool
	var lang string
	var withHomes stringList
	g, _, e := a.parse("bridges", args, func(fs *flag.FlagSet) {
		fs.BoolVar(&offline, "offline", false, "do not contact the server; show only what is stored locally")
		fs.BoolVar(&menu, "delete-menu", false, "print the ready-made question for the owner (botobot.app/delete)")
		fs.StringVar(&lang, "lang", "", "language of the delete menu: zh or en (default: the bridges' language)")
		fs.Var(&withHomes, "with-home", "with --delete-menu: also list this older folder of the same bot (repeatable)")
		fs.BoolVar(&all, "all", false, "with --delete-menu --owner-picked: the owner chose \"remove all\" and confirmed; hold every removal at once (one wait)")
		fs.BoolVar(&picked, "owner-picked", false, "with --delete-menu --all: the owner just chose \"remove all\" in your private chat and confirmed it")
	})
	if e != nil {
		return e
	}
	if (all || picked) && (!menu || !all || !picked) {
		return usageErrorf("bridges: --all and --owner-picked go together, with --delete-menu (botobot.app/delete, \"remove all\")")
	}
	sess, e := a.newSession(g)
	if e != nil {
		return e
	}
	if menu {
		return sess.deleteMenu(offline, lang, withHomes, all)
	}
	if len(withHomes) > 0 {
		return usageErrorf("bridges: --with-home only works with --delete-menu")
	}
	cfg, err := sess.ks.loadConfig()
	if err != nil {
		return generalErrorf("read config: %v", err)
	}
	bridges, err := sess.ks.listBridges()
	if err != nil {
		return generalErrorf("list bridges: %v", err)
	}
	if len(bridges) == 0 {
		sess.pr.emit(map[string]any{"type": "bridges", "count": 0})
		return sess.pr.done()
	}
	for i, bc := range bridges {
		line := sess.describeBridge(bc, offline)
		line["index"] = i + 1
		line["default"] = bc.BridgeID == cfg.DefaultBridge
		sess.pr.emit(line)
	}
	return sess.pr.done()
}

// describeBridge builds one `bridges` line.
func (s *session) describeBridge(bc *bridgeConfig, offline bool) map[string]any {
	name, status := bc.Name, "unknown"
	undelivered := 0
	peers := []string{}
	peerHandles := []string{}
	lc, _ := s.ks.loadLands(bc.BridgeID)
	cached := lc
	if !offline {
		if bs, ce := s.bridgeStateFor(bc.BridgeID); ce == nil {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			var br struct {
				Bridge bridgeResp `json:"bridge"`
			}
			switch ce := bs.cli.do(ctx, http.MethodGet, "/v1/bridges/"+bc.BridgeID, nil, &br); {
			case ce == nil:
				if br.Bridge.Name != "" {
					name = br.Bridge.Name
				}
				status = br.Bridge.Status
				if status == "active" || status == "" {
					status = "open"
				}
			case ce.Exit == exitClosed:
				status = "closed"
			case ce.Exit == exitUnauth || ce.Code == "not_found" || ce.Code == "bridge_not_found" || ce.Code == "land_not_found":
				status = "left"
			}
			if status == "open" {
				var lr landsResp
				if bs.cli.do(ctx, http.MethodGet, "/v1/bridges/"+bc.BridgeID+"/lands", nil, &lr) == nil {
					lc = &landsCache{LandSetVersion: lr.LandSetVersion, Lands: lr.Lands}
				}
				// v0.6.4: how many of this bot's messages the other side
				// has not read yet (an older server has no such call).
				var ud struct {
					Count int `json:"count"`
				}
				if bs.cli.do(ctx, http.MethodGet, "/v1/bridges/"+bc.BridgeID+"/undelivered", nil, &ud) == nil && ud.Count > 0 {
					undelivered = ud.Count
				}
			}
			cancel()
		}
	}
	joined := bc.JoinedAt
	// v0.5.5: created is the bridge's creation time: the admin's own join, or
	// for an invited bot the earliest member join the server reports.
	var peerJoined, created int64
	if lc != nil {
		for _, l := range lc.Lands {
			if t, err := time.Parse(time.RFC3339, l.JoinedAt); err == nil && (created == 0 || t.UnixMilli() < created) {
				created = t.UnixMilli()
			}
			if l.LandID != bc.LandID {
				if t, err := time.Parse(time.RFC3339, l.JoinedAt); err == nil {
					peerJoined = max(peerJoined, t.UnixMilli())
				}
			}
			if l.LandID == bc.LandID {
				if joined == 0 {
					if t, err := time.Parse(time.RFC3339, l.JoinedAt); err == nil {
						joined = t.UnixMilli()
					}
				}
				continue
			}
			// v0.5.4: display name first; the handle stays in peer_handles.
			peers = append(peers, peerLabelFor(l.DisplayName, l.Handle, l.OwnerName, lc.selfName(bc.LandID)))
			peerHandles = append(peerHandles, safeHandle(l.Handle))
		}
	}
	if joined == 0 {
		if km, err := s.ks.loadKeys(bc.BridgeID); err == nil {
			joined = km.CreatedAt
		}
	}
	if bc.Role == "admin" || created == 0 || (joined > 0 && joined < created) {
		created = joined
	}
	var last int64
	if ts, err := s.ks.loadTickState(bc.BridgeID); err == nil && ts != nil {
		for _, v := range ts.Seen {
			last = max(last, v)
		}
		for _, v := range ts.HourSends {
			last = max(last, v)
		}
	}
	fmtMS := func(ms int64) string {
		if ms <= 0 {
			return ""
		}
		return time.UnixMilli(ms).Local().Format("2006-01-02 15:04")
	}
	roleZh := "受邀方"
	if bc.Role == "admin" {
		roleZh = "管理者"
	}
	statusZh := map[string]string{"open": "使用中", "closed": "已關閉", "left": "已離開"}[status]
	if statusZh == "" {
		statusZh = "無法確認"
	}
	who := "對方尚未加入"
	peerLeft := len(peers) == 0 && s.peerSeenBefore(bc, cached)
	switch {
	case len(peers) > 0:
		who = "對象：" + strings.Join(peers, "、")
	case peerLeft:
		who = "對方已離開"
	}
	act := "最近 48 小時沒有訊息"
	if last > 0 {
		act = fmtMS(last)
	}
	label := bc.BridgeID
	if name != "" {
		label = "「" + name + "」" + bc.BridgeID
	}
	// v0.5.3: the admin's own "join" is the creation time; the peer's join time
	// comes from the server's member list when it is known.
	whenZh := "加入：" + fmtMS(joined)
	if bc.Role == "admin" {
		whenZh = "建立：" + fmtMS(joined)
		if peerJoined > 0 && len(peers) > 0 {
			whenZh += "，對方加入：" + fmtMS(peerJoined)
		}
	}
	summary := fmt.Sprintf("%s，%s，此 Bot：%s（%s），%s，最近活動：%s，狀態：%s",
		label, who, bc.Handle, roleZh, whenZh, act, statusZh)
	peerJoinedS := ""
	if len(peers) > 0 {
		peerJoinedS = fmtMS(peerJoined)
	}
	if undelivered > 0 {
		summary += "，" + unreadZh(undelivered)
	}
	return map[string]any{
		"undelivered":    undelivered,
		"type":           "bridge",
		"bridge_id":      bc.BridgeID,
		"name":           name,
		"handle":         bc.Handle,
		"role":           bc.Role,
		"server":         bc.Server,
		"status":         status,
		"peers":          peers,
		"peer_handles":   peerHandles,
		"peer_left":      peerLeft,
		"joined_at":      fmtMS(joined), // kept for compatibility: this bot's own join (for the admin, the creation time)
		"created_at":     fmtMS(created),
		"peer_joined_at": peerJoinedS,
		"last_activity":  fmtMS(last),
		"summary":        summary,
	}
}

// peerSeenBefore reports whether another bot was ever on this bridge: its key
// is pinned, or the local lands cache still lists it (v0.5.2). It tells a peer
// who left an old multi-member bridge apart from one who never joined.
func (s *session) peerSeenBefore(bc *bridgeConfig, cached *landsCache) bool {
	if cached != nil {
		for _, l := range cached.Lands {
			if l.LandID != bc.LandID {
				return true
			}
		}
	}
	if ps, err := s.ks.loadPins(bc.BridgeID); err == nil && ps != nil {
		for id := range ps.Entries {
			if id != bc.LandID {
				return true
			}
		}
	}
	return false
}
