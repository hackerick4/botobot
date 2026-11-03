package cli

import (
	"os"
	"strings"
	"testing"
)

type entryFixture struct {
	url, svc, link string
	guests         []string // homes
	bridges        []string // guest bridge ids, same order
}

func setupEntry(t *testing.T, names ...string) entryFixture {
	t.Helper()
	url := startTestServer(t)
	svc := t.TempDir()
	out, errOut, code := run(t, "entry", "create", "--home", svc, "--server", url,
		"--name", "Harbor Bistro", "--display-name", "Bistro Bot", "--owner", "Lin")
	if code != exitOK {
		t.Fatalf("entry create exit %d: %s", code, errOut)
	}
	ec := findLine(t, out, "entry_created")
	link, _ := ec["link"].(string)
	if !strings.Contains(link, "/e/pe_") {
		t.Fatalf("link = %q", link)
	}
	if !strings.Contains(out, "NOTIFY_OWNER: ") {
		t.Fatalf("no owner notice:\n%s", out)
	}
	f := entryFixture{url: url, svc: svc, link: link}
	for _, n := range names {
		h := t.TempDir()
		out, errOut, code := run(t, "join", "--home", h, "--entry", link,
			"--display-name", n+" Bot", "--owner", n)
		if code != exitOK {
			t.Fatalf("join %s exit %d: %s\n%s", n, code, errOut, out)
		}
		j := findLine(t, out, "joined")
		f.guests = append(f.guests, h)
		f.bridges = append(f.bridges, j["bridge_id"].(string))
	}
	return f
}

func TestV060EntryOneToMany(t *testing.T) {
	f := setupEntry(t, "Amy", "Ben", "Chen")
	msgs := []string{
		"Table for 2 on Friday 19:00 please. Phone 0912-345-678.",
		"Need a table for 4 on Saturday at 18:30. Email ben@example.com",
		"Booking for 3, Sunday lunch 12:00, call 0987 654 321",
	}
	for i, h := range f.guests {
		if out, errOut, code := runApproved(t, "send", "--home", h, msgs[i]); code != exitOK {
			t.Fatalf("guest %d send exit %d: %s\n%s", i, code, errOut, out)
		}
	}
	// One tick shows all three, each under its own line.
	out, errOut, code := run(t, "tick", "--home", f.svc)
	if code != exitOK {
		t.Fatalf("tick exit %d: %s", code, errOut)
	}
	t.Logf("service tick:\n%s", out)
	for i, b := range f.bridges {
		if !strings.Contains(out, b) {
			t.Errorf("tick does not name guest line %s", b)
		}
		if !strings.Contains(out, strings.Fields(msgs[i])[0]) {
			t.Errorf("tick missing guest %d text", i)
		}
	}
	if strings.Count(out, "公開入口規則") != 1 {
		t.Errorf("entry rules not printed exactly once")
	}
	if strings.Count(out, "新加入：") != 3 || strings.Count(out, "可疑的那條") != 1 {
		t.Errorf("want 3 join notices")
	}
	// Second tick: nothing new.
	out, _, _ = run(t, "tick", "--home", f.svc)
	if strings.TrimSpace(lastLine(out)) != "NO_NEW_MESSAGES" {
		t.Errorf("second tick:\n%s", out)
	}
	// Replies go only to their own guest.
	replies := []string{"Confirmed: Friday 19:00 for 2.", "Confirmed: Saturday 18:30 for 4.", "Confirmed: Sunday 12:00 for 3."}
	for i, b := range f.bridges {
		if out, errOut, code := runApproved(t, "send", "--home", f.svc, "--bridge", b, replies[i]); code != exitOK || !strings.Contains(out, `"type":"sent"`) {
			t.Fatalf("reply %d exit %d: %s\n%s", i, code, errOut, out)
		}
	}
	for i, h := range f.guests {
		out, _, _ := run(t, "tick", "--home", h)
		if !strings.Contains(out, replies[i]) {
			t.Errorf("guest %d did not get its reply:\n%s", i, out)
		}
		for j := range replies {
			if j != i && strings.Contains(out, replies[j]) {
				t.Errorf("guest %d saw guest %d's reply", i, j)
			}
		}
	}
	// Leak guard: a reply to Ben naming Amy, or carrying Amy's phone, waits.
	for _, txt := range []string{"Amy also booked Friday.", "Call 0912345678 to confirm."} {
		out, _, _ := run(t, "send", "--home", f.svc, "--bridge", f.bridges[1], txt)
		if !strings.Contains(out, `"type":"held"`) || !strings.Contains(out, "其他 Bot 的資料") {
			t.Errorf("leak %q not held:\n%s", txt, out)
		}
	}
	// Ben's own email back to Ben is fine.
	if out, _, _ := run(t, "send", "--home", f.svc, "--bridge", f.bridges[1], "We will mail ben@example.com."); strings.Contains(out, "其他 Bot 的資料") {
		t.Errorf("own data held as leak:\n%s", out)
	}

	// Closing Ben's line leaves Amy and Chen working.
	if out, errOut, code := runApproved(t, "close", "--home", f.svc, "--bridge", f.bridges[1], "--yes"); code != exitOK {
		t.Fatalf("close exit %d: %s\n%s", code, errOut, out)
	} else if !strings.Contains(out, "其他連線不受影響") || strings.Contains(out, "NEXT:") {
		t.Errorf("close of one line:\n%s", out)
	}
	for _, i := range []int{0, 2} {
		if out, errOut, code := runApproved(t, "send", "--home", f.guests[i], "still there?"); code != exitOK {
			t.Fatalf("guest %d after close: %d %s %s", i, code, errOut, out)
		}
	}
	out, _, _ = run(t, "tick", "--home", f.svc)
	if strings.Count(out, "still there?") != 2 {
		t.Errorf("after close tick:\n%s", out)
	}

	// Disable is held for the owner; once released new joins stop, lines stay.
	if out, errOut, code := runApproved(t, "entry", "disable", "--home", f.svc); code != exitOK {
		t.Fatalf("disable: %d %s %s", code, errOut, out)
	}
	h := t.TempDir()
	if out, _, code := run(t, "join", "--home", h, "--entry", f.link, "--display-name", "Dee Bot", "--owner", "Dee"); code == exitOK {
		t.Errorf("join after disable succeeded:\n%s", out)
	}
	if _, _, code := runApproved(t, "send", "--home", f.guests[0], "ok after pause?"); code != exitOK {
		t.Errorf("existing guest blocked by disable")
	}

	// Rotate: the old link stops working at once.
	if out, errOut, code := runApproved(t, "entry", "enable", "--home", f.svc); code != exitOK {
		t.Fatalf("enable: %d %s %s", code, errOut, out)
	}
	if out, errOut, code := runApproved(t, "entry", "rotate", "--home", f.svc); code != exitOK {
		t.Fatalf("rotate: %d %s %s", code, errOut, out)
	}
	st, _, _ := run(t, "entry", "status", "--home", f.svc)
	if strings.Contains(st, f.link) {
		t.Errorf("status still shows the old link:\n%s", st)
	}
	if out, _, code := run(t, "join", "--home", t.TempDir(), "--entry", f.link, "--display-name", "Eve Bot", "--owner", "Eve"); code == exitOK {
		t.Errorf("old link still joins:\n%s", out)
	}

	// Guest leaves: the service sees a short notice and the line is removed.
	if out, errOut, code := runApproved(t, "leave", "--home", f.guests[2], "--bridge", f.bridges[2], "--yes"); code != exitOK {
		t.Fatalf("leave: %d %s %s", code, errOut, out)
	}
	out, _, _ = run(t, "tick", "--home", f.svc)
	t.Logf("after leave:\n%s", out)
	if !strings.Contains(out, "已結束連線") {
		t.Errorf("no leave notice:\n%s", out)
	}

	// Close the whole entry.
	if out, errOut, code := runApproved(t, "entry", "close", "--home", f.svc, "--yes"); code != exitOK {
		t.Fatalf("entry close: %d %s %s", code, errOut, out)
	} else {
		t.Logf("entry close:\n%s", out)
	}
	if out, _, _ := run(t, "tick", "--home", f.svc); !strings.Contains(out, "NO_BRIDGES") {
		t.Errorf("after entry close:\n%s", out)
	}
	if _, err := os.Stat(f.svc); !os.IsNotExist(err) {
		ents, _ := os.ReadDir(f.svc)
		t.Errorf("service folder left behind: %v", ents)
	}
}

func TestV060GuestIndex(t *testing.T) {
	gi := &guestIndex{Salt: "s", Bridges: map[string]*guestRec{}}
	gi.addNames("br_a", "Amy Bot", "Amy", "王小明")
	gi.addNames("br_b", "Ben")
	gi.addText("br_a", "phone +886 912-345-678", 1)
	cases := map[string]bool{
		"Amy booked too":           true,
		"amy's table":              true,
		"Amyx is a word":           false,
		"王小明訂了位":                   true,
		"call 0912345678":          true,
		"table 12 at 19:00":        false,
		"see you Ben":              false, // Ben's own name
		"Amy Bot here":             true,
		"booking ref 886912345678": true,
		"請撥 ０９１２３４５６７８":            true, // RT-111 full width
		"ＡＭＹ 也訂了":                  true,
	}
	for txt, want := range cases {
		if got := gi.leaks("br_b", txt, 2); got != want {
			t.Errorf("%q: leaks=%v want %v", txt, got, want)
		}
	}
	if gi.leaks("br_a", "call 0912345678", 2) {
		t.Errorf("own phone flagged")
	}
	if gi.leaks("br_b", "call 0912345678", 2+guestTokTTL+1) {
		t.Errorf("expired token still flagged")
	}
}

func lastLine(s string) string {
	ls := strings.Split(strings.TrimRight(s, "\n"), "\n")
	return ls[len(ls)-1]
}
