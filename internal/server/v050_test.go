package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// v0.5.0: every new bridge connects exactly two bots.

func (ts *testServer) bridgeRow(t *testing.T, id string) (pair bool, maxLands int, status string) {
	t.Helper()
	b, err := ts.st.GetBridge(context.Background(), id)
	if err != nil {
		t.Fatalf("GetBridge: %v", err)
	}
	return b.Pair, b.MaxLands, b.Status
}

func closeReasonOf(m map[string]any) string {
	e, _ := m["error"].(map[string]any)
	r, _ := e["close_reason"].(string)
	return r
}

func TestPairBridgeCreatedByBothPaths(t *testing.T) {
	ts := newIntegServer(t, nil)
	b := ts.createBridge(t, "sample-bot", newTestLand(t))
	if pair, max, _ := ts.bridgeRow(t, b.bridgeID); !pair || max != 2 {
		t.Fatalf("create: pair=%v max=%d, want true 2", pair, max)
	}
	status, m := ts.joinCreate(t, map[string]any{"name": "Pair"}, reqOpts{remoteAddr: "203.0.113.7:1"})
	if status != http.StatusCreated {
		t.Fatalf("POST /join = %d", status)
	}
	id, _ := m["bridge_id"].(string)
	if pair, max, _ := ts.bridgeRow(t, id); !pair || max != 2 {
		t.Fatalf("/join: pair=%v max=%d, want true 2", pair, max)
	}
	tok, _ := m["claim_token"].(string)
	if status, m := ts.claim(t, id, tok, "sample-bot", newTestLand(t), "203.0.113.7:1"); status != http.StatusCreated {
		t.Fatalf("claim = %d %v", status, m)
	} else if br, _ := m["bridge"].(map[string]any); br["pair"] != true || br["max_lands"] != float64(2) {
		t.Fatalf("claim bridge out: %v", br)
	}
}

func TestPairBridgeThirdJoinRefused(t *testing.T) {
	ts := newIntegServer(t, nil)
	b := ts.createBridge(t, "sample-bot", newTestLand(t))
	if status, m := ts.join(t, b.bridgeID, b.code, "amy-bot", newTestLand(t), "192.0.2.10:1"); status != http.StatusCreated {
		t.Fatalf("peer join = %d (%v)", status, m)
	}
	// The used code now says bridge_full (the caller holds the right code).
	status, m := ts.join(t, b.bridgeID, b.code, "zoe-bot", newTestLand(t), "192.0.2.11:1")
	if status != http.StatusConflict || errCode(m) != "bridge_full" {
		t.Fatalf("third join = %d %q, want 409 bridge_full", status, errCode(m))
	}
	// A wrong code still fails like a wrong code.
	status, m = ts.join(t, b.bridgeID, "000000", "zoe-bot", newTestLand(t), "192.0.2.12:1")
	if status != http.StatusUnauthorized || errCode(m) != "join_failed" {
		t.Fatalf("wrong code = %d %q, want 401 join_failed", status, errCode(m))
	}
	// Rotate after the peer joined is refused; no new code is minted.
	status, m, _ = ts.do(t, http.MethodPost, "/v1/bridges/"+b.bridgeID+"/invite-code/rotate", nil, reqOpts{token: b.token})
	if status != http.StatusConflict || errCode(m) != "bridge_full" {
		t.Fatalf("rotate after peer = %d %q, want 409 bridge_full", status, errCode(m))
	}
	status, m, _ = ts.do(t, http.MethodGet, "/v1/bridges/"+b.bridgeID+"/invite-code", nil, reqOpts{token: b.token})
	if status != http.StatusOK || m["usable"] != false {
		t.Fatalf("invite-code after peer = %d usable=%v, want false", status, m["usable"])
	}
	if status, m := ts.join(t, b.bridgeID, b.code, "zoe-bot", newTestLand(t), "192.0.2.13:1"); status != http.StatusConflict {
		t.Fatalf("join after refused rotate = %d (%v)", status, m)
	}
}

func TestPairBridgeRotateBeforePeer(t *testing.T) {
	ts := newIntegServer(t, nil)
	b := ts.createBridge(t, "sample-bot", newTestLand(t))
	fresh := ts.rotateCode(t, b)
	if status, m := ts.join(t, b.bridgeID, b.code, "amy-bot", newTestLand(t), "192.0.2.20:1"); status != http.StatusUnauthorized || errCode(m) != "join_failed" {
		t.Fatalf("old code after rotate = %d %q", status, errCode(m))
	}
	if status, m := ts.join(t, b.bridgeID, fresh, "amy-bot", newTestLand(t), "192.0.2.21:1"); status != http.StatusCreated {
		t.Fatalf("fresh code = %d (%v)", status, m)
	}
}

func TestPairBridgeMemberLeaveClosesForBoth(t *testing.T) {
	ts := newIntegServer(t, nil)
	rick, b := ts.newAdmin(t, "sample-bot")
	amy := ts.newMember(t, b, "amy-bot", "192.0.2.30:1")
	status, m, _ := ts.do(t, http.MethodDelete, "/v1/bridges/"+b.bridgeID+"/lands/"+amy.landID, nil, reqOpts{token: amy.token})
	if status != http.StatusOK || m["bridge_closed"] != true {
		t.Fatalf("leave = %d %v, want bridge_closed", status, m)
	}
	if _, _, st := ts.bridgeRow(t, b.bridgeID); st != "closed" {
		t.Fatalf("status = %s", st)
	}
	for _, tok := range []string{rick.token, amy.token} {
		status, m, _ := ts.do(t, http.MethodGet, "/v1/bridges/"+b.bridgeID+"/inbox", nil, reqOpts{token: tok})
		if status != http.StatusGone || closeReasonOf(m) != "member_left" {
			t.Fatalf("inbox after leave = %d %v, want 410 member_left", status, m)
		}
	}
}

func TestPairBridgeKickClosesForBoth(t *testing.T) {
	ts := newIntegServer(t, nil)
	rick, b := ts.newAdmin(t, "sample-bot")
	amy := ts.newMember(t, b, "amy-bot", "192.0.2.40:1")
	status, m, _ := ts.do(t, http.MethodDelete, "/v1/bridges/"+b.bridgeID+"/lands/"+amy.landID, nil, reqOpts{token: rick.token})
	if status != http.StatusOK || m["bridge_closed"] != true {
		t.Fatalf("kick = %d %v, want bridge_closed", status, m)
	}
	status, m, _ = ts.do(t, http.MethodGet, "/v1/bridges/"+b.bridgeID+"/inbox", nil, reqOpts{token: amy.token})
	if status != http.StatusGone || closeReasonOf(m) != "member_removed" {
		t.Fatalf("kicked inbox = %d %v, want 410 member_removed", status, m)
	}
	// A closed pair bridge takes no new peer even with a fresh-looking code,
	// and (v0.6.6) answers like an unknown bridge to a caller with no key.
	status, m = ts.join(t, b.bridgeID, b.code, "zoe-bot", newTestLand(t), "192.0.2.41:1")
	if status != http.StatusUnauthorized || errCode(m) != "join_failed" {
		t.Fatalf("join closed = %d %v, want 401 join_failed", status, m)
	}
}

// TestLegacyBridgeGrandfathered: a bridge from before v0.5.0 keeps its old
// rules: more members, rotate after joins, a member leaving closes nothing.
func TestLegacyBridgeGrandfathered(t *testing.T) {
	ts := newIntegServer(t, nil)
	rick, b := ts.newAdmin(t, "sample-bot")
	ts.legacy(t, b)
	amy := ts.newMember(t, b, "amy-bot", "192.0.2.50:1")
	ts.newMember(t, b, "zoe-bot", "192.0.2.51:1")
	status, m, _ := ts.do(t, http.MethodDelete, "/v1/bridges/"+b.bridgeID+"/lands/"+amy.landID, nil, reqOpts{token: amy.token})
	if status != http.StatusOK || m["bridge_closed"] != false {
		t.Fatalf("legacy leave = %d %v", status, m)
	}
	if _, _, st := ts.bridgeRow(t, b.bridgeID); st != "active" {
		t.Fatalf("legacy status = %s", st)
	}
	ts.inbox(t, rick.token, b.bridgeID)
}

func TestHealthzSchemaVersion7(t *testing.T) {
	ts := newIntegServer(t, nil)
	status, body, _ := ts.do(t, http.MethodGet, "/healthz", nil, reqOpts{})
	if status != http.StatusOK || body["schema_version"] != float64(10) {
		t.Fatalf("healthz = %d %v, want schema_version 10", status, body)
	}
}

// A two-bot bridge has nobody else to invite: lands_can_invite cannot be
// turned on (v0.5.0 audit).
func TestPairBridgeNoMemberInviteSetting(t *testing.T) {
	ts := newIntegServer(t, nil)
	b := ts.createBridge(t, "sample-bot", newTestLand(t))
	body := map[string]any{"settings": map[string]any{"lands_can_invite": true}}
	status, m, _ := ts.do(t, http.MethodPatch, "/v1/bridges/"+b.bridgeID, body, reqOpts{token: b.token})
	if status != http.StatusBadRequest {
		t.Fatalf("PATCH lands_can_invite=true on a pair bridge = %d %v, want 400", status, m)
	}
	body = map[string]any{"settings": map[string]any{"lands_can_invite": false}}
	if status, m, _ := ts.do(t, http.MethodPatch, "/v1/bridges/"+b.bridgeID, body, reqOpts{token: b.token}); status != http.StatusOK {
		t.Fatalf("PATCH lands_can_invite=false = %d %v", status, m)
	}
}

// The /b/<id> page says a bridge is for two bots and the invite works once.
func TestBridgePageSaysTwoBots(t *testing.T) {
	ts := newIntegServer(t, nil)
	rec := httptest.NewRecorder()
	ts.h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/b/br_example", nil))
	body := rec.Body.String()
	if rec.Code != http.StatusOK || !strings.Contains(body, "Each bridge connects two bots.") || !strings.Contains(body, "works once") {
		t.Fatalf("/b/ page = %d\n%s", rec.Code, body)
	}
	for _, bad := range []string{"member", "group", "rotate"} {
		if strings.Contains(strings.ToLower(body), bad) {
			t.Fatalf("/b/ page mentions %q:\n%s", bad, body)
		}
	}
}
