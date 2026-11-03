package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// claim --new creates and claims in one process: the claim token never
// appears in the output, and the invite and manual follow --lang (v0.4.9).
func TestClaimNew(t *testing.T) {
	url := startTestServer(t)
	homeA := t.TempDir()
	out, errOut, rc := run(t, "claim", "--new", "--server", url, "--home", homeA, "--name", "研究小組", "--handle", "dana-bot", "--owner", "Dana")
	if rc != exitOK {
		t.Fatalf("claim --new exit %d: %s", rc, errOut)
	}
	if strings.Contains(out, "bbc_") || strings.Contains(errOut, "bbc_") {
		t.Fatalf("claim token leaked:\n%s", out)
	}
	inv := inviteBlockOf(out)
	if !strings.Contains(inv, "邀請碼（請保密）：") || !strings.Contains(inv, url+"/join.md") || !strings.Contains(out, "ROUTINE_PROMPT: ") {
		t.Fatalf("zh claim --new output:\n%s", out)
	}
	br := findLine(t, out, "claimed")["bridge_id"].(string)
	homeB := t.TempDir()
	if _, errOut, rc := runStdin(t, inv, "join", "--home", homeB, "--invite-stdin", "--handle", "amy-bot", "--owner", "Amy"); rc != exitOK {
		t.Fatalf("join zh invite: %d %s", rc, errOut)
	}

	// English: invite, manual and owner notice in English; stored for rotate.
	homeE := t.TempDir()
	out, errOut, rc = run(t, "claim", "--new", "--server", url, "--home", homeE, "--name", "Team", "--handle", "bob-bot", "--owner", "Bob", "--lang", "en")
	if rc != exitOK {
		t.Fatalf("claim --new --lang en exit %d: %s", rc, errOut)
	}
	inv = inviteBlockOf(out)
	for _, want := range []string{"bot-bridge invite.", "Invite code (keep private): ", "Join instructions: " + url + "/en/join.md", "Admin fingerprint: "} {
		if !strings.Contains(inv, want) {
			t.Errorf("english invite missing %q:\n%s", want, inv)
		}
	}
	if rawLine(out, "NOTIFY_OWNER: ") != claimNotice("Team", "en") || !strings.Contains(out, "NOTIFY_OWNER: "+ownerCautionEN) || strings.Contains(out, "admin. ") {
		t.Errorf("english owner notice:\n%s", out)
	}
	brE := findLine(t, out, "claimed")["bridge_id"].(string)
	b, _ := os.ReadFile(filepath.Join(homeE, brE, "bridge.json"))
	var bc bridgeConfig
	if json.Unmarshal(b, &bc) != nil || bc.Lang != "en" {
		t.Errorf("lang not stored: %s", b)
	}
	// Before the other bot joins, a rotate replaces the invite (English kept).
	rout, _, rc := runApproved(t, "invite-code", "--home", homeE, "--bridge", brE, "--rotate")
	if rc != exitOK || !strings.Contains(inviteBlockOf(rout), "/en/join.md") {
		t.Fatalf("rotated english invite:\n%s", rout)
	}
	inv = inviteBlockOf(rout)
	homeF := t.TempDir()
	jout, errOut, rc := runStdin(t, inv, "join", "--home", homeF, "--invite-stdin", "--handle", "amy-bot", "--owner", "Amy")
	if rc != exitOK || findLine(t, jout, "joined")["admin_fingerprint_check"] != fpMatch {
		t.Fatalf("join english invite: %d %s\n%s", rc, errOut, jout)
	}
	// Two bots now: a further rotate is refused before anything is held.
	if out, errOut, rc := run(t, "invite-code", "--home", homeE, "--bridge", brE, "--rotate"); rc == exitOK || !strings.Contains(errOut, "bridge_full") || strings.Contains(out, `"held"`) {
		t.Fatalf("rotate on a full bridge rc=%d out=%s err=%s", rc, out, errOut)
	}
	_ = br

	// Misuse is refused before anything is created.
	for name, args := range map[string][]string{
		"with link":    {"claim", "--new", "--server", url, "--home", t.TempDir(), "--link", url + "/b/br_ABCDEFGHIJKLMNOPQRSTUV", "--handle", "x-bot", "--owner", "X"},
		"placeholder":  {"claim", "--new", "--server", url, "--home", t.TempDir(), "--handle", "HANDLE", "--owner", "OWNER NAME"},
		"name w/o new": {"claim", "--name", "x", "--link", url + "/b/br_ABCDEFGHIJKLMNOPQRSTUV", "--home", t.TempDir(), "--handle", "x-bot"},
	} {
		if _, _, rc := run(t, args...); rc == exitOK {
			t.Errorf("%s: claim accepted", name)
		}
	}
}
