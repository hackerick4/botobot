package cli

import (
	"encoding/json"
	"strings"
	"testing"
)

// joinInto creates a fresh bridge (admin in a new home) and joins homeB to it.
func joinInto(t *testing.T, url, homeB, handle string) (br, homeA string) {
	t.Helper()
	link, code, out, homeA := claimFresh(t, url)
	fp := findLine(t, out, "claimed")["fingerprint"].(string)
	out, errOut, rc := run(t, "join", "--home", homeB, "--link", link, "--code", code, "--handle", handle, "--admin-fp", fp)
	if rc != exitOK {
		t.Fatalf("join exit %d: %s", rc, errOut)
	}
	return findLine(t, out, "joined")["bridge_id"].(string), homeA
}

func linesOfType(t *testing.T, out, typ string) []map[string]any {
	t.Helper()
	var res []map[string]any
	for _, l := range strings.Split(out, "\n") {
		var m map[string]any
		if json.Unmarshal([]byte(l), &m) == nil && m["type"] == typ {
			res = append(res, m)
		}
	}
	return res
}

// leave, close and forget never fall back to the default or only bridge.
func TestRemovalNeedsExplicitBridge(t *testing.T) {
	url := startTestServer(t)
	homeB := t.TempDir()
	_, homeA := joinInto(t, url, homeB, "amy-bot")
	cases := [][]string{
		{"leave", "--home", homeB},
		{"leave", "--home", homeB, "--yes"},
		{"leave", "--home", homeB, "--bridge="},
		{"leave", "--home", homeB, "--bridge", " "},
		{"forget", "--home", homeB, "--yes"},
		{"forget", "--home", homeB, "--yes", "--force"},
		{"close", "--home", homeA},
		{"leave", "--home", homeA},
	}
	for _, c := range cases {
		out, errOut, rc := run(t, c...)
		if rc != exitUsage || !strings.Contains(out+errOut, "bridge_required") || strings.Contains(out, `"held"`) {
			t.Fatalf("%v: rc=%d out=%s err=%s", c, rc, out, errOut)
		}
	}
	if _, _, rc := run(t, "forget", "--home", homeB, "--all", "--yes", "--force"); rc != exitUsage {
		t.Fatalf("forget --all --force rc=%d", rc)
	}
}

// One bot in two bridges removes only one; the other keeps working.
func TestRemoveOneOfTwoBridges(t *testing.T) {
	url := startTestServer(t)
	homeB := t.TempDir()
	br1, admin1 := joinInto(t, url, homeB, "amy-bot")
	br2, admin2 := joinInto(t, url, homeB, "amy-bot")

	out, errOut, rc := run(t, "bridges", "--home", homeB)
	if rc != exitOK {
		t.Fatalf("bridges rc=%d %s", rc, errOut)
	}
	ls := linesOfType(t, out, "bridge")
	if len(ls) != 2 {
		t.Fatalf("want 2 bridges: %s", out)
	}
	for _, l := range ls {
		peers, _ := l["peers"].([]any)
		if l["name"] != "研究小組" || l["status"] != "open" || len(peers) != 1 || peers[0] != "sample-bot（Rick）" ||
			l["joined_at"] == "" || !strings.Contains(l["summary"].(string), "研究小組") {
			t.Fatalf("bridge line = %v", l)
		}
	}
	if out, _, rc = run(t, "bridges", "--home", homeB, "--offline"); rc != exitOK || len(linesOfType(t, out, "bridge")) != 2 {
		t.Fatalf("bridges --offline rc=%d %s", rc, out)
	}

	// --all only lists; --all --yes refuses while still a member (nothing deleted).
	out, errOut, rc = run(t, "forget", "--home", homeB, "--all")
	if rc != exitUsage || len(linesOfType(t, out, "will_forget")) != 2 || !strings.Contains(out+errOut, "confirm_all") {
		t.Fatalf("forget --all rc=%d out=%s err=%s", rc, out, errOut)
	}
	if _, errOut, rc = run(t, "forget", "--home", homeB, "--all", "--yes"); rc == exitOK || !strings.Contains(errOut, "still_member") {
		t.Fatalf("forget --all --yes while member rc=%d %s", rc, errOut)
	}

	// Remove bridge 1 only.
	if out, errOut, rc = runApproved(t, "leave", "--home", homeB, "--bridge", br1); rc != exitOK {
		t.Fatalf("leave br1 rc=%d %s %s", rc, out, errOut)
	}
	out, errOut, rc = run(t, "forget", "--home", homeB, "--bridge", br1, "--yes")
	if rc != exitOK || findLine(t, out, "forgotten")["bridges_remaining"].(float64) != 1 {
		t.Fatalf("forget br1 rc=%d out=%s err=%s", rc, out, errOut)
	}
	out, _, _ = run(t, "bridges", "--home", homeB)
	if ls = linesOfType(t, out, "bridge"); len(ls) != 1 || ls[0]["bridge_id"] != br2 {
		t.Fatalf("after forget: %s", out)
	}

	// Bridge 2 still works both ways of the check.
	if _, errOut, rc = runStdin(t, "還在嗎\n", "send", "--home", homeB, "--bridge", br2, "--stdin"); rc != exitOK {
		t.Fatalf("send on br2 rc=%d %s", rc, errOut)
	}
	if out, errOut, rc = run(t, "poll", "--home", admin2); rc != exitOK || !strings.Contains(out, "還在嗎") {
		t.Fatalf("admin2 poll rc=%d out=%s err=%s", rc, out, errOut)
	}
	// Bridge 1 had two bots: the leave closed it for admin1 too (v0.5.0).
	if out, _, _ = run(t, "poll", "--home", admin1); !strings.Contains(out, `"bridge_closed"`) || !strings.Contains(out, `"member_left"`) {
		t.Fatalf("admin1 should see bridge 1 closed: %s", out)
	}

	// After leaving the last one, --all --yes clears everything.
	if _, errOut, rc = runApproved(t, "leave", "--home", homeB, "--bridge", br2); rc != exitOK {
		t.Fatalf("leave br2 rc=%d %s", rc, errOut)
	}
	if out, errOut, rc = run(t, "forget", "--home", homeB, "--all", "--yes"); rc != exitOK || len(linesOfType(t, out, "forgotten")) != 1 {
		t.Fatalf("forget --all --yes rc=%d out=%s err=%s", rc, out, errOut)
	}
	if out, _, _ = run(t, "bridges", "--home", homeB); !strings.Contains(out, `"count":0`) {
		t.Fatalf("bridges after all: %s", out)
	}
}
