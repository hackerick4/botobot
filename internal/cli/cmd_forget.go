package cli

import (
	"context"
	"crypto/rand"
	"flag"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"time"
)

func init() {
	commands["forget"] = cmdForget
}

var bridgeIDRe = regexp.MustCompile(`^br_[A-Za-z0-9_-]{1,64}$`)

// cmdForget implements `forget --bridge X --yes`: delete this bot's local keys
// and state for one bridge (the removal manual at /delete, step 6). It refuses
// while the server still accepts this land's API key, so a bot cannot strand
// itself as a silent member; leave (or close) first. --force skips that check
// and is held for the owner like other admin actions. `forget --all` lists every
// bridge and does nothing; `forget --all --yes` forgets them all, but only when
// every one is confirmed left or closed (all or nothing).
func cmdForget(a *app, args []string) *cliError {
	var yes, force, all bool
	g, _, e := a.parse("forget", args, func(fs *flag.FlagSet) {
		fs.BoolVar(&yes, "yes", false, "confirm deleting the local keys and state (required)")
		fs.BoolVar(&force, "force", false, "delete even if this bot is still a member or the server cannot be reached (held for the owner)")
		fs.BoolVar(&all, "all", false, "every bridge on this machine; prints the list, deletes only with --yes and only after the owner said all")
	})
	if e != nil {
		return e
	}
	if all {
		if g.bridge != "" || force {
			return usageErrorf("forget: --all cannot be combined with --bridge or --force")
		}
		return forgetAll(a, g, yes)
	}
	if g.bridge == "" {
		return requireExplicitBridge("forget", nil)
	}
	if !bridgeIDRe.MatchString(g.bridge) {
		return usageErrorf("forget: invalid bridge id")
	}
	if !yes {
		return usageErrorf("forget: this deletes the keys for %s on this machine. Re-run with --yes to confirm", g.bridge)
	}
	sess, e := a.newSession(g)
	if e != nil {
		return e
	}
	bc, err := sess.ks.loadBridge(g.bridge)
	if err != nil || bc.BridgeID != g.bridge {
		return &cliError{Code: "unknown_bridge", Message: "forget: no local state for that bridge (see bot-bridge-cli bridges)", Exit: exitUsage}
	}
	membership := "unknown"
	if !force {
		var ce *cliError
		if membership, ce = sess.membership(bc); ce != nil {
			return ce
		}
	}
	remaining, ce := sess.forgetOne(bc, membership)
	if ce != nil {
		return ce
	}
	name := bc.Name
	if name == "" {
		name = bc.BridgeID
	}
	// v0.5.3: the owner line only says what happened; the release time and
	// code stay in the bot-facing "forgotten" JSON line (release_confirmed).
	msg := "NOTIFY_OWNER: 已刪除「" + sanitizeDisplayName(name) + "」。"
	if remaining > 0 {
		msg += "其他 bridge 不受影響。"
	} else {
		_ = sess.cleanupHome()
	}
	sess.pr.raw(msg)
	// One routine serves every bridge of this bot (v0.5.1): it goes only with
	// the last bridge. A pre-v0.5.1 routine made for this one bridge always goes.
	// v0.6.2: bridges closed but not yet forgotten no longer keep it.
	switch {
	case remaining == 0:
		sess.pr.raw("NEXT: 此 Bot 已無 bridge，刪掉「bot-bridge inbox」routine（不是暫停），不必再問主人。")
		sess.pr.raw(keepNameLine(bc.Handle, false))
	case sess.ks.liveCount() == 0:
		sess.pr.raw(noLiveRoutineLine(false))
	default:
		sess.pr.raw("NEXT: 「bot-bridge inbox」routine 保留，其他 bridge 仍要用。")
	}
	return sess.pr.done()
}

// membership asks the server whether this land is still in bc: "closed" or
// "left" allow forgetting; still a member or an unclear answer is an error.
func (s *session) membership(bc *bridgeConfig) (string, *cliError) {
	bs, ce := s.bridgeStateFor(bc.BridgeID)
	if ce != nil {
		return "", ce
	}
	var resp landsResp
	ce = bs.cli.do(context.Background(), http.MethodGet, "/v1/bridges/"+bc.BridgeID+"/lands", nil, &resp)
	s.memberEntryClosed = ce != nil && ce.entryClosed
	switch {
	case ce == nil:
		return "", &cliError{Code: "still_member", Message: "forget: this bot is still a member of " + bc.BridgeID + "; leave first (bot-bridge-cli leave --bridge " + bc.BridgeID + ") or close it", Exit: exitGeneral}
	case ce.Exit == exitClosed:
		return "closed", nil
	case ce.Exit == exitUnauth || ce.Code == "not_found" || ce.Code == "bridge_not_found" || ce.Code == "land_not_found":
		return "left", nil
	default:
		return "", &cliError{Code: "membership_unknown", Message: "forget: could not confirm this bot has left " + bc.BridgeID + " (" + ce.Code + "); try again later, or use --force (held for the owner)", Exit: ce.Exit}
	}
}

// bridgeStateFor builds the client for one bridge id, ignoring the default.
func (s *session) bridgeStateFor(id string) (*bridgeState, *cliError) {
	saved := s.g.bridge
	s.g.bridge = id
	defer func() { s.g.bridge = saved }()
	return s.resolveBridge()
}

// forgetOne shreds one bridge's local state, drops its held items, moves the
// default, emits the "forgotten" line, and returns how many bridges remain.
func (s *session) forgetOne(bc *bridgeConfig, membership string) (int, *cliError) {
	if err := shredTree(s.ks.bridgeDir(bc.BridgeID)); err != nil {
		return 0, generalErrorf("forget: delete %s: %v", bc.BridgeID, err)
	}
	// Drop held items for this bridge; they can never be released now.
	droppedHolds := 0
	for _, it := range s.ks.listHeld(s.app.now().UnixMilli()) {
		if it.Bridge == bc.BridgeID {
			if os.Remove(s.ks.heldPath(it.ID)) == nil {
				droppedHolds++
			}
		}
	}
	// Unshown release notices of this bridge: tell the owner now, since no
	// tick of this bridge will run again.
	// A released leave/close of this very bridge is what the owner just asked
	// for; it is folded into forget's own NOTIFY (with its time and code) instead
	// of a separate "if you did not approve this" alert. Anything else released
	// on this bridge is still shown in full.
	for _, n := range s.ks.takeReleaseNotices(bc.BridgeID) {
		if n.Cmd == "leave" || n.Cmd == "close" {
			verb := map[string]string{"leave": "離開", "close": "關閉"}[n.Cmd]
			s.removal = fmt.Sprintf("%s已於 %s 確認，確認碼 %s", verb, time.UnixMilli(n.ReleasedMS).Format("01/02 15:04"), shortHash(n.Hash))
			continue
		}
		s.pr.raw(releaseNoticeLine(n))
	}
	s.ks.dropReleaseNotices(bc.BridgeID)
	remaining, _ := s.ks.listBridges()
	if cfg, err := s.ks.loadConfig(); err == nil && cfg.DefaultBridge == bc.BridgeID {
		cfg.DefaultBridge = ""
		if len(remaining) > 0 {
			cfg.DefaultBridge = remaining[0].BridgeID
		}
		_ = s.ks.saveConfig(cfg)
	}
	// A guest line of this bot's public entry is not a "bridge left" for
	// the routine: the entry stays, and closed guest lines are not listed
	// one by one later (v0.6.0).
	entries := s.ks.entryCount()
	if len(remaining) > 0 && bc.EntryID == "" {
		s.ks.addCleaned(bc, membership, s.app.now().UnixMilli())
	}
	line := map[string]any{
		"type":              "forgotten",
		"bridge_id":         bc.BridgeID,
		"membership":        membership,
		"held_dropped":      droppedHolds,
		"bridges_remaining": len(remaining),
	}
	if entries > 0 {
		line["entries"] = entries
	}
	if s.removal != "" {
		line["release_confirmed"] = s.removal
	}
	s.pr.emit(line)
	return len(remaining) + entries, nil
}

// forgetAll handles `forget --all [--yes]`. It always prints the full list
// first. Without --yes it stops (confirm_all). With --yes it checks every bridge
// first and deletes nothing unless all of them are left or closed.
func forgetAll(a *app, g *globalFlags, yes bool) *cliError {
	sess, e := a.newSession(g)
	if e != nil {
		return e
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
		sess.pr.emit(map[string]any{
			"type":      "will_forget",
			"index":     i + 1,
			"bridge_id": bc.BridgeID,
			"name":      bc.Name,
			"role":      bc.Role,
		})
	}
	_ = sess.pr.flush() // the list must reach the owner even when we stop here
	if !yes {
		return &cliError{Code: "confirm_all", Exit: exitUsage, Message: fmt.Sprintf("forget --all: this deletes the keys for all %d bridges above. Show this list to your owner; only if they confirm all of them, re-run with --all --yes", len(bridges))}
	}
	states := make([]string, len(bridges))
	for i := range bridges {
		m, ce := sess.membership(bridges[i])
		if ce != nil {
			ce.Message = "forget --all: nothing deleted. " + ce.Message
			return ce
		}
		states[i] = m
	}
	for i := range bridges {
		if _, ce := sess.forgetOne(bridges[i], states[i]); ce != nil {
			return ce
		}
	}
	msg := "NOTIFY_OWNER: 已刪除全部 " + fmt.Sprint(len(bridges)) + " 個 bridge。"
	_ = sess.cleanupHome()
	sess.pr.raw(msg)
	sess.pr.raw("NEXT: 刪掉此 Bot 的「bot-bridge inbox」routine（含舊的每個 bridge 各一個的 routine，不是暫停），不必再問主人。")
	if _, mixed := homeIdentity(sess.ks.home); !mixed && bridges[0].Handle != "" {
		sess.pr.raw(keepNameLine(bridges[0].Handle, false))
	}
	return sess.pr.done()
}

// shredTree overwrites every regular file under dir with random bytes, syncs
// it, and then removes the tree. On SSDs and copy-on-write file systems the
// overwrite is best effort; the keys are useless once the server has removed
// the land either way.
func shredTree(dir string) error {
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || !d.Type().IsRegular() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		f, err := os.OpenFile(p, os.O_WRONLY, 0)
		if err != nil {
			return nil
		}
		buf := make([]byte, 32*1024)
		for left := info.Size(); left > 0; {
			n := int64(len(buf))
			if left < n {
				n = left
			}
			_, _ = rand.Read(buf[:n])
			if _, err := f.Write(buf[:n]); err != nil {
				break
			}
			left -= n
		}
		_ = f.Sync()
		_ = f.Close()
		return nil
	})
	if err != nil {
		return err
	}
	return os.RemoveAll(dir)
}

// homeFiles are the only entries bot-bridge itself keeps at the top of a home
// (bridge folders are already removed by forget when this runs).
var homeFiles = []string{"config.json", "update.json", "held"}

// cleanupHome removes this bot's state folder once no bridge is left, so the
// owner is not asked to delete it by hand. It only shreds bot-bridge's own
// entries and then removes the folder if it is empty (os.Remove, not
// RemoveAll): a folder holding anything else, such as an unrelated BB_HOME, is
// left in place. The shared ~/.bot-bridge root and the CLI binary are never
// removed.
func (s *session) cleanupHome() bool {
	if rest, err := s.ks.listBridges(); err != nil || len(rest) > 0 {
		return false
	}
	// A public entry keeps the folder even with no guest line open (v0.6.0).
	if s.ks.entryCount() > 0 {
		return false
	}
	home := filepath.Clean(s.ks.home)
	root, err := defaultRoot()
	if err != nil || filepath.Clean(root) == home || filepath.Clean(botsDir(root)) == home {
		return false
	}
	// Never touch a general-purpose folder that BB_HOME happened to point at.
	if uh, err := os.UserHomeDir(); err != nil || filepath.Clean(uh) == home || filepath.Dir(home) == home {
		return false
	}
	for _, f := range homeFiles {
		p := filepath.Join(home, f)
		fi, err := os.Lstat(p)
		if err != nil {
			continue
		}
		if fi.IsDir() || fi.Mode().IsRegular() {
			_ = shredTree(p)
			_ = os.RemoveAll(p)
		}
	}
	return os.Remove(home) == nil
}
