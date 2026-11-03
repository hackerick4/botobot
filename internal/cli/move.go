package cli

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

func init() {
	commands["move"] = cmdMove
	commands["delete-menu"] = func(a *app, args []string) *cliError {
		return cmdBridges(a, append([]string{"--delete-menu"}, args...))
	}
	adminGated["move"] = func([]string) bool { return true }
}

// moveFlags are move's own flags (also used by the hold pre-check).
func moveFlags(from *string, bridges *stringList, yes *bool) func(fs *flag.FlagSet) {
	return func(fs *flag.FlagSet) {
		fs.StringVar(from, "from", "", "the older folder of this same bot whose bridges move here")
		fs.Var(bridges, "bridge-id", "move only this bridge (repeatable; default: all in --from)")
		fs.BoolVar(yes, "yes", false, "confirm")
	}
}

// movePlan is a checked move: source keystore, bridges to move, destination.
type movePlan struct {
	src     *session
	from    string
	bridges []*bridgeConfig
}

// planMove checks a move from --from into this session's folder (v0.5.2).
//
// A bot that has bridges in several older folders (one per handle, before
// v0.5.1) moves them into one folder so that a single routine line and a
// single `bridges` list cover them. Nothing is copied: each bridge folder is
// renamed, with its held items. The bridges keep their own handle on the
// server. Refused: the same folder, a folder that is not there or has no such
// bridge, a destination that already has the bridge (that would be the other
// side of the same bridge), a destination shared by several bots, and a bridge
// whose tick is running.
func (s *session) planMove(from string, only []string) (*movePlan, *cliError) {
	if from == "" {
		return nil, usageErrorf("move: --from FOLDER is required (the older folder of this same bot)")
	}
	src := absOr(expandTilde(from))
	dst := absOr(s.ks.home)
	if filepath.Clean(src) == filepath.Clean(dst) {
		return nil, usageErrorf("move: --from is this folder already")
	}
	if fi, err := os.Lstat(src); err == nil && fi.Mode()&os.ModeSymlink != 0 {
		return nil, &cliError{Code: "unknown_folder", Exit: exitUsage, Message: "move: " + src + " is a link; use the real folder"}
	}
	if fi, err := os.Stat(src); err != nil || !fi.IsDir() {
		return nil, &cliError{Code: "unknown_folder", Exit: exitUsage, Message: "move: " + src + " is not a folder"}
	}
	if !isBotFolder(src) {
		return nil, &cliError{Code: "unknown_folder", Exit: exitUsage, Message: "move: " + src + " is not a bot-bridge folder (no config.json or br_* bridge); nothing touched"}
	}
	if _, err := os.Stat(s.ks.configPath()); err != nil {
		return nil, &cliError{Code: "unknown_folder", Exit: exitUsage, Message: "move: this folder (" + dst + ") has no bot yet; run move with the BB_HOME of the bot's main folder (its routine line)"}
	}
	if _, mixed := homeIdentity(dst); mixed {
		return nil, &cliError{Code: "bridge_required", Exit: exitUsage, Message: "move: this folder holds several bots' bridges; move into a folder of one bot (BB_HOME=~/.bot-bridge/bots/<handle>)"}
	}
	gg := *s.g
	gg.home = src
	ss, ce := s.app.newSession(&gg)
	if ce != nil {
		return nil, ce
	}
	all, err := ss.ks.listBridges()
	if err != nil {
		return nil, generalErrorf("move: list %s: %v", src, err)
	}
	want := map[string]bool{}
	for _, b := range only {
		if !bridgeIDRe.MatchString(b) {
			return nil, usageErrorf("move: --bridge-id must look like br_…")
		}
		want[b] = true
	}
	var pick []*bridgeConfig
	for _, bc := range all {
		if len(want) == 0 || want[bc.BridgeID] {
			pick = append(pick, bc)
			delete(want, bc.BridgeID)
		}
	}
	for b := range want {
		return nil, &cliError{Code: "unknown_bridge", Exit: exitUsage, Message: "move: " + b + " is not in " + src}
	}
	if len(pick) == 0 {
		return nil, &cliError{Code: "unknown_bridge", Exit: exitUsage, Message: "move: " + src + " has no bridge"}
	}
	sort.Slice(pick, func(i, j int) bool { return pick[i].BridgeID < pick[j].BridgeID })
	for _, bc := range pick {
		if fi, err := os.Lstat(ss.ks.bridgeDir(bc.BridgeID)); err != nil || !fi.IsDir() {
			return nil, &cliError{Code: "unknown_bridge", Exit: exitUsage, Message: "move: " + bc.BridgeID + " in " + src + " is not a plain folder; nothing moved"}
		}
		if _, err := os.Lstat(s.ks.bridgeDir(bc.BridgeID)); err == nil {
			return nil, &cliError{Code: "already_joined", Exit: exitUsage, Message: "move: this folder already has " + bc.BridgeID + " (the other side of the same bridge, or a copy); nothing moved"}
		}
	}
	return &movePlan{src: ss, from: src, bridges: pick}, nil
}

// moveArgv is the exact, fully expanded command a move hold replays.
func (p *movePlan) argv() []string {
	out := []string{"move", "--from", p.from}
	for _, bc := range p.bridges {
		out = append(out, "--bridge-id", bc.BridgeID)
	}
	return append(out, "--yes")
}

func (p *movePlan) reason(dst string) string {
	var names []string
	for _, bc := range p.bridges {
		n := displayName(bc)
		if n == "" {
			n = bc.BridgeID
		}
		names = append(names, "「"+n+"」")
	}
	return fmt.Sprintf("要把 %d 個 bridge（%s）從 %s 移到 %s，之後由同一個 routine 檢查", len(p.bridges), strings.Join(names, "、"), p.from, dst)
}

// cmdMove implements `move --from FOLDER [--bridge-id ID]… --yes` (held for
// the owner; see gateAdmin).
func cmdMove(a *app, args []string) *cliError {
	var from string
	var only stringList
	var yes bool
	g, _, e := a.parse("move", args, moveFlags(&from, &only, &yes))
	if e != nil {
		return e
	}
	if !yes {
		return usageErrorf("move: add --yes (only when the owner asked to put this bot's bridges in one folder)")
	}
	sess, e := a.newSession(g)
	if e != nil {
		return e
	}
	p, e := sess.planMove(from, only)
	if e != nil {
		return e
	}
	dstCfg, err := sess.ks.loadConfig()
	if err != nil {
		return generalErrorf("read config: %v", err)
	}
	if dstCfg.Handle == "" {
		// Record the folder's own bot first, so the moved bridges (which keep
		// their own handle) do not make it look shared.
		if h, _ := homeIdentity(sess.ks.home); h != "" {
			dstCfg.Handle = h
		}
	}
	srcCfg, _ := p.src.ks.loadConfig()
	nowMS := a.now().UnixMilli()
	var moved []string
	for _, bc := range p.bridges {
		bid := bc.BridgeID
		lk, ok, err := acquireLock(p.src.ks.tickLockPath(bid))
		if err != nil || !ok {
			if lk != nil {
				lk.release()
			}
			return &cliError{Code: "busy", Exit: exitGeneral, Message: "move: a tick is running on " + bid + "; try again in a minute (moved so far: " + strings.Join(moved, ", ") + ")"}
		}
		err = os.Rename(p.src.ks.bridgeDir(bid), sess.ks.bridgeDir(bid))
		lk.release()
		if err != nil {
			return generalErrorf("move %s: %v (moved so far: %s)", bid, err, strings.Join(moved, ", "))
		}
		moved = append(moved, bid)
		// Held items of this bridge follow it.
		for _, it := range p.src.ks.listHeld(nowMS) {
			if it.Bridge == bid {
				if ensureDir(sess.ks.heldDir()) == nil {
					_ = os.Rename(p.src.ks.heldPath(it.ID), sess.ks.heldPath(it.ID))
				}
			}
		}
		if srcCfg != nil && srcCfg.DefaultBridge == bid {
			srcCfg.DefaultBridge = ""
		}
		if dstCfg.DefaultBridge == "" {
			dstCfg.DefaultBridge = bid
		}
	}
	if err := sess.ks.saveConfig(dstCfg); err != nil {
		return generalErrorf("save config: %v", err)
	}
	left, _ := p.src.ks.listBridges()
	if srcCfg != nil && len(left) > 0 {
		if srcCfg.DefaultBridge == "" {
			srcCfg.DefaultBridge = left[0].BridgeID
		}
		_ = p.src.ks.saveConfig(srcCfg)
	}
	removed := len(left) == 0 && p.src.cleanupHome()
	sess.pr.emit(map[string]any{
		"type": "moved", "bridges": moved, "from": p.from, "to": absOr(sess.ks.home),
		"left_in_from": len(left), "from_removed": removed,
	})
	routine := sess.routinePrompt()
	sess.pr.raw("ROUTINE_PROMPT: " + routine)
	old := "BB_HOME=" + p.from
	if len(left) > 0 {
		sess.pr.raw("NEXT: routine 只留一個「bot-bridge inbox」，提示詞是上面 ROUTINE_PROMPT: 後的那一行；提示詞含 tick --bridge 移過來的 bridge 的舊 routine 刪掉（不是暫停）。" + p.from + " 還有 " + fmt.Sprint(len(left)) + " 個 bridge，含 " + old + " 的 routine 先保留。")
	} else {
		sess.pr.raw("NEXT: routine 只留一個「bot-bridge inbox」，提示詞是上面 ROUTINE_PROMPT: 後的那一行；提示詞含 " + old + " 或 tick --bridge 移過來的 bridge 的舊 routine 刪掉（不是暫停），不必再問主人。")
	}
	return sess.pr.done()
}

// isBotFolder reports whether dir looks like a bot-bridge state folder: a
// config.json or at least one br_* bridge folder holding a bridge.json. It only
// reads, so callers can check a folder named by --from / --with-home before
// opening a keystore there (openKeystore tightens the folder to 0700).
func isBotFolder(dir string) bool {
	if fi, err := os.Lstat(filepath.Join(dir, "config.json")); err == nil && fi.Mode().IsRegular() {
		return true
	}
	ents, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, e := range ents {
		if !e.IsDir() || !strings.HasPrefix(e.Name(), "br_") {
			continue
		}
		if fi, err := os.Lstat(filepath.Join(dir, e.Name(), "bridge.json")); err == nil && fi.Mode().IsRegular() {
			return true
		}
	}
	return false
}
