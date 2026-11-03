package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// setupUntrusted is like setupPair but WITHOUT the admin trusting the member, so
// the member is a new/untrusted land from the admin's point of view.
func setupUntrusted(t *testing.T) pair {
	t.Helper()
	url := startTestServer(t)
	homeA, homeB := t.TempDir(), t.TempDir()
	out, errOut, code := run(t, "create", "--home", homeA, "--server", url,
		"--name", "Bridge", "--handle", "admin-bot", "--owner", "Dana")
	if code != exitOK {
		t.Fatalf("create: %d %s", code, errOut)
	}
	created := findLine(t, out, "created")
	link, _ := created["invite_link"].(string)
	codeStr, _ := created["invite_code"].(string)
	bridgeID, _ := created["bridge_id"].(string)
	_, errOut, code = run(t, "join", "--home", homeB,
		"--link", link, "--code", codeStr, "--handle", "member-bot", "--owner", "Amy")
	if code != exitOK {
		t.Fatalf("join: %d %s", code, errOut)
	}
	return pair{url: url, homeA: homeA, homeB: homeB, bridgeID: bridgeID}
}

// Pure TOFU: a member seen for the first time is trusted silently. No hold, no
// question, no NOTIFY_OWNER; sends work both ways at once.
func TestTOFUNewMemberTrustedSilently(t *testing.T) {
	p := setupUntrusted(t)
	out, errOut, code := run(t, "send", "--home", p.homeA, "--bridge", p.bridgeID, "hello")
	if code != exitOK || strings.Contains(out, "held") || strings.Contains(out, "NOTIFY_OWNER") {
		t.Fatalf("send to new member: exit %d out=%s err=%s", code, out, errOut)
	}
	if _, errOut, code := run(t, "send", "--home", p.homeB, "--bridge", p.bridgeID, "hi admin"); code != exitOK {
		t.Fatalf("member send exit %d, %s", code, errOut)
	}
	out, _, _ = run(t, "tick", "--home", p.homeA, "--bridge", p.bridgeID)
	if !strings.Contains(out, "hi admin") || strings.Contains(out, "trust --bridge") || strings.Contains(out, "指紋") {
		t.Fatalf("admin tick should show the message with no trust prompt:\n%s", out)
	}
	out, _, _ = run(t, "tick", "--home", p.homeB, "--bridge", p.bridgeID)
	if !strings.Contains(out, "hello") || strings.Contains(out, "trust --bridge") {
		t.Fatalf("member tick:\n%s", out)
	}
}

// A third member who joins later is also trusted on first use, without any
// prompt to the existing members.
func TestTOFULaterJoinerTrustedSilently(t *testing.T) {
	p := setupUntrusted(t)
	makeLegacy(t, p.url, p.bridgeID) // a third bot needs a pre-v0.5.0 bridge
	run(t, "tick", "--home", p.homeA, "--bridge", p.bridgeID)
	inv, errOut, code := runApproved(t, "invite-code", "--home", p.homeA, "--bridge", p.bridgeID, "--rotate")
	if code != exitOK {
		t.Fatalf("rotate invite: %d %s", code, errOut)
	}
	homeC := t.TempDir()
	if _, errOut, code := runStdin(t, inviteBlockOf(inv), "join", "--home", homeC, "--invite-stdin", "--handle", "third-bot"); code != exitOK {
		t.Fatalf("third join: %d %s", code, errOut)
	}
	out, errOut, code := run(t, "send", "--home", p.homeA, "--bridge", p.bridgeID, "welcome")
	if code != exitOK || strings.Contains(out, "NOTIFY_OWNER") {
		t.Fatalf("send after a new joiner: %d %s %s", code, out, errOut)
	}
	out, _, _ = run(t, "tick", "--home", p.homeB, "--bridge", p.bridgeID)
	if strings.Contains(out, "NOTIFY_OWNER: ") && strings.Contains(out, "third-bot") && strings.Contains(out, "金鑰") {
		t.Fatalf("existing member was prompted about the new joiner:\n%s", out)
	}
}

func TestTOFUKeySwapHaltsSend(t *testing.T) {
	p := setupPair(t) // admin already trusts member
	// Member rotates its key; the admin's pin no longer matches.
	// The admin has seen (and silently pinned) the member's first key.
	run(t, "tick", "--home", p.homeA, "--bridge", p.bridgeID)
	if _, errOut, code := run(t, "rekey", "--home", p.homeB); code != exitOK {
		t.Fatalf("member rekey exit %d, %s", code, errOut)
	}
	// The tick tells the owner in one plain sentence and asks once.
	out, _, _ := run(t, "tick", "--home", p.homeA, "--bridge", p.bridgeID)
	if !strings.Contains(out, "的金鑰已變更") || !strings.Contains(out, "詢問一次") || strings.Contains(out, "核對指紋") {
		t.Fatalf("key change notice:\n%s", out)
	}
	_, errOut, code := run(t, "send", "--home", p.homeA, "after rekey")
	if code != exitGeneral || !strings.Contains(errOut, "untrusted_members") || strings.Contains(errOut, "compare fingerprints") && !strings.Contains(errOut, "Do not ask") {
		t.Fatalf("send after peer rekey should halt with untrusted_members; exit %d stderr %s", code, errOut)
	}
	// ONE owner yes is enough: trust is held, then a single release (which
	// waits out the delay itself) sends it, even inside the same wake.
	held, _, code := run(t, "trust", "--home", p.homeA, "--bridge", p.bridgeID, "member-bot")
	if code != exitOK || !strings.Contains(held, `"type":"held"`) {
		t.Fatalf("trust should be held once: %d %s", code, held)
	}
	id, hash := heldIDHash(t, held)
	before := sleptNS.Load()
	if o, e, c := run(t, "release", id, "--home", p.homeA, "--bridge", p.bridgeID, "--hash", hash, "--owner-approved"); c != exitOK || !strings.Contains(o, `"type":"waiting"`) {
		t.Fatalf("single release after one yes: exit %d %s %s", c, o, e)
	}
	if sleptNS.Load() == before {
		t.Fatal("release should have waited out the minimum delay")
	}
	if _, errOut, code = run(t, "send", "--home", p.homeA, "after rekey"); code != exitOK {
		t.Fatalf("send after one-step re-trust exit %d, %s", code, errOut)
	}
}

// Deferred admin check: when join could not compare the invite's admin
// fingerprint, the first later look at the roster does it, silently. A match
// trusts the admin; a mismatch locks the bridge (nothing auto-trusted).
func TestTOFUDeferredAdminCheck(t *testing.T) {
	for _, tc := range []struct {
		name   string
		match  bool
		sendOK bool
	}{{"match", true, true}, {"mismatch", false, false}} {
		t.Run(tc.name, func(t *testing.T) {
			p := setupPair(t)
			adminOut, _, _ := run(t, "fingerprint", "--home", p.homeA, "--bridge", p.bridgeID)
			fp, _ := findLine(t, adminOut, "fingerprint")["fingerprint"].(string)
			if !tc.match {
				fp = "11111 22222 33333 44444 55555 66666"
			}
			// Pretend join could not fetch the roster: no pins, check pending.
			path := filepath.Join(p.homeB, p.bridgeID, "pins.json")
			b, _ := json.Marshal(map[string]any{"entries": map[string]any{}, "expect_admin_fp": fp})
			if err := os.WriteFile(path, b, 0o600); err != nil {
				t.Fatal(err)
			}
			out, errOut, code := run(t, "send", "--home", p.homeB, "--bridge", p.bridgeID, "hi")
			if (code == exitOK) != tc.sendOK {
				t.Fatalf("send: exit %d %s %s", code, out, errOut)
			}
			nb, _ := os.ReadFile(path)
			if tc.match == strings.Contains(string(nb), "admin_fingerprint_mismatch") || strings.Contains(string(nb), "expect_admin_fp") {
				t.Fatalf("pins after check: %s", nb)
			}
		})
	}
}

// A member recorded untrusted with one key that later shows a DIFFERENT key is
// never auto-accepted (a changed key always needs the owner's one yes).
func TestTOFUChangedKeyNeverAutoAccepted(t *testing.T) {
	p := setupPair(t)
	run(t, "tick", "--home", p.homeA, "--bridge", p.bridgeID)
	path := filepath.Join(p.homeA, p.bridgeID, "pins.json")
	var ps map[string]any
	nb, _ := os.ReadFile(path)
	_ = json.Unmarshal(nb, &ps)
	for id, e := range ps["entries"].(map[string]any) {
		m := e.(map[string]any)
		if m["fingerprint"] != nil {
			m["trusted"] = false
			m["fingerprint"] = "99999 99999 99999 99999 99999 99999"
			ps["entries"].(map[string]any)[id] = m
		}
	}
	nb, _ = json.Marshal(ps)
	_ = os.WriteFile(path, nb, 0o600)
	if _, e, c := run(t, "send", "--home", p.homeA, "--bridge", p.bridgeID, "x"); c == exitOK || !strings.Contains(e, "untrusted_members") {
		t.Fatalf("changed key was auto-accepted: %d %s", c, e)
	}
}
