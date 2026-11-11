package server

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/hackerick4/botobot/internal/config"
	"github.com/hackerick4/botobot/internal/protocol"
)

// ---- Typed helpers for the message/inbox flow (M4) ----

// lander bundles a land's server identity (ids, token) with the keypair needed
// to sign and decrypt (DEV_SPEC §7).
type lander struct {
	landID string
	keyID  string
	token  string
	k      testLand
}

// landsWire / landsLand decode GET /lands; inboxWire / inboxEnv decode GET
// /inbox. They reuse protocol.Header so the stored signed header round-trips.
type landsWire struct {
	LandSetVersion int64 `json:"land_set_version"`
	Lands          []struct {
		LandID string `json:"land_id"`
		KeyID  string `json:"key_id"`
		EncPub []byte `json:"enc_pub"`
	} `json:"lands"`
}

type inboxWire struct {
	Envelopes []struct {
		ID         string          `json:"id"`
		Kind       string          `json:"kind"`
		FetchCount int             `json:"fetch_count"`
		Header     protocol.Header `json:"header"`
		Signature  []byte          `json:"signature"`
		Ciphertext []byte          `json:"ciphertext"`
		WrappedKey []byte          `json:"wrapped_key"`
		Event      protocol.Event  `json:"event"`
	} `json:"envelopes"`
	HasMore         bool   `json:"has_more"`
	PollIntervalSec int    `json:"poll_interval_sec"`
	ServerTime      string `json:"server_time"`
}

// doRaw is do without JSON-decoding the body, for responses with binary fields.
func (ts *testServer) doRaw(t *testing.T, method, path string, body any, o reqOpts) (int, []byte, http.Header) {
	t.Helper()
	var rdr io.Reader
	hasBody := false
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal body: %v", err)
		}
		rdr = bytes.NewReader(b)
		hasBody = true
	}
	req := httptest.NewRequest(method, path, rdr)
	if hasBody {
		req.Header.Set("Content-Type", "application/json")
	}
	if o.token != "" {
		req.Header.Set("Authorization", "Bearer "+o.token)
	}
	rec := httptest.NewRecorder()
	ts.h.ServeHTTP(rec, req)
	return rec.Code, rec.Body.Bytes(), rec.Header()
}

func (ts *testServer) newAdmin(t *testing.T, handle string) (lander, createdBridge) {
	t.Helper()
	k := newTestLand(t)
	b := ts.createBridge(t, handle, k)
	return lander{landID: b.landID, keyID: ts.keyIDOf(t, b.token, b.bridgeID, b.landID), token: b.token, k: k}, b
}

func (ts *testServer) newMember(t *testing.T, b createdBridge, handle, ip string) lander {
	t.Helper()
	k := newTestLand(t)
	// Invite codes are one-time (Batch 1 item 5): the admin rotates a fresh code
	// for each member it adds, which is the realistic flow.
	status, m := ts.join(t, b.bridgeID, ts.rotateCode(t, b), handle, k, ip)
	if status != http.StatusCreated {
		t.Fatalf("join %s status = %d (%v)", handle, status, m)
	}
	land := m["land"].(map[string]any)
	return lander{
		landID: land["land_id"].(string),
		keyID:  land["key_id"].(string),
		token:  m["api_key"].(string),
		k:      k,
	}
}

// keyIDOf returns the active key id of a land via GET /lands.
func (ts *testServer) keyIDOf(t *testing.T, token, bridgeID, landID string) string {
	t.Helper()
	lw := ts.lands(t, token, bridgeID)
	for _, l := range lw.Lands {
		if l.LandID == landID {
			return l.KeyID
		}
	}
	t.Fatalf("keyIDOf: land %s not found", landID)
	return ""
}

func (ts *testServer) lands(t *testing.T, token, bridgeID string) landsWire {
	t.Helper()
	status, body, _ := ts.doRaw(t, http.MethodGet, "/v1/bridges/"+bridgeID+"/lands", nil, reqOpts{token: token})
	if status != http.StatusOK {
		t.Fatalf("GET lands status = %d: %s", status, body)
	}
	var lw landsWire
	if err := json.Unmarshal(body, &lw); err != nil {
		t.Fatalf("unmarshal lands: %v", err)
	}
	return lw
}

// encrypt builds a real signed envelope from s to every other current land,
// using the land set captured now (so a later join makes it stale).
func (ts *testServer) encrypt(t *testing.T, s lander, b createdBridge, msgID, text string) *protocol.Envelope {
	t.Helper()
	lw := ts.lands(t, s.token, b.bridgeID)
	var recips []protocol.RecipientKey
	for _, l := range lw.Lands {
		if l.LandID == s.landID {
			continue
		}
		recips = append(recips, protocol.RecipientKey{LandID: l.LandID, KeyID: l.KeyID, EncPub: l.EncPub})
	}
	env, err := protocol.Encrypt(protocol.EncryptInput{
		BridgeID:       b.bridgeID,
		MsgID:          msgID,
		SenderLandID:   s.landID,
		SenderKeyID:    s.keyID,
		SenderSignPriv: s.k.signPriv,
		LandSetVersion: lw.LandSetVersion,
		CreatedAt:      "2026-10-29T07:02:00.000Z",
		Plaintext:      []byte(`{"type":"message","text":` + strconv.Quote(text) + `,"sent_at":"2026-10-29T07:02:00.000Z"}`),
		Recipients:     recips,
	})
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	return env
}

func (ts *testServer) send(t *testing.T, token, bridgeID string, env *protocol.Envelope) (int, map[string]any) {
	t.Helper()
	status, m, _ := ts.do(t, http.MethodPost, "/v1/bridges/"+bridgeID+"/send", env, reqOpts{token: token})
	return status, m
}

func (ts *testServer) inbox(t *testing.T, token, bridgeID string) inboxWire {
	t.Helper()
	status, body, _ := ts.doRaw(t, http.MethodGet, "/v1/bridges/"+bridgeID+"/inbox", nil, reqOpts{token: token})
	if status != http.StatusOK {
		t.Fatalf("GET inbox status = %d: %s", status, body)
	}
	var iw inboxWire
	if err := json.Unmarshal(body, &iw); err != nil {
		t.Fatalf("unmarshal inbox: %v", err)
	}
	return iw
}

func (ts *testServer) ack(t *testing.T, token, bridgeID string, ids []string) map[string]any {
	t.Helper()
	status, m, _ := ts.do(t, http.MethodPost, "/v1/bridges/"+bridgeID+"/inbox/ack", map[string]any{"ids": ids}, reqOpts{token: token})
	if status != http.StatusOK {
		t.Fatalf("ack status = %d (%v)", status, m)
	}
	return m
}

// decryptMessage reconstructs an inbox-delivered envelope (only this
// recipient's wrapped key) and returns the plaintext text (DEV_SPEC §7.8).
func decryptMessage(t *testing.T, iw inboxWire, me lander, senderSignPub []byte) (string, string) {
	t.Helper()
	for _, e := range iw.Envelopes {
		if e.Kind != "message" {
			continue
		}
		env := &protocol.Envelope{
			Header:      e.Header,
			Signature:   e.Signature,
			Ciphertext:  e.Ciphertext,
			WrappedKeys: map[string][]byte{me.landID: e.WrappedKey},
		}
		pt, err := protocol.Decrypt(protocol.DecryptInput{
			Envelope:      env,
			SenderSignPub: senderSignPub,
			MyLandID:      me.landID,
			MyEncKeys:     map[string][]byte{me.keyID: me.k.encPriv},
		})
		if err != nil {
			t.Fatalf("decrypt as %s: %v", me.landID, err)
		}
		var p protocol.Payload
		if err := json.Unmarshal(pt, &p); err != nil {
			t.Fatalf("payload unmarshal: %v", err)
		}
		return e.ID, p.Text
	}
	t.Fatalf("no message envelope found for %s", me.landID)
	return "", ""
}

// ---- §17.2 #1: create -> join x2 -> send -> both decrypt -> ack -> status ----

func TestSendDeliverDecryptAckStatus(t *testing.T) {
	ts := newIntegServer(t, nil)
	rick, b := ts.newAdmin(t, "sample-bot")
	ts.legacy(t, b) // pre-v0.5.0 multi-member bridge
	amy := ts.newMember(t, b, "amy-bot", "192.0.2.10:1")
	zoe := ts.newMember(t, b, "zoe-bot", "192.0.2.11:1")

	env := ts.encrypt(t, rick, b, "c-20261029-0001", "hello bots")
	status, m := ts.send(t, rick.token, b.bridgeID, env)
	if status != http.StatusAccepted {
		t.Fatalf("send status = %d (%v)", status, m)
	}
	if got := len(m["deliveries"].([]any)); got != 2 {
		t.Fatalf("deliveries = %d, want 2", got)
	}

	for _, me := range []lander{amy, zoe} {
		iw := ts.inbox(t, me.token, b.bridgeID)
		id, text := decryptMessage(t, iw, me, rick.k.signPub)
		if text != "hello bots" {
			t.Fatalf("%s decrypted text = %q", me.landID, text)
		}
		// Ack every fetched item (message + any join events) to drain the inbox.
		var ids []string
		for _, e := range iw.Envelopes {
			ids = append(ids, e.ID)
		}
		res := ts.ack(t, me.token, b.bridgeID, ids)
		if int(res["deleted"].(float64)) != len(ids) {
			t.Fatalf("%s ack deleted = %v, want %d", me.landID, res["deleted"], len(ids))
		}
		_ = id
	}

	// Sender sees both recipients delivered.
	status, sm, _ := ts.do(t, http.MethodGet, "/v1/bridges/"+b.bridgeID+"/messages/c-20261029-0001/status", nil, reqOpts{token: rick.token})
	if status != http.StatusOK {
		t.Fatalf("status endpoint = %d (%v)", status, sm)
	}
	ds := sm["deliveries"].([]any)
	if len(ds) != 2 {
		t.Fatalf("status deliveries = %d, want 2", len(ds))
	}
	for _, d := range ds {
		if s := d.(map[string]any)["status"]; s != "delivered" {
			t.Fatalf("delivery status = %v, want delivered", s)
		}
	}
}

// ---- §17.2 #3: join a new land, then send with the old version -> 409 ----

func TestSendRecipientsMismatchAfterJoin(t *testing.T) {
	ts := newIntegServer(t, nil)
	rick, b := ts.newAdmin(t, "sample-bot")
	ts.legacy(t, b) // pre-v0.5.0 multi-member bridge
	ts.newMember(t, b, "amy-bot", "192.0.2.20:1")

	// Build an envelope for the current land set (rick + amy, version 2).
	env := ts.encrypt(t, rick, b, "c-stale-0001", "stale")

	// A new land joins, bumping the version and changing the recipient set.
	ts.newMember(t, b, "zoe-bot", "192.0.2.21:1")

	status, m := ts.send(t, rick.token, b.bridgeID, env)
	if status != http.StatusConflict || errCode(m) != "recipients_mismatch" {
		t.Fatalf("stale send = %d %q, want 409 recipients_mismatch", status, errCode(m))
	}
	ev := m["error"].(map[string]any)
	if int64(ev["land_set_version"].(float64)) != 3 {
		t.Fatalf("mismatch land_set_version = %v, want 3", ev["land_set_version"])
	}
	if len(ev["recipients"].([]any)) != 2 {
		t.Fatalf("mismatch recipients = %v, want 2 (current set)", ev["recipients"])
	}
}

// ---- §17.2 #4: lease hides a fetched-but-unacked item until it expires ----

func TestInboxLeaseHidesThenReappears(t *testing.T) {
	ts := newIntegServer(t, func(c *config.Config) { c.RateInboxPerMin = 100000 })
	rick, b := ts.newAdmin(t, "sample-bot")
	amy := ts.newMember(t, b, "amy-bot", "192.0.2.30:1")

	env := ts.encrypt(t, rick, b, "c-lease-0001", "lease me")
	if status, m := ts.send(t, rick.token, b.bridgeID, env); status != http.StatusAccepted {
		t.Fatalf("send = %d (%v)", status, m)
	}

	first := ts.inbox(t, amy.token, b.bridgeID)
	if countMessages(first) != 1 {
		t.Fatalf("first fetch messages = %d, want 1", countMessages(first))
	}
	// Re-fetch within the lease: the message is hidden (no ack happened).
	again := ts.inbox(t, amy.token, b.bridgeID)
	if countMessages(again) != 0 {
		t.Fatalf("leased re-fetch messages = %d, want 0", countMessages(again))
	}
	// Advance past the 5-minute lease: it reappears with fetch_count incremented.
	ts.clk.Advance(5*time.Minute + time.Second)
	third := ts.inbox(t, amy.token, b.bridgeID)
	if countMessages(third) != 1 {
		t.Fatalf("post-lease fetch messages = %d, want 1", countMessages(third))
	}
	for _, e := range third.Envelopes {
		if e.Kind == "message" && e.FetchCount != 2 {
			t.Fatalf("fetch_count = %d, want 2", e.FetchCount)
		}
	}
}

func countMessages(iw inboxWire) int {
	n := 0
	for _, e := range iw.Envelopes {
		if e.Kind == "message" {
			n++
		}
	}
	return n
}

// ---- §17.2 #6: small quota -> 507; after ack can send again ----

func TestSendQuotaExceededThenReclaimed(t *testing.T) {
	ts := newIntegServer(t, func(c *config.Config) { c.MaxQueueQuotaBytes = 1 << 20 }) // 1 MiB bridge quota
	rick, b := ts.newAdmin(t, "sample-bot")
	amy := ts.newMember(t, b, "amy-bot", "192.0.2.40:1")

	big := string(bytes.Repeat([]byte("a"), 700000)) // ~700 KiB; two won't fit in 1 MiB

	env1 := ts.encrypt(t, rick, b, "c-q-1", big)
	if status, m := ts.send(t, rick.token, b.bridgeID, env1); status != http.StatusAccepted {
		t.Fatalf("first send = %d (%v)", status, m)
	}

	// A second large message would exceed the quota.
	env2 := ts.encrypt(t, rick, b, "c-q-2", big)
	status, m := ts.send(t, rick.token, b.bridgeID, env2)
	if status != http.StatusInsufficientStorage || errCode(m) != "queue_quota_exceeded" {
		t.Fatalf("second send = %d %q, want 507 queue_quota_exceeded", status, errCode(m))
	}

	// Amy acks the first (single recipient -> refcount 0 -> quota freed).
	iw := ts.inbox(t, amy.token, b.bridgeID)
	var ids []string
	for _, e := range iw.Envelopes {
		ids = append(ids, e.ID)
	}
	ts.ack(t, amy.token, b.bridgeID, ids)

	// Now the second message fits.
	if status, m := ts.send(t, rick.token, b.bridgeID, env2); status != http.StatusAccepted {
		t.Fatalf("resend after ack = %d %q (%v)", status, errCode(m), m)
	}
}

// ---- §17.2 #9: idempotent send + duplicate ack ----

func TestSendIdempotentAndDuplicateAck(t *testing.T) {
	ts := newIntegServer(t, nil)
	rick, b := ts.newAdmin(t, "sample-bot")
	amy := ts.newMember(t, b, "amy-bot", "192.0.2.50:1")

	env := ts.encrypt(t, rick, b, "c-idem-1", "once")
	status, m1 := ts.send(t, rick.token, b.bridgeID, env)
	if status != http.StatusAccepted {
		t.Fatalf("first send = %d (%v)", status, m1)
	}
	// Identical resend is idempotent: same 202 result, no duplicate delivery.
	status, m2 := ts.send(t, rick.token, b.bridgeID, env)
	if status != http.StatusAccepted {
		t.Fatalf("idempotent resend = %d (%v)", status, m2)
	}
	if m1["expires_at"] != m2["expires_at"] {
		t.Fatalf("idempotent expires_at differ: %v vs %v", m1["expires_at"], m2["expires_at"])
	}

	// A conflicting resend (different content, same msg_id) is 409.
	bad := ts.encrypt(t, rick, b, "c-idem-1", "different")
	if status, m := ts.send(t, rick.token, b.bridgeID, bad); status != http.StatusConflict || errCode(m) != "duplicate_msg_id" {
		t.Fatalf("conflicting resend = %d %q, want 409 duplicate_msg_id", status, errCode(m))
	}

	// Amy has exactly one copy of the message.
	iw := ts.inbox(t, amy.token, b.bridgeID)
	if countMessages(iw) != 1 {
		t.Fatalf("messages after idempotent resend = %d, want 1", countMessages(iw))
	}
	var ids []string
	for _, e := range iw.Envelopes {
		ids = append(ids, e.ID)
	}
	first := ts.ack(t, amy.token, b.bridgeID, ids)
	if int(first["deleted"].(float64)) != len(ids) {
		t.Fatalf("first ack deleted = %v, want %d", first["deleted"], len(ids))
	}
	// Repeat ack of the same ids: nothing deleted, all reported unknown (DEV_SPEC §10).
	second := ts.ack(t, amy.token, b.bridgeID, ids)
	if int(second["deleted"].(float64)) != 0 {
		t.Fatalf("duplicate ack deleted = %v, want 0", second["deleted"])
	}
	if len(second["unknown"].([]any)) != len(ids) {
		t.Fatalf("duplicate ack unknown = %v, want %d", second["unknown"], len(ids))
	}
}

// ---- tamper: a flipped signature is rejected ----

func TestSendRejectsTamperedSignature(t *testing.T) {
	ts := newIntegServer(t, nil)
	rick, b := ts.newAdmin(t, "sample-bot")
	ts.newMember(t, b, "amy-bot", "192.0.2.60:1")

	env := ts.encrypt(t, rick, b, "c-tamper-1", "tamper")
	env.Signature[0] ^= 0xff // break the signature

	status, m := ts.send(t, rick.token, b.bridgeID, env)
	if status != http.StatusBadRequest || errCode(m) != "bad_signature" {
		t.Fatalf("tampered send = %d %q, want 400 bad_signature", status, errCode(m))
	}
}

// ---- oversize: an envelope over MaxEnvelopeBytes -> 413 ----

func TestSendOversizeEnvelope(t *testing.T) {
	ts := newIntegServer(t, func(c *config.Config) { c.MaxEnvelopeBytes = 300 })
	rick, b := ts.newAdmin(t, "sample-bot")
	ts.newMember(t, b, "amy-bot", "192.0.2.70:1")

	env := ts.encrypt(t, rick, b, "c-big-1", "any normal message exceeds 300 bytes once encrypted")
	status, m := ts.send(t, rick.token, b.bridgeID, env)
	if status != http.StatusRequestEntityTooLarge || errCode(m) != "payload_too_large" {
		t.Fatalf("oversize send = %d %q, want 413 payload_too_large", status, errCode(m))
	}
}

// ---- rate limit: send burst exhausted -> 429 ----

func TestSendRateLimited(t *testing.T) {
	ts := newIntegServer(t, nil)
	rick, b := ts.newAdmin(t, "sample-bot")
	ts.newMember(t, b, "amy-bot", "192.0.2.80:1")

	env := ts.encrypt(t, rick, b, "c-rate-1", "rate")
	// Burst is 10; the clock does not advance, so tokens never refill. The first
	// 10 succeed (idempotent resends still consume a token); the 11th is 429.
	for i := 0; i < sendBurst; i++ {
		if status, m := ts.send(t, rick.token, b.bridgeID, env); status != http.StatusAccepted {
			t.Fatalf("send %d = %d (%v), want 202", i, status, m)
		}
	}
	status, m, hdr := ts.do(t, http.MethodPost, "/v1/bridges/"+b.bridgeID+"/send", env, reqOpts{token: rick.token})
	if status != http.StatusTooManyRequests || errCode(m) != "rate_limited" {
		t.Fatalf("over-burst send = %d %q, want 429 rate_limited", status, errCode(m))
	}
	if hdr.Get("Retry-After") == "" {
		t.Fatalf("429 missing Retry-After header")
	}
}
