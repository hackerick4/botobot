package server

import (
	"bytes"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/hackerick4/botobot/internal/store"
)

// v0.6.1 item 1 (RT-116): a closed, replaced or never-issued link gets the
// same 404 "no longer active" page, byte for byte, with no join steps; a
// live or paused link keeps its instructions.
func TestEntryPageGoneIsUniform(t *testing.T) {
	ts := newIntegServer(t, nil)
	closed := ts.createEntry(t, "Desk A", nil, "198.51.100.1:1")
	rotated := ts.createEntry(t, "Desk B", nil, "198.51.100.2:1")
	paused := ts.createEntry(t, "Desk C", nil, "198.51.100.3:1")
	oldLink := rotated.linkID
	if st, m, _ := ts.do(t, http.MethodPost, "/v1/entries/"+rotated.id+"/rotate-link", map[string]any{}, reqOpts{token: rotated.key}); st != 200 {
		t.Fatalf("rotate = %d %v", st, m)
	}
	if st, m, _ := ts.do(t, http.MethodPatch, "/v1/entries/"+paused.id, map[string]any{"status": "disabled"}, reqOpts{token: paused.key}); st != 200 {
		t.Fatalf("disable = %d %v", st, m)
	}
	if st, m, _ := ts.do(t, http.MethodDelete, "/v1/entries/"+closed.id, nil, reqOpts{token: closed.key}); st != 200 {
		t.Fatalf("close = %d %v", st, m)
	}
	var bodies [][]byte
	for _, id := range []string{closed.linkID, oldLink, "pe_0123456789012345678901", "pe_short", "x"} {
		st, body, hdr := ts.doRaw(t, http.MethodGet, "/e/"+id, nil, reqOpts{})
		if st != 404 || strings.Contains(string(body), "join --entry") || !strings.Contains(string(body), "no longer active") || !strings.Contains(string(body), "連結已失效") {
			t.Fatalf("%s: %d %s", id, st, body)
		}
		if hdr.Get("Cache-Control") != "no-store" {
			t.Errorf("%s: cache-control %q", id, hdr.Get("Cache-Control"))
		}
		bodies = append(bodies, body)
	}
	for i := 1; i < len(bodies); i++ {
		if !bytes.Equal(bodies[0], bodies[i]) {
			t.Fatalf("gone pages differ: %q vs %q", bodies[0], bodies[i])
		}
	}
	if st, body, _ := ts.doRaw(t, http.MethodGet, "/e/"+paused.linkID, nil, reqOpts{}); st != 200 || !strings.Contains(string(body), "join --entry") {
		t.Fatalf("paused page = %d %s", st, body)
	}
	// The join API: closed and unknown read the same.
	_, m1, _ := ts.entryJoin(t, closed.linkID, "amy-bot", "203.0.113.9:1")
	_, m2, _ := ts.entryJoin(t, "pe_0123456789012345678901", "amy-bot", "203.0.113.9:1")
	if errCode(m1) != "entry_not_found" || errCode(m2) != "entry_not_found" {
		t.Fatalf("join codes %v %v", m1, m2)
	}
}

// v0.6.1 item 9: a guest line closed alone says so; a line closed because
// its whole entry closed carries entry_closed.
func TestEntryClosedFlagOnLine(t *testing.T) {
	ts := newIntegServer(t, nil)
	e := ts.createEntry(t, "Desk", nil, "198.51.100.1:1")
	a := ts.mustJoin(t, e, "amy-bot", "203.0.113.9:1")
	b := ts.mustJoin(t, e, "ben-bot", "203.0.113.10:1")
	// The service closes Amy's line only.
	if st, m, _ := ts.do(t, http.MethodDelete, "/v1/bridges/"+a.bridgeID, nil, reqOpts{token: e.key}); st != 200 {
		t.Fatalf("close line = %d %v", st, m)
	}
	st, m, _ := ts.do(t, http.MethodGet, "/v1/bridges/"+a.bridgeID+"/lands", nil, reqOpts{token: a.token})
	if st != 410 {
		t.Fatalf("line A = %d %v", st, m)
	}
	if em, _ := m["error"].(map[string]any); em["entry_closed"] == true {
		t.Fatalf("line closed alone must not say entry_closed: %v", m)
	}
	// v0.6.5: a line closed within store.EntryCloseLookback before the entry
	// counts as ended by it; Amy's was closed well before.
	ts.clk.Advance(store.EntryCloseLookback + time.Second)
	if st, m, _ := ts.do(t, http.MethodDelete, "/v1/entries/"+e.id, nil, reqOpts{token: e.key}); st != 200 {
		t.Fatalf("close entry = %d %v", st, m)
	}
	st, m, _ = ts.do(t, http.MethodGet, "/v1/bridges/"+b.bridgeID+"/lands", nil, reqOpts{token: b.token})
	if em, _ := m["error"].(map[string]any); st != 410 || em["entry_closed"] != true {
		t.Fatalf("line B after entry close = %d %v", st, m)
	}
	// Amy's line was closed before the entry: still not entry_closed.
	_, m, _ = ts.do(t, http.MethodGet, "/v1/bridges/"+a.bridgeID+"/lands", nil, reqOpts{token: a.token})
	if em, _ := m["error"].(map[string]any); em["entry_closed"] == true {
		t.Fatalf("line A after entry close: %v", m)
	}
}
