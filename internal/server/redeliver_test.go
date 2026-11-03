package server

import (
	"net/http"
	"testing"
	"time"

	"github.com/hackerick4/botobot/internal/config"
	"github.com/hackerick4/botobot/internal/protocol"
)

// joinEvents returns the land_joined events present in an inbox response.
func joinEvents(iw inboxWire) []struct {
	id      string
	landID  string
	rebuilt bool
} {
	var out []struct {
		id      string
		landID  string
		rebuilt bool
	}
	for _, e := range iw.Envelopes {
		if e.Kind != "event" || e.Event.Type != protocol.EventLandJoined {
			continue
		}
		lid, _ := e.Event.Land["land_id"].(string)
		out = append(out, struct {
			id      string
			landID  string
			rebuilt bool
		}{id: e.ID, landID: lid, rebuilt: e.Event.Redelivered})
	}
	return out
}

func joinEventIDs(iw inboxWire) []string {
	var ids []string
	for _, j := range joinEvents(iw) {
		ids = append(ids, j.id)
	}
	return ids
}

// ---- §11 re-delivery: admin away misses land_joined, gets them rebuilt ----

func TestAdminAwayRebuildsMissedJoins(t *testing.T) {
	ts := newIntegServer(t, func(c *config.Config) { c.RateInboxPerMin = 100000 })
	dana, b := ts.newAdmin(t, "dana-bot")
	ts.legacy(t, b) // pre-v0.5.0 multi-member bridge

	// Three members join while the admin is away; a fourth joins then leaves.
	amy := ts.newMember(t, b, "amy-bot", "192.0.2.10:1")
	bob := ts.newMember(t, b, "bob-bot", "192.0.2.11:1")
	cam := ts.newMember(t, b, "cam-bot", "192.0.2.12:1")
	dan := ts.newMember(t, b, "dan-bot", "192.0.2.13:1")

	// dan leaves before the admin ever sees it (admin kicks / dan departs).
	status, m, _ := ts.do(t, http.MethodDelete, "/v1/bridges/"+b.bridgeID+"/lands/"+dan.landID, nil, reqOpts{token: dana.token})
	if status != http.StatusOK {
		t.Fatalf("remove dan = %d (%v)", status, m)
	}

	// The admin is away long enough that every original land_joined notice (24h
	// TTL) has expired, but less than the 7-day inactivity close window.
	ts.clk.Advance(3*24*time.Hour + time.Second)

	// Admin returns: the missed joins are rebuilt and delivered now.
	iw := ts.inbox(t, dana.token, b.bridgeID)
	joins := joinEvents(iw)
	got := map[string]bool{}
	for _, j := range joins {
		if !j.rebuilt {
			t.Errorf("rebuilt join for %s missing redelivered=true", j.landID)
		}
		got[j.landID] = true
	}
	for _, want := range []string{amy.landID, bob.landID, cam.landID} {
		if !got[want] {
			t.Errorf("rebuilt land_joined missing for %s", want)
		}
	}
	if got[dan.landID] {
		t.Errorf("land that joined then left (%s) must not be reported as joined", dan.landID)
	}
	if len(joins) != 3 {
		t.Fatalf("rebuilt join count = %d, want 3", len(joins))
	}

	// Admin acks them; the watermark advances past these joins.
	ts.ack(t, dana.token, b.bridgeID, joinEventIDs(iw))

	// A subsequent fetch (after the lease window) must not re-deliver duplicates.
	ts.clk.Advance(10 * time.Minute)
	if j := joinEvents(ts.inbox(t, dana.token, b.bridgeID)); len(j) != 0 {
		t.Fatalf("duplicate joins after ack = %d, want 0", len(j))
	}
}

// ---- §11 re-delivery: present admin never sees duplicate joins ----

func TestAdminPresentNoDuplicateJoins(t *testing.T) {
	ts := newIntegServer(t, func(c *config.Config) { c.RateInboxPerMin = 100000 })
	dana, b := ts.newAdmin(t, "dana-bot")
	ts.legacy(t, b) // pre-v0.5.0 multi-member bridge

	amy := ts.newMember(t, b, "amy-bot", "192.0.2.20:1")

	// First poll delivers amy's original notice (not a rebuild).
	iw := ts.inbox(t, dana.token, b.bridgeID)
	joins := joinEvents(iw)
	if len(joins) != 1 || joins[0].landID != amy.landID || joins[0].rebuilt {
		t.Fatalf("first poll joins = %+v, want one original for amy", joins)
	}
	ts.ack(t, dana.token, b.bridgeID, joinEventIDs(iw))

	// Immediately re-polling must not re-deliver the acked join.
	ts.clk.Advance(10 * time.Minute)
	if j := joinEvents(ts.inbox(t, dana.token, b.bridgeID)); len(j) != 0 {
		t.Fatalf("joins after ack = %d, want 0", len(j))
	}

	// A new member joins while the admin is present and polling normally.
	bob := ts.newMember(t, b, "bob-bot", "192.0.2.21:1")
	iw = ts.inbox(t, dana.token, b.bridgeID)
	joins = joinEvents(iw)
	if len(joins) != 1 || joins[0].landID != bob.landID {
		t.Fatalf("bob poll joins = %+v, want one for bob", joins)
	}
	if joins[0].rebuilt {
		t.Errorf("bob's still-queued original must not be rebuilt as a duplicate")
	}

	// Polling again before acking (original still leased, not expired) must not
	// rebuild a duplicate of the in-flight notice.
	ts.clk.Advance(10 * time.Minute)
	if j := joinEvents(ts.inbox(t, dana.token, b.bridgeID)); len(j) != 1 || j[0].landID != bob.landID || j[0].rebuilt {
		t.Fatalf("re-poll joins = %+v, want the single original for bob", j)
	}
}
