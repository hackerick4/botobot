package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// v0.5.0: one bridge connects exactly two bots; one bot can run several
// bridges side by side.

func pairQuiet(t *testing.T, who, out string) {
	t.Helper()
	if out != "NO_NEW_MESSAGES\n" {
		t.Errorf("%s: want exact NO_NEW_MESSAGES, got:\n%s", who, out)
	}
}

func pairTick(t *testing.T, home, br string) string {
	t.Helper()
	o, e, rc := run(t, "tick", "--home", home, "--bridge", br)
	if rc != exitOK {
		t.Fatalf("tick %s exit %d: %s", br, rc, e)
	}
	return o
}

// TestPairBridgesIsolated: A runs bridge AB (with B) and AC (with C) from one
// home. A third bot cannot join AB. Messages and notices stay on their own
// bridge. B leaving AB closes it for A and B with one notice for A only; AC
// keeps working. A removing C closes AC with one notice for C only.
func TestPairBridgesIsolated(t *testing.T) {
	url := startTestServer(t)
	homeA, homeB, homeC, homeD := t.TempDir(), t.TempDir(), t.TempDir(), t.TempDir()

	claim := func(name string) (string, string) {
		out, e, rc := run(t, "claim", "--new", "--server", url, "--home", homeA, "--name", name, "--handle", "amy-bot", "--owner", "Amy")
		if rc != exitOK {
			t.Fatalf("claim --new %s exit %d: %s", name, rc, e)
		}
		return findLine(t, out, "claimed")["bridge_id"].(string), inviteBlockOf(out)
	}
	join := func(home, handle, owner, invite string) (string, string, int) {
		return runStdin(t, invite, "join", "--home", home, "--invite-stdin", "--handle", handle, "--owner", owner)
	}
	ab, invAB := claim("AB")
	ac, invAC := claim("AC")
	if ab == ac {
		t.Fatal("same bridge id twice")
	}
	if _, e, rc := join(homeB, "bob-bot", "Bob", invAB); rc != exitOK {
		t.Fatalf("B join AB: %d %s", rc, e)
	}
	if _, e, rc := join(homeC, "cam-bot", "Cam", invAC); rc != exitOK {
		t.Fatalf("C join AC: %d %s", rc, e)
	}

	// A third bot is refused with a clear reason, and nothing is saved.
	o, e, rc := join(homeD, "dan-bot", "Dan", invAB)
	if rc == exitOK || !strings.Contains(e, "bridge_full") || !strings.Contains(e, "two bots") {
		t.Fatalf("third join: rc=%d out=%s err=%s", rc, o, e)
	}
	if bl, _, _ := run(t, "bridges", "--home", homeD, "--offline"); strings.Contains(bl, ab) {
		t.Fatalf("refused join left local state:\n%s", bl)
	}

	// Join notices: one per bridge, naming the right bot, offering a close.
	if n := rawLine(pairTick(t, homeA, ab), "NOTIFY_OWNER: "); n != "bob-bot（Bob）已加入「AB」。不認識可要求關閉。" {
		t.Errorf("AB join notice = %q", n)
	}
	if n := rawLine(pairTick(t, homeA, ac), "NOTIFY_OWNER: "); n != "cam-bot（Cam）已加入「AC」。不認識可要求關閉。" {
		t.Errorf("AC join notice = %q", n)
	}
	pairQuiet(t, "A/AB idle", pairTick(t, homeA, ab))
	pairQuiet(t, "A/AC idle", pairTick(t, homeA, ac))

	// Messages stay on their bridge.
	if _, e, rc := runStdin(t, "AB only 7781\n", "send", "--home", homeA, "--bridge", ab, "--stdin"); rc != exitOK {
		t.Fatalf("send AB: %d %s", rc, e)
	}
	if bo := pairTick(t, homeB, ab); !strings.Contains(bo, "AB only 7781") {
		t.Errorf("B missed AB message:\n%s", bo)
	}
	pairQuiet(t, "C after AB send", pairTick(t, homeC, ac))
	pairQuiet(t, "A/AC after AB send", pairTick(t, homeA, ac))

	// B leaves AB: B goes quiet; A hears once; AC is untouched.
	lo, e, rc := runApproved(t, "leave", "--home", homeB, "--bridge", ab)
	if rc != exitOK || !strings.Contains(lo, `"bridge_closed":true`) {
		t.Fatalf("B leave: rc=%d out=%s err=%s", rc, lo, e)
	}
	pairQuiet(t, "B after own leave", pairTick(t, homeB, ab))
	ao := pairTick(t, homeA, ab)
	if n := rawLine(ao, "NOTIFY_OWNER: "); n != "「AB」已關閉（對方已離開），資料已自動刪除。" || strings.Count(ao, "NOTIFY_OWNER") != 1 {
		t.Errorf("A close notice = %q\n%s", n, ao)
	}
	// v0.5.1: A's AB state is deleted without asking; AC stays; the old
	// per-bridge routine is told to go, without asking the owner.
	if n := rawLine(ao, "NEXT: "); n != "刪掉提示詞含 tick --bridge "+ab+" 的 routine（不是暫停），不必問主人。" {
		t.Errorf("A next = %q", n)
	}
	if _, err := os.Stat(filepath.Join(homeA, ab)); !os.IsNotExist(err) {
		t.Errorf("AB state still on disk: %v", err)
	}
	if bl, _, _ := run(t, "bridges", "--home", homeA, "--offline"); strings.Contains(bl, ab) || !strings.Contains(bl, ac) {
		t.Errorf("A bridges after auto-forget:\n%s", bl)
	}
	pairQuiet(t, "A/AC after AB closed", pairTick(t, homeA, ac))
	pairQuiet(t, "C after AB closed", pairTick(t, homeC, ac))
	if _, e, rc := runStdin(t, "AC still here 5520\n", "send", "--home", homeC, "--bridge", ac, "--stdin"); rc != exitOK {
		t.Fatalf("C send AC: %d %s", rc, e)
	}
	if ao := pairTick(t, homeA, ac); !strings.Contains(ao, "AC still here 5520") || strings.Contains(ao, "AB") {
		t.Errorf("A/AC message:\n%s", ao)
	}

	// The old AB invite is useless after the close.
	if _, e, rc := join(homeD, "dan-bot", "Dan", invAB); rc == exitOK {
		t.Fatalf("join a closed bridge succeeded: %s", e)
	}

	// kick is refused on a two-bot bridge (use close); A closes AC: A goes
	// quiet, C hears once.
	if ko, e, rc := run(t, "kick", "--home", homeA, "--bridge", ac, "cam-bot"); rc == exitOK || !strings.Contains(e, "use_close") {
		t.Fatalf("kick: rc=%d out=%s err=%s", rc, ko, e)
	}
	ko, e, rc := runApproved(t, "close", "--yes", "--home", homeA, "--bridge", ac)
	if rc != exitOK || !strings.Contains(ko, "forget --bridge "+ac) {
		t.Fatalf("close: rc=%d out=%s err=%s", rc, ko, e)
	}
	pairQuiet(t, "A after own close", pairTick(t, homeA, ac))
	co := pairTick(t, homeC, ac)
	if n := rawLine(co, "NOTIFY_OWNER: "); n != "「AC」已由管理者關閉，資料已自動刪除。" || strings.Count(co, "NOTIFY_OWNER: ") != 1 {
		t.Errorf("C close notice = %q\n%s", n, co)
	}
	// C has no bridge left: its folder is gone, and an all-bridges tick of the
	// gone folder stays cheap and creates nothing.
	if _, err := os.Stat(homeC); !os.IsNotExist(err) {
		t.Errorf("C's empty folder still exists: %v", err)
	}
	if o, _, rc := run(t, "tick", "--home", homeC); rc != exitOK || o != noBridgesLine+"\n"+noBridgesNext+"\n" {
		t.Errorf("tick of a removed folder: %d %q", rc, o)
	}
	if _, err := os.Stat(homeC); !os.IsNotExist(err) {
		t.Errorf("tick recreated the folder: %v", err)
	}

	// Nothing secret in any output of this run.
	for _, s := range []string{ao, co, lo, ko} {
		if strings.Contains(s, "bbk_") || strings.Contains(s, "bbc_") {
			t.Fatalf("secret in output:\n%s", s)
		}
	}
}

// TestPairBridgeAdminLeaveEnglish: the admin leaving closes the bridge; the
// English peer hears once, and the leaver stays quiet.
func TestPairBridgeAdminLeaveEnglish(t *testing.T) {
	url := startTestServer(t)
	admin, member := t.TempDir(), t.TempDir()
	out, e, rc := run(t, "claim", "--new", "--server", url, "--home", admin, "--name", "Duo", "--handle", "amy-bot", "--owner", "Amy", "--lang", "en")
	if rc != exitOK {
		t.Fatalf("claim: %d %s", rc, e)
	}
	br := findLine(t, out, "claimed")["bridge_id"].(string)
	if _, e, rc := runStdin(t, inviteBlockOf(out), "join", "--home", member, "--invite-stdin", "--handle", "bob-bot", "--owner", "Bob"); rc != exitOK {
		t.Fatalf("join: %d %s", rc, e)
	}
	if n := rawLine(pairTick(t, admin, br), "NOTIFY_OWNER: "); n != `bob-bot (Bob) joined "Duo". If unknown, ask to close it.` {
		t.Errorf("join notice = %q", n)
	}
	// invite-code on a full two-bot bridge says so, with no rotate hint.
	io, _, _ := run(t, "invite-code", "--home", admin, "--bridge", br)
	if n := rawLine(io, "NOTIFY_OWNER: "); n != `"Duo" already connects two bots and takes no one else. To connect another bot, create a new bridge.` || strings.Contains(io, "--rotate") {
		t.Errorf("invite-code on a full bridge:\n%s", io)
	}
	if _, e, rc := runApproved(t, "leave", "--home", admin, "--bridge", br, "--yes"); rc != exitOK {
		t.Fatalf("admin leave: %d %s", rc, e)
	}
	pairQuiet(t, "admin after leave", pairTick(t, admin, br))
	mo := pairTick(t, member, br)
	if n := rawLine(mo, "NOTIFY_OWNER: "); n != `"Duo" was closed by the admin. Its data was deleted automatically.` {
		t.Errorf("member notice = %q", n)
	}
	// The member's state is gone; an old per-bridge routine is told to go.
	if o, e, rc := run(t, "tick", "--home", member, "--bridge", br); rc != exitOK || !strings.HasPrefix(o, "NO_BRIDGES: ") || !strings.Contains(o, "tick --bridge "+br) {
		t.Errorf("tick after auto-forget: %d %s %s", rc, o, e)
	}
}
