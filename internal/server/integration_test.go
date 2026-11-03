package server

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hackerick4/botobot/internal/clock"
	"github.com/hackerick4/botobot/internal/config"
	"github.com/hackerick4/botobot/internal/protocol"
	"github.com/hackerick4/botobot/internal/store"
)

var integEpoch = time.Date(2026, 10, 29, 7, 0, 0, 0, time.UTC)

// testServer bundles a running M3 server over a temp SQLite store.
type testServer struct {
	h   http.Handler
	srv *Server
	st  *store.Store
	clk *clock.Fake
	buf *bytes.Buffer
	// dbPath is the SQLite file, for tests that count rows directly.
	dbPath string
}

func integrationConfig(mutate func(*config.Config)) *config.Config {
	c := &config.Config{
		PublicURL:                    "https://bridge.example",
		APIKeyPepper:                 bytes.Repeat([]byte{0x11}, 32),
		InviteKey:                    bytes.Repeat([]byte{0x22}, 32),
		TrustProxyHeader:             "",
		MaxLands:                     32,
		MaxEnvelopeBytes:             26214400,
		MaxQueueQuotaBytes:           2147483648,
		MaxInboxItemsPerLand:         10000,
		MaxInboxResponseBytes:        67108864,
		InboxTTL:                     24 * time.Hour,
		Lease:                        5 * time.Minute,
		DefaultRateLimitPerMin:       30,
		RateInboxPerMin:              6,
		RateAckPerMin:                60,
		RateOtherPerMin:              100000, // effectively unlimited for tests
		RateCreateBridgePerHourPerIP: 10000,
		ClaimTTL:                     time.Hour,
		JoinCreatePerHour:            10000, // effectively unlimited unless a test overrides
		MaxPendingBridges:            1000,
		MaxPendingBridgesPerPrefix:   1000, // effectively unlimited unless a test overrides
		AllowLegacyCreate:            true, // integration tests use the direct-create endpoint
		// Public entry (v0.6.0): generous unless a test overrides.
		EntryCreatePerHour:                1000,
		MaxEntriesPerPrefix:               1000,
		EntryMaxOpenBridges:               200,
		EntryJoinsPerHour:                 600,
		EntryJoinPerPrefixPerHour:         1000,
		EntryJoinPerPrefixPerEntryPerHour: 1000,
		LogLevel:                          "info",
		LogFormat:                         "json",
	}
	if mutate != nil {
		mutate(c)
	}
	return c
}

func newIntegServer(t *testing.T, mutate func(*config.Config)) *testServer {
	t.Helper()
	dir := t.TempDir()
	clk := clock.NewFake(integEpoch)
	st, err := store.Open(context.Background(), store.Options{
		Path:    filepath.Join(dir, "bridge.db"),
		BlobDir: filepath.Join(dir, "blobs"),
		Clock:   clk,
		Flush:   time.Millisecond,
	})
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	buf := &bytes.Buffer{}
	cfg := integrationConfig(mutate)
	logger := NewLogger("info", "json", buf)
	srv := New(cfg, logger, clk, WithStore(st), WithReady(st.Ping), WithJoinJitter(func() {}))
	return &testServer{h: srv.Handler(), srv: srv, st: st, clk: clk, buf: buf, dbPath: filepath.Join(dir, "bridge.db")}
}

// testLand is a land's full keypair for signing key bindings in requests.
type testLand struct {
	encPriv  []byte
	encPub   []byte
	signPub  ed25519.PublicKey
	signPriv ed25519.PrivateKey
}

func newTestLand(t *testing.T) testLand {
	t.Helper()
	encPriv := make([]byte, 32)
	if _, err := rand.Read(encPriv); err != nil {
		t.Fatalf("rand: %v", err)
	}
	encPub, err := protocol.PublicEncKey(encPriv)
	if err != nil {
		t.Fatalf("enc pub: %v", err)
	}
	signPub, signPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("ed25519: %v", err)
	}
	return testLand{encPriv: encPriv, encPub: encPub, signPub: signPub, signPriv: signPriv}
}

func (k testLand) keyInput(t *testing.T) landKeyInput {
	t.Helper()
	sig, err := protocol.SignBinding(k.signPriv, k.encPub, k.signPub)
	if err != nil {
		t.Fatalf("binding: %v", err)
	}
	return landKeyInput{EncPub: k.encPub, SignPub: k.signPub, BindingSig: sig}
}

// reqOpts customises a test request.
type reqOpts struct {
	token      string
	remoteAddr string
	rawBody    io.Reader         // overrides body marshalling when set
	contentTp  string            // overrides the default application/json
	noCT       bool              // omit Content-Type entirely
	headers    map[string]string // extra request headers (e.g. X-Forwarded-For)
}

func (ts *testServer) do(t *testing.T, method, path string, body any, o reqOpts) (int, map[string]any, http.Header) {
	t.Helper()
	var rdr io.Reader
	hasBody := false
	switch {
	case o.rawBody != nil:
		rdr = o.rawBody
		hasBody = true
	case body != nil:
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal body: %v", err)
		}
		rdr = bytes.NewReader(b)
		hasBody = true
	}
	req := httptest.NewRequest(method, path, rdr)
	if hasBody && !o.noCT {
		ct := o.contentTp
		if ct == "" {
			ct = "application/json"
		}
		req.Header.Set("Content-Type", ct)
	}
	if o.token != "" {
		req.Header.Set("Authorization", "Bearer "+o.token)
	}
	if o.remoteAddr != "" {
		req.RemoteAddr = o.remoteAddr
	}
	for k, v := range o.headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	ts.h.ServeHTTP(rec, req)

	var m map[string]any
	if b := rec.Body.Bytes(); len(bytes.TrimSpace(b)) > 0 {
		_ = json.Unmarshal(b, &m) // text pages (e.g. /b/...) decode to nil
	}
	return rec.Code, m, rec.Header()
}

// createdBridge holds the identifiers returned by a successful create.
type createdBridge struct {
	bridgeID string
	landID   string
	token    string
	code     string
}

func (ts *testServer) createBridge(t *testing.T, handle string, k testLand) createdBridge {
	t.Helper()
	body := createBridgeReq{
		Name: "ACME x Dana",
		Land: landBlock{
			Profile: landProfileInput{Handle: handle, DisplayName: "Dana Bot", OwnerName: "Dana"},
			Key:     k.keyInput(t),
		},
	}
	code, m, _ := ts.do(t, http.MethodPost, "/v1/bridges", body, reqOpts{})
	if code != http.StatusCreated {
		t.Fatalf("createBridge status = %d, body=%v", code, m)
	}
	return createdBridge{
		bridgeID: m["bridge"].(map[string]any)["bridge_id"].(string),
		landID:   m["land"].(map[string]any)["land_id"].(string),
		token:    m["api_key"].(string),
		code:     m["invite_code"].(string),
	}
}

// rotateCode rotates the bridge's invite code (admin) and returns the fresh
// code. Invite codes are one-time (Batch 1 item 5), so adding more than one
// member needs a fresh code per join.
// legacy turns b into a pre-v0.5.0 multi-member bridge (grandfathered rules).
func (ts *testServer) legacy(t *testing.T, b createdBridge) createdBridge {
	t.Helper()
	if err := ts.st.SetBridgeLegacy(context.Background(), b.bridgeID, 32); err != nil {
		t.Fatalf("SetBridgeLegacy: %v", err)
	}
	return b
}

func (ts *testServer) rotateCode(t *testing.T, b createdBridge) string {
	t.Helper()
	status, m, _ := ts.do(t, http.MethodPost, "/v1/bridges/"+b.bridgeID+"/invite-code/rotate", nil, reqOpts{token: b.token})
	if status != http.StatusOK {
		t.Fatalf("rotate invite-code status = %d (%v)", status, m)
	}
	return m["invite_code"].(string)
}

func (ts *testServer) join(t *testing.T, bridgeID, code, handle string, k testLand, remoteAddr string) (int, map[string]any) {
	t.Helper()
	body := joinReq{Code: code, Land: landBlock{
		Profile: landProfileInput{Handle: handle, DisplayName: "", OwnerName: ""},
		Key:     k.keyInput(t),
	}}
	code2, m, _ := ts.do(t, http.MethodPost, "/v1/bridges/"+bridgeID+"/join", body, reqOpts{remoteAddr: remoteAddr})
	return code2, m
}

// ---- §17.2 #2: wrong code -> 401; 6 tries -> 429; bridge lock after 20 ----

func TestJoinWrongCodeThenIPBackoff(t *testing.T) {
	ts := newIntegServer(t, nil)
	b := ts.createBridge(t, "dana-bot", newTestLand(t))

	const ip = "203.0.113.5:40000"
	// Five wrong guesses from one IP: each is 401 join_failed.
	for i := 0; i < 5; i++ {
		status, m := ts.join(t, b.bridgeID, "000001", "amy-bot", newTestLand(t), ip)
		if status != http.StatusUnauthorized {
			t.Fatalf("attempt %d status = %d, want 401 (%v)", i, status, m)
		}
		if ec := errCode(m); ec != "join_failed" {
			t.Fatalf("attempt %d code = %q, want join_failed", i, ec)
		}
	}
	// Sixth attempt is throttled: 429 rate_limited (IP backoff).
	status, m := ts.join(t, b.bridgeID, "000001", "amy-bot", newTestLand(t), ip)
	if status != http.StatusTooManyRequests {
		t.Fatalf("6th attempt status = %d, want 429 (%v)", status, m)
	}
	if ec := errCode(m); ec != "rate_limited" {
		t.Fatalf("6th attempt code = %q, want rate_limited", ec)
	}
}

func TestJoinBridgeLockAfterTwenty(t *testing.T) {
	ts := newIntegServer(t, nil)
	b := ts.createBridge(t, "dana-bot", newTestLand(t))

	// Twenty wrong guesses from distinct IPs in one /24 (a distributed attack):
	// each 401, no single IP backs off, and the subnet 1h/30 ban is not reached.
	for i := 1; i <= 20; i++ {
		ip := "198.51.100." + itoa(i) + ":5000"
		status, m := ts.join(t, b.bridgeID, "000001", "amy-bot", newTestLand(t), ip)
		if status != http.StatusUnauthorized {
			t.Fatalf("attempt %d status = %d, want 401 (%v)", i, status, m)
		}
	}
	// The bridge is now locked: even a fresh IP is throttled.
	status, m := ts.join(t, b.bridgeID, "000001", "amy-bot", newTestLand(t), "198.51.100.200:5000")
	if status != http.StatusTooManyRequests {
		t.Fatalf("post-lock status = %d, want 429 (%v)", status, m)
	}
	if ec := errCode(m); ec != "rate_limited" {
		t.Fatalf("post-lock code = %q, want rate_limited", ec)
	}
}

// ---- §17.2 #3: land_set_version increments on join ----

func TestLandSetVersionIncrementsOnJoin(t *testing.T) {
	ts := newIntegServer(t, nil)
	b := ts.legacy(t, ts.createBridge(t, "dana-bot", newTestLand(t)))

	// Fresh bridge: version 1 (set at create, not bumped).
	if v := ts.bridgeVersion(t, b); v != 1 {
		t.Fatalf("version after create = %d, want 1", v)
	}
	// Each successful join bumps the version by one.
	if status, m := ts.join(t, b.bridgeID, b.code, "amy-bot", newTestLand(t), "192.0.2.10:1"); status != http.StatusCreated {
		t.Fatalf("join amy status = %d (%v)", status, m)
	}
	if v := ts.bridgeVersion(t, b); v != 2 {
		t.Fatalf("version after 1 join = %d, want 2", v)
	}
	if status, m := ts.join(t, b.bridgeID, ts.rotateCode(t, b), "zoe-bot", newTestLand(t), "192.0.2.11:1"); status != http.StatusCreated {
		t.Fatalf("join zoe status = %d (%v)", status, m)
	}
	if v := ts.bridgeVersion(t, b); v != 3 {
		t.Fatalf("version after 2 joins = %d, want 3", v)
	}
	// GET /lands reports the same version and three lands.
	status, m, _ := ts.do(t, http.MethodGet, "/v1/bridges/"+b.bridgeID+"/lands", nil, reqOpts{token: b.token})
	if status != http.StatusOK {
		t.Fatalf("GET lands status = %d", status)
	}
	if v := int64(m["land_set_version"].(float64)); v != 3 {
		t.Fatalf("lands version = %d, want 3", v)
	}
	if n := len(m["lands"].([]any)); n != 3 {
		t.Fatalf("land count = %d, want 3", n)
	}

	// The admin received land_joined events reflecting the new land set.
	assertEventEnqueued(t, ts, b.bridgeID, b.landID, protocol.EventLandJoined)
}

func (ts *testServer) bridgeVersion(t *testing.T, b createdBridge) int64 {
	t.Helper()
	status, m, _ := ts.do(t, http.MethodGet, "/v1/bridges/"+b.bridgeID, nil, reqOpts{token: b.token})
	if status != http.StatusOK {
		t.Fatalf("GET bridge status = %d (%v)", status, m)
	}
	return int64(m["bridge"].(map[string]any)["land_set_version"].(float64))
}

// ---- §17.2 #8: key change -> key_changed event ----

func TestRekeyEmitsKeyChangedEvent(t *testing.T) {
	ts := newIntegServer(t, nil)
	b := ts.createBridge(t, "dana-bot", newTestLand(t))

	member := newTestLand(t)
	status, jm := ts.join(t, b.bridgeID, b.code, "amy-bot", member, "192.0.2.20:1")
	if status != http.StatusCreated {
		t.Fatalf("join status = %d (%v)", status, jm)
	}
	memberToken := jm["api_key"].(string)

	versionBefore := ts.bridgeVersion(t, b)

	// Member rotates its public key.
	newKey := newTestLand(t)
	status, m, _ := ts.do(t, http.MethodPut, "/v1/me/public-key", newKey.keyInput(t), reqOpts{token: memberToken})
	if status != http.StatusOK {
		t.Fatalf("PUT public-key status = %d (%v)", status, m)
	}

	if v := ts.bridgeVersion(t, b); v != versionBefore+1 {
		t.Fatalf("version after rekey = %d, want %d", v, versionBefore+1)
	}
	// The admin received a key_changed event.
	ev := assertEventEnqueued(t, ts, b.bridgeID, b.landID, protocol.EventKeyChanged)
	if ev.LandSetVersion != versionBefore+1 {
		t.Fatalf("key_changed land_set_version = %d, want %d", ev.LandSetVersion, versionBefore+1)
	}
}

// assertEventEnqueued drains the recipient's inbox (store level, since GET
// /inbox is M4) and returns the first event of the given type, failing if none.
func assertEventEnqueued(t *testing.T, ts *testServer, bridgeID, recipientLandID string, typ protocol.EventType) protocol.Event {
	t.Helper()
	items, err := ts.st.FetchInbox(context.Background(), bridgeID, recipientLandID, 200)
	if err != nil {
		t.Fatalf("FetchInbox: %v", err)
	}
	for _, it := range items {
		if it.Kind != "event" {
			continue
		}
		var ev protocol.Event
		if err := json.Unmarshal(it.EventJSON, &ev); err != nil {
			t.Fatalf("unmarshal event: %v", err)
		}
		if ev.Type == typ {
			return ev
		}
	}
	t.Fatalf("no %s event enqueued to %s", typ, recipientLandID)
	return protocol.Event{}
}

func errCode(m map[string]any) string {
	if m == nil {
		return ""
	}
	e, ok := m["error"].(map[string]any)
	if !ok {
		return ""
	}
	c, _ := e["code"].(string)
	return c
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var buf [8]byte
	n := len(buf)
	for i > 0 {
		n--
		buf[n] = byte('0' + i%10)
		i /= 10
	}
	return string(buf[n:])
}

// ---- Error-code coverage (DEV_SPEC §6.2) ----

func TestErrorCodes(t *testing.T) {
	ts := newIntegServer(t, nil)
	b := ts.legacy(t, ts.createBridge(t, "dana-bot", newTestLand(t)))

	t.Run("missing auth -> 401 unauthorized", func(t *testing.T) {
		status, m, _ := ts.do(t, http.MethodGet, "/v1/me", nil, reqOpts{})
		if status != http.StatusUnauthorized || errCode(m) != "unauthorized" {
			t.Fatalf("got %d %q", status, errCode(m))
		}
	})

	t.Run("bad token -> 401 unauthorized", func(t *testing.T) {
		status, m, _ := ts.do(t, http.MethodGet, "/v1/me", nil, reqOpts{token: "bbk_not_a_real_key"})
		if status != http.StatusUnauthorized || errCode(m) != "unauthorized" {
			t.Fatalf("got %d %q", status, errCode(m))
		}
	})

	t.Run("wrong bridge in path -> 404 not_found", func(t *testing.T) {
		status, m, _ := ts.do(t, http.MethodGet, "/v1/bridges/br_doesnotexist000000000000/lands", nil, reqOpts{token: b.token})
		if status != http.StatusNotFound || errCode(m) != "not_found" {
			t.Fatalf("got %d %q", status, errCode(m))
		}
	})

	t.Run("member PATCH bridge -> 403 forbidden", func(t *testing.T) {
		_, jm := ts.join(t, b.bridgeID, b.code, "amy-bot", newTestLand(t), "192.0.2.30:1")
		memberToken := jm["api_key"].(string)
		status, m, _ := ts.do(t, http.MethodPatch, "/v1/bridges/"+b.bridgeID, patchBridgeReq{Name: ptr("x")}, reqOpts{token: memberToken})
		if status != http.StatusForbidden || errCode(m) != "forbidden" {
			t.Fatalf("got %d %q", status, errCode(m))
		}
	})

	t.Run("unknown field -> 400 bad_request", func(t *testing.T) {
		status, m, _ := ts.do(t, http.MethodPatch, "/v1/me", nil, reqOpts{token: b.token, rawBody: strings.NewReader(`{"nope":1}`)})
		if status != http.StatusBadRequest || errCode(m) != "bad_request" {
			t.Fatalf("got %d %q", status, errCode(m))
		}
	})

	t.Run("non-JSON content type -> 415", func(t *testing.T) {
		status, m, _ := ts.do(t, http.MethodPatch, "/v1/me", nil, reqOpts{token: b.token, rawBody: strings.NewReader(`{}`), contentTp: "text/plain"})
		if status != http.StatusUnsupportedMediaType || errCode(m) != "unsupported_media_type" {
			t.Fatalf("got %d %q", status, errCode(m))
		}
	})

	t.Run("body too large -> 413", func(t *testing.T) {
		big := strings.NewReader(`{"display_name":"` + strings.Repeat("a", 70*1024) + `"}`)
		status, m, _ := ts.do(t, http.MethodPatch, "/v1/me", nil, reqOpts{token: b.token, rawBody: big})
		if status != http.StatusRequestEntityTooLarge || errCode(m) != "payload_too_large" {
			t.Fatalf("got %d %q", status, errCode(m))
		}
	})

	t.Run("handle_taken -> 409", func(t *testing.T) {
		// Fresh code (the earlier subtest consumed the one-time code) so the join
		// reaches the handle check rather than failing as a used code.
		status, m := ts.join(t, b.bridgeID, ts.rotateCode(t, b), "dana-bot", newTestLand(t), "192.0.2.40:1") // admin's handle
		if status != http.StatusConflict || errCode(m) != "handle_taken" {
			t.Fatalf("got %d %q", status, errCode(m))
		}
	})
}

func TestBridgeFull(t *testing.T) {
	ts := newIntegServer(t, func(c *config.Config) { c.MaxLands = 1 })
	b := ts.createBridge(t, "dana-bot", newTestLand(t))
	// The admin already fills the single slot; a correct code now yields 409.
	status, m := ts.join(t, b.bridgeID, b.code, "amy-bot", newTestLand(t), "192.0.2.50:1")
	if status != http.StatusConflict || errCode(m) != "bridge_full" {
		t.Fatalf("got %d %q, want 409 bridge_full", status, errCode(m))
	}
}

func TestCloseBridgeThenGone(t *testing.T) {
	ts := newIntegServer(t, nil)
	b := ts.createBridge(t, "dana-bot", newTestLand(t))

	status, m, _ := ts.do(t, http.MethodDelete, "/v1/bridges/"+b.bridgeID, nil, reqOpts{token: b.token})
	if status != http.StatusOK || m["closed"] != true {
		t.Fatalf("delete bridge got %d %v", status, m)
	}
	// Subsequent bridge-scoped requests see 410 bridge_closed (tombstone).
	status, m, _ = ts.do(t, http.MethodGet, "/v1/bridges/"+b.bridgeID+"/lands", nil, reqOpts{token: b.token})
	if status != http.StatusGone || errCode(m) != "bridge_closed" {
		t.Fatalf("post-close got %d %q, want 410 bridge_closed", status, errCode(m))
	}
}

func ptr[T any](v T) *T { return &v }

// ---- §15 / §17.2 #10: logs across endpoints contain no secrets ----

func TestLogsHaveNoSecretsAcrossEndpoints(t *testing.T) {
	ts := newIntegServer(t, nil)
	admin := newTestLand(t)
	b := ts.createBridge(t, "dana-bot", admin)

	// Exercise a representative spread of endpoints with real secrets in play.
	member := newTestLand(t)
	_, jm := ts.join(t, b.bridgeID, b.code, "amy-bot", member, "192.0.2.60:1")
	memberToken, _ := jm["api_key"].(string)

	ts.do(t, http.MethodGet, "/v1/bridges/"+b.bridgeID, nil, reqOpts{token: b.token})
	ts.do(t, http.MethodGet, "/v1/bridges/"+b.bridgeID+"/invite-code", nil, reqOpts{token: b.token})
	ts.do(t, http.MethodGet, "/v1/me", nil, reqOpts{token: b.token})
	ts.do(t, http.MethodPost, "/v1/me/ping", nil, reqOpts{token: memberToken})
	// A wrong-code guess must never be logged either.
	ts.join(t, b.bridgeID, "000002", "eve-bot", newTestLand(t), "192.0.2.61:1")

	// M4: a real send -> inbox -> ack round trip must not leak any crypto
	// material (ciphertext, wrapped_key, signature, nonce) into the logs.
	adminLander := lander{landID: b.landID, keyID: ts.keyIDOf(t, b.token, b.bridgeID, b.landID), token: b.token, k: admin}
	env := ts.encrypt(t, adminLander, b, "c-logsecret-1", "secret payload text")
	if status, m := ts.send(t, b.token, b.bridgeID, env); status != http.StatusAccepted {
		t.Fatalf("send for log test = %d (%v)", status, m)
	}
	iw := ts.inbox(t, memberToken, b.bridgeID)
	var ackIDs []string
	for _, e := range iw.Envelopes {
		ackIDs = append(ackIDs, e.ID)
	}
	if len(ackIDs) > 0 {
		ts.ack(t, memberToken, b.bridgeID, ackIDs)
	}

	logs := ts.buf.String()
	secrets := []string{
		b.token, memberToken, b.code, "000002",
		b.token[:12], "Authorization", "Bearer",
	}
	for _, s := range secrets {
		if s == "" {
			continue
		}
		if strings.Contains(logs, s) {
			t.Errorf("log output contains forbidden token %q", s)
		}
	}
	// Public-key material must not appear in logs either.
	if strings.Contains(logs, encodeStd(admin.encPub)) || strings.Contains(logs, encodeStd(member.signPub)) {
		t.Errorf("log output contains public key material")
	}
	// M4 crypto material must not appear either (base64 of ciphertext, the
	// recipient's wrapped key, the signature, and the nonce).
	cryptoFragments := map[string]string{
		"ciphertext":  encodeStd(env.Ciphertext),
		"signature":   encodeStd(env.Signature),
		"nonce":       encodeStd(env.Header.Nonce),
		"wrapped_key": encodeStd(env.WrappedKeys[adminLander.landID]),
	}
	// The sender is not a recipient, so use amy's wrapped key explicitly.
	cryptoFragments["wrapped_key"] = encodeStd(env.WrappedKeys[iwFirstRecipient(env)])
	for name, frag := range cryptoFragments {
		if frag == "" {
			continue
		}
		if strings.Contains(logs, frag) {
			t.Errorf("log output contains %s base64 fragment", name)
		}
	}
}

// concretePath substitutes placeholder values for the {…} wildcards in a route
// pattern so a request can be dispatched to it. The values are not secrets (path
// values are never logged — only the route template is, DEV_SPEC §15).
func concretePath(pattern string) string {
	return strings.NewReplacer(
		"{bridge_id}", "br_placeholderbridgeid0",
		"{land_id}", "land_placeholderlandid0",
		"{msg_id}", "c-placeholder-msg",
		"{key_id}", "key_placeholderkeyid00",
	).Replace(pattern)
}

// TestLogsHaveNoSecretsEveryRoute enumerates the server's route table and drives
// a secret-laden request at every endpoint (bogus bearer token, invite code and
// key material in the body, secrets in the query, a spoofable forwarding header)
// so the no-secret logging guarantee is proven against the whole surface and new
// routes are covered automatically (DEV_SPEC §15, §16 T7.1). Error paths — bad
// auth, wrong invite code, oversize body — are included; the panic path is
// covered by TestPanicRecovery.
func TestLogsHaveNoSecretsEveryRoute(t *testing.T) {
	ts := newIntegServer(t, nil)

	const (
		apiKey   = "bbk_EVERYROUTESECRETKEY1234567890abcdefg"
		code     = "482913"
		claimTok = "bbc_EVERYROUTECLAIMTOKEN1234567890abcdefg"
		rawIP    = "203.0.113.77"
		encPub   = "QUJDREVGR0hJSktMTU5PUFFSU1RVVldYWVowMTIzNDU=" // 32-byte b64
		signPub  = "YWJjZGVmZ2hpamtsbW5vcHFyc3R1dnd4eXowMTIzNDU2" // 32-byte b64
	)
	// A body carrying every kind of secret a handler might see.
	secretBody := map[string]any{
		"code":  code,
		"token": claimTok,
		"name":  "x",
		"land": map[string]any{
			"profile": map[string]any{"handle": "eve-bot"},
			"key":     map[string]any{"enc_pub": encPub, "sign_pub": signPub, "binding_sig": encPub},
		},
		"ids":           []string{"env_1"},
		"revoke_others": true,
	}

	for _, rt := range ts.srv.routes() {
		path := concretePath(rt.pattern) + "?code=" + code + "&secret=" + apiKey
		var rdr io.Reader
		switch rt.method {
		case http.MethodPost, http.MethodPut, http.MethodPatch:
			b, _ := json.Marshal(secretBody)
			rdr = bytes.NewReader(b)
		}
		req := httptest.NewRequest(rt.method, path, rdr)
		if rdr != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		req.Header.Set("Authorization", "Bearer "+apiKey)
		req.Header.Set("X-Forwarded-For", rawIP)
		req.RemoteAddr = "198.51.100.9:1234"
		ts.h.ServeHTTP(httptest.NewRecorder(), req)
	}

	// Oversize bodies on representative endpoints hit the 413 error path with a
	// secret-laden Authorization header in play.
	big := bytes.Repeat([]byte("a"), 70*1024)
	for _, p := range []string{"/v1/bridges", "/v1/bridges/br_x/join"} {
		req := httptest.NewRequest(http.MethodPost, p, bytes.NewReader(big))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+apiKey)
		ts.h.ServeHTTP(httptest.NewRecorder(), req)
	}

	logs := ts.buf.String()
	for _, secret := range []string{apiKey, apiKey[:12], code, claimTok, "Authorization", "Bearer", rawIP, encPub, signPub} {
		if strings.Contains(logs, secret) {
			t.Errorf("log output contains forbidden token %q", secret)
		}
	}
}

// TestOversizeBodyRejected verifies the body-size caps (DEV_SPEC §6.1): a small
// JSON endpoint rejects >64 KiB and /send rejects beyond its base64-aware cap,
// both as 413 payload_too_large with the §6.2 error shape.
func TestOversizeBodyRejected(t *testing.T) {
	ts := newIntegServer(t, func(c *config.Config) { c.MaxEnvelopeBytes = 4096 })
	b := ts.createBridge(t, "dana-bot", newTestLand(t))

	// Valid-leading JSON whose string value overruns the cap, so the decoder
	// reads past the limit and MaxBytesReader fires (a non-JSON blob would just
	// 400 on the first byte).
	oversizeJSON := func(field string, n int) io.Reader {
		return bytes.NewReader([]byte(`{"` + field + `":"` + strings.Repeat("a", n) + `"}`))
	}

	// Small JSON endpoint: 64 KiB cap (create bridge, unauthenticated).
	status, m, _ := ts.do(t, http.MethodPost, "/v1/bridges", nil,
		reqOpts{rawBody: oversizeJSON("name", 65*1024)})
	if status != http.StatusRequestEntityTooLarge || errCode(m) != "payload_too_large" {
		t.Fatalf("oversize create = %d %q, want 413 payload_too_large", status, errCode(m))
	}

	// /send: cap is 4/3·MaxEnvelopeBytes + 1 MiB. Exceed it.
	bodyCap := sendBodyCap(ts.srv.cfg.MaxEnvelopeBytes)
	status, m, _ = ts.do(t, http.MethodPost, "/v1/bridges/"+b.bridgeID+"/send", nil,
		reqOpts{token: b.token, rawBody: oversizeJSON("ciphertext", int(bodyCap)+1024)})
	if status != http.StatusRequestEntityTooLarge || errCode(m) != "payload_too_large" {
		t.Fatalf("oversize send = %d %q, want 413 payload_too_large", status, errCode(m))
	}
}

// iwFirstRecipient returns the land_id of the envelope's first recipient (for
// locating a wrapped key to scan logs against).
func iwFirstRecipient(env *protocol.Envelope) string {
	if len(env.Header.Recipients) == 0 {
		return ""
	}
	return env.Header.Recipients[0].LandID
}

func encodeStd(b []byte) string {
	// Match the JSON wire encoding (standard base64 with padding).
	buf, _ := json.Marshal(b)
	return strings.Trim(string(buf), `"`)
}
