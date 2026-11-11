package server

import (
	"fmt"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/hackerick4/botobot/internal/config"
)

// TestInviteCodeOneTime verifies a code is consumed on the first successful join
// and a correct-but-used code then fails like a wrong code (Batch 1 item 5).
// It runs on a pre-v0.5.0 multi-member bridge; TestPairBridgeThirdJoinRefused
// covers the two-bot rules.
func TestInviteCodeOneTime(t *testing.T) {
	ts := newIntegServer(t, nil)
	b := ts.legacy(t, ts.createBridge(t, "sample-bot", newTestLand(t)))

	// First join with the create-time code succeeds.
	if status, m := ts.join(t, b.bridgeID, b.code, "amy-bot", newTestLand(t), "192.0.2.10:1"); status != http.StatusCreated {
		t.Fatalf("first join = %d (%v)", status, m)
	}
	// Re-using the now-consumed code fails as a generic join_failed.
	status, m := ts.join(t, b.bridgeID, b.code, "zoe-bot", newTestLand(t), "192.0.2.11:1")
	if status != http.StatusUnauthorized || errCode(m) != "join_failed" {
		t.Fatalf("reused code = %d code=%q, want 401 join_failed", status, errCode(m))
	}
	// A fresh rotated code works again.
	if status, m := ts.join(t, b.bridgeID, ts.rotateCode(t, b), "zoe-bot", newTestLand(t), "192.0.2.12:1"); status != http.StatusCreated {
		t.Fatalf("join with fresh code = %d (%v)", status, m)
	}
}

// TestInviteCodeExpiry verifies a code past its 24h life fails like a wrong code
// (Batch 1 item 5). The fake clock is advanced past the expiry.
func TestInviteCodeExpiry(t *testing.T) {
	ts := newIntegServer(t, nil)
	b := ts.createBridge(t, "sample-bot", newTestLand(t))

	ts.clk.Advance(24*time.Hour + time.Minute)

	status, m := ts.join(t, b.bridgeID, b.code, "amy-bot", newTestLand(t), "192.0.2.10:1")
	if status != http.StatusUnauthorized || errCode(m) != "join_failed" {
		t.Fatalf("expired code = %d code=%q, want 401 join_failed", status, errCode(m))
	}
}

// TestJoinCreatePendingCapPerPrefix verifies the per-prefix cap on unclaimed
// pending bridges (Batch 1 item 6).
func TestJoinCreatePendingCapPerPrefix(t *testing.T) {
	ts := newIntegServer(t, func(c *config.Config) {
		c.MaxPendingBridgesPerPrefix = 2
		c.MaxPendingBridges = 1000
	})
	// Two pending from the same /24 are allowed.
	for i := 0; i < 2; i++ {
		if status, m := ts.joinCreate(t, nil, reqOpts{remoteAddr: "203.0.113.70:1"}); status != http.StatusCreated {
			t.Fatalf("pending %d = %d (%v)", i, status, m)
		}
	}
	// A third from the same /24 is refused.
	status, m := ts.joinCreate(t, nil, reqOpts{remoteAddr: "203.0.113.71:1"})
	if status != http.StatusServiceUnavailable || errCode(m) != "pending_capacity" {
		t.Fatalf("3rd pending from prefix = %d code=%q, want 503 pending_capacity", status, errCode(m))
	}
	// A different /24 is unaffected.
	if status, m := ts.joinCreate(t, nil, reqOpts{remoteAddr: "198.51.100.1:1"}); status != http.StatusCreated {
		t.Fatalf("pending from other prefix = %d (%v)", status, m)
	}
}

// TestLegacyCreateDisabledByDefault verifies POST /v1/bridges is off unless
// AllowLegacyCreate is set (Batch 1 item 6).
func TestLegacyCreateDisabledByDefault(t *testing.T) {
	ts := newIntegServer(t, func(c *config.Config) { c.AllowLegacyCreate = false })
	body := createBridgeReq{
		Name: "x",
		Land: landBlock{
			Profile: landProfileInput{Handle: "sample-bot"},
			Key:     newTestLand(t).keyInput(t),
		},
	}
	status, m, _ := ts.do(t, http.MethodPost, "/v1/bridges", body, reqOpts{})
	if status != http.StatusNotFound || errCode(m) != "not_found" {
		t.Fatalf("legacy create with flag off = %d code=%q, want 404 not_found", status, errCode(m))
	}
}

// TestJoinCreatedBridgeLandsCantInvite verifies a /join bridge defaults to
// members not being able to read the invite code (Batch 1 item 5).
func TestJoinCreatedBridgeLandsCantInvite(t *testing.T) {
	ts := newIntegServer(t, nil)
	_, created := ts.joinCreate(t, map[string]any{"name": "Room"}, reqOpts{remoteAddr: "203.0.113.80:1"})
	bridgeID := created["bridge_id"].(string)
	token := created["claim_token"].(string)

	// Claim as admin, add a member, then the member must be refused the code.
	adminKey := newTestLand(t)
	status, cm := ts.claim(t, bridgeID, token, "sample-bot", adminKey, "203.0.113.80:1")
	if status != http.StatusCreated {
		t.Fatalf("claim = %d (%v)", status, cm)
	}
	adminToken := cm["api_key"].(string)
	// Rotate a code and add a member.
	status, m, _ := ts.do(t, http.MethodPost, "/v1/bridges/"+bridgeID+"/invite-code/rotate", nil, reqOpts{token: adminToken})
	if status != http.StatusOK {
		t.Fatalf("rotate = %d (%v)", status, m)
	}
	code := m["invite_code"].(string)
	status, jm := ts.join(t, bridgeID, code, "amy-bot", newTestLand(t), "198.51.100.5:1")
	if status != http.StatusCreated {
		t.Fatalf("member join = %d (%v)", status, jm)
	}
	memberToken := jm["api_key"].(string)
	// The member reads the invite code -> 403 (lands_can_invite defaulted false).
	status, em, _ := ts.do(t, http.MethodGet, "/v1/bridges/"+bridgeID+"/invite-code", nil, reqOpts{token: memberToken})
	if status != http.StatusForbidden || errCode(em) != "forbidden" {
		t.Fatalf("member invite-code read = %d code=%q, want 403 forbidden", status, errCode(em))
	}
}

// TestInviteCodeOneTimeConcurrent: N concurrent joins racing with the same
// one-time code admit exactly one land (red-team RT-07).
func TestInviteCodeOneTimeConcurrent(t *testing.T) {
	ts := newIntegServer(t, nil)
	b := ts.createBridge(t, "sample-bot", newTestLand(t))
	const n = 8
	lands := make([]testLand, n)
	for i := range lands {
		lands[i] = newTestLand(t)
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	ok := 0
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			status, _ := ts.join(t, b.bridgeID, b.code, fmt.Sprintf("race%d-bot", i), lands[i], fmt.Sprintf("192.0.2.%d:1", 20+i))
			if status == http.StatusCreated {
				mu.Lock()
				ok++
				mu.Unlock()
			}
		}(i)
	}
	wg.Wait()
	if ok != 1 {
		t.Fatalf("concurrent joins with one code admitted %d lands, want 1", ok)
	}
}
