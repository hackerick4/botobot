package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// cmdArgs turns a printed "BB_HOME=… bot-bridge-cli …" line into run args.
func cmdArgs(t *testing.T, line, home string) []string {
	t.Helper()
	f := strings.Fields(line)
	if len(f) < 3 || !strings.HasPrefix(f[0], "BB_HOME=") {
		t.Fatalf("command line: %q", line)
	}
	return append(f[2:], "--home", home)
}

// joinOneToOne adds a two-bot bridge to home (this bot joins it).
func joinOneToOne(t *testing.T, url, home string) string {
	t.Helper()
	link, code, cout, _ := claimFresh(t, url)
	fp := findLine(t, cout, "claimed")["fingerprint"].(string)
	out, e, c := run(t, "join", "--home", home, "--link", link, "--code", code, "--admin-fp", fp)
	if c != exitOK {
		t.Fatalf("join 1:1: %d %s %s", c, e, out)
	}
	return findLine(t, out, "joined")["bridge_id"].(string)
}

// Finding 1: "remove all" holds every removal with one command and releases
// them with one line after one wait.
func TestV062DeleteAllOneWait(t *testing.T) {
	f := setupEntry(t, "Amy", "Ben")
	pair := joinOneToOne(t, f.url, f.svc)
	run(t, "tick", "--home", f.svc)
	out, e, c := run(t, "delete-menu", "--home", f.svc)
	if c != exitOK {
		t.Fatalf("menu: %d %s", c, e)
	}
	all := rawLine(out, "ALL_COMMAND: ")
	if !strings.HasSuffix(all, " bridges --delete-menu --all --owner-picked") ||
		!strings.Contains(rawLine(out, "NEXT: "), "只執行 ALL_COMMAND: 後那一行（不要逐項執行 end_command），再執行它印出的那一行 release（只需等一次，最多約 1 分鐘）") {
		t.Fatalf("menu:\n%s", out)
	}
	out, e, c = run(t, cmdArgs(t, all, f.svc)...)
	held := findLine(t, out, "held_all")
	if c != exitOK || held["count"].(float64) != 4 || strings.Contains(out, `"type":"held"`) ||
		strings.Count(out, " release --batch ") != 1 || !strings.Contains(out, "OWNER_ALREADY_CONFIRMED: 主人已在私人對話中選了「全部刪除」") {
		t.Fatalf("delete all: %d %s\n%s", c, e, out)
	}
	if n := len(mustGlob(t, filepath.Join(f.svc, "held", "h_*.json"))); n != 4 {
		t.Fatalf("held files: %d", n)
	}
	relLine := rawLine(out, "NEXT: ")
	// Run the release fresh (no ageing): it waits exactly once for all four.
	before := sleptNS.Load()
	rel, e, c := run(t, cmdArgs(t, relLine, f.svc)...)
	if c != exitOK || strings.Count(rel, `"type":"waiting"`) != 1 || sleptNS.Load() == before {
		t.Fatalf("batch release: %d %s\n%s", c, e, rel)
	}
	for _, want := range []string{
		"與 Amy Bot（Amy） 的連線已關閉", "與 Ben Bot（Ben） 的連線已關閉",
		"NOTIFY_OWNER: 公開入口「Harbor Bistro」已關閉。",
		"forget --bridge " + pair + " --yes",
		// Finding 3: the left 1:1 bridge is closed, not "in use".
		"NEXT: 此 Bot 已無使用中的 bridge，刪掉「bot-bridge inbox」routine（不是暫停），不必再問主人。",
	} {
		if !strings.Contains(rel, want) {
			t.Fatalf("batch release lacks %q:\n%s", want, rel)
		}
	}
	if strings.Contains(rel, "其他 bridge 仍要用") {
		t.Fatalf("keep routine with only a closed bridge left:\n%s", rel)
	}
	if n := len(mustGlob(t, filepath.Join(f.svc, "held", "h_*.json"))); n != 0 {
		t.Fatalf("held left: %d", n)
	}
	// The forget then removes the last one and the folder.
	fo, e, c := run(t, "forget", "--home", f.svc, "--bridge", pair, "--yes")
	if c != exitOK || !strings.Contains(fo, "NEXT: 此 Bot 已無 bridge，刪掉") {
		t.Fatalf("forget: %d %s\n%s", c, e, fo)
	}
	// Finding 2 on the joining side: closed by the other side when only the
	// line went; Ben's line ended with the entry here, so either wording is
	// allowed for him, never the bare one.
	out, _, _ = run(t, "tick", "--home", f.guests[0])
	if strings.Contains(out, "的連線已關閉，資料已自動刪除") {
		t.Fatalf("bare line notice:\n%s", out)
	}
}

func mustGlob(t *testing.T, p string) []string {
	t.Helper()
	m, err := filepath.Glob(p)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

// Finding 1, safety: a bad code releases nothing; the flags are checked.
func TestV062BatchReleaseChecks(t *testing.T) {
	f := setupEntry(t, "Amy", "Ben")
	run(t, "tick", "--home", f.svc)
	if out, _, _ := run(t, "delete-menu", "--home", f.svc, "--lang", "en"); !strings.HasSuffix(rawLine(out, "ALL_COMMAND: "), " bridges --delete-menu --all --owner-picked --lang en") ||
		!strings.Contains(out, "run only the ALL_COMMAND: line (not the end_commands one by one), then the one release line it prints (it waits only once, up to about a minute)") {
		t.Fatalf("en menu:\n%s", out)
	}
	if _, e, c := run(t, "bridges", "--home", f.svc, "--delete-menu", "--all"); c == exitOK || !strings.Contains(e, "--all and --owner-picked go together") {
		t.Fatalf("--all alone: %d %s", c, e)
	}
	if _, e, c := run(t, "bridges", "--home", f.svc, "--all", "--owner-picked"); c == exitOK {
		t.Fatalf("--all without menu: %d %s", c, e)
	}
	out, e, c := run(t, "bridges", "--home", f.svc, "--delete-menu", "--all", "--owner-picked")
	if c != exitOK {
		t.Fatalf("delete all: %d %s", c, e)
	}
	relLine := rawLine(out, "NEXT: ")
	i := strings.Index(relLine, "--batch ")
	pairs := strings.Fields(relLine[i+len("--batch "):])[0]
	ps := strings.Split(pairs, ",")
	if len(ps) != 3 {
		t.Fatalf("pairs: %q", pairs)
	}
	bad := ps[0] + "," + strings.Split(ps[1], ":")[0] + ":000000000000," + ps[2]
	for _, tc := range []struct{ args []string }{
		{[]string{"release", "--home", f.svc, "--batch", bad, "--owner-approved"}},
		{[]string{"release", "--home", f.svc, "--batch", ps[0] + "," + ps[0], "--owner-approved"}},
		{[]string{"release", "--home", f.svc, "--batch", pairs}},
		{[]string{"release", "--home", f.svc, "--batch", pairs, "--hash", "abcdefabcdef", "--owner-approved"}},
		{[]string{"release", "--home", f.svc, "--batch", "h_zz:1", "--owner-approved"}},
		{[]string{"release", "--home", f.svc, "--batch", strings.TrimSuffix(strings.Repeat(ps[0]+",", 51), ","), "--owner-approved"}},
	} {
		if _, e, c := run(t, tc.args...); c == exitOK {
			t.Fatalf("%v accepted: %s", tc.args, e)
		}
	}
	if n := len(mustGlob(t, filepath.Join(f.svc, "held", "h_*.json"))); n != 3 {
		t.Fatalf("held after refusals: %d", n)
	}
	// --no-wait refuses while fresh and says one wait.
	if _, e, c := run(t, "release", "--home", f.svc, "--batch", pairs, "--owner-approved", "--no-wait"); c == exitOK || !strings.Contains(e, "release_too_soon") {
		t.Fatalf("no-wait: %d %s", c, e)
	}
	rel, e, c := run(t, "release", "--home", f.svc, "--batch", pairs, "--owner-approved")
	if c != exitOK || strings.Count(rel, `"type":"waiting"`) != 1 || strings.Count(rel, "WARNING: ") != 3 {
		t.Fatalf("aged batch: %d %s\n%s", c, e, rel)
	}
	if _, err := os.Stat(f.svc); !os.IsNotExist(err) {
		t.Fatalf("folder kept after closing everything: %v", err)
	}
}

// Finding 3: a closed, not yet forgotten bridge does not keep the routine.
func TestV062RoutineIgnoresClosed(t *testing.T) {
	url := startTestServer(t)
	h := t.TempDir()
	a := joinOneToOne(t, url, h)
	b := joinOneToOne(t, url, h)
	// Leave both (owner-picked, released), forget only the first.
	for _, bid := range []string{a, b} {
		out, _, _ := run(t, "leave", "--home", h, "--bridge", bid, "--owner-picked")
		if rel, e, c := approveHeld(t, h, out); c != exitOK {
			t.Fatalf("leave %s: %d %s %s", bid, c, e, rel)
		}
	}
	out, e, c := run(t, "forget", "--home", h, "--bridge", a, "--yes")
	if c != exitOK || !strings.Contains(out, "NEXT: 此 Bot 已無使用中的 bridge，刪掉「bot-bridge inbox」routine（不是暫停），不必再問主人。") || strings.Contains(out, "仍要用") {
		t.Fatalf("forget with only a closed bridge left: %d %s\n%s", c, e, out)
	}
	// Joining again after that: no "this bot has other bridges".
	link, code, cout, _ := claimFresh(t, url)
	fp := findLine(t, cout, "claimed")["fingerprint"].(string)
	out, e, c = run(t, "join", "--home", h, "--link", link, "--code", code, "--admin-fp", fp)
	if c != exitOK || strings.Contains(out, "此 Bot 還有別的 bridge") || !strings.Contains(out, "NEXT: 查看此 Bot 的 routine：已有「bot-bridge inbox」routine 時沿用") {
		t.Fatalf("join after closed bridges: %d %s\n%s", c, e, out)
	}
	// With a live bridge the routine stays.
	out, e, c = run(t, "forget", "--home", h, "--bridge", b, "--yes")
	if c != exitOK || !strings.Contains(out, "NEXT: 「bot-bridge inbox」routine 保留，其他 bridge 仍要用。") {
		t.Fatalf("forget with a live bridge: %d %s\n%s", c, e, out)
	}
}

// Finding 2: the joining bot's notice texts.
func TestV062GuestLineNotice(t *testing.T) {
	for _, tc := range []struct {
		entry, other, en bool
		want             string
	}{
		{false, true, false, "NOTIFY_OWNER: 與「X」的連線已由對方關閉，資料已自動刪除。"},
		{false, true, true, "NOTIFY_OWNER: The line with \"X\" was closed by the other side. Its data was deleted automatically."},
		{false, false, false, "NOTIFY_OWNER: 與「X」的連線已關閉，資料已自動刪除。"},
		{true, true, false, "NOTIFY_OWNER: 「X」的公開入口已關閉，這條連線也已結束，資料已自動刪除。"},
	} {
		if got := guestLineClosedNotice("X", tc.entry, tc.other, tc.en); got != tc.want {
			t.Errorf("%+v: %q", tc, got)
		}
	}
	if !endedClosed(&tickState{Ended: endedSelf}) || !endedClosed(&tickState{Ended: "已由管理者關閉"}) ||
		endedClosed(&tickState{Ended: "無法連線：此 Bot 可能已不在這個 bridge，或金鑰已失效"}) || endedClosed(&tickState{}) {
		t.Fatal("endedClosed")
	}
}

// RT-121: changing one held item after delete-menu --all (here: its command)
// makes the whole batch refuse before anything runs.
func TestV062BatchTamperedItem(t *testing.T) {
	f := setupEntry(t, "Amy")
	run(t, "tick", "--home", f.svc)
	out, e, c := run(t, "bridges", "--home", f.svc, "--delete-menu", "--all", "--owner-picked")
	if c != exitOK {
		t.Fatalf("delete all: %d %s", c, e)
	}
	relLine := rawLine(out, "NEXT: ")
	pairs := strings.Fields(relLine[strings.Index(relLine, "--batch ")+len("--batch "):])[0]
	last := strings.Split(strings.Split(pairs, ",")[1], ":")[0]
	p := filepath.Join(f.svc, "held", last+".json")
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	nb := strings.Replace(string(b), `"close"`, `"disable"`, 1)
	if nb == string(b) {
		t.Fatalf("no argv to tamper: %s", b)
	}
	if err := os.WriteFile(p, []byte(nb), 0o600); err != nil {
		t.Fatal(err)
	}
	rel, e, c := run(t, "release", "--home", f.svc, "--batch", pairs, "--owner-approved")
	if c == exitOK || !strings.Contains(e, "hash_mismatch") || strings.Contains(rel, `"type":"closed"`) {
		t.Fatalf("tampered batch: %d %s\n%s", c, e, rel)
	}
	if n := len(mustGlob(t, filepath.Join(f.svc, "held", "h_*.json"))); n != 2 {
		t.Fatalf("held after refusal: %d", n)
	}
}
