package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Rick's setup: two bots on one computer, no --home given. The admin bot's
// state is in the old shared ~/.bot-bridge; the member bot joining the same
// bridge must get its own folder, both directions must deliver, and one
// bridge's release notice must not show up in another bridge's tick.
func TestTwoBotsOneComputer(t *testing.T) {
	url := startTestServer(t)
	userHome := t.TempDir()
	t.Setenv("HOME", userHome)
	t.Setenv("BB_HOME", "")
	root := filepath.Join(userHome, ".bot-bridge")

	// Admin (an older bot): an old install that used ~/.bot-bridge.
	out, errOut, code := run(t, "create", "--home", root, "--server", url, "--name", "Dash", "--handle", "rick-chiu-bot", "--owner", "Rick")
	if code != exitOK {
		t.Fatalf("create: %d %s", code, errOut)
	}
	c := findLine(t, out, "created")
	br, _ := c["bridge_id"].(string)
	link, _ := c["invite_link"].(string)
	inv, _ := c["invite_code"].(string)
	// It also has an older bridge it will close.
	out, _, _ = run(t, "create", "--home", root, "--server", url, "--name", "Old", "--handle", "rick-chiu-bot")
	oldBr, _ := findLine(t, out, "created")["bridge_id"].(string)

	// Member (dashboard-client) joins with an explicit shared home: refused.
	_, errOut, code = run(t, "join", "--home", root, "--link", link, "--code", inv, "--handle", "rick-dashboard-bot")
	if code != exitUsage || !strings.Contains(errOut, "already_joined") {
		t.Fatalf("join into the admin's folder must be refused: %d %s", code, errOut)
	}
	if _, err := os.Stat(filepath.Join(root, br, "bridge.json")); err != nil {
		t.Fatalf("admin state was touched: %v", err)
	}
	// Default: the member gets ~/.bot-bridge/bots/rick-dashboard-bot.
	out, errOut, code = run(t, "join", "--link", link, "--code", inv, "--handle", "rick-dashboard-bot", "--owner", "Rick")
	if code != exitOK {
		t.Fatalf("member join: %d %s", code, errOut)
	}
	memberHome := filepath.Join(root, "bots", "rick-dashboard-bot")
	if r := rawLine(out, "ROUTINE_PROMPT: "); !strings.Contains(r, "BB_HOME="+memberHome+" bot-bridge-cli tick；") {
		t.Fatalf("routine line must carry the member's BB_HOME: %q", r)
	}
	// A different bot in a folder that belongs to someone else: refused.
	_, errOut, code = run(t, "create", "--home", memberHome, "--server", url, "--name", "X", "--handle", "rick-chiu-bot")
	if code != exitUsage || !strings.Contains(errOut, "identity_conflict") {
		t.Fatalf("identity conflict not refused: %d %s", code, errOut)
	}

	// Both ways, correct deliveries. The admin uses no BB_HOME (old routine
	// line), the member uses its own.
	if _, e, c := run(t, "send", "--bridge", br, "from admin"); c != exitOK {
		t.Fatalf("admin send: %d %s", c, e)
	}
	if _, e, c := run(t, "send", "--home", memberHome, "--bridge", br, "from member"); c != exitOK {
		t.Fatalf("member send (no untrusted_members on its own handle): %d %s", c, e)
	}
	a1, _, _ := run(t, "tick", "--bridge", br)
	m1, _, _ := run(t, "tick", "--home", memberHome, "--bridge", br)
	if !strings.Contains(a1, "from member") || strings.Contains(a1, "from admin") {
		t.Fatalf("admin tick wrong:\n%s", a1)
	}
	if !strings.Contains(m1, "from admin") || strings.Contains(m1, "from member") {
		t.Fatalf("member tick wrong:\n%s", m1)
	}

	// The admin closes its old bridge (one owner yes). The notice belongs to
	// that bridge only: neither the admin's tick on br nor the member sees it.
	if co, e, c := runApproved(t, "close", "--home", root, "--bridge", oldBr, "--yes"); c != exitOK || !strings.Contains(co, "NOTIFY_OWNER: 「Old」已關閉") || !strings.Contains(co, "NOTIFY_OWNER: 這項變更已於 ") {
		t.Fatalf("close old: %d %s\n%s", c, e, co)
	}
	a2, _, _ := run(t, "tick", "--bridge", br)
	m2, _, _ := run(t, "tick", "--home", memberHome, "--bridge", br)
	if strings.Contains(a2, "已放行") || strings.Contains(m2, "已放行") {
		t.Fatalf("release notice leaked into another bridge:\nadmin:\n%s\nmember:\n%s", a2, m2)
	}
	// The member releases a held action of its own (item 4 of Rick's report:
	// the admin's routine flagged the member's release). The admin never sees it;
	// the member's own tick does.
	if _, e, c := runApproved(t, "config", "--home", memberHome, "--bridge", br, "--display-name", "Dashboard"); c != exitOK {
		t.Fatalf("member config: %d %s", c, e)
	}
	a3, _, _ := run(t, "tick", "--bridge", br)
	if strings.Contains(a3, "已放行") {
		t.Fatalf("admin saw the member's release:\n%s", a3)
	}
	if m3, _, _ := run(t, "tick", "--home", memberHome, "--bridge", br); !strings.Contains(m3, "已放行") {
		t.Fatalf("member should see its own release notice:\n%s", m3)
	}
	// v0.6.5: the close's audit line was printed when it was released, so
	// forget shows just its own notice.
	f, e, cd := run(t, "forget", "--home", root, "--bridge", oldBr, "--yes")
	if cd != exitOK || strings.Contains(f, "release_confirmed") || strings.Contains(rawLine(f, "NOTIFY_OWNER: "), "確認碼") || strings.Count(f, "NOTIFY_OWNER:") != 1 {
		t.Fatalf("forget should show the old bridge's release notice: %d %s %s", cd, f, e)
	}
}

func TestAutoHomeNeedsChoiceWithSeveralBots(t *testing.T) {
	url := startTestServer(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("BB_HOME", "")
	for _, h := range []string{"bot-one", "bot-two"} {
		if _, e, c := run(t, "create", "--server", url, "--name", "B", "--handle", h); c != exitOK {
			t.Fatalf("create %s: %d %s", h, c, e)
		}
	}
	if _, e, c := run(t, "bridges", "--offline"); c != exitUsage || !strings.Contains(e, "home_required") {
		t.Fatalf("want home_required: %d %s", c, e)
	}
}

func TestHelp(t *testing.T) {
	for _, args := range [][]string{{"help"}, {"--help"}, {"-h"}, {"leave", "--help"}, {"help", "release"}} {
		out, e, c := run(t, args...)
		if c != exitOK || !strings.Contains(out, "usage: bot-bridge-cli") {
			t.Fatalf("%v: %d %q %q", args, c, out, e)
		}
	}
}
