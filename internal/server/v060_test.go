package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hackerick4/botobot/internal/config"
)

// v0.6.0: public entry. One service bot, one reusable link, one private
// two-bot bridge per guest.

type testEntry struct {
	id, link, linkID, key, fp string
	k                         testLand
}

func (ts *testServer) createEntry(t *testing.T, name string, settings map[string]any, ip string) testEntry {
	t.Helper()
	k := newTestLand(t)
	body := map[string]any{
		"name": name,
		"land": landBlock{Profile: landProfileInput{Handle: "shop-bot", DisplayName: "Shop Bot", OwnerName: "Shop"}, Key: k.keyInput(t)},
	}
	if settings != nil {
		body["settings"] = settings
	}
	status, m, _ := ts.do(t, http.MethodPost, "/v1/entries", body, reqOpts{remoteAddr: ip})
	if status != http.StatusCreated {
		t.Fatalf("create entry = %d %v", status, m)
	}
	e := m["entry"].(map[string]any)
	link := e["link"].(string)
	return testEntry{id: e["entry_id"].(string), link: link, linkID: link[strings.LastIndex(link, "/")+1:],
		key: m["entry_key"].(string), fp: e["fingerprint"].(string), k: k}
}

type testGuest struct {
	bridgeID string
	lander
}

func (ts *testServer) entryJoin(t *testing.T, linkID, handle, ip string) (int, map[string]any, testGuest) {
	t.Helper()
	k := newTestLand(t)
	body := entryJoinReq{Land: landBlock{Profile: landProfileInput{Handle: handle, DisplayName: handle, OwnerName: "Owner " + handle}, Key: k.keyInput(t)}}
	status, m, _ := ts.do(t, http.MethodPost, "/v1/e/"+linkID+"/join", body, reqOpts{remoteAddr: ip})
	if status != http.StatusCreated {
		return status, m, testGuest{}
	}
	land := m["land"].(map[string]any)
	return status, m, testGuest{bridgeID: m["bridge"].(map[string]any)["bridge_id"].(string),
		lander: lander{landID: land["land_id"].(string), keyID: land["key_id"].(string), token: m["api_key"].(string), k: k}}
}

func (ts *testServer) mustJoin(t *testing.T, e testEntry, handle, ip string) testGuest {
	t.Helper()
	status, m, g := ts.entryJoin(t, e.linkID, handle, ip)
	if status != http.StatusCreated {
		t.Fatalf("entry join %s = %d %v", handle, status, m)
	}
	return g
}

type entryInboxWire struct {
	Envelopes []struct {
		BridgeID string `json:"bridge_id"`
		ID       string `json:"id"`
		Kind     string `json:"kind"`
		Event    struct {
			Type string `json:"type"`
		} `json:"event"`
	} `json:"envelopes"`
	OpenBridges int    `json:"open_bridges"`
	Status      string `json:"status"`
	ServerMS    int64  `json:"server_time_ms"`
	Closed      []struct {
		BridgeID string `json:"bridge_id"`
		Reason   string `json:"close_reason"`
	} `json:"closed"`
}

func (ts *testServer) entryInbox(t *testing.T, e testEntry, query string) entryInboxWire {
	t.Helper()
	status, body, _ := ts.doRaw(t, http.MethodGet, "/v1/entries/"+e.id+"/inbox"+query, nil, reqOpts{token: e.key})
	if status != http.StatusOK {
		t.Fatalf("entry inbox = %d %s", status, body)
	}
	var w entryInboxWire
	if err := json.Unmarshal(body, &w); err != nil {
		t.Fatalf("decode entry inbox: %v", err)
	}
	return w
}

// serviceLander is the service side of one entry bridge, for encrypt().
func (ts *testServer) serviceLander(t *testing.T, e testEntry, bridgeID string) lander {
	t.Helper()
	status, m, _ := ts.do(t, http.MethodGet, "/v1/bridges/"+bridgeID, nil, reqOpts{token: e.key})
	if status != http.StatusOK {
		t.Fatalf("entry key GET bridge = %d %v", status, m)
	}
	admin := m["bridge"].(map[string]any)["admin_land_id"].(string)
	return lander{landID: admin, keyID: ts.keyIDOf(t, e.key, bridgeID, admin), token: e.key, k: e.k}
}

func TestEntryGuestsGetSeparateBridges(t *testing.T) {
	ts := newIntegServer(t, nil)
	e := ts.createEntry(t, "Trattoria Uno", nil, "198.51.100.1:1")
	if !strings.HasPrefix(e.key, "bbe_") || !strings.HasPrefix(e.linkID, "pe_") || e.link != "https://bridge.example/e/"+e.linkID {
		t.Fatalf("entry = %+v", e)
	}
	var gs []testGuest
	for i, h := range []string{"amy-bot", "ben-bot", "cat-bot"} {
		gs = append(gs, ts.mustJoin(t, e, h, fmt.Sprintf("203.0.%d.9:1", 113+i)))
	}
	seen := map[string]bool{}
	for i, g := range gs {
		if seen[g.bridgeID] {
			t.Fatalf("guest %d shares a bridge", i)
		}
		seen[g.bridgeID] = true
		lw := ts.lands(t, g.token, g.bridgeID)
		if len(lw.Lands) != 2 {
			t.Fatalf("guest %d sees %d lands, want 2", i, len(lw.Lands))
		}
		// The guest sees the entry's fingerprint as the admin key.
		_, m, _ := ts.do(t, http.MethodGet, "/v1/bridges/"+g.bridgeID+"/lands", nil, reqOpts{token: g.token})
		found := false
		for _, l := range m["lands"].([]any) {
			lm := l.(map[string]any)
			if lm["role"] == "admin" && lm["fingerprint"] == e.fp {
				found = true
			}
		}
		if !found {
			t.Fatalf("guest %d: admin land does not carry the entry fingerprint", i)
		}
		// Bridge out marks the bridge and carries no invite link.
		_, bm, _ := ts.do(t, http.MethodGet, "/v1/bridges/"+g.bridgeID, nil, reqOpts{token: g.token})
		br := bm["bridge"].(map[string]any)
		if br["public_entry"] != true || br["invite_link"] != "" || br["pair"] != true || br["name"] != "Trattoria Uno" {
			t.Fatalf("bridge out = %v", br)
		}
	}
	// Guest A cannot reach guest B's bridge in any way.
	a, b := gs[0], gs[1]
	for _, p := range []string{"", "/lands", "/inbox"} {
		if status, _, _ := ts.do(t, http.MethodGet, "/v1/bridges/"+b.bridgeID+p, nil, reqOpts{token: a.token}); status != http.StatusNotFound {
			t.Fatalf("guest A on B%s = %d, want 404", p, status)
		}
	}
	if status, _, _ := ts.do(t, http.MethodGet, "/v1/entries/"+e.id+"/inbox", nil, reqOpts{token: a.token}); status != http.StatusUnauthorized {
		t.Fatalf("guest key on entry inbox = %d, want 401", status)
	}
}

func TestEntryAggregatedInboxAndReplies(t *testing.T) {
	ts := newIntegServer(t, nil)
	e := ts.createEntry(t, "Trattoria", nil, "198.51.100.1:1")
	var gs []testGuest
	for i, h := range []string{"amy-bot", "ben-bot", "cat-bot"} {
		gs = append(gs, ts.mustJoin(t, e, h, fmt.Sprintf("203.0.%d.9:1", 113+i)))
	}
	// Empty of messages: three join notices, one per bridge.
	w := ts.entryInbox(t, e, "")
	if len(w.Envelopes) != 3 || w.OpenBridges != 3 {
		t.Fatalf("join notices = %d open=%d", len(w.Envelopes), w.OpenBridges)
	}
	var ids []string
	for _, env := range w.Envelopes {
		if env.Kind != "event" || env.Event.Type != "land_joined" || !seenBridge(gs, env.BridgeID) {
			t.Fatalf("unexpected envelope %+v", env)
		}
		ids = append(ids, env.ID)
	}
	if st, m, _ := ts.do(t, http.MethodPost, "/v1/entries/"+e.id+"/inbox/ack", map[string]any{"ids": ids}, reqOpts{token: e.key}); st != 200 || m["deleted"] != float64(3) {
		t.Fatalf("entry ack = %d %v", st, m)
	}
	// Each guest books; one aggregated poll returns all three.
	for i, g := range gs {
		env := ts.encrypt(t, g.lander, createdBridge{bridgeID: g.bridgeID}, fmt.Sprintf("m%d", i), "table for 2")
		if st, m := ts.send(t, g.token, g.bridgeID, env); st != http.StatusAccepted {
			t.Fatalf("guest send = %d %v", st, m)
		}
	}
	w = ts.entryInbox(t, e, "")
	if len(w.Envelopes) != 3 {
		t.Fatalf("aggregated inbox = %d, want 3", len(w.Envelopes))
	}
	per := map[string]int{}
	ids = ids[:0]
	for _, env := range w.Envelopes {
		per[env.BridgeID]++
		ids = append(ids, env.ID)
	}
	if len(per) != 3 {
		t.Fatalf("messages by bridge = %v", per)
	}
	// Leased: a second poll right away is empty.
	if w2 := ts.entryInbox(t, e, ""); len(w2.Envelopes) != 0 {
		t.Fatalf("leased rows came back: %d", len(w2.Envelopes))
	}
	// Ack of a guest's own row id through the entry is refused (unknown).
	gw := ts.inbox(t, gs[0].token, gs[0].bridgeID)
	foreign := []string{}
	for _, env := range gw.Envelopes {
		foreign = append(foreign, env.ID)
	}
	st, m, _ := ts.do(t, http.MethodPost, "/v1/entries/"+e.id+"/inbox/ack", map[string]any{"ids": append(ids, foreign...)}, reqOpts{token: e.key})
	if st != 200 || m["deleted"] != float64(3) || len(m["unknown"].([]any)) != len(foreign) {
		t.Fatalf("mixed ack = %d %v", st, m)
	}
	// The service answers guest A only; B and C get nothing.
	svc := ts.serviceLander(t, e, gs[0].bridgeID)
	env := ts.encrypt(t, svc, createdBridge{bridgeID: gs[0].bridgeID}, "r1", "booked 19:00")
	if st, m := ts.send(t, e.key, gs[0].bridgeID, env); st != http.StatusAccepted {
		t.Fatalf("service send = %d %v", st, m)
	}
	if n := countMessages(ts.inbox(t, gs[0].token, gs[0].bridgeID)); n != 1 {
		t.Fatalf("guest A got %d messages", n)
	}
	for _, g := range gs[1:] {
		if n := countMessages(ts.inbox(t, g.token, g.bridgeID)); n != 0 {
			t.Fatalf("another guest got %d messages", n)
		}
	}
}

func seenBridge(gs []testGuest, id string) bool {
	for _, g := range gs {
		if g.bridgeID == id {
			return true
		}
	}
	return false
}

func TestEntryKeyScope(t *testing.T) {
	ts := newIntegServer(t, nil)
	e := ts.createEntry(t, "Clinic", nil, "198.51.100.1:1")
	other := ts.createEntry(t, "Other", nil, "198.51.100.2:1")
	g := ts.mustJoin(t, e, "amy-bot", "203.0.113.9:1")
	_, plain := ts.newAdmin(t, "sample-bot")
	cases := []struct {
		method, path, token string
		want                int
	}{
		{"GET", "/v1/bridges/" + plain.bridgeID, e.key, 404},
		{"GET", "/v1/bridges/" + g.bridgeID, other.key, 404},
		{"GET", "/v1/me", e.key, 403},
		{"POST", "/v1/me/api-keys", e.key, 403},
		{"GET", "/v1/entries/" + e.id, other.key, 404},
		{"GET", "/v1/entries/" + e.id, plain.token, 401},
		{"GET", "/v1/entries/" + e.id, "", 401},
		{"PATCH", "/v1/bridges/" + g.bridgeID, e.key, 403},
		{"GET", "/v1/bridges/" + g.bridgeID + "/invite-code", e.key, 403},
		{"POST", "/v1/bridges/" + g.bridgeID + "/invite-code/rotate", e.key, 403},
		{"GET", "/v1/bridges/" + g.bridgeID, e.key, 200},
	}
	for _, c := range cases {
		var body any
		if c.method != "GET" {
			body = map[string]any{}
		}
		if st, m, _ := ts.do(t, c.method, c.path, body, reqOpts{token: c.token}); st != c.want {
			t.Errorf("%s %s = %d %v, want %d", c.method, c.path, st, m, c.want)
		}
	}
}

func TestEntryDisableRotateAndClose(t *testing.T) {
	ts := newIntegServer(t, nil)
	e := ts.createEntry(t, "Shop", nil, "198.51.100.1:1")
	a := ts.mustJoin(t, e, "amy-bot", "203.0.113.9:1")
	patch := func(body map[string]any) map[string]any {
		st, m, _ := ts.do(t, http.MethodPatch, "/v1/entries/"+e.id, body, reqOpts{token: e.key})
		if st != 200 {
			t.Fatalf("patch = %d %v", st, m)
		}
		return m["entry"].(map[string]any)
	}
	if out := patch(map[string]any{"status": "disabled"}); out["status"] != "disabled" {
		t.Fatalf("disable: %v", out)
	}
	if st, m, _ := ts.entryJoin(t, e.linkID, "ben-bot", "203.0.114.9:1"); st != 403 || errCode(m) != "entry_disabled" {
		t.Fatalf("join while disabled = %d %v", st, m)
	}
	// Existing bridge keeps working.
	env := ts.encrypt(t, a.lander, createdBridge{bridgeID: a.bridgeID}, "m1", "still there?")
	if st, m := ts.send(t, a.token, a.bridgeID, env); st != http.StatusAccepted {
		t.Fatalf("send on existing bridge while disabled = %d %v", st, m)
	}
	patch(map[string]any{"status": "active"})
	ts.mustJoin(t, e, "ben-bot", "203.0.114.9:1")

	// Rotate: the old link is dead at once, the new one works, bridges stay.
	st, m, _ := ts.do(t, http.MethodPost, "/v1/entries/"+e.id+"/rotate-link", map[string]any{}, reqOpts{token: e.key})
	if st != 200 {
		t.Fatalf("rotate = %d %v", st, m)
	}
	newLink := m["entry"].(map[string]any)["link"].(string)
	newID := newLink[strings.LastIndex(newLink, "/")+1:]
	if newID == e.linkID {
		t.Fatal("rotate kept the link")
	}
	if st, m, _ := ts.entryJoin(t, e.linkID, "cat-bot", "203.0.115.9:1"); st != 404 || errCode(m) != "entry_not_found" {
		t.Fatalf("old link = %d %v", st, m)
	}
	e2 := e
	e2.linkID = newID
	ts.mustJoin(t, e2, "cat-bot", "203.0.115.9:1")
	if w := ts.entryInbox(t, e, ""); w.OpenBridges != 3 {
		t.Fatalf("open after rotate = %d", w.OpenBridges)
	}

	// The service closes one guest's bridge: only that one goes.
	w := ts.entryInbox(t, e, "?closed_since=0")
	since := w.ServerMS
	ts.clk.Advance(time.Second)
	if st, m, _ := ts.do(t, http.MethodDelete, "/v1/bridges/"+a.bridgeID, nil, reqOpts{token: e.key}); st != 200 {
		t.Fatalf("service close A = %d %v", st, m)
	}
	if st, m, _ := ts.do(t, http.MethodGet, "/v1/bridges/"+a.bridgeID, nil, reqOpts{token: a.token}); st != 410 {
		t.Fatalf("guest A after close = %d %v", st, m)
	}
	w = ts.entryInbox(t, e, fmt.Sprintf("?closed_since=%d", since))
	if w.OpenBridges != 2 || len(w.Closed) != 1 || w.Closed[0].BridgeID != a.bridgeID || w.Closed[0].Reason != "admin_closed" {
		t.Fatalf("after closing A: open=%d closed=%+v", w.OpenBridges, w.Closed)
	}

	// Closing the entry closes every bridge and kills the key and the link.
	st, m, _ = ts.do(t, http.MethodDelete, "/v1/entries/"+e.id, nil, reqOpts{token: e.key})
	if st != 200 || m["bridges_closed"] != float64(2) {
		t.Fatalf("close entry = %d %v", st, m)
	}
	if st, _, _ := ts.do(t, http.MethodGet, "/v1/entries/"+e.id, nil, reqOpts{token: e.key}); st != 410 {
		t.Fatalf("entry key after close = %d", st)
	}
	// v0.6.1 (RT-116): a closed link answers exactly like an unknown one.
	if st, m, _ := ts.entryJoin(t, newID, "dan-bot", "203.0.116.9:1"); st != 404 || errCode(m) != "entry_not_found" {
		t.Fatalf("join after close = %d %v", st, m)
	}
}

func TestEntryGuestLeaveClosesOnlyItsBridge(t *testing.T) {
	ts := newIntegServer(t, nil)
	e := ts.createEntry(t, "Shop", nil, "198.51.100.1:1")
	a := ts.mustJoin(t, e, "amy-bot", "203.0.113.9:1")
	b := ts.mustJoin(t, e, "ben-bot", "203.0.114.9:1")
	if st, m, _ := ts.do(t, http.MethodDelete, "/v1/bridges/"+a.bridgeID+"/lands/"+a.landID, nil, reqOpts{token: a.token}); st != 200 {
		t.Fatalf("guest leave = %d %v", st, m)
	}
	if _, _, status := ts.bridgeRow(t, a.bridgeID); status != "closed" {
		t.Fatalf("A status = %s", status)
	}
	if _, _, status := ts.bridgeRow(t, b.bridgeID); status != "active" {
		t.Fatalf("B status = %s", status)
	}
	w := ts.entryInbox(t, e, "?closed_since=0")
	if w.OpenBridges != 1 || len(w.Closed) != 1 || w.Closed[0].Reason != "member_left" {
		t.Fatalf("after leave: %+v", w)
	}
}

func TestEntryLimits(t *testing.T) {
	ts := newIntegServer(t, func(c *config.Config) {
		c.EntryJoinPerPrefixPerEntryPerHour = 2
		c.EntryJoinPerPrefixPerHour = 3
		c.MaxEntriesPerPrefix = 2
	})
	e := ts.createEntry(t, "Shop", map[string]any{"max_open_bridges": 3}, "198.51.100.1:1")
	// Per network per entry: the third join from one /24 waits.
	ts.mustJoin(t, e, "a1-bot", "203.0.113.1:1")
	ts.mustJoin(t, e, "a2-bot", "203.0.113.2:1")
	if st, m, _ := ts.entryJoin(t, e.linkID, "a3-bot", "203.0.113.3:1"); st != 429 {
		t.Fatalf("third join from one /24 = %d %v", st, m)
	}
	// The network-wide bucket (3) is now spent too, for any entry.
	e2 := ts.createEntry(t, "Shop 2", nil, "198.51.100.1:1")
	if st, m, _ := ts.entryJoin(t, e2.linkID, "a4-bot", "203.0.113.4:1"); st != 429 {
		t.Fatalf("fourth join from one /24 = %d %v", st, m)
	}
	// Cap: max_open_bridges = 3.
	ts.mustJoin(t, e, "b1-bot", "203.0.114.1:1")
	if st, m, _ := ts.entryJoin(t, e.linkID, "c1-bot", "203.0.115.1:1"); st != 409 || errCode(m) != "entry_full" {
		t.Fatalf("join over cap = %d %v", st, m)
	}
	// Per-prefix entry cap.
	k := newTestLand(t)
	body := map[string]any{"name": "Third", "land": landBlock{Profile: landProfileInput{Handle: "x-bot"}, Key: k.keyInput(t)}}
	if st, m, _ := ts.do(t, http.MethodPost, "/v1/entries", body, reqOpts{remoteAddr: "198.51.100.9:1"}); st != 429 || errCode(m) != "too_many_entries" {
		t.Fatalf("third entry = %d %v", st, m)
	}
	// Settings outside the ceilings are refused.
	for _, s := range []map[string]any{{"max_open_bridges": 0}, {"max_open_bridges": 5000}, {"joins_per_hour": 601}} {
		if st, _, _ := ts.do(t, http.MethodPatch, "/v1/entries/"+e.id, map[string]any{"settings": s}, reqOpts{token: e.key}); st != 400 {
			t.Fatalf("patch %v = %d, want 400", s, st)
		}
	}
	// The service handle cannot be reused by a guest.
	if st, m, _ := ts.entryJoin(t, e2.linkID, "shop-bot", "203.0.120.1:1"); st != 409 || errCode(m) != "handle_taken" {
		t.Fatalf("guest with service handle = %d %v", st, m)
	}
}

func TestEntryJoinsPerHourBudget(t *testing.T) {
	ts := newIntegServer(t, nil)
	e := ts.createEntry(t, "Shop", map[string]any{"joins_per_hour": 3}, "198.51.100.1:1")
	for i := 0; i < 3; i++ {
		ts.mustJoin(t, e, fmt.Sprintf("g%d-bot", i), fmt.Sprintf("203.0.%d.1:1", 100+i))
	}
	if st, m, _ := ts.entryJoin(t, e.linkID, "g9-bot", "203.0.150.1:1"); st != 429 {
		t.Fatalf("fourth join in the hour = %d %v", st, m)
	}
	ts.clk.Advance(21 * time.Minute)
	ts.mustJoin(t, e, "g9-bot", "203.0.150.1:1")
}

func TestEntryConcurrentJoinsRespectCap(t *testing.T) {
	ts := newIntegServer(t, nil)
	e := ts.createEntry(t, "Shop", map[string]any{"max_open_bridges": 5}, "198.51.100.1:1")
	var wg sync.WaitGroup
	var mu sync.Mutex
	codes := map[int]int{}
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			st, _, _ := ts.entryJoin(t, e.linkID, fmt.Sprintf("g%d-bot", i), fmt.Sprintf("203.0.%d.1:1", 100+i))
			mu.Lock()
			codes[st]++
			mu.Unlock()
		}(i)
	}
	wg.Wait()
	if codes[201] != 5 || codes[409] != 15 {
		t.Fatalf("concurrent joins = %v, want 5x201 15x409", codes)
	}
	if n, _ := ts.st.CountOpenEntryBridges(context.Background(), e.id); n != 5 {
		t.Fatalf("open = %d", n)
	}
}

func TestEntryInactivityFollowsEntry(t *testing.T) {
	ts := newIntegServer(t, func(c *config.Config) { c.RateInboxPerMin = 100000 })
	e := ts.createEntry(t, "Shop", nil, "198.51.100.1:1")
	a := ts.mustJoin(t, e, "amy-bot", "203.0.113.9:1")
	sw := ts.newSweeper(7 * 24 * time.Hour)
	// The service polls only the aggregated inbox; that keeps every bridge.
	for d := 0; d < 9; d++ {
		ts.clk.Advance(24 * time.Hour)
		ts.entryInbox(t, e, "")
		if _, err := sw.RunOnce(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, status := ts.bridgeRow(t, a.bridgeID); status != "active" {
		t.Fatalf("bridge closed although the entry polled: %s", status)
	}
	// Silent for 8 days: bridges close, the entry stops taking guests.
	ts.clk.Advance(8 * 24 * time.Hour)
	res, err := sw.RunOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, _, status := ts.bridgeRow(t, a.bridgeID); status != "closed" || res.EntriesDisabled != 1 {
		t.Fatalf("after silence: status=%s disabled=%d", status, res.EntriesDisabled)
	}
	if st, m, _ := ts.entryJoin(t, e.linkID, "ben-bot", "203.0.114.9:1"); st != 403 || errCode(m) != "entry_disabled" {
		t.Fatalf("join after inactivity = %d %v", st, m)
	}
}

func TestEntryPageAndUnknownLink(t *testing.T) {
	ts := newIntegServer(t, nil)
	e := ts.createEntry(t, "Desk", nil, "198.51.100.1:1")
	status, body, _ := ts.doRaw(t, http.MethodGet, "/e/"+e.linkID, nil, reqOpts{})
	if status != 200 || !strings.Contains(string(body), "join --entry 'https://bridge.example/e/"+e.linkID+"'") {
		t.Fatalf("entry page = %d %s", status, body)
	}
	// Industry-neutral wording (no shop, guest or visitor examples).
	for _, w := range []string{"shop", "guest", "visitor", "商家", "店家", "訪客", "主人"} {
		if strings.Contains(strings.ToLower(string(body)), w) {
			t.Errorf("entry page should be neutral, found %q", w)
		}
	}
	_, body, _ = ts.doRaw(t, http.MethodGet, "/e/%3Cscript%3E", nil, reqOpts{})
	if strings.Contains(string(body), "<script>") {
		t.Fatal("entry page echoed raw input")
	}
	if st, m, _ := ts.entryJoin(t, "pe_doesnotexist", "amy-bot", "203.0.113.9:1"); st != 404 || errCode(m) != "entry_not_found" {
		t.Fatalf("unknown link = %d %v", st, m)
	}
}

func TestEntryLogsHaveNoSecrets(t *testing.T) {
	ts := newIntegServer(t, nil)
	e := ts.createEntry(t, "Shop", nil, "198.51.100.1:1")
	g := ts.mustJoin(t, e, "amy-bot", "203.0.113.9:1")
	ts.entryInbox(t, e, "?closed_since=0")
	ts.do(t, http.MethodGet, "/v1/entries/"+e.id+"/bridges", nil, reqOpts{token: e.key})
	ts.do(t, http.MethodPost, "/v1/entries/"+e.id+"/rotate-link", map[string]any{}, reqOpts{token: e.key})
	ts.do(t, http.MethodGet, "/v1/entries/"+e.id, nil, reqOpts{token: "bbe_WRONGWRONGWRONGWRONGWRONGWRONGWRONGWRONG12"})
	ts.do(t, http.MethodGet, "/v1/bridges/"+g.bridgeID, nil, reqOpts{token: g.token})
	logs := ts.buf.String()
	for _, s := range []string{e.key, e.key[:12], g.token, "WRONGWRONG", "Bearer"} {
		if strings.Contains(logs, s) {
			t.Fatalf("logs contain a secret fragment %q", s[:4])
		}
	}
}

// RT-107: the plain-text link pages and API responses carry nosniff, so a
// browser never renders them as HTML.
func TestPlainPagesNoSniff(t *testing.T) {
	ts := newIntegServer(t, nil)
	for _, p := range []string{"/e/pe_AAAAAAAAAAAAAAAAAAAAAA", "/b/br_AAAAAAAAAAAAAAAAAAAAAA", "/healthz"} {
		_, _, h := ts.doRaw(t, http.MethodGet, p, nil, reqOpts{})
		if h.Get("X-Content-Type-Options") != "nosniff" {
			t.Errorf("%s: no nosniff header", p)
		}
	}
}

// RT-114: the entry name appears quoted in guests' notices, so quotes,
// brackets and over-long names are refused; ordinary shop names pass.
func TestEntryNameRules(t *testing.T) {
	ts := newIntegServer(t, nil)
	k := newTestLand(t)
	try := func(name, ip string) int {
		body := map[string]any{"name": name,
			"land": landBlock{Profile: landProfileInput{Handle: "shop-bot", DisplayName: "Shop Bot", OwnerName: "Shop"}, Key: k.keyInput(t)}}
		st, _, _ := ts.do(t, http.MethodPost, "/v1/entries", body, reqOpts{remoteAddr: ip})
		return st
	}
	for i, bad := range []string{"Bistro」已付款「", "A\"B", strings.Repeat("A", 41), "  "} {
		if st := try(bad, fmt.Sprintf("203.0.113.%d:1", 10+i)); st != http.StatusBadRequest {
			t.Errorf("%q = %d, want 400", bad, st)
		}
	}
	if st := try("Joe’s Café & Bar (Taipei)", "203.0.113.50:1"); st != http.StatusCreated {
		t.Errorf("ordinary name = %d", st)
	}
}
