package server

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/hackerick4/botobot/internal/config"
	"github.com/hackerick4/botobot/internal/sweeper"
)

// newSweeper builds a sweeper bound to the test server's store and fake clock,
// invalidating the key cache on close just as cmd/bot-bridge-server wires it.
func (ts *testServer) newSweeper(inactivity time.Duration) *sweeper.Sweeper {
	return sweeper.New(sweeper.Options{
		Store:           ts.st,
		Logger:          NewLogger("info", "json", ts.buf),
		AdminInactivity: inactivity,
		OnBridgeClosed:  func(string) { ts.srv.ClearKeyCache() },
	})
}

// ---- §17.2 #5: advance 24h -> inbox item gone, sender sees `expired` ----

func TestInboxTTLExpiredBySweeper(t *testing.T) {
	ts := newIntegServer(t, func(c *config.Config) { c.RateInboxPerMin = 100000 })
	dana, b := ts.newAdmin(t, "dana-bot")
	amy := ts.newMember(t, b, "amy-bot", "192.0.2.50:1")

	env := ts.encrypt(t, dana, b, "c-ttl-0001", "fades away")
	if status, m := ts.send(t, dana.token, b.bridgeID, env); status != http.StatusAccepted {
		t.Fatalf("send = %d (%v)", status, m)
	}

	// amy has the message waiting but never acks it.
	if n := countMessages(ts.inbox(t, amy.token, b.bridgeID)); n != 1 {
		t.Fatalf("pre-expiry message count = %d, want 1", n)
	}

	// Advance past the 24h hard TTL and run one sweep pass.
	ts.clk.Advance(24*time.Hour + time.Second)
	res, err := ts.newSweeper(168 * time.Hour).RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if res.ExpiredInbox == 0 {
		t.Fatalf("sweeper expired 0 inbox items")
	}

	// The item is gone from amy's inbox...
	if n := countMessages(ts.inbox(t, amy.token, b.bridgeID)); n != 0 {
		t.Fatalf("post-expiry message count = %d, want 0", n)
	}
	// ...and the sender sees it as `expired` (status kept 24h after the update).
	status, sm, _ := ts.do(t, http.MethodGet, "/v1/bridges/"+b.bridgeID+"/messages/c-ttl-0001/status", nil, reqOpts{token: dana.token})
	if status != http.StatusOK {
		t.Fatalf("status endpoint = %d (%v)", status, sm)
	}
	ds := sm["deliveries"].([]any)
	if len(ds) != 1 || ds[0].(map[string]any)["status"] != "expired" {
		t.Fatalf("delivery status = %v, want one `expired`", ds)
	}
}

// ---- §17.2 #7a: admin leaves -> other lands get 410 ----

func TestAdminLeaveClosesBridge(t *testing.T) {
	ts := newIntegServer(t, nil)
	dana, b := ts.newAdmin(t, "dana-bot")
	amy := ts.newMember(t, b, "amy-bot", "192.0.2.60:1")

	// Admin leaves (DELETE self): the bridge closes immediately (admin_left).
	status, m, _ := ts.do(t, http.MethodDelete, "/v1/bridges/"+b.bridgeID+"/lands/"+dana.landID, nil, reqOpts{token: dana.token})
	if status != http.StatusOK || m["bridge_closed"] != true {
		t.Fatalf("admin leave = %d %v, want 200 bridge_closed:true", status, m)
	}

	assertBridgeClosed(t, ts, b, amy.token, "admin_left")
}

// ---- §17.2 #7b: admin 7 days inactive -> 410; #7c: polling keeps it open ----

func TestAdminInactivityClosesBridge(t *testing.T) {
	ts := newIntegServer(t, func(c *config.Config) {
		c.RateInboxPerMin = 100000
		c.RateOtherPerMin = 100000
	})
	dana, b := ts.newAdmin(t, "dana-bot")
	amy := ts.newMember(t, b, "amy-bot", "192.0.2.70:1")

	const window = 168 * time.Hour // 7 days
	sw := ts.newSweeper(window)

	// #7c: the admin keeps polling across more than the window, each gap below
	// it, so the bridge is never closed.
	for i := 0; i < 3; i++ {
		ts.clk.Advance(window - time.Hour)
		if status, _, _ := ts.do(t, http.MethodGet, "/v1/me", nil, reqOpts{token: dana.token}); status != http.StatusOK {
			t.Fatalf("round %d admin poll = %d", i, status)
		}
		if res, err := sw.RunOnce(context.Background()); err != nil || len(res.ClosedInactive) != 0 {
			t.Fatalf("round %d closed active admin bridge: %v (err=%v)", i, res.ClosedInactive, err)
		}
		if status, _, _ := ts.do(t, http.MethodGet, "/v1/bridges/"+b.bridgeID, nil, reqOpts{token: amy.token}); status != http.StatusOK {
			t.Fatalf("round %d amy request = %d, want 200 (still open)", i, status)
		}
	}

	// #7b: now let the admin go silent past the window; the sweep closes it.
	ts.clk.Advance(window + time.Hour)
	res, err := sw.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("final RunOnce: %v", err)
	}
	if len(res.ClosedInactive) != 1 || res.ClosedInactive[0] != b.bridgeID {
		t.Fatalf("ClosedInactive = %v, want [%s]", res.ClosedInactive, b.bridgeID)
	}

	assertBridgeClosed(t, ts, b, amy.token, "admin_inactive")
}

// assertBridgeClosed checks that a bridge-scoped request from another land now
// gets 410 bridge_closed with the expected reason (DEV_SPEC §6.1, §11).
func assertBridgeClosed(t *testing.T, ts *testServer, b createdBridge, token, wantReason string) {
	t.Helper()
	status, m, _ := ts.do(t, http.MethodGet, "/v1/bridges/"+b.bridgeID+"/inbox", nil, reqOpts{token: token})
	if status != http.StatusGone {
		t.Fatalf("post-close request = %d, want 410 (%v)", status, m)
	}
	e, _ := m["error"].(map[string]any)
	if e == nil || e["code"] != "bridge_closed" {
		t.Fatalf("post-close error = %v, want bridge_closed", m)
	}
	if e["close_reason"] != wantReason {
		t.Fatalf("close_reason = %v, want %s", e["close_reason"], wantReason)
	}
}
