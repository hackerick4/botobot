package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"testing"

	"github.com/hackerick4/botobot/internal/invite"
)

// postJoin calls the public POST /join endpoint directly (as a bot would with
// curl) and returns the decoded join_created response.
func postJoin(t *testing.T, serverURL string) map[string]any {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, serverURL+"/join", bytes.NewReader([]byte(`{"name":"研究小組"}`)))
	if err != nil {
		t.Fatalf("build /join request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST /join: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST /join status = %d: %s", resp.StatusCode, body)
	}
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		t.Fatalf("decode /join: %v", err)
	}
	// v0.4.9: the response carries no invite (claim prints the real one).
	for _, k := range []string{"invite_code", "invite_prompt"} {
		if _, ok := m[k]; ok {
			t.Fatalf("POST /join still returns %s", k)
		}
	}
	if ref, ok := testStores.Load(serverURL); ok {
		r := ref.(testStoreRef)
		id, _ := m["bridge_id"].(string)
		b, err := r.st.GetBridge(context.Background(), id)
		if err != nil {
			t.Fatalf("read pending bridge: %v", err)
		}
		code, err := invite.Decrypt(r.key, id, b.InviteCodeEnc)
		if err != nil {
			t.Fatalf("decrypt invite code: %v", err)
		}
		m["invite_code"] = code // test convenience only
	}
	return m
}

// TestE2EJoinClaim exercises the full one-call onboarding flow (DEV_SPEC §6
// /join): POST /join -> CLI claim as admin (home A) -> home B joins using the
// link+code from the response -> messages both ways -> NO_NEW_MESSAGES.
func TestE2EJoinClaim(t *testing.T) {
	url := startTestServer(t)
	homeA := t.TempDir() // admin (claims)
	homeB := t.TempDir() // member (joins)

	created := postJoin(t, url)
	inviteLink, _ := created["invite_link"].(string)
	inviteCode, _ := created["invite_code"].(string)
	claimToken, _ := created["claim_token"].(string)
	if inviteLink == "" || inviteCode == "" || claimToken == "" {
		t.Fatalf("join_created missing fields: %v", created)
	}

	// --- claim (admin) via the CLI, using link + token from the response ---
	out, errOut, code := run(t, "claim", "--home", homeA,
		"--link", inviteLink, "--token", claimToken, "--handle", "sample-bot", "--owner", "Rick")
	if code != exitOK {
		t.Fatalf("claim exit %d, stderr=%s", code, errOut)
	}
	claimed := findLine(t, out, "claimed")
	if claimed["fingerprint"] == "" || claimed["bridge_id"] == "" {
		t.Fatalf("claimed missing fields: %v", claimed)
	}

	// --- join before/after: member joins with the invite code (bridge now active) ---
	out, errOut, code = run(t, "join", "--home", homeB,
		"--link", inviteLink, "--code", inviteCode, "--handle", "amy-bot", "--owner", "Amy")
	if code != exitOK {
		t.Fatalf("join exit %d, stderr=%s", code, errOut)
	}
	findLine(t, out, "joined")

	// --- admin confirms the new member (TOFU, Batch 1 item 4) before sending ---
	if _, errOut, code = runApproved(t, "trust", "--home", homeA, "amy-bot"); code != exitOK {
		t.Fatalf("trust exit %d, stderr=%s", code, errOut)
	}

	// --- admin -> member ---
	if _, errOut, code = run(t, "send", "--home", homeA, "hello from admin"); code != exitOK {
		t.Fatalf("admin send exit %d, stderr=%s", code, errOut)
	}
	out, errOut, code = run(t, "poll", "--home", homeB)
	if code != exitOK {
		t.Fatalf("member poll exit %d, stderr=%s", code, errOut)
	}
	if msg := findLine(t, out, "message"); msg["verified"] != true || msg["text"] != "hello from admin" {
		t.Fatalf("member did not receive admin message: %v", msg)
	}

	// --- member -> admin ---
	if _, errOut, code = run(t, "send", "--home", homeB, "hi back from member"); code != exitOK {
		t.Fatalf("member send exit %d, stderr=%s", code, errOut)
	}
	out, errOut, code = run(t, "poll", "--home", homeA)
	if code != exitOK {
		t.Fatalf("admin poll exit %d, stderr=%s", code, errOut)
	}
	if msg := findLine(t, out, "message"); msg["verified"] != true || msg["text"] != "hi back from member" {
		t.Fatalf("admin did not receive member message: %v", msg)
	}

	// --- both inboxes now drained: exactly NO_NEW_MESSAGES, exit 0 ---
	for _, home := range []string{homeA, homeB} {
		out, errOut, code = run(t, "poll", "--home", home)
		if code != exitOK {
			t.Fatalf("drained poll exit %d, stderr=%s", code, errOut)
		}
		if out != "NO_NEW_MESSAGES\n" {
			t.Fatalf("drained poll stdout = %q, want %q", out, "NO_NEW_MESSAGES\n")
		}
	}
}

// TestJoinBeforeClaimViaCLI verifies the CLI join fails clearly when the bridge
// is still pending (admin has not claimed yet).
func TestJoinBeforeClaimViaCLI(t *testing.T) {
	url := startTestServer(t)
	created := postJoin(t, url)
	inviteLink, _ := created["invite_link"].(string)
	inviteCode, _ := created["invite_code"].(string)

	_, errOut, code := run(t, "join", "--home", t.TempDir(),
		"--link", inviteLink, "--code", inviteCode, "--handle", "amy-bot")
	if code == exitOK {
		t.Fatalf("join before claim should fail; stderr=%s", errOut)
	}
	if !bytes.Contains([]byte(errOut), []byte("bridge_pending")) {
		t.Fatalf("expected bridge_pending error, got: %s", errOut)
	}
}
