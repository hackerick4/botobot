package server

import (
	"context"
	"database/sql"
	"net/http"
	"testing"
	"time"
)

// v0.6.6: a closed bridge answers 410 only to its own former members; anyone
// else gets exactly what an unknown bridge id gives.

func closedPair(t *testing.T, ts *testServer) (b createdBridge, memberToken string) {
	t.Helper()
	b = ts.createBridge(t, "my-bot", newTestLand(t))
	_, jm := ts.join(t, b.bridgeID, b.code, "helper-bot-x7k2", newTestLand(t), "192.0.2.80:1")
	memberToken, _ = jm["api_key"].(string)
	if memberToken == "" {
		t.Fatalf("join gave no key: %v", jm)
	}
	status, m, _ := ts.do(t, http.MethodDelete, "/v1/bridges/"+b.bridgeID, nil, reqOpts{token: b.token})
	if status != http.StatusOK || m["closed"] != true {
		t.Fatalf("delete bridge got %d %v", status, m)
	}
	return b, memberToken
}

func TestClosedBridgeGoneOnlyToMembers(t *testing.T) {
	ts := newIntegServer(t, nil)
	b, memberToken := closedPair(t, ts)
	paths := []string{"", "/lands", "/undelivered", "/inbox"}

	for _, tok := range []string{b.token, memberToken} {
		for _, p := range paths {
			status, m, _ := ts.do(t, http.MethodGet, "/v1/bridges/"+b.bridgeID+p, nil, reqOpts{token: tok})
			if status != http.StatusGone || errCode(m) != "bridge_closed" {
				t.Fatalf("member GET %s = %d %q, want 410 bridge_closed", p, status, errCode(m))
			}
		}
	}

	// An outsider with a valid key of another bridge: same as an unknown id.
	other := ts.createBridge(t, "my-bot", newTestLand(t))
	unknown := "br_AAAAAAAAAAAAAAAAAAAAAA"
	for _, p := range paths {
		sc, mc, _ := ts.do(t, http.MethodGet, "/v1/bridges/"+b.bridgeID+p, nil, reqOpts{token: other.token})
		su, mu, _ := ts.do(t, http.MethodGet, "/v1/bridges/"+unknown+p, nil, reqOpts{token: other.token})
		if sc != http.StatusNotFound || sc != su || errCode(mc) != errCode(mu) {
			t.Fatalf("outsider GET %s closed=%d %q unknown=%d %q, want same 404", p, sc, errCode(mc), su, errCode(mu))
		}
		// No key / a made-up key: same as an unknown id (401).
		for _, tok := range []string{"", "bbk_notarealkeynotarealkeynotarealkey00"} {
			sc, mc, _ := ts.do(t, http.MethodGet, "/v1/bridges/"+b.bridgeID+p, nil, reqOpts{token: tok})
			su, mu, _ := ts.do(t, http.MethodGet, "/v1/bridges/"+unknown+p, nil, reqOpts{token: tok})
			if sc == http.StatusGone || sc != su || errCode(mc) != errCode(mu) {
				t.Fatalf("anon GET %s closed=%d %q unknown=%d %q, want identical non-410", p, sc, errCode(mc), su, errCode(mu))
			}
		}
	}
}

func TestClosedBridgeJoinLooksUnknown(t *testing.T) {
	ts := newIntegServer(t, nil)
	b, _ := closedPair(t, ts)
	status, m := ts.join(t, b.bridgeID, b.code, "my-bot-2", newTestLand(t), "192.0.2.81:1")
	if status != http.StatusUnauthorized || errCode(m) != "join_failed" {
		t.Fatalf("join closed = %d %q, want 401 join_failed", status, errCode(m))
	}
}

func TestClosedKeysGoWithTombstone(t *testing.T) {
	ts := newIntegServer(t, nil)
	b, _ := closedPair(t, ts)
	count := func() int {
		t.Helper()
		db, err := sql.Open("sqlite", "file:"+ts.dbPath+"?mode=ro")
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		var n int
		if err := db.QueryRow("SELECT COUNT(*) FROM closed_keys WHERE bridge_id = ?", b.bridgeID).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if n := count(); n != 2 {
		t.Fatalf("closed_keys = %d, want 2 (hashes only)", n)
	}
	ts.clk.Advance(31 * 24 * time.Hour)
	if n, err := ts.st.SweepTombstones(context.Background(), 30*24*time.Hour); err != nil || n != 1 {
		t.Fatalf("SweepTombstones = %d %v", n, err)
	}
	if n := count(); n != 0 {
		t.Fatalf("closed_keys after tombstone sweep = %d, want 0", n)
	}
}
