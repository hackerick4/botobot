package cli

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/hackerick4/botobot/internal/prompts"
)

// Public entry (v0.6.0, DEV_SPEC §20). A service bot owns one reusable link;
// every guest bot that joins through it gets its own private two-bot bridge.
// The service side keeps the entry's keys once, in entries/<entry_id>/, and a
// light bridge folder per guest (bridge.json, pins, tick state) that points
// back at the entry.

func init() { commands["entry"] = cmdEntry }

var (
	entryIDRe   = regexp.MustCompile(`^en_[A-Za-z0-9_-]{4,64}$`)
	entryLinkRe = regexp.MustCompile(`^pe_[0-9A-Za-z]{22}$`)
)

// entryConfig is entries/<id>/entry.json.
type entryConfig struct {
	Server      string `json:"server"`
	EntryID     string `json:"entry_id"`
	Link        string `json:"link"`
	Name        string `json:"name"`
	Handle      string `json:"handle"`
	DisplayName string `json:"display_name,omitempty"`
	Lang        string `json:"lang,omitempty"`
	CreatedAt   int64  `json:"created_at_ms"`
}

// entryTickState is entries/<id>/tick.json: the aggregated inbox's deferred
// acks and where the closed-bridge list was last read.
type entryTickState struct {
	PendingAckIDs       []string `json:"pending_ack_ids,omitempty"`
	ClosedSinceMS       int64    `json:"closed_since_ms"`
	ConsecutiveFailures int      `json:"consecutive_failures,omitempty"`
	Notified            bool     `json:"notified,omitempty"`
	Ended               string   `json:"ended,omitempty"`
}

func (ks *keystore) entriesDir() string         { return filepath.Join(ks.home, "entries") }
func (ks *keystore) entryDir(id string) string  { return filepath.Join(ks.entriesDir(), id) }
func (ks *keystore) entryPath(id string) string { return filepath.Join(ks.entryDir(id), "entry.json") }
func (ks *keystore) entryTickPath(id string) string {
	return filepath.Join(ks.entryDir(id), "tick.json")
}
func (ks *keystore) entryLockPath(id string) string {
	return filepath.Join(ks.entryDir(id), "tick.lock")
}

func (ks *keystore) loadEntry(id string) (*entryConfig, error) {
	var ec entryConfig
	if err := readJSON(ks.entryPath(id), &ec); err != nil {
		return nil, err
	}
	return &ec, nil
}

// listEntries returns this bot's public entries (normally zero or one).
func (ks *keystore) listEntries() []*entryConfig {
	ents, err := os.ReadDir(ks.entriesDir())
	if err != nil {
		return nil
	}
	var out []*entryConfig
	for _, e := range ents {
		if !e.IsDir() || !entryIDRe.MatchString(e.Name()) {
			continue
		}
		if ec, err := ks.loadEntry(e.Name()); err == nil && ec.EntryID == e.Name() {
			out = append(out, ec)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt < out[j].CreatedAt })
	return out
}

func (ks *keystore) loadEntryTick(id string) *entryTickState {
	var ts entryTickState
	_ = readJSON(ks.entryTickPath(id), &ts)
	return &ts
}

func (ks *keystore) saveEntryTick(id string, ts *entryTickState) error {
	return writeJSONAtomic(ks.entryTickPath(id), ts)
}

func (ks *keystore) entryAPIKey(id string) (string, error) {
	p := filepath.Join(ks.entryDir(id), "apikey")
	ks.enforcePrivatePerms(p)
	b, err := os.ReadFile(p)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(b)), nil
}

// entryBridges returns the local guest bridges of entry id.
func (ks *keystore) entryBridges(id string) []*bridgeConfig {
	bcs, _ := ks.listBridges()
	var out []*bridgeConfig
	for _, bc := range bcs {
		if bc.EntryID == id {
			out = append(out, bc)
		}
	}
	return out
}

type entryResp struct {
	Entry struct {
		EntryID        string `json:"entry_id"`
		Link           string `json:"link"`
		Name           string `json:"name"`
		Status         string `json:"status"`
		Handle         string `json:"handle"`
		DisplayName    string `json:"display_name"`
		Fingerprint    string `json:"fingerprint"`
		Lang           string `json:"lang"`
		MaxOpenBridges int    `json:"max_open_bridges"`
		JoinsPerHour   int    `json:"joins_per_hour"`
		OpenBridges    int    `json:"open_bridges"`
		DisabledReason string `json:"disabled_reason"`
	} `json:"entry"`
	EntryKey string `json:"entry_key"`
}

// cmdEntry dispatches `entry create|status|disable|enable|rotate|close`.
func cmdEntry(a *app, args []string) *cliError {
	// release re-runs a held item as: entry --home H <sub> …
	var pre []string
	if len(args) >= 2 && (args[0] == "--home" || args[0] == "-home") {
		pre, args = args[:2], args[2:]
	}
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		return usageErrorf("entry: give a subcommand: create, status, disable, enable, rotate or close")
	}
	sub, rest := args[0], append(append([]string{}, pre...), args[1:]...)
	switch sub {
	case "create":
		return entryCreate(a, rest)
	case "status", "enable":
		return entryManage(a, sub, rest)
	case "disable", "rotate", "close":
		if !a.releasing {
			return entryHold(a, sub, rest)
		}
		return entryManage(a, sub, rest)
	}
	return usageErrorf("entry: unknown subcommand %q (create, status, disable, enable, rotate, close)", sub)
}

func entryCreate(a *app, args []string) *cliError {
	var name, handle, displayName, owner, lang string
	var maxOpen, jph int
	g, _, e := a.parse("entry create", args, func(fs *flag.FlagSet) {
		fs.StringVar(&name, "name", "", "public name of the entry, e.g. the service's name (required)")
		fs.StringVar(&handle, "handle", "", "keep a stable name: the name this bot had before; otherwise leave out")
		fs.StringVar(&displayName, "display-name", "", "this bot's own name")
		fs.StringVar(&owner, "owner", "", "the owner's or business's name (unverified)")
		fs.StringVar(&lang, "lang", "", "en for English owner notices (default zh-TW)")
		fs.IntVar(&maxOpen, "max-open", 0, "most private lines open at once (server default 200)")
		fs.IntVar(&jph, "joins-per-hour", 0, "most new joining bots per hour (server default 60)")
	})
	if e != nil {
		return e
	}
	if g.server == "" {
		return usageErrorf("entry create: --server is required (or set $BB_SERVER)")
	}
	if strings.TrimSpace(name) == "" {
		return usageErrorf("entry create: --name is required")
	}
	if lang != "" && lang != "en" && lang != "zh" {
		return usageErrorf("entry create: --lang is en or zh")
	}
	if e := checkPlaceholderNames("entry create", handle, owner); e != nil {
		return e
	}
	if e := checkProfileNames(displayName, owner); e != nil {
		return e
	}
	home, handle, hce := onboardHome("create", firstNonEmpty(g.home, os.Getenv("BB_HOME")), handle, displayName, owner, "")
	if hce != nil {
		return hce
	}
	g.home = home
	prior := len(homeBridges(home))
	sess, e := a.newSession(g)
	if e != nil {
		return e
	}
	if len(sess.ks.listEntries()) > 0 {
		return &cliError{Code: "entry_exists", Exit: exitUsage, Message: "entry create: this bot already has a public entry; one is enough (entry status shows its link). Each joining bot gets a private line through the same link"}
	}
	encPriv, encPub, signPriv, signPub, bindingSig, err := newKeypair()
	if err != nil {
		return generalErrorf("%v", err)
	}
	settings := map[string]any{}
	if maxOpen != 0 {
		settings["max_open_bridges"] = maxOpen
	}
	if jph != 0 {
		settings["joins_per_hour"] = jph
	}
	body := map[string]any{
		"name": name,
		"lang": lang,
		"land": landBlockBody{
			Profile: profileBody{Handle: handle, DisplayName: displayName, OwnerName: owner},
			Key:     keyInputBody{EncPub: encPub, SignPub: signPub, BindingSig: bindingSig},
		},
	}
	if len(settings) > 0 {
		body["settings"] = settings
	}
	cli := newClient(&http.Client{Timeout: g.timeout}, g.server, "")
	var resp entryResp
	if ce := cli.do(context.Background(), http.MethodPost, "/v1/entries", body, &resp); ce != nil {
		return ce
	}
	if !entryIDRe.MatchString(resp.Entry.EntryID) || !strings.HasPrefix(resp.EntryKey, "bbe_") {
		return generalErrorf("entry create: unexpected server answer")
	}
	nowMS := a.now().UnixMilli()
	id := resp.Entry.EntryID
	if err := ensureDir(sess.ks.entriesDir()); err != nil {
		return generalErrorf("save entry: %v", err)
	}
	if err := ensureDir(sess.ks.entryDir(id)); err != nil {
		return generalErrorf("save entry: %v", err)
	}
	ec := &entryConfig{Server: strings.TrimRight(g.server, "/"), EntryID: id, Link: resp.Entry.Link, Name: resp.Entry.Name,
		Handle: handle, DisplayName: displayName, Lang: lang, CreatedAt: nowMS}
	km := &keyMaterial{EncPriv: encPriv, SignPriv: signPriv, CreatedAt: nowMS}
	if err := writeJSONAtomic(sess.ks.entryPath(id), ec); err != nil {
		return generalErrorf("save entry: %v", err)
	}
	if err := writeJSONAtomic(filepath.Join(sess.ks.entryDir(id), "keys.json"), km); err != nil {
		return generalErrorf("save entry keys: %v", err)
	}
	if err := writeFileAtomic(filepath.Join(sess.ks.entryDir(id), "apikey"), []byte(resp.EntryKey), privFilePerm); err != nil {
		return generalErrorf("save entry key: %v", err)
	}
	_ = sess.ks.saveEntryTick(id, &entryTickState{ClosedSinceMS: nowMS})
	sess.ks.saveIdentity(handle)
	clearClosedEntry(sess.ks.home, nowMS)
	routine := sess.routinePrompt()
	sess.pr.emit(map[string]any{
		"type":           "entry_created",
		"entry_id":       id,
		"link":           resp.Entry.Link,
		"name":           resp.Entry.Name,
		"handle":         handle,
		"fingerprint":    resp.Entry.Fingerprint,
		"max_open":       resp.Entry.MaxOpenBridges,
		"joins_per_hour": resp.Entry.JoinsPerHour,
		"routine_prompt": routine,
	})
	en := prompts.IsEnglish(lang)
	for _, ln := range entryCreatedNotice(sanitizeDisplayName(resp.Entry.Name), resp.Entry.Link, en) {
		sess.pr.raw(ln)
	}
	sess.printRoutine(routine, prior, lang, false)
	return sess.pr.done()
}

// entryCreatedNotice is the owner's notice after entry create: short, plain.
func entryCreatedNotice(name, link string, en bool) []string {
	if en {
		return []string{
			"NOTIFY_OWNER: Public entry for \"" + name + "\" is ready: " + link,
			"NOTIFY_OWNER: The link can go on a website or a map listing. Each bot that joins gets its own private line; joining bots never see each other.",
			"NOTIFY_OWNER: When a new bot joins, a short notice appears in this chat; nothing else needs checking. Payments, refunds and personal data are always confirmed first.",
		}
	}
	return []string{
		"NOTIFY_OWNER: 「" + name + "」的公開入口已建立：" + link,
		"NOTIFY_OWNER: 連結可放在網站或地圖資訊。每個加入的 Bot 各自一條私人連線，彼此看不到。",
		"NOTIFY_OWNER: 有新的 Bot 加入時，會在這個對話裡簡短通知，不必另外查看。付款、退款或個資，一律先確認再處理。",
	}
}

// pickEntry selects the entry for a management command.
func (s *session) pickEntry(want string) (*entryConfig, *cliError) {
	ents := s.ks.listEntries()
	if want != "" {
		for _, ec := range ents {
			if ec.EntryID == want {
				return ec, nil
			}
		}
		return nil, &cliError{Code: "unknown_entry", Exit: exitUsage, Message: "entry: no public entry " + want + " in this bot's folder"}
	}
	switch len(ents) {
	case 0:
		return nil, &cliError{Code: "no_entry", Exit: exitUsage, Message: "entry: this bot has no public entry (entry create makes one)"}
	case 1:
		return ents[0], nil
	}
	return nil, usageErrorf("entry: this folder has %d entries; pass --entry en_…", len(ents))
}

func (s *session) entryClient(ec *entryConfig) (*client, *cliError) {
	k, err := s.ks.entryAPIKey(ec.EntryID)
	if err != nil {
		return nil, generalErrorf("read entry key: %v", err)
	}
	return newClient(&http.Client{Timeout: s.g.timeout}, ec.Server, k), nil
}

func entryHold(a *app, sub string, args []string) *cliError {
	var want string
	var yes, picked bool
	g, _, e := a.parse("entry "+sub, args, func(fs *flag.FlagSet) {
		fs.StringVar(&want, "entry", "", "entry id (only when this bot has several)")
		fs.BoolVar(&yes, "yes", false, "confirm (close only)")
		fs.BoolVar(&picked, "owner-picked", false, "close only: the owner just picked this entry by name from delete-menu in your private chat")
	})
	if e != nil {
		return e
	}
	if sub == "close" && !yes {
		return usageErrorf("entry close: this ends every joining bot's private line and the link. Re-run with --yes to confirm")
	}
	if picked && sub != "close" {
		return usageErrorf("entry %s: --owner-picked is only for entry close", sub)
	}
	sess, e := a.newSession(g)
	if e != nil {
		return e
	}
	ec, e := sess.pickEntry(want)
	if e != nil {
		return sess.closedEntryErr(e, sub)
	}
	argv := []string{"entry", sub, "--entry", ec.EntryID}
	if yes {
		argv = append(argv, "--yes")
	}
	if picked {
		argv = append(argv, "--owner-picked")
	}
	reason := map[string]string{
		"disable": "要暫停公開入口「" + sanitizeDisplayName(ec.Name) + "」（不再接受新的 Bot 加入，現有連線不受影響）",
		"rotate":  "要更換公開入口「" + sanitizeDisplayName(ec.Name) + "」的連結（舊連結立即失效，現有連線不受影響）",
		"close":   "要關閉公開入口「" + sanitizeDisplayName(ec.Name) + "」，所有加入的 Bot 的連線都會結束",
	}[sub]
	return sess.createHold(&heldItem{Kind: holdKindAdmin, Argv: argv, Reasons: []string{reason}})
}

func entryManage(a *app, sub string, args []string) *cliError {
	var want string
	var yes, picked bool
	g, _, e := a.parse("entry "+sub, args, func(fs *flag.FlagSet) {
		fs.StringVar(&want, "entry", "", "entry id (only when this bot has several)")
		fs.BoolVar(&yes, "yes", false, "confirm (close only)")
		fs.BoolVar(&picked, "owner-picked", false, "close only (see entry close --help)")
	})
	if e != nil {
		return e
	}
	sess, e := a.newSession(g)
	if e != nil {
		return e
	}
	ec, e := sess.pickEntry(want)
	if e != nil {
		if sub == "status" && want == "" {
			if rec := lookupClosedEntry(sess.ks.home, a.now().UnixMilli()); rec != nil {
				return sess.closedEntryStatus(rec)
			}
		}
		return sess.closedEntryErr(e, sub)
	}
	cli, e := sess.entryClient(ec)
	if e != nil {
		return e
	}
	ctx := context.Background()
	path := "/v1/entries/" + ec.EntryID
	en := prompts.IsEnglish(ec.Lang)
	name := sanitizeDisplayName(ec.Name)
	var resp entryResp
	var ce *cliError
	switch sub {
	case "status":
		ce = cli.do(ctx, http.MethodGet, path, nil, &resp)
	case "enable":
		ce = cli.do(ctx, http.MethodPatch, path, map[string]any{"status": "active"}, &resp)
	case "disable":
		ce = cli.do(ctx, http.MethodPatch, path, map[string]any{"status": "disabled"}, &resp)
	case "rotate":
		ce = cli.do(ctx, http.MethodPost, path+"/rotate-link", map[string]any{}, &resp)
	case "close":
		if !yes {
			return usageErrorf("entry close: re-run with --yes to confirm")
		}
		var out struct {
			BridgesClosed int  `json:"bridges_closed"`
			LinesEnded    *int `json:"lines_ended"` // v0.6.5 server
			closeOutcome
		}
		if ce = cli.do(ctx, http.MethodDelete, path, nil, &out); ce != nil && ce.Exit != exitClosed {
			return ce
		}
		n := sess.dropEntry(ec)
		nowMS := a.now().UnixMilli()
		recordClosedEntry(sess.ks.home, ec, nowMS)
		line := out.closeOutcome.addTo(map[string]any{"type": "entry_closed", "entry_id": ec.EntryID, "lines_ended": linesEnded(out.LinesEnded, out.BridgesClosed), "local_removed": n})
		if a.releasingItem != nil {
			// v0.6.1: the confirmation code stays in this bot-facing line; the
			// owner notice below stays plain (same rule as forget, v0.5.3).
			line["release_confirmed"] = fmt.Sprintf("關閉已於 %s 確認，確認碼 %s", time.UnixMilli(nowMS).Format("01/02 15:04"), shortHash(a.releasingItem.Hash))
		}
		sess.pr.emit(line)
		left := len(homeBridges(sess.ks.home))
		sess.pr.raw(pick(en,
			"NOTIFY_OWNER: Public entry \""+name+"\" is closed. The link no longer works and every joining bot's line has ended.",
			"NOTIFY_OWNER: 公開入口「"+name+"」已關閉。連結已失效，所有加入的 Bot 的連線都已結束。"))
		if out.Undelivered > 0 {
			sess.pr.raw("NOTIFY_OWNER: " + undeliveredText(out.closeOutcome, a.now(), en))
		}
		if left == 0 {
			_ = sess.cleanupHome()
			sess.pr.raw(pick(en,
				"NEXT: this bot has no bridge left: delete its \"bot-bridge inbox\" routine (do not pause it); no need to ask the owner.",
				"NEXT: 此 Bot 已無 bridge，刪掉「bot-bridge inbox」routine（不是暫停），不必再問主人。"))
			sess.pr.raw(dedicatedEndLine(en))
			sess.pr.raw(keepNameLine(ec.Handle, en))
		} else if sess.ks.liveCount() == 0 {
			// v0.6.2: only closed, not yet forgotten bridges are left.
			sess.pr.raw(noLiveRoutineLine(en))
		} else {
			sess.pr.raw(pick(en,
				"NEXT: keep the \"bot-bridge inbox\" routine; this bot's other bridges still use it.",
				"NEXT: 「bot-bridge inbox」routine 保留，其他 bridge 仍要用。"))
		}
		return sess.pr.done()
	}
	if ce != nil {
		return ce
	}
	if sub == "rotate" && resp.Entry.Link != "" {
		ec.Link = resp.Entry.Link
		_ = writeJSONAtomic(sess.ks.entryPath(ec.EntryID), ec)
	}
	out := map[string]any{
		"type": "entry", "entry_id": resp.Entry.EntryID, "link": resp.Entry.Link, "status": resp.Entry.Status,
		"open_bridges": resp.Entry.OpenBridges, "max_open": resp.Entry.MaxOpenBridges, "joins_per_hour": resp.Entry.JoinsPerHour,
		"fingerprint": resp.Entry.Fingerprint,
	}
	var lines []entryLineOut
	if sub == "status" {
		lines = sess.entryLines(ctx, cli, ec)
		out["lines"] = lines
	}
	sess.pr.emit(out)
	switch sub {
	case "status":
		for i, l := range lines {
			sess.pr.raw(entryLineText(i+1, l, en))
		}
	case "enable":
		sess.pr.raw(pick(en, "NOTIFY_OWNER: Public entry \""+name+"\" accepts new bots again.", "NOTIFY_OWNER: 公開入口「"+name+"」已恢復，可接受新的 Bot 加入。"))
	case "disable":
		sess.pr.raw(pick(en, "NOTIFY_OWNER: Public entry \""+name+"\" is paused: no new bots can join. Current lines keep working.", "NOTIFY_OWNER: 公開入口「"+name+"」已暫停：不接受新的 Bot 加入，現有連線照常。"))
	case "rotate":
		sess.pr.raw(pick(en, "NOTIFY_OWNER: New link for \""+name+"\": "+resp.Entry.Link+" The old link no longer works; current lines keep working.", "NOTIFY_OWNER: 「"+name+"」的新連結："+resp.Entry.Link+" 舊連結已失效，現有連線照常。"))
	}
	return sess.pr.done()
}

// entryLineOut is one open guest line in `entry status` (v0.6.1).
type entryLineOut struct {
	BridgeID    string `json:"bridge_id"`
	DisplayName string `json:"display_name"`
	Owner       string `json:"owner"`
	Handle      string `json:"handle"`
	JoinedAt    string `json:"joined_at"`
}

// entryLines lists the entry's open guest lines from the server, falling back
// to the local line folders when the server cannot be read.
func (s *session) entryLines(ctx context.Context, cli *client, ec *entryConfig) []entryLineOut {
	var resp struct {
		Bridges []struct {
			BridgeID string      `json:"bridge_id"`
			Guest    *cachedLand `json:"guest"`
		} `json:"bridges"`
	}
	out := []entryLineOut{}
	if cli.do(ctx, http.MethodGet, "/v1/entries/"+ec.EntryID+"/bridges", nil, &resp) == nil {
		for _, b := range resp.Bridges {
			l := entryLineOut{BridgeID: b.BridgeID}
			if b.Guest != nil {
				l.DisplayName = sanitizeDisplayName(b.Guest.DisplayName)
				l.Owner = sanitizeDisplayName(b.Guest.OwnerName)
				l.Handle = safeHandle(b.Guest.Handle)
				l.JoinedAt = b.Guest.JoinedAt
			}
			out = append(out, l)
		}
		return out
	}
	for _, bc := range s.ks.entryBridges(ec.EntryID) {
		l := entryLineOut{BridgeID: bc.BridgeID, DisplayName: sanitizeDisplayName(bc.Peer)}
		if bc.JoinedAt > 0 {
			l.JoinedAt = time.UnixMilli(bc.JoinedAt).UTC().Format(time.RFC3339)
		}
		out = append(out, l)
	}
	return out
}

// entryLineText is the readable form of one open line in `entry status`.
func entryLineText(n int, l entryLineOut, en bool) string {
	who := l.DisplayName
	if who == "" {
		who = l.Handle
	}
	if l.Owner != "" && l.Owner != who {
		who += pick(en, " ("+l.Owner+")", "（"+l.Owner+"）")
	}
	joined := ""
	if t, err := time.Parse(time.RFC3339, l.JoinedAt); err == nil {
		joined = t.Local().Format("01/02 15:04")
	}
	if en {
		return fmt.Sprintf("LINE: %d. %s · joined %s · %s", n, who, joined, l.BridgeID)
	}
	return fmt.Sprintf("LINE: %d. %s · 加入 %s · %s", n, who, joined, l.BridgeID)
}

// ---- closed-entry record (v0.6.1) ------------------------------------------
//
// After entry close the entry folder (and, with no bridge left, the bot's
// whole folder) is gone, so `entry status` would only say "no entry". A small
// record in the shared root, keyed by the bot's folder, keeps the entry's
// name, id and close time for closedEntryKeep so status can say it was
// closed. It holds no key, link or visitor data.

type closedEntryRec struct {
	Home    string `json:"home"`
	EntryID string `json:"entry_id"`
	Name    string `json:"name"`
	Lang    string `json:"lang,omitempty"`
	AtMS    int64  `json:"at_ms"`
}

const closedEntryKeep = 30 * 24 * 60 * 60 * 1000

func closedEntriesPath() string {
	root, err := defaultRoot()
	if err != nil {
		return ""
	}
	return filepath.Join(root, "closed-entries.json")
}

func loadClosedEntries(nowMS int64) []closedEntryRec {
	var rs []closedEntryRec
	if p := closedEntriesPath(); p != "" {
		_ = readJSON(p, &rs)
	}
	keep := rs[:0]
	for _, r := range rs {
		if nowMS-r.AtMS < closedEntryKeep {
			keep = append(keep, r)
		}
	}
	return keep
}

func saveClosedEntries(rs []closedEntryRec) {
	p := closedEntriesPath()
	if p == "" {
		return
	}
	if len(rs) == 0 {
		_ = os.Remove(p)
		return
	}
	if ensureDir(filepath.Dir(p)) == nil {
		_ = writeJSONAtomic(p, rs)
	}
}

func recordClosedEntry(home string, ec *entryConfig, nowMS int64) {
	home = filepath.Clean(home)
	rs := loadClosedEntries(nowMS)
	keep := rs[:0]
	for _, r := range rs {
		if r.Home != home {
			keep = append(keep, r)
		}
	}
	saveClosedEntries(append(keep, closedEntryRec{Home: home, EntryID: ec.EntryID, Name: ec.Name, Lang: ec.Lang, AtMS: nowMS}))
}

func lookupClosedEntry(home string, nowMS int64) *closedEntryRec {
	home = filepath.Clean(home)
	for _, r := range loadClosedEntries(nowMS) {
		if r.Home == home {
			return &r
		}
	}
	return nil
}

// clearClosedEntry drops home's record once it has a new entry.
func clearClosedEntry(home string, nowMS int64) {
	home = filepath.Clean(home)
	rs := loadClosedEntries(nowMS)
	keep := rs[:0]
	found := false
	for _, r := range rs {
		if r.Home == home {
			found = true
			continue
		}
		keep = append(keep, r)
	}
	if found {
		saveClosedEntries(keep)
	}
}

// closedEntryStatus answers `entry status` for an entry this bot closed.
func (s *session) closedEntryStatus(rec *closedEntryRec) *cliError {
	en := prompts.IsEnglish(rec.Lang)
	at := time.UnixMilli(rec.AtMS)
	s.pr.emit(map[string]any{"type": "entry", "entry_id": rec.EntryID, "status": "closed", "closed_at": at.UTC().Format(time.RFC3339), "source": "local_record", "lines": []entryLineOut{}})
	s.pr.raw(pick(en,
		"STATUS: public entry \""+sanitizeDisplayName(rec.Name)+"\" was closed on "+at.Format("01/02 15:04")+" (local record). The link no longer works; no line is open.",
		"STATUS: 公開入口「"+sanitizeDisplayName(rec.Name)+"」已於 "+at.Format("01/02 15:04")+" 關閉（本機紀錄）。連結已失效，沒有開著的連線。"))
	return s.pr.done()
}

// closedEntryErr turns no_entry into entry_closed_local when this bot's
// entry was closed recently, so a manage command says why.
func (s *session) closedEntryErr(e *cliError, sub string) *cliError {
	if e == nil || e.Code != "no_entry" {
		return e
	}
	rec := lookupClosedEntry(s.ks.home, s.app.now().UnixMilli())
	if rec == nil {
		return e
	}
	return &cliError{Code: "entry_closed_local", Exit: exitUsage, Message: "entry " + sub + ": this bot's public entry \"" + sanitizeDisplayName(rec.Name) + "\" was closed on " + time.UnixMilli(rec.AtMS).Format("01/02 15:04") + " (local record); entry create makes a new one"}
}

func pick(en bool, a, b string) string {
	if en {
		return a
	}
	return b
}

// dropEntry removes the entry folder and every local guest bridge of it.
func (s *session) dropEntry(ec *entryConfig) int {
	n := 0
	for _, bc := range s.ks.entryBridges(ec.EntryID) {
		s.dropGuestLine(bc) // folder, held items, unshown notices
		n++
	}
	_ = shredTree(s.ks.entryDir(ec.EntryID))
	if ents, err := os.ReadDir(s.ks.entriesDir()); err == nil && len(ents) == 0 {
		_ = os.Remove(s.ks.entriesDir())
	}
	if cfg, err := s.ks.loadConfig(); err == nil && cfg.DefaultBridge != "" {
		if _, err := os.Stat(s.ks.bridgeDir(cfg.DefaultBridge)); os.IsNotExist(err) {
			cfg.DefaultBridge = ""
			_ = s.ks.saveConfig(cfg)
		}
	}
	return n
}

// ---- guest side: join --entry --------------------------------------------

// parseEntryLink splits <server>/e/pe_… into the server base and link id.
func parseEntryLink(link string) (server, linkID string, err error) {
	u, perr := url.Parse(strings.TrimSpace(link))
	if perr != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "", "", &parseLinkError{"the entry link must be an http(s) URL"}
	}
	idx := strings.LastIndex(u.Path, "/e/")
	if idx < 0 {
		return "", "", &parseLinkError{"the entry link must contain /e/pe_…"}
	}
	linkID = strings.Trim(u.Path[idx+len("/e/"):], "/")
	if !entryLinkRe.MatchString(linkID) {
		return "", "", &parseLinkError{"the entry link does not contain a valid entry id"}
	}
	return u.Scheme + "://" + u.Host + u.Path[:idx], linkID, nil
}

type entryJoinResp struct {
	createJoinResp
	Entry struct {
		Name        string `json:"name"`
		Link        string `json:"link"`
		Fingerprint string `json:"fingerprint"`
		DisplayName string `json:"display_name"`
		Lang        string `json:"lang"`
	} `json:"entry"`
}

func cmdJoinEntry(a *app, g *globalFlags, link, handle, displayName, owner, expectFP string) *cliError {
	server, linkID, perr := parseEntryLink(link)
	if perr != nil {
		return usageErrorf("join: %v", perr)
	}
	norm := server + "/e/" + linkID
	if e := checkPlaceholderNames("join", handle, owner); e != nil {
		return e
	}
	if e := checkProfileNames(displayName, owner); e != nil {
		return e
	}
	home, handle, hce := onboardHome("join", firstNonEmpty(g.home, os.Getenv("BB_HOME")), handle, displayName, owner, "")
	if hce != nil {
		return hce
	}
	g.home = home
	prior := homeBridges(home)
	for _, bc := range prior {
		if bc.EntryLink == norm {
			return &cliError{Code: "already_joined", Exit: exitUsage, Message: "join: this bot already has a private line through this entry (" + bc.BridgeID + "); use it instead of joining again"}
		}
	}
	// RT-112: a service bot never joins its own public entry (it would talk to
	// itself in a loop), whatever handle or name it tries.
	for _, ec := range (&keystore{home: home, warn: func(string, ...any) {}}).listEntries() {
		if _, own, err := parseEntryLink(ec.Link); err == nil && own == linkID {
			return &cliError{Code: "own_entry", Exit: exitUsage, Message: "join: this is this bot's own public entry; other bots join it, the bot itself does not"}
		}
	}
	sess, e := a.newSession(g)
	if e != nil {
		return e
	}
	encPriv, encPub, signPriv, signPub, bindingSig, err := newKeypair()
	if err != nil {
		return generalErrorf("%v", err)
	}
	body := map[string]any{"land": landBlockBody{
		Profile: profileBody{Handle: handle, DisplayName: displayName, OwnerName: owner},
		Key:     keyInputBody{EncPub: encPub, SignPub: signPub, BindingSig: bindingSig},
	}}
	cli := newClient(&http.Client{Timeout: g.timeout}, server, "")
	var resp entryJoinResp
	if ce := cli.do(context.Background(), http.MethodPost, "/v1/e/"+linkID+"/join", body, &resp); ce != nil {
		switch ce.Code {
		case "entry_disabled":
			sess.pr.raw("NOTIFY_OWNER: 此公開入口目前不接受新的 Bot 加入。 / This public entry is not taking new bots right now.")
		case "entry_closed", "entry_not_found":
			// v0.6.1 (RT-116): a closed, replaced or unknown link reads the same.
			sess.pr.raw("NOTIFY_OWNER: 此連結已失效，請向提供連結的一方索取新的連結。 / This link is no longer active; ask whoever shared it for a current link.")
		case "entry_full":
			sess.pr.raw("NOTIFY_OWNER: 此公開入口目前客滿，請稍後再試。 / This public entry is full right now; try again later.")
		}
		_ = sess.pr.done()
		return ce
	}
	lang := resp.Entry.Lang
	if expectFP == "" {
		expectFP = resp.Entry.Fingerprint // the entry's own key, echoed by the server
	}
	gotAdmin, check, ce := persistNewBridge(sess, server, "member", handle, lang, signPriv, encPriv, &resp.createJoinResp, expectFP)
	if ce != nil {
		return ce
	}
	if bc, err := sess.ks.loadBridge(resp.Bridge.BridgeID); err == nil {
		bc.EntryLink = norm
		_ = sess.ks.saveBridge(bc)
	}
	routine := sess.routinePrompt()
	out := map[string]any{
		"type": "joined", "via": "public_entry", "handle": handle, "bridge_id": resp.Bridge.BridgeID,
		"land_id": resp.Land.LandID, "fingerprint": resp.Land.Fingerprint, "admin_fingerprint": gotAdmin,
		"admin_fingerprint_check": check,
	}
	if check != fpMismatch {
		out["routine_prompt"] = routine
	}
	sess.pr.emit(out)
	name := sanitizeDisplayName(resp.Entry.Name)
	if check == fpMismatch {
		sess.pr.raw(joinOwnerNotice(resp.Bridge.BridgeID, name, gotAdmin, check, lang))
		_ = sess.pr.done()
		return &cliError{Code: "admin_fingerprint_mismatch", Message: "join: the entry's key does not match; stop, do not create the routine, and tell the owner", Exit: exitGeneral}
	}
	if prompts.IsEnglish(lang) {
		sess.pr.raw("NOTIFY_OWNER: Connected to \"" + name + "\" through its public entry. This line is private; other joining bots cannot see it.")
		sess.pr.raw("NOTIFY_OWNER: " + ownerCautionEN)
	} else {
		sess.pr.raw("NOTIFY_OWNER: 已透過公開入口連上「" + name + "」。這是一條私人連線，其他加入的 Bot 看不到。")
		sess.pr.raw("NOTIFY_OWNER: " + ownerCautionZH)
	}
	sess.printRoutine(routine, len(prior), lang, sess.ks.entryCount() > 0)
	return sess.pr.done()
}

// entryRefuse is the error for per-bridge commands that do not apply to a
// public-entry bridge (its keys and link belong to the whole entry).
func entryRefuse(cmd string) *cliError {
	return &cliError{Code: "entry_bridge_fixed", Exit: exitUsage, Message: cmd + ": this is one joining bot's line of a public entry; its key, name and link belong to the entry (see bot-bridge-cli entry status)"}
}

// entryCount is how many entries this home has, for "no bridge left" checks.
func (ks *keystore) entryCount() int { return len(ks.listEntries()) }

// linesEnded is entry close's count of lines that ended (v0.6.5): the
// server's lines_ended (which includes lines closed just before in the same
// batch); an older server only reports bridges_closed.
func linesEnded(v *int, bridgesClosed int) int {
	if v != nil {
		return *v
	}
	return bridgesClosed
}
