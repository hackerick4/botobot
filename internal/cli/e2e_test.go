package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hackerick4/botobot/internal/clock"
	"github.com/hackerick4/botobot/internal/config"
	"github.com/hackerick4/botobot/internal/server"
	"github.com/hackerick4/botobot/internal/store"
)

// startTestServer brings up a real bot-bridge server over httptest backed by a
// temp SQLite store (DEV_SPEC §17.3). Join jitter is disabled so there is no
// sleep (CLAUDE.md rule 8).
func startTestServer(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	clk := clock.Real{}
	st, err := store.Open(context.Background(), store.Options{
		Path:                 filepath.Join(dir, "bridge.db"),
		BlobDir:              filepath.Join(dir, "blobs"),
		Clock:                clk,
		Flush:                time.Millisecond,
		TTL:                  24 * time.Hour,
		Lease:                5 * time.Minute,
		MaxInboxItemsPerLand: 10000,
	})
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	cfg := &config.Config{
		PublicURL:                         "", // set below to the live URL
		APIKeyPepper:                      bytes.Repeat([]byte{0x11}, 32),
		InviteKey:                         bytes.Repeat([]byte{0x22}, 32),
		MaxLands:                          32,
		MaxEnvelopeBytes:                  26214400,
		MaxQueueQuotaBytes:                2147483648,
		MaxInboxItemsPerLand:              10000,
		MaxInboxResponseBytes:             67108864,
		InboxTTL:                          24 * time.Hour,
		Lease:                             5 * time.Minute,
		CloseGrace:                        10 * time.Minute,
		DefaultRateLimitPerMin:            600,
		RateInboxPerMin:                   100000,
		RateAckPerMin:                     100000,
		RateOtherPerMin:                   100000,
		RateCreateBridgePerHourPerIP:      100000,
		ClaimTTL:                          time.Hour,
		JoinCreatePerHour:                 100000,
		MaxPendingBridges:                 10000,
		MaxPendingBridgesPerPrefix:        10000,
		AllowLegacyCreate:                 true, // the CLI `create` command uses the direct-create endpoint
		EntryCreatePerHour:                1000,
		MaxEntriesPerPrefix:               1000,
		EntryMaxOpenBridges:               200,
		EntryJoinsPerHour:                 600,
		EntryJoinPerPrefixPerHour:         1000,
		EntryJoinPerPrefixPerEntryPerHour: 1000,
		LogLevel:                          "error",
		LogFormat:                         "json",
	}
	logger := server.NewLogger("error", "json", io.Discard)
	srv := server.New(cfg, logger, clk, server.WithStore(st), server.WithReady(st.Ping), server.WithJoinJitter(func() {}))
	live := httptest.NewServer(srv.Handler())
	t.Cleanup(live.Close)
	cfg.PublicURL = live.URL // invite_link uses this
	testStores.Store(live.URL, testStoreRef{st: st, key: cfg.InviteKey})
	return live.URL
}

// testStores lets postJoin read a pending bridge's invite code straight from
// the test server's store: since v0.4.9 POST /join no longer returns it (claim
// fetches it with the admin key), but some tests try joins before the claim.
var testStores sync.Map

type testStoreRef struct {
	st  *store.Store
	key []byte
}

// run executes one CLI invocation with isolated stdout/stderr and returns them
// plus the exit code (DEV_SPEC §17.3).
func run(t *testing.T, args ...string) (stdout, stderr string, exit int) {
	t.Helper()
	return runStdin(t, "", args...)
}

// runStdin is run with a stdin body attached (DEV_SPEC §12.3; Batch 1 item 2).
func runStdin(t *testing.T, input string, args ...string) (stdout, stderr string, exit int) {
	t.Helper()
	// Secrets go in the environment, never on the command line (the CLI refuses
	// --code/--token); tests write them as flags for brevity.
	env := map[string]string{}
	var kept []string
	for i := 0; i < len(args); i++ {
		if (args[i] == "--code" || args[i] == "--token") && i+1 < len(args) {
			env[map[string]string{"--code": "BB_CODE", "--token": "BB_CLAIM_TOKEN"}[args[i]]] = args[i+1]
			i++
			continue
		}
		kept = append(kept, args[i])
	}
	return runEnv(t, input, env, kept...)
}

// runEnv runs the CLI in-process with the given secret environment and the
// args exactly as written.
func runEnv(t *testing.T, input string, env map[string]string, args ...string) (stdout, stderr string, exit int) {
	t.Helper()
	var out, errb bytes.Buffer
	a := &app{version: "test", stdout: &out, stderr: &errb, stdin: strings.NewReader(input), now: time.Now, sleep: testSleep, env: env}
	exit = a.run(args)
	checkOwnerWording(t, out.String())
	return out.String(), errb.String(), exit
}

// lastJSONLine parses the JSON object lines in s and returns the first one whose
// "type" equals typ.
func findLine(t *testing.T, s, typ string) map[string]any {
	t.Helper()
	for _, line := range strings.Split(strings.TrimRight(s, "\n"), "\n") {
		if line == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			continue
		}
		if m["type"] == typ {
			return m
		}
	}
	t.Fatalf("no %q line in output:\n%s", typ, s)
	return nil
}

// pngImage returns a detectable PNG of n bytes whose body is a verifiable
// pattern (DEV_SPEC §17.3 1 MB image attachment).
func pngImage(n int) []byte {
	b := make([]byte, n)
	copy(b, []byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A})
	for i := 8; i < n; i++ {
		b[i] = byte(i * 7)
	}
	return b
}

// pair is a two-land bridge set up for a test: an admin and a member sharing one
// bridge over a live test server.
type pair struct {
	url      string
	homeA    string // admin
	homeB    string // member
	bridgeID string
}

// setupPair creates a bridge (admin) and joins a member, returning both homes
// and the bridge id (DEV_SPEC §17.3). It fails the test on any error.
func setupPair(t *testing.T) pair {
	t.Helper()
	url := startTestServer(t)
	homeA, homeB := t.TempDir(), t.TempDir()
	out, errOut, code := run(t, "create", "--home", homeA, "--server", url,
		"--name", "Test Bridge", "--handle", "admin-bot", "--owner", "Dana")
	if code != exitOK {
		t.Fatalf("create exit %d, stderr=%s", code, errOut)
	}
	created := findLine(t, out, "created")
	inviteLink, _ := created["invite_link"].(string)
	inviteCode, _ := created["invite_code"].(string)
	bridgeID, _ := created["bridge_id"].(string)
	_, errOut, code = run(t, "join", "--home", homeB,
		"--link", inviteLink, "--code", inviteCode, "--handle", "member-bot", "--owner", "Amy")
	if code != exitOK {
		t.Fatalf("join exit %d, stderr=%s", code, errOut)
	}
	// Pure TOFU: the admin trusts the new member on first sight, no step needed.
	return pair{url: url, homeA: homeA, homeB: homeB, bridgeID: bridgeID}
}

func TestSendStdinInjectionUnchanged(t *testing.T) {
	p := setupPair(t)
	// A body full of shell metacharacters must pass through stdin verbatim and
	// never be expanded/executed (Batch 1 item 2, test plan).
	body := "hi $(whoami) `id` ${HOME} && rm -rf / | cat <(echo x)"
	out, errOut, code := runStdin(t, body+"\n", "send", "--home", p.homeA, "--stdin")
	if code != exitOK {
		t.Fatalf("send --stdin exit %d, stderr=%s", code, errOut)
	}
	findLine(t, out, "sent")

	out, errOut, code = run(t, "poll", "--home", p.homeB)
	if code != exitOK {
		t.Fatalf("poll exit %d, stderr=%s", code, errOut)
	}
	msg := findLine(t, out, "message")
	if msg["verified"] != true {
		t.Fatalf("message not verified: %v", msg)
	}
	if msg["text"] != body {
		t.Fatalf("stdin body altered in transit:\n got %q\nwant %q", msg["text"], body)
	}
}

func TestSendStdinEmptyRejected(t *testing.T) {
	p := setupPair(t)
	_, _, code := runStdin(t, "\n\n", "send", "--home", p.homeA, "--stdin")
	if code != exitUsage {
		t.Fatalf("empty --stdin body exit %d, want usage error %d", code, exitUsage)
	}
}

func TestE2E(t *testing.T) {
	url := startTestServer(t)
	homeA := t.TempDir() // admin
	homeB := t.TempDir() // member

	// --- create (admin) ---
	out, errOut, code := run(t, "create", "--home", homeA, "--server", url,
		"--name", "Test Bridge", "--handle", "admin-bot", "--owner", "Dana")
	if code != exitOK {
		t.Fatalf("create exit %d, stderr=%s", code, errOut)
	}
	created := findLine(t, out, "created")
	inviteLink, _ := created["invite_link"].(string)
	inviteCode, _ := created["invite_code"].(string)
	if inviteLink == "" || inviteCode == "" {
		t.Fatalf("create missing invite link/code: %v", created)
	}

	// --- join (member) ---
	out, errOut, code = run(t, "join", "--home", homeB,
		"--link", inviteLink, "--code", inviteCode, "--handle", "member-bot", "--owner", "Amy")
	if code != exitOK {
		t.Fatalf("join exit %d, stderr=%s", code, errOut)
	}
	joined := findLine(t, out, "joined")
	memberLandID, _ := joined["land_id"].(string)
	if memberLandID == "" {
		t.Fatalf("join missing land_id: %v", joined)
	}

	// --- trust (admin confirms the new member before sending, Batch 1 item 4) ---
	if _, errOut, code = runApproved(t, "trust", "--home", homeA, "member-bot"); code != exitOK {
		t.Fatalf("trust exit %d, stderr=%s", code, errOut)
	}

	// --- send text + 1 MB image (admin -> member) ---
	img := pngImage(1 << 20)
	imgPath := filepath.Join(t.TempDir(), "quote.png")
	if err := os.WriteFile(imgPath, img, 0o600); err != nil {
		t.Fatalf("write image: %v", err)
	}
	out, errOut, code = runApproved(t, "send", "--home", homeA, "--file", imgPath, "hello world")
	if code != exitOK {
		t.Fatalf("send exit %d, stderr=%s", code, errOut)
	}
	sent := findLine(t, out, "sent")
	msgID, _ := sent["msg_id"].(string)
	if msgID == "" {
		t.Fatalf("send missing msg_id: %v", sent)
	}

	// --- poll (member): expect the message + attachment, verify file bytes ---
	out, errOut, code = run(t, "poll", "--home", homeB)
	if code != exitOK {
		t.Fatalf("poll exit %d, stderr=%s", code, errOut)
	}
	msg := findLine(t, out, "message")
	if msg["verified"] != true {
		t.Fatalf("message not verified: %v", msg)
	}
	if msg["text"] != "hello world" {
		t.Fatalf("text = %v, want 'hello world'", msg["text"])
	}
	atts, ok := msg["attachments"].([]any)
	if !ok || len(atts) != 1 {
		t.Fatalf("expected 1 attachment, got %v", msg["attachments"])
	}
	att := atts[0].(map[string]any)
	gotPath, _ := att["path"].(string)
	gotBytes, err := os.ReadFile(gotPath)
	if err != nil {
		t.Fatalf("read decrypted attachment %q: %v", gotPath, err)
	}
	if !bytes.Equal(gotBytes, img) {
		t.Fatalf("attachment content mismatch: got %d bytes, want %d", len(gotBytes), len(img))
	}
	// Attachment file must be 0600 (DEV_SPEC §12.1).
	assertMode(t, gotPath, 0o600)
	// A summary line must be present (DEV_SPEC §12.3).
	findLine(t, out, "summary")

	// --- poll again (member): exactly NO_NEW_MESSAGES, exit 0 ---
	out, errOut, code = run(t, "poll", "--home", homeB)
	if code != exitOK {
		t.Fatalf("empty poll exit %d, stderr=%s", code, errOut)
	}
	if out != "NO_NEW_MESSAGES\n" {
		t.Fatalf("empty poll stdout = %q, want exactly %q", out, "NO_NEW_MESSAGES\n")
	}

	// --- status (admin): member delivery is delivered ---
	out, errOut, code = run(t, "status", "--home", homeA, msgID)
	if code != exitOK {
		t.Fatalf("status exit %d, stderr=%s", code, errOut)
	}
	st := findLine(t, out, "status")
	ds, _ := st["deliveries"].([]any)
	if len(ds) != 1 {
		t.Fatalf("expected 1 delivery, got %v", st["deliveries"])
	}
	if d0 := ds[0].(map[string]any); d0["status"] != "delivered" {
		t.Fatalf("delivery status = %v, want delivered", d0["status"])
	}

	// --- rekey in-flight: admin sends, member rekeys, then still decrypts ---
	_, errOut, code = run(t, "send", "--home", homeA, "second message")
	if code != exitOK {
		t.Fatalf("send 2 exit %d, stderr=%s", code, errOut)
	}
	out, errOut, code = run(t, "rekey", "--home", homeB)
	if code != exitOK {
		t.Fatalf("rekey exit %d, stderr=%s", code, errOut)
	}
	findLine(t, out, "rekeyed")

	out, errOut, code = run(t, "poll", "--home", homeB)
	if code != exitOK {
		t.Fatalf("poll after rekey exit %d, stderr=%s", code, errOut)
	}
	msg2 := findLine(t, out, "message")
	if msg2["verified"] != true || msg2["text"] != "second message" {
		t.Fatalf("in-flight message did not decrypt after rekey: %v", msg2)
	}

	// --- kick on a two-bot bridge is refused in favour of close (v0.5.0) ---
	_, errOut, code = run(t, "kick", "--home", homeA, "member-bot")
	if code == exitOK || !strings.Contains(errOut, "use_close") {
		t.Fatalf("kick on a two-bot bridge: exit %d, stderr=%s", code, errOut)
	}
	_ = memberLandID

	// --- the member leaves: the two-bot bridge closes for both ---
	bridgeID, _ := created["bridge_id"].(string)
	_, errOut, code = runApproved(t, "leave", "--home", homeB, "--bridge", bridgeID)
	if code != exitOK {
		t.Fatalf("leave exit %d, stderr=%s", code, errOut)
	}

	// --- the leave closed the two-bot bridge: poll -> bridge_closed + exit 3 ---

	out, _, code = run(t, "poll", "--home", homeA)
	if code != exitClosed {
		t.Fatalf("poll after close exit %d, want %d; stdout=%s", code, exitClosed, out)
	}
	ev := findLine(t, out, "event")
	if ev["event"] != "bridge_closed" {
		t.Fatalf("expected bridge_closed event, got %v", ev)
	}
}

// makeLegacy turns a test bridge into a pre-v0.5.0 multi-member bridge
// (grandfathered rules: more than two bots, rotate after joins, a member
// leaving closes nothing).
func makeLegacy(t *testing.T, serverURL, bridgeID string) {
	t.Helper()
	ref, ok := testStores.Load(serverURL)
	if !ok {
		t.Fatalf("no test store for %s", serverURL)
	}
	if err := ref.(testStoreRef).st.SetBridgeLegacy(context.Background(), bridgeID, 32); err != nil {
		t.Fatalf("SetBridgeLegacy: %v", err)
	}
}
