package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// v0.6.1 items 2, 3, 7, 8, 9: the service side of one public entry.
func TestV061EntryFlow(t *testing.T) {
	f := setupEntry(t, "Amy", "Ben")
	// Item 7: the entry gets its own block before the per-line sections.
	for _, h := range f.guests {
		if out, e, c := runApproved(t, "send", "--home", h, "hello from "+filepath.Base(h)); c != exitOK {
			t.Fatalf("send: %d %s %s", c, e, out)
		}
	}
	out, _, _ := run(t, "tick", "--home", f.svc)
	hdr := strings.Index(out, "=== 公開入口「Harbor Bistro」 ===")
	rules := strings.Index(out, "公開入口規則")
	join := strings.Index(out, "新加入：")
	line := strings.Index(out, "=== 加入的 Bot ")
	if hdr < 0 || rules < hdr || join < rules || line < join || strings.Count(out, "=== 公開入口「Harbor Bistro」 ===") != 1 {
		t.Fatalf("entry block order (hdr %d rules %d join %d line %d):\n%s", hdr, rules, join, line, out)
	}

	// Item 2: status lists the open lines.
	out, _, c := run(t, "entry", "status", "--home", f.svc)
	if c != exitOK || strings.Count(out, "LINE: ") != 2 || !strings.Contains(out, "Amy Bot（Amy）") || !strings.Contains(out, f.bridges[1]) || !strings.Contains(out, `"lines":[`) {
		t.Fatalf("status lines:\n%s", out)
	}

	// Item 8: a plain close of one line is held and names the line.
	out, _, _ = run(t, "close", "--home", f.svc, "--bridge", f.bridges[0], "--yes")
	if !strings.Contains(out, `"type":"held"`) || !strings.Contains(out, "要關閉與「Amy Bot（Amy）」的連線（公開入口和其他連線不受影響）") {
		t.Fatalf("held line close:\n%s", out)
	}
	rel, e, c := approveHeld(t, f.svc, out)
	if c != exitOK || !strings.Contains(rel, "與 Amy Bot（Amy） 的連線已關閉") || !strings.Contains(rel, "NOTIFY_OWNER: 這項變更已於 ") {
		t.Fatalf("release line close: %d %s\n%s", c, e, rel)
	}
	// Item 9: Amy's side says the line closed, not the entry.
	out, _, _ = run(t, "tick", "--home", f.guests[0])
	if !strings.Contains(out, "NOTIFY_OWNER: 與「Harbor Bistro」的連線已由對方關閉，資料已自動刪除。") || strings.Contains(out, "公開入口已關閉") {
		t.Fatalf("guest A notice:\n%s", out)
	}
	// The release was shown at once; the next service tick does not repeat it.
	if out, _, _ := run(t, "tick", "--home", f.svc); strings.Contains(out, "已放行") {
		t.Fatalf("release notice repeated:\n%s", out)
	}

	// The service bot also has a 1:1 bridge, so the folder stays after close.
	link, code, cout, _ := claimFresh(t, f.url)
	fp := findLine(t, cout, "claimed")["fingerprint"].(string)
	if out, e, c := run(t, "join", "--home", f.svc, "--link", link, "--code", code, "--admin-fp", fp); c != exitOK {
		t.Fatalf("join 1:1: %d %s %s", c, e, out)
	} else if !strings.Contains(out, "此 Bot 已有公開入口，所以已有「bot-bridge inbox」routine：沿用它") {
		// Item 5.
		t.Fatalf("routine NEXT with an entry:\n%s", out)
	}

	// Item 3: entry close says what to do with the routine and shows the
	// release at once.
	out, _, _ = run(t, "entry", "close", "--home", f.svc, "--yes")
	if !strings.Contains(out, "要關閉公開入口「Harbor Bistro」") {
		t.Fatalf("entry close hold:\n%s", out)
	}
	rel, e, c = approveHeld(t, f.svc, out)
	if c != exitOK || !strings.Contains(rel, "NOTIFY_OWNER: 公開入口「Harbor Bistro」已關閉。") ||
		!strings.Contains(rel, "NEXT: 「bot-bridge inbox」routine 保留，其他 bridge 仍要用。") ||
		!strings.Contains(rel, "NOTIFY_OWNER: 這項變更已於 ") || !strings.Contains(rel, `"release_confirmed":"關閉已於 `) {
		t.Fatalf("entry close release: %d %s\n%s", c, e, rel)
	}
	if strings.Contains(rel, "確認碼") && strings.Contains(rawLine(rel, "NOTIFY_OWNER: 這項變更"), "確認碼") {
		t.Fatalf("code in owner line:\n%s", rel)
	}
	if out, _, _ := run(t, "tick", "--home", f.svc); strings.Contains(out, "已放行") {
		t.Fatalf("entry release repeated:\n%s", out)
	}
	// Item 9: Ben's side says the whole entry closed.
	out, _, _ = run(t, "tick", "--home", f.guests[1])
	if !strings.Contains(out, "NOTIFY_OWNER: 「Harbor Bistro」的公開入口已關閉，這條連線也已結束，資料已自動刪除。") {
		t.Fatalf("guest B notice:\n%s", out)
	}
	// Item 2: status after close reads the local record.
	out, e, c = run(t, "entry", "status", "--home", f.svc)
	if c != exitOK || !strings.Contains(out, "STATUS: 公開入口「Harbor Bistro」已於 ") || !strings.Contains(out, `"status":"closed"`) {
		t.Fatalf("status after close: %d %s\n%s", c, e, out)
	}
	if _, e, c := run(t, "entry", "rotate", "--home", f.svc); c == exitOK || !strings.Contains(e, "entry_closed_local") {
		t.Fatalf("rotate after close: %d %s", c, e)
	}
	// A new entry clears the record.
	if _, e, c := run(t, "entry", "create", "--home", f.svc, "--server", f.url, "--name", "Desk Two"); c != exitOK {
		t.Fatalf("entry create again: %d %s", c, e)
	}
	if lookupClosedEntry(f.svc, time.Now().UnixMilli()) != nil {
		t.Fatal("closed record kept after a new entry")
	}
}

// Item 3: with no bridge left, entry close says to delete the routine and
// keep the name; status still answers from the local record.
func TestV061EntryCloseLast(t *testing.T) {
	f := setupEntry(t)
	out, _, _ := run(t, "entry", "close", "--home", f.svc, "--yes")
	rel, e, c := approveHeld(t, f.svc, out)
	if c != exitOK || !strings.Contains(rel, "NEXT: 此 Bot 已無 bridge，刪掉「bot-bridge inbox」routine（不是暫停），不必再問主人。") ||
		!strings.Contains(rel, "NEXT: 此 Bot 在 bridge 上的名稱是 ") || !strings.Contains(rel, "NOTIFY_OWNER: 這項變更已於 ") {
		t.Fatalf("close last: %d %s\n%s", c, e, rel)
	}
	if _, err := os.Stat(f.svc); !os.IsNotExist(err) {
		t.Fatalf("folder kept: %v", err)
	}
	out, _, c = run(t, "entry", "status", "--home", f.svc)
	if c != exitOK || !strings.Contains(out, "（本機紀錄）") {
		t.Fatalf("status: %d\n%s", c, out)
	}
	if _, err := os.Stat(f.svc); !os.IsNotExist(err) {
		t.Fatalf("status recreated the folder")
	}
}

// Item 4: delete-menu lists each line by the joining bot and the entry last;
// picking the entry is owner-picked (no card).
func TestV061DeleteMenuEntry(t *testing.T) {
	f := setupEntry(t, "Amy")
	if out, _, _ := run(t, "tick", "--home", f.svc); !strings.Contains(out, "新加入") {
		t.Fatalf("tick:\n%s", out)
	}
	out, e, c := run(t, "delete-menu", "--home", f.svc)
	if c != exitOK {
		t.Fatalf("menu: %d %s", c, e)
	}
	m := menuBlock(t, out)
	t.Logf("menu:\n%s", out)
	if len(m) < 5 || !strings.HasPrefix(m[0], "目前有 2 項") ||
		!strings.HasPrefix(m[1], "1. 關閉與 Amy Bot（Amy） 的連線（公開入口「Harbor Bistro」）（") ||
		!strings.HasPrefix(m[2], "2. 關閉公開入口「Harbor Bistro」（連結失效，1 條連線一起結束：Amy Bot（Amy））") ||
		!strings.HasPrefix(m[3], "3. 全部刪除（2 項") {
		t.Fatalf("menu lines: %q", m)
	}
	var lineOpt, entryOpt map[string]any
	for _, l := range strings.Split(out, "\n") {
		if !strings.Contains(l, `"type":"delete_option"`) {
			continue
		}
		o := findLine(t, l, "delete_option")
		switch o["kind"] {
		case "entry_line":
			lineOpt = o
		case "entry":
			entryOpt = o
		}
	}
	if lineOpt == nil || lineOpt["forget_command"] != "" || !strings.HasSuffix(lineOpt["end_command"].(string), "close --bridge "+f.bridges[0]+" --yes --owner-picked") {
		t.Fatalf("line option: %v", lineOpt)
	}
	if entryOpt == nil || entryOpt["forget_command"] != "" || !strings.Contains(entryOpt["end_command"].(string), "entry close --entry en_") || !strings.HasSuffix(entryOpt["end_command"].(string), "--yes --owner-picked") {
		t.Fatalf("entry option: %v", entryOpt)
	}
	if !strings.Contains(rawLine(out, "ALL_CONFIRM: "), "這 2 項全部刪除？") {
		t.Fatalf("all confirm: %s", rawLine(out, "ALL_CONFIRM: "))
	}
	// Run the entry's end_command as the bot would.
	args := strings.Fields(entryOpt["end_command"].(string))
	args = args[2:] // drop BB_HOME=… and the binary name
	args = append(args, "--home", f.svc)
	out, e, c = run(t, args...)
	if c != exitOK || !strings.Contains(out, "OWNER_ALREADY_CONFIRMED: ") || strings.Contains(out, "NOTIFY_OWNER: 有一項") ||
		!strings.Contains(out, "公開入口已關閉，連結已失效，所有連線都已結束") || strings.Contains(out, " --bridge  ") {
		t.Fatalf("owner-picked entry close: %d %s\n%s", c, e, out)
	}
	if rel, e, c := approveHeld(t, f.svc, out); c != exitOK || !strings.Contains(rel, "已關閉") {
		t.Fatalf("release: %d %s %s", c, e, rel)
	}
}

// Item 6: update needs no bot folder and never creates or picks one.
func TestV061UpdateWithoutHome(t *testing.T) {
	h := t.TempDir()
	t.Setenv("HOME", h)
	t.Setenv("BB_HOME", "")
	if err := os.MkdirAll(filepath.Join(h, ".bot-bridge", "bots", "other-bot-ab12"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(h, ".bot-bridge", "bots", "other-bot-ab12", "config.json"), []byte(`{"handle":"other-bot-ab12"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	a := &app{version: "test", stdout: &out, stderr: &out, now: time.Now}
	s, ce := a.updateSession(&globalFlags{})
	if ce != nil || s.ks.home != filepath.Join(h, ".bot-bridge") {
		t.Fatalf("no BB_HOME: %v %v", ce, s)
	}
	gone := filepath.Join(h, ".bot-bridge", "bots", "gone-bot-zz99")
	t.Setenv("BB_HOME", gone)
	s, ce = a.updateSession(&globalFlags{})
	if ce != nil || s.ks.home != filepath.Join(h, ".bot-bridge") {
		t.Fatalf("gone BB_HOME: %v %v", ce, s)
	}
	if _, err := os.Stat(gone); !os.IsNotExist(err) {
		t.Fatal("gone folder created")
	}
	// The home_required wording no longer tells the bot to delete routines.
	t.Setenv("BB_HOME", "")
	_, e, c := run(t, "bridges", "--offline")
	if c != exitUsage || !strings.Contains(e, "home_required") || strings.Contains(e, "delete its") {
		t.Fatalf("home_required: %d %s", c, e)
	}
}

// Item 1 on the CLI side: joining a closed link reads "no longer active".
func TestV061JoinClosedLink(t *testing.T) {
	f := setupEntry(t)
	out, _, _ := run(t, "entry", "close", "--home", f.svc, "--yes")
	if _, e, c := approveHeld(t, f.svc, out); c != exitOK {
		t.Fatalf("close: %s", e)
	}
	out, _, c := run(t, "join", "--home", t.TempDir(), "--entry", f.link, "--display-name", "Dee Bot", "--owner", "Dee")
	if c == exitOK || !strings.Contains(out, "NOTIFY_OWNER: 此連結已失效，請向提供連結的一方索取新的連結。") {
		t.Fatalf("join closed link: %d\n%s", c, out)
	}
}

// A line that joined after the last tick is listed by entry status; closing it
// by id must work before any tick has created its local folder.
func TestV061CloseLineBeforeTick(t *testing.T) {
	f := setupEntry(t, "Amy", "Ben")
	br := f.bridges[1]
	out, e, c := runApproved(t, "close", "--home", f.svc, "--bridge", br, "--yes")
	if c != exitOK || !strings.Contains(out, `"type":"closed"`) {
		t.Fatalf("close before tick: %d %s %s", c, e, out)
	}
	out, _, _ = run(t, "entry", "status", "--home", f.svc)
	if strings.Contains(out, br) || !strings.Contains(out, f.bridges[0]) {
		t.Fatalf("status after close:\n%s", out)
	}
	// An id that is not this entry's line is still refused.
	if _, e, c := run(t, "close", "--home", f.svc, "--bridge", "br_AAAAAAAAAAAAAAAAAAAAAA", "--yes"); c == exitOK || !strings.Contains(e, "not in the local state") {
		t.Fatalf("foreign id: %d %s", c, e)
	}
}
