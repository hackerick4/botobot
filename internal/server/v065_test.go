package server

import (
	"net/http"
	"testing"
	"time"
)

// v0.6.5 round-12 items 2 and 3: a delete-all batch closes the lines a few
// seconds before the entry. Those guests are told the whole entry closed, and
// entry close reports every line that ended (lines_ended), not only the ones
// it closed itself (bridges_closed).
func TestEntryCloseAfterLinesInSameBatch(t *testing.T) {
	ts := newIntegServer(t, nil)
	e := ts.createEntry(t, "Desk", nil, "198.51.100.1:1")
	a := ts.mustJoin(t, e, "amy-bot", "203.0.113.9:1")
	b := ts.mustJoin(t, e, "ben-bot", "203.0.113.10:1")
	c := ts.mustJoin(t, e, "cat-bot", "203.0.113.11:1")
	for _, g := range []string{a.bridgeID, b.bridgeID} {
		if st, m, _ := ts.do(t, http.MethodDelete, "/v1/bridges/"+g, nil, reqOpts{token: e.key}); st != 200 {
			t.Fatalf("close line = %d %v", st, m)
		}
		ts.clk.Advance(2 * time.Second)
	}
	st, m, _ := ts.do(t, http.MethodDelete, "/v1/entries/"+e.id, nil, reqOpts{token: e.key})
	if st != 200 || m["bridges_closed"] != float64(1) || m["lines_ended"] != float64(3) {
		t.Fatalf("close entry = %d %v", st, m)
	}
	for _, g := range []testGuest{a, b, c} {
		st, m, _ := ts.do(t, http.MethodGet, "/v1/bridges/"+g.bridgeID+"/lands", nil, reqOpts{token: g.token})
		if em, _ := m["error"].(map[string]any); st != 410 || em["entry_closed"] != true {
			t.Fatalf("guest %s after entry close = %d %v", g.bridgeID, st, m)
		}
	}
}
