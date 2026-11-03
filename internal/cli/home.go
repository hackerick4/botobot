package cli

import (
	"crypto/rand"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Per-bot homes (v0.4.5). Two bots on one computer must never share a state
// directory: they would overwrite each other's keys and see each other's
// notices. join/claim/create therefore pick a home per handle when none is
// given (~/.bot-bridge/bots/<handle>), refuse to write into a home that holds a
// different bot or already holds this bridge, and print the routine line with
// BB_HOME so every later tick uses the same home.

var handleDirSafe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,62}$`)

// defaultRoot is ~/.bot-bridge, the pre-v0.4.5 single home.
func defaultRoot() (string, error) { return resolveHome("", "") }

// botsDir holds one home per bot handle.
func botsDir(root string) string { return filepath.Join(root, "bots") }

// homeBridges lists the bridge handles stored directly in home (no network).
func homeBridges(home string) []*bridgeConfig {
	ks := &keystore{home: home, warn: func(string, ...any) {}}
	bs, err := ks.listBridges()
	if err != nil {
		return nil
	}
	return bs
}

// homeIdentity is the bot handle a home belongs to: config.json's handle, or
// (for homes written before v0.4.5) the handle of its bridges. "" when empty.
// mixed is true when a legacy home already holds bridges of several handles.
func homeIdentity(home string) (handle string, mixed bool) {
	ks := &keystore{home: home, warn: func(string, ...any) {}}
	if c, err := ks.loadConfig(); err == nil && c.Handle != "" {
		return c.Handle, false
	}
	seen := map[string]bool{}
	for _, b := range homeBridges(home) {
		if b.Handle != "" {
			seen[b.Handle] = true
		}
	}
	for h := range seen {
		if handle == "" || h < handle {
			handle = h
		}
	}
	return handle, len(seen) > 1
}

// onboardHome picks and checks the home for join/claim/create and settles the
// handle. explicit is the --home flag or $BB_HOME; handle may be empty.
//
// v0.5.1: a bot's identity is its folder. Without an explicit home, an empty
// handle gets a fresh, unique name (owner-bot-xxxx) and its own folder, so two
// or more bots on one computer never collide and the other side can tell them
// apart. A given handle whose folder already exists (or that the old single
// ~/.bot-bridge already uses) is refused (handle_taken): the CLI cannot tell
// "the same bot again" from "another bot with the same name", so the bot must
// say which (BB_HOME from its routine line, or no --handle). With an explicit
// home, an empty handle is the folder's own (later bridges of the same bot).
func onboardHome(cmd, explicit, handle, botName, owner, bridgeID string) (string, string, *cliError) {
	home := explicit
	root, err := defaultRoot()
	if err != nil {
		return "", "", generalErrorf("%v", err)
	}
	taken := func(h string) string {
		if id, _ := homeIdentity(root); id == h {
			return root
		}
		p := filepath.Join(botsDir(root), h)
		if _, err := os.Stat(p); err == nil {
			if id, _ := homeIdentity(p); id != "" || len(homeBridges(p)) > 0 {
				return p
			}
			if ents, err := os.ReadDir(p); err == nil && len(ents) > 0 {
				return p
			}
		}
		return ""
	}
	if home != "" {
		if handle == "" {
			if id, _ := homeIdentity(home); id != "" {
				handle = id
			} else {
				handle = newHandle(botName, owner, func(string) bool { return false })
			}
		}
	} else {
		if handle == "" {
			handle = newHandle(botName, owner, func(h string) bool { return taken(h) != "" })
		} else {
			if !handleDirSafe.MatchString(handle) {
				return "", "", usageErrorf("%s: handle %q cannot name a state folder; leave out --handle (a unique name is picked), or pass --home / set BB_HOME", cmd, handle)
			}
			if p := taken(handle); p != "" {
				return "", "", &cliError{Code: "handle_taken", Exit: exitUsage, Message: fmt.Sprintf(
					"%s: the name %q is already used by the state folder %s on this computer. Nothing was written. If that folder is this bot's own (its routine line starts with BB_HOME=%s), run the same command with that BB_HOME in front. Otherwise run it again without --handle: a unique name and folder are picked",
					cmd, handle, p, p)}
			}
		}
		home = filepath.Join(botsDir(root), handle)
	}
	if bridgeID != "" {
		if _, err := os.Stat(filepath.Join(home, bridgeID, "bridge.json")); err == nil {
			return "", "", &cliError{Code: "already_joined", Exit: exitUsage, Message: fmt.Sprintf(
				"%s: the state folder %s already has bridge %s. Nothing was written. If this is another bot on the same computer, run the command without BB_HOME, --home and --handle: it gets its own folder. If it is the same bot rejoining, run forget --bridge %s first",
				cmd, home, bridgeID, bridgeID)}
		}
	}
	if id, mixed := homeIdentity(home); id != "" && (id != handle || mixed) {
		return "", "", &cliError{Code: "identity_conflict", Exit: exitUsage, Message: fmt.Sprintf(
			"%s: the state folder %s belongs to bot %q, not %q. Two bots must not share one folder. Nothing was written. If this bot already has a folder, use the BB_HOME from its routine line; otherwise run the command without BB_HOME, --home and --handle to get a new one",
			cmd, home, id, handle)}
	}
	return home, handle, nil
}

// handleAlphabet avoids look-alike characters (0/o, 1/l).
const handleAlphabet = "abcdefghijkmnpqrstuvwxyz23456789"

// newHandle picks a fresh handle: the owner's latin name plus -bot (or just
// "bot"), then "-" and four random characters, re-drawn while taken says the
// name is in use. It fits the server's 32-character handle limit.
func newHandle(botName, owner string, taken func(string) bool) string {
	// v0.5.3: the bot's own name (--display-name) comes first, so bots of one
	// owner no longer all start with the owner's name.
	base := slugName(botName)
	if base == "" {
		base = defaultHandle("", owner)
	}
	if base == "" {
		base = "bot"
	}
	if len(base) > 27 {
		base = strings.TrimRight(base[:27], "-_")
	}
	var h string
	for i := 0; i < 64; i++ {
		b := make([]byte, 4)
		if _, err := rand.Read(b); err != nil {
			panic(err)
		}
		suf := make([]byte, 4)
		for j := range b {
			suf[j] = handleAlphabet[int(b[j])%len(handleAlphabet)]
		}
		h = base + "-" + string(suf)
		if !taken(h) {
			break
		}
	}
	return h
}

// autoHome is the home for every other command when neither --home nor
// $BB_HOME is set: the old ~/.bot-bridge if it has bridges (so routine lines
// written before v0.4.5 keep working), or ~/.bot-bridge when no bot folder
// exists. Since v0.5.3 it never picks or names a per-bot folder: a bot whose own
// folder is gone (its last bridge was closed and cleaned up) must not land in,
// or be pointed at, another bot's folder (RT-78). Each bot's commands carry the
// BB_HOME from its own routine line.
func autoHome() (string, *cliError) {
	root, err := defaultRoot()
	if err != nil {
		return "", generalErrorf("%v", err)
	}
	if len(homeBridges(root)) > 0 {
		return root, nil
	}
	entries, _ := os.ReadDir(botsDir(root))
	n := 0
	for _, e := range entries {
		if e.IsDir() {
			n++
		}
	}
	if n == 0 {
		return root, nil
	}
	return "", &cliError{Code: "home_required", Exit: exitUsage, Message: "each bot keeps its own folder on this computer. Run the command with the BB_HOME=… from this bot's own routine line; never use another bot's folder. (bot-bridge-cli update and version need no folder.)"}
}

// saveIdentity records which bot a home belongs to.
func (ks *keystore) saveIdentity(handle string) {
	c, err := ks.loadConfig()
	if err != nil || c.Handle == handle {
		return
	}
	c.Handle = handle
	_ = ks.saveConfig(c)
}

func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if s != "" {
			return s
		}
	}
	return ""
}
