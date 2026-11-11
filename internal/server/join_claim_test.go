package server

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/hackerick4/botobot/internal/config"
	"github.com/hackerick4/botobot/internal/invite"
	"github.com/hackerick4/botobot/internal/store"
)

// joinCreate calls POST /join and returns the status and body.
func (ts *testServer) joinCreate(t *testing.T, body any, o reqOpts) (int, map[string]any) {
	t.Helper()
	status, m, _ := ts.do(t, http.MethodPost, "/join", body, o)
	return status, m
}

// claim calls POST /v1/bridges/{id}/claim with a land registration and token.
func (ts *testServer) claim(t *testing.T, bridgeID, token, handle string, k testLand, remoteAddr string) (int, map[string]any) {
	t.Helper()
	body := claimReq{Token: token, Land: landBlock{
		Profile: landProfileInput{Handle: handle, DisplayName: "Admin Bot", OwnerName: "Rick"},
		Key:     k.keyInput(t),
	}}
	status, m, _ := ts.do(t, http.MethodPost, "/v1/bridges/"+bridgeID+"/claim", body, reqOpts{remoteAddr: remoteAddr})
	return status, m
}

// ---- POST /join creates a pending bridge with prompts ----

func TestJoinCreateReturnsPromptsAndClaimToken(t *testing.T) {
	ts := newIntegServer(t, nil)

	status, m := ts.joinCreate(t, map[string]any{"name": "研究小組"}, reqOpts{remoteAddr: "203.0.113.5:1"})
	if status != http.StatusCreated {
		t.Fatalf("POST /join status = %d, want 201 (%v)", status, m)
	}
	for _, key := range []string{"bridge_id", "invite_link", "claim_token", "claim_expires_at", "creator_prompt"} {
		if _, ok := m[key]; !ok {
			t.Fatalf("join_created missing %q: %v", key, m)
		}
	}
	// v0.4.9: no invite in the /join response; claim prints the real one.
	for _, key := range []string{"invite_code", "invite_prompt"} {
		if _, ok := m[key]; ok {
			t.Fatalf("join_created still has %q", key)
		}
	}
	if m["type"] != "join_created" {
		t.Fatalf("type = %v, want join_created", m["type"])
	}
	bridgeID, _ := m["bridge_id"].(string)
	if !strings.HasPrefix(bridgeID, "br_") {
		t.Fatalf("bridge_id = %q", bridgeID)
	}
	code := ts.pendingCode(t, bridgeID)
	token, _ := m["claim_token"].(string)
	if len(code) != 6 || !strings.HasPrefix(token, "bbc_") {
		t.Fatalf("code=%q token=%q", code, token)
	}
	creator, _ := m["creator_prompt"].(string)
	if !strings.Contains(creator, token) || !strings.Contains(creator, "sha256sum -c") || !strings.Contains(creator, "verify-self") || !strings.Contains(creator, "/join.md") || strings.Contains(creator, code) {
		t.Fatalf("creator_prompt must carry the token, the sha256 step and the manual, never the code")
	}

	// lang=en: English creator prompt, /en manual, --lang en on claim.
	status, m = ts.joinCreate(t, map[string]any{"name": "Team", "lang": "en"}, reqOpts{remoteAddr: "203.0.113.5:1"})
	if status != http.StatusCreated || m["lang"] != "en" {
		t.Fatalf("POST /join lang=en: %d %v", status, m["lang"])
	}
	creator, _ = m["creator_prompt"].(string)
	if !strings.Contains(creator, "/en/join.md") || !strings.Contains(creator, "--lang en") || !strings.Contains(creator, "1 hour") {
		t.Fatalf("english creator_prompt:\n%s", creator)
	}
}

// pendingCode reads a bridge's invite code from the store (POST /join no
// longer returns it).
func (ts *testServer) pendingCode(t *testing.T, bridgeID string) string {
	t.Helper()
	b, err := ts.st.GetBridge(context.Background(), bridgeID)
	if err != nil {
		t.Fatalf("GetBridge: %v", err)
	}
	code, err := invite.Decrypt(ts.srv.cfg.InviteKey, bridgeID, b.InviteCodeEnc)
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	return code
}

func TestJoinCreateEmptyBodyUsesDefaultName(t *testing.T) {
	ts := newIntegServer(t, nil)
	status, m := ts.joinCreate(t, nil, reqOpts{remoteAddr: "203.0.113.6:1", noCT: true})
	if status != http.StatusCreated {
		t.Fatalf("POST /join (empty) status = %d (%v)", status, m)
	}
	if _, ok := m["bridge_id"]; !ok {
		t.Fatalf("missing bridge_id: %v", m)
	}
}

func TestJoinUsageGET(t *testing.T) {
	ts := newIntegServer(t, nil)
	status, _, _ := ts.do(t, http.MethodGet, "/join", nil, reqOpts{})
	if status != http.StatusOK {
		t.Fatalf("GET /join status = %d, want 200", status)
	}
}

// ---- full flow: /join -> claim (admin) -> member join -> admin is first land ----

func TestJoinClaimThenMemberJoin(t *testing.T) {
	ts := newIntegServer(t, nil)

	_, created := ts.joinCreate(t, map[string]any{"name": "Room"}, reqOpts{remoteAddr: "203.0.113.5:1"})
	bridgeID := created["bridge_id"].(string)
	token := created["claim_token"].(string)
	code := ts.pendingCode(t, bridgeID)
	inviteLink := created["invite_link"].(string)
	if !strings.HasSuffix(inviteLink, "/b/"+bridgeID) {
		t.Fatalf("invite_link = %q", inviteLink)
	}

	// Joining before the claim is refused with bridge_pending.
	status, m := ts.join(t, bridgeID, code, "amy-bot", newTestLand(t), "198.51.100.10:1")
	if status != http.StatusConflict || errCode(m) != "bridge_pending" {
		t.Fatalf("pre-claim join status=%d code=%q, want 409 bridge_pending", status, errCode(m))
	}

	// Admin claims the bridge.
	adminKey := newTestLand(t)
	status, cm := ts.claim(t, bridgeID, token, "sample-bot", adminKey, "203.0.113.5:1")
	if status != http.StatusCreated {
		t.Fatalf("claim status = %d (%v)", status, cm)
	}
	adminLandID := cm["land"].(map[string]any)["land_id"].(string)
	if cm["land"].(map[string]any)["role"] != "admin" {
		t.Fatalf("claimant role = %v, want admin", cm["land"].(map[string]any)["role"])
	}
	adminAPIKey := cm["api_key"].(string)

	// The bridge is now active: the admin's own API key works.
	status, _, _ = ts.do(t, http.MethodGet, "/v1/bridges/"+bridgeID, nil, reqOpts{token: adminAPIKey})
	if status != http.StatusOK {
		t.Fatalf("GET bridge as admin = %d, want 200", status)
	}

	// Now a member may join with the invite code.
	status, jm := ts.join(t, bridgeID, code, "amy-bot", newTestLand(t), "198.51.100.10:1")
	if status != http.StatusCreated {
		t.Fatalf("post-claim member join = %d (%v)", status, jm)
	}

	// The admin is recorded as the bridge admin and is land #1.
	_, bm, _ := ts.do(t, http.MethodGet, "/v1/bridges/"+bridgeID, nil, reqOpts{token: adminAPIKey})
	if bm["bridge"].(map[string]any)["admin_land_id"] != adminLandID {
		t.Fatalf("admin_land_id = %v, want %q", bm["bridge"].(map[string]any)["admin_land_id"], adminLandID)
	}
	if lc := bm["bridge"].(map[string]any)["land_count"].(float64); lc != 2 {
		t.Fatalf("land_count = %v, want 2", lc)
	}
}

// ---- claim twice is refused (token consumed) ----

func TestClaimTwice(t *testing.T) {
	ts := newIntegServer(t, nil)
	_, created := ts.joinCreate(t, nil, reqOpts{remoteAddr: "203.0.113.7:1"})
	bridgeID := created["bridge_id"].(string)
	token := created["claim_token"].(string)

	if status, m := ts.claim(t, bridgeID, token, "sample-bot", newTestLand(t), "203.0.113.7:1"); status != http.StatusCreated {
		t.Fatalf("first claim status = %d (%v)", status, m)
	}
	status, m := ts.claim(t, bridgeID, token, "rick2-bot", newTestLand(t), "203.0.113.7:1")
	if status != http.StatusGone || errCode(m) != "claim_consumed" {
		t.Fatalf("second claim status=%d code=%q, want 410 claim_consumed", status, errCode(m))
	}
}

// ---- wrong claim token is refused uniformly ----

func TestClaimWrongToken(t *testing.T) {
	ts := newIntegServer(t, nil)
	_, created := ts.joinCreate(t, nil, reqOpts{remoteAddr: "203.0.113.8:1"})
	bridgeID := created["bridge_id"].(string)

	status, m := ts.claim(t, bridgeID, "bbc_wrongtokenvalue", "sample-bot", newTestLand(t), "203.0.113.8:1")
	if status != http.StatusUnauthorized || errCode(m) != "claim_failed" {
		t.Fatalf("wrong-token claim status=%d code=%q, want 401 claim_failed", status, errCode(m))
	}
}

// ---- expired claim is refused and the sweeper deletes the pending bridge ----

func TestClaimExpiredAndSwept(t *testing.T) {
	ts := newIntegServer(t, func(c *config.Config) { c.ClaimTTL = time.Hour })
	_, created := ts.joinCreate(t, nil, reqOpts{remoteAddr: "203.0.113.9:1"})
	bridgeID := created["bridge_id"].(string)
	token := created["claim_token"].(string)

	// Advance past the claim TTL: claim now fails as claim_failed (expired).
	ts.clk.Advance(time.Hour + time.Minute)
	status, m := ts.claim(t, bridgeID, token, "sample-bot", newTestLand(t), "203.0.113.9:1")
	if status != http.StatusUnauthorized || errCode(m) != "claim_failed" {
		t.Fatalf("expired claim status=%d code=%q, want 401 claim_failed", status, errCode(m))
	}

	ctx := context.Background()
	if n, err := ts.st.CountPendingBridges(ctx); err != nil || n != 1 {
		t.Fatalf("pending count before sweep = %d (err %v), want 1", n, err)
	}
	// The sweeper deletes the unclaimed pending bridge entirely (no tombstone).
	n, err := ts.st.SweepExpiredPending(ctx)
	if err != nil {
		t.Fatalf("SweepExpiredPending: %v", err)
	}
	if n != 1 {
		t.Fatalf("swept %d pending bridges, want 1", n)
	}
	if _, err := ts.st.GetBridge(ctx, bridgeID); err != store.ErrNotFound {
		t.Fatalf("bridge after sweep err = %v, want ErrNotFound (deleted entirely)", err)
	}
}

// ---- per-IP rate limit on POST /join; spoofed XFF cannot bypass it ----

func TestJoinCreateRateLimitPerPrefix(t *testing.T) {
	ts := newIntegServer(t, func(c *config.Config) {
		c.JoinCreatePerHour = 5
		c.MaxPendingBridgesPerPrefix = 1000 // isolate the rate limit from the pending cap
		c.TrustProxyHeader = "X-Forwarded-For"
	})
	// Peer A: five creates from the same /24 succeed, each with a different
	// (spoofed) XFF; the XFF is ignored because the direct peer is untrusted, and
	// different hosts within the same /24 share one bucket (per-prefix, Batch 1
	// item 6). 203.0.113.50 and .51 are the same /24.
	for i := 0; i < 5; i++ {
		status, m := ts.joinCreate(t, nil, reqOpts{remoteAddr: "203.0.113.50:1111", headers: map[string]string{"X-Forwarded-For": "10.0.0." + itoa(i)}})
		if status != http.StatusCreated {
			t.Fatalf("join %d from prefix A = %d (%v)", i, status, m)
		}
	}
	// Sixth from a different host in the SAME /24 is throttled: the bucket is shared.
	status, m := ts.joinCreate(t, nil, reqOpts{remoteAddr: "203.0.113.51:2222"})
	if status != http.StatusTooManyRequests || errCode(m) != "rate_limited" {
		t.Fatalf("6th from prefix A = %d code=%q, want 429 rate_limited", status, errCode(m))
	}
	// A genuinely different /24 still has its own fresh bucket.
	status, m = ts.joinCreate(t, nil, reqOpts{remoteAddr: "198.51.100.10:2222"})
	if status != http.StatusCreated {
		t.Fatalf("join from a different prefix = %d (%v), want 201", status, m)
	}
}

// ---- global cap on simultaneously-pending bridges ----

func TestJoinCreatePendingCap(t *testing.T) {
	ts := newIntegServer(t, func(c *config.Config) { c.MaxPendingBridges = 1 })

	if status, m := ts.joinCreate(t, nil, reqOpts{remoteAddr: "203.0.113.60:1"}); status != http.StatusCreated {
		t.Fatalf("first create = %d (%v)", status, m)
	}
	status, m := ts.joinCreate(t, nil, reqOpts{remoteAddr: "203.0.113.61:1"})
	if status != http.StatusServiceUnavailable || errCode(m) != "pending_capacity" {
		t.Fatalf("over-cap create = %d code=%q, want 503 pending_capacity", status, errCode(m))
	}
}
