package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The /delete flow: forget refuses while still a member, leave is held for the
// owner, then forget deletes the local keys and state, and the admin sees a
// "left" event.
func TestForgetAfterLeave(t *testing.T) {
	url := startTestServer(t)
	link, code, out, homeA := claimFresh(t, url)
	adminFP := findLine(t, out, "claimed")["fingerprint"].(string)
	homeB := t.TempDir()
	out, errOut, rc := run(t, "join", "--home", homeB, "--link", link, "--code", code, "--handle", "amy-bot", "--admin-fp", adminFP)
	if rc != exitOK {
		t.Fatalf("join exit %d: %s", rc, errOut)
	}
	br := findLine(t, out, "joined")["bridge_id"].(string)
	dir := filepath.Join(homeB, br)

	// Usage checks.
	if _, errOut, rc = run(t, "forget", "--home", homeB, "--yes"); rc != exitUsage || !strings.Contains(errOut, "--bridge") {
		t.Fatalf("forget without --bridge rc=%d %s", rc, errOut)
	}
	if _, _, rc = run(t, "forget", "--home", homeB, "--bridge", "../x", "--yes"); rc != exitUsage {
		t.Fatalf("forget with a bad id rc=%d", rc)
	}
	if _, _, rc = run(t, "forget", "--home", homeB, "--bridge", br); rc != exitUsage {
		t.Fatalf("forget without --yes rc=%d", rc)
	}
	// Still a member: refused, nothing deleted.
	if _, errOut, rc = run(t, "forget", "--home", homeB, "--bridge", br, "--yes"); rc == exitOK || !strings.Contains(errOut, "still_member") {
		t.Fatalf("forget while member rc=%d %s", rc, errOut)
	}
	// --force is held for the owner, not executed.
	out, _, rc = run(t, "forget", "--home", homeB, "--bridge", br, "--yes", "--force")
	if rc != exitOK || !strings.Contains(out, `"type":"held"`) {
		t.Fatalf("forget --force should be held: rc=%d %s", rc, out)
	}
	if _, err := os.Stat(filepath.Join(dir, "keys.json")); err != nil {
		t.Fatalf("keys gone before approval: %v", err)
	}

	// Leave (held, then approved by the owner) prints the next steps.
	out, errOut, rc = runApproved(t, "leave", "--home", homeB, "--bridge", br)
	if rc != exitOK || !strings.Contains(out, "forget --bridge "+br) {
		t.Fatalf("leave rc=%d out=%s err=%s", rc, out, errOut)
	}
	// v0.6.5: the released leave prints its closed line and audit line at
	// once, like an entry line; forget no longer repeats them.
	if !strings.Contains(out, "NOTIFY_OWNER: 「研究小組」已關閉，雙方都無法再傳訊息。其他連線不受影響。") || !strings.Contains(out, "NOTIFY_OWNER: 這項變更已於 ") || !strings.Contains(out, `"release_confirmed":"離開已於`) {
		t.Fatalf("released leave lines:\n%s", out)
	}
	out, errOut, rc = run(t, "forget", "--home", homeB, "--bridge", br, "--yes")
	if rc != exitOK {
		t.Fatalf("forget after leave rc=%d %s", rc, errOut)
	}
	f := findLine(t, out, "forgotten")
	// A two-bot bridge closes when the member leaves (v0.5.0).
	if f["membership"] != "closed" || f["bridges_remaining"].(float64) != 0 || f["held_dropped"].(float64) != 1 {
		t.Fatalf("forgotten = %v", f)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("bridge dir still exists: %v", err)
	}
	// One owner notice, no routine reminder for the owner, and the now-empty
	// home is removed. The release was reported when it ran (v0.6.5).
	n := rawLine(out, "NOTIFY_OWNER: ")
	if strings.Count(out, "NOTIFY_OWNER:") != 1 || n != "已刪除「研究小組」。" || strings.Contains(out, "release_confirmed") || strings.Contains(n, "routine") || strings.Contains(out, "若未經同意") {
		t.Fatalf("forget owner notice:\n%s", out)
	}
	// The last bridge is gone: the bot's one routine goes too (v0.5.1).
	if strings.Contains(n, "資料夾") || rawLine(out, "NEXT: ") != "此 Bot 已無 bridge，刪掉「bot-bridge inbox」routine（不是暫停），不必再問主人。" {
		t.Fatalf("forget should remove the empty home without repeating the routine step:\n%s", out)
	}
	if _, err := os.Stat(homeB); !os.IsNotExist(err) {
		t.Fatalf("empty per-bot home still exists: %v", err)
	}
	// Commands run afterwards (a check, or a routine not yet deleted) must not
	// bring the empty folder back.
	run(t, "bridges", "--home", homeB)
	run(t, "tick", "--home", homeB, "--bridge", br)
	run(t, "update", "--check", "--home", homeB)
	if _, err := os.Stat(homeB); !os.IsNotExist(err) {
		t.Fatalf("per-bot home came back after later commands: %v", err)
	}

	// Two bots: the member leaving closed the bridge for the admin too.
	out, errOut, rc = run(t, "poll", "--home", homeA)
	if rc != exitClosed || !strings.Contains(out, `"member_left"`) {
		t.Fatalf("admin poll rc=%d out=%s err=%s", rc, out, errOut)
	}
}

func TestShredTreeRemovesEverything(t *testing.T) {
	d := filepath.Join(t.TempDir(), "br_x")
	if err := os.MkdirAll(filepath.Join(d, "files", "m1"), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"keys.json", "apikey", "files/m1/a.txt"} {
		if err := os.WriteFile(filepath.Join(d, p), []byte(strings.Repeat("s", 70000)), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := shredTree(d); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(d); !os.IsNotExist(err) {
		t.Fatalf("dir remains: %v", err)
	}
}

// cleanupHome never removes a folder that holds anything bot-bridge did not
// create, and never the shared root.
func TestForgetKeepsForeignFiles(t *testing.T) {
	url := startTestServer(t)
	link, code, out, _ := claimFresh(t, url)
	adminFP := findLine(t, out, "claimed")["fingerprint"].(string)
	homeB := t.TempDir()
	jo, errOut, rc := run(t, "join", "--home", homeB, "--link", link, "--code", code, "--handle", "amy-bot", "--admin-fp", adminFP)
	if rc != exitOK {
		t.Fatalf("join: %s", errOut)
	}
	br := findLine(t, jo, "joined")["bridge_id"].(string)
	foreign := filepath.Join(homeB, "notes.txt")
	if err := os.WriteFile(foreign, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, rc := runApproved(t, "leave", "--home", homeB, "--bridge", br); rc != exitOK {
		t.Fatal("leave")
	}
	out, errOut, rc = run(t, "forget", "--home", homeB, "--bridge", br, "--yes")
	if rc != exitOK || strings.Contains(out, "資料夾已一併移除") {
		t.Fatalf("forget rc=%d %s %s", rc, out, errOut)
	}
	if b, err := os.ReadFile(foreign); err != nil || string(b) != "keep" {
		t.Fatalf("foreign file touched: %v", err)
	}
	if _, err := os.Stat(filepath.Join(homeB, "config.json")); !os.IsNotExist(err) {
		t.Fatalf("config.json should be gone: %v", err)
	}
}

// A BB_HOME that is the user's home folder is never cleaned up, even after its
// last bridge is forgotten (red team v0.4.7).
func TestForgetNeverCleansUserHome(t *testing.T) {
	url := startTestServer(t)
	link, code, out, _ := claimFresh(t, url)
	adminFP := findLine(t, out, "claimed")["fingerprint"].(string)
	homeB := t.TempDir()
	t.Setenv("HOME", homeB)
	jo, errOut, rc := run(t, "join", "--home", homeB, "--link", link, "--code", code, "--handle", "amy-bot", "--admin-fp", adminFP)
	if rc != exitOK {
		t.Fatalf("join: %s", errOut)
	}
	br := findLine(t, jo, "joined")["bridge_id"].(string)
	if _, _, rc := runApproved(t, "leave", "--home", homeB, "--bridge", br); rc != exitOK {
		t.Fatal("leave")
	}
	out, errOut, rc = run(t, "forget", "--home", homeB, "--bridge", br, "--yes")
	if rc != exitOK || strings.Contains(out, "資料夾已一併移除") {
		t.Fatalf("forget rc=%d %s %s", rc, out, errOut)
	}
	if _, err := os.Stat(filepath.Join(homeB, "config.json")); err != nil {
		t.Fatalf("config.json in the user's home must be kept: %v", err)
	}
}
