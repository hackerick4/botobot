package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/hackerick4/botobot/internal/config"
	"github.com/hackerick4/botobot/internal/protocol"
)

// v0.6.4: messages the other side had not fetched when a bridge closed stay
// readable by that side only, for a short window, then are deleted.

func graceServer(t *testing.T, grace time.Duration) *testServer {
	t.Helper()
	return newIntegServer(t, func(c *config.Config) {
		c.CloseGrace = grace
		c.RateInboxPerMin = 100000
		c.RateAckPerMin = 100000
	})
}

type closingWire struct {
	inboxWire
	Closing map[string]any `json:"closing"`
}

func (ts *testServer) inboxStatus(t *testing.T, token, bridgeID string) (int, closingWire) {
	t.Helper()
	status, body, _ := ts.doRaw(t, http.MethodGet, "/v1/bridges/"+bridgeID+"/inbox", nil, reqOpts{token: token})
	var w closingWire
	_ = json.Unmarshal(body, &w)
	return status, w
}

func (ts *testServer) pairWithMessages(t *testing.T, n int) (lander, lander, createdBridge) {
	t.Helper()
	a, b := ts.newAdmin(t, "amy-bot")
	m := ts.newMember(t, b, "ben-bot", "192.0.2.20:1")
	a.keyID = ts.keyIDOf(t, a.token, b.bridgeID, a.landID)
	// Ben reads (and acks) his join notices first so only messages remain.
	if iw := ts.inbox(t, m.token, b.bridgeID); len(iw.Envelopes) > 0 {
		var ids []string
		for _, e := range iw.Envelopes {
			ids = append(ids, e.ID)
		}
		ts.ack(t, m.token, b.bridgeID, ids)
	}
	for i := 0; i < n; i++ {
		if st, r := ts.send(t, a.token, b.bridgeID, ts.encrypt(t, a, b, "m-"+itoa(i), "hello "+itoa(i))); st != http.StatusAccepted && st != http.StatusOK && st != http.StatusCreated {
			t.Fatalf("send %d = %d %v", i, st, r)
		}
	}
	return a, m, b
}

func msgIDs(w closingWire) []string {
	var ids []string
	for _, e := range w.Envelopes {
		if e.Kind == "message" {
			ids = append(ids, e.ID)
		}
	}
	return ids
}

func TestCloseGraceOtherSideGetsLastMessages(t *testing.T) {
	ts := graceServer(t, 10*time.Minute)
	a, m, b := ts.pairWithMessages(t, 2)

	// The sender can see how many the other side has not read; the other side
	// sees only its own count.
	if st, r, _ := ts.do(t, http.MethodGet, "/v1/bridges/"+b.bridgeID+"/undelivered", nil, reqOpts{token: a.token}); st != 200 || r["count"] != float64(2) {
		t.Fatalf("undelivered (sender) = %d %v", st, r)
	}
	if _, r, _ := ts.do(t, http.MethodGet, "/v1/bridges/"+b.bridgeID+"/undelivered", nil, reqOpts{token: m.token}); r["count"] != float64(0) {
		t.Fatalf("undelivered (other side) = %v, want 0", r)
	}

	st, r, _ := ts.do(t, http.MethodDelete, "/v1/bridges/"+b.bridgeID, nil, reqOpts{token: a.token})
	if st != 200 || r["undelivered"] != float64(2) || r["final_until"] == nil {
		t.Fatalf("close = %d %v, want undelivered 2 + final_until", st, r)
	}

	// The closer is out at once, for everything.
	if st, _ := ts.inboxStatus(t, a.token, b.bridgeID); st != http.StatusGone {
		t.Fatalf("closer inbox = %d, want 410", st)
	}
	if st, _ := ts.send(t, a.token, b.bridgeID, ts.encryptNoLookup(t, a, b, m, "late", "late")); st != http.StatusGone {
		t.Fatalf("closer send = %d, want 410", st)
	}
	// The other side may not send, list, or do anything but fetch + ack.
	if st, _ := ts.send(t, m.token, b.bridgeID, ts.encryptNoLookup(t, m, b, a, "reply", "reply")); st != http.StatusGone {
		t.Fatalf("other side send = %d, want 410", st)
	}
	if st, _, _ := ts.do(t, http.MethodGet, "/v1/bridges/"+b.bridgeID+"/lands", nil, reqOpts{token: m.token}); st != http.StatusGone {
		t.Fatalf("other side lands = %d, want 410", st)
	}
	if st, _, _ := ts.do(t, http.MethodGet, "/v1/bridges/"+b.bridgeID+"/undelivered", nil, reqOpts{token: a.token}); st != http.StatusGone {
		t.Fatalf("undelivered after close = %d, want 410", st)
	}

	st, w := ts.inboxStatus(t, m.token, b.bridgeID)
	ids := msgIDs(w)
	if st != 200 || len(ids) != 2 || w.Closing == nil || w.Closing["close_reason"] != "admin_closed" || w.Closing["final_until"] == nil {
		t.Fatalf("other side inbox = %d %d msgs closing=%v", st, len(ids), w.Closing)
	}
	for _, e := range w.Envelopes {
		if e.Kind != "message" {
			t.Fatalf("only messages are kept, got %s", e.Kind)
		}
	}
	ts.ack(t, m.token, b.bridgeID, ids)

	// All read: the window ends at once and everything is gone.
	if st, _ := ts.inboxStatus(t, m.token, b.bridgeID); st != http.StatusGone {
		t.Fatalf("inbox after final ack = %d, want 410", st)
	}
	br, _ := ts.st.GetBridge(context.Background(), b.bridgeID)
	if br.GraceUntil != nil || br.GraceCloser != nil || br.AdminLandID != nil {
		t.Fatalf("bridge after final ack still in window: %+v", br)
	}
	if _, err := ts.st.GetLand(context.Background(), m.landID); err == nil {
		t.Fatal("lands should be deleted once the window ends")
	}
	if n := ts.rowCount(t, "SELECT COUNT(*) FROM messages WHERE bridge_id = ?", b.bridgeID); n != 0 {
		t.Fatalf("messages left = %d", n)
	}
}

func TestCloseGraceEndsAtWindow(t *testing.T) {
	ts := graceServer(t, 10*time.Minute)
	a, m, b := ts.pairWithMessages(t, 1)
	if st, r, _ := ts.do(t, http.MethodDelete, "/v1/bridges/"+b.bridgeID, nil, reqOpts{token: a.token}); st != 200 || r["undelivered"] != float64(1) {
		t.Fatalf("close = %d %v", st, r)
	}
	ts.clk.Advance(10*time.Minute + time.Second)
	if st, w := ts.inboxStatus(t, m.token, b.bridgeID); st != http.StatusGone {
		t.Fatalf("inbox after window = %d %v, want 410", st, w)
	}
	if n := ts.rowCount(t, "SELECT COUNT(*) FROM inbox_queue WHERE bridge_id = ?", b.bridgeID); n != 0 {
		t.Fatalf("inbox rows left after window = %d", n)
	}
}

func TestCloseGraceSweeperEndsWindow(t *testing.T) {
	ts := graceServer(t, 10*time.Minute)
	a, _, b := ts.pairWithMessages(t, 1)
	ts.do(t, http.MethodDelete, "/v1/bridges/"+b.bridgeID, nil, reqOpts{token: a.token})
	ts.clk.Advance(11 * time.Minute)
	if _, err := ts.newSweeper(30 * 24 * time.Hour).RunOnce(context.Background()); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if n := ts.rowCount(t, "SELECT COUNT(*) FROM lands WHERE bridge_id = ?", b.bridgeID); n != 0 {
		t.Fatalf("lands left after sweep = %d", n)
	}
	if n := ts.rowCount(t, "SELECT COUNT(*) FROM bridges WHERE grace_until IS NOT NULL"); n != 0 {
		t.Fatalf("windows left after sweep = %d", n)
	}
}

func TestCloseNoPendingNoWindow(t *testing.T) {
	ts := graceServer(t, 10*time.Minute)
	a, m, b := ts.pairWithMessages(t, 0)
	st, r, _ := ts.do(t, http.MethodDelete, "/v1/bridges/"+b.bridgeID, nil, reqOpts{token: a.token})
	if st != 200 || r["undelivered"] != float64(0) || r["final_until"] != nil {
		t.Fatalf("close = %d %v", st, r)
	}
	if st, _ := ts.inboxStatus(t, m.token, b.bridgeID); st != http.StatusGone {
		t.Fatalf("inbox = %d, want 410", st)
	}
}

func TestCloseGraceOffKeepsOldBehaviour(t *testing.T) {
	ts := graceServer(t, 0)
	a, m, b := ts.pairWithMessages(t, 2)
	st, r, _ := ts.do(t, http.MethodDelete, "/v1/bridges/"+b.bridgeID, nil, reqOpts{token: a.token})
	if st != 200 || r["undelivered"] != float64(2) || r["final_until"] != nil {
		t.Fatalf("close = %d %v", st, r)
	}
	if st, _ := ts.inboxStatus(t, m.token, b.bridgeID); st != http.StatusGone {
		t.Fatalf("inbox = %d, want 410", st)
	}
}

func TestCloseGracePeerLeaveGivesAdminLastMessages(t *testing.T) {
	ts := graceServer(t, 10*time.Minute)
	a, b := ts.newAdmin(t, "amy-bot")
	m := ts.newMember(t, b, "ben-bot", "192.0.2.21:1")
	// Amy reads the join notice first.
	if iw := ts.inbox(t, a.token, b.bridgeID); len(iw.Envelopes) > 0 {
		var ids []string
		for _, e := range iw.Envelopes {
			ids = append(ids, e.ID)
		}
		ts.ack(t, a.token, b.bridgeID, ids)
	}
	if st, r := ts.send(t, m.token, b.bridgeID, ts.encrypt(t, m, b, "r-1", "bye")); st >= 300 {
		t.Fatalf("send = %d %v", st, r)
	}
	st, r, _ := ts.do(t, http.MethodDelete, "/v1/bridges/"+b.bridgeID+"/lands/"+m.landID, nil, reqOpts{token: m.token})
	if st != 200 || r["bridge_closed"] != true || r["undelivered"] != float64(1) {
		t.Fatalf("leave = %d %v", st, r)
	}
	if st, _ := ts.inboxStatus(t, m.token, b.bridgeID); st != http.StatusGone {
		t.Fatalf("leaver inbox = %d, want 410", st)
	}
	st, w := ts.inboxStatus(t, a.token, b.bridgeID)
	if st != 200 || len(msgIDs(w)) != 1 || w.Closing == nil {
		t.Fatalf("admin inbox = %d %v", st, w)
	}
	// Extending the window by polling is not possible.
	ts.clk.Advance(9 * time.Minute)
	ts.inboxStatus(t, a.token, b.bridgeID)
	ts.clk.Advance(2 * time.Minute)
	if st, _ := ts.inboxStatus(t, a.token, b.bridgeID); st != http.StatusGone {
		t.Fatalf("inbox past window = %d, want 410", st)
	}
}

func TestCloseGraceEntryGuestLeaves(t *testing.T) {
	ts := graceServer(t, 10*time.Minute)
	e := ts.createEntry(t, "Trattoria Uno", nil, "198.51.100.1:1")
	g := ts.mustJoin(t, e, "amy-bot", "192.0.2.30:1")
	// The service side reads the join notice.
	w := ts.entryInbox(t, e, "")
	var ids []string
	for _, en := range w.Envelopes {
		ids = append(ids, en.ID)
	}
	if len(ids) > 0 {
		ts.do(t, http.MethodPost, "/v1/entries/"+e.id+"/inbox/ack", map[string]any{"ids": ids}, reqOpts{token: e.key})
	}
	cb := createdBridge{bridgeID: g.bridgeID}
	if st, r := ts.send(t, g.token, g.bridgeID, ts.encrypt(t, g.lander, cb, "g-1", "thanks")); st >= 300 {
		t.Fatalf("guest send = %d %v", st, r)
	}
	if st, r, _ := ts.do(t, http.MethodDelete, "/v1/bridges/"+g.bridgeID+"/lands/"+g.landID, nil, reqOpts{token: g.token}); st != 200 || r["undelivered"] != float64(1) {
		t.Fatalf("guest leave = %d %v", st, r)
	}
	w = ts.entryInbox(t, e, "?closed_since=0")
	if len(w.Envelopes) != 1 || w.Envelopes[0].BridgeID != g.bridgeID || len(w.Closed) != 1 {
		t.Fatalf("entry inbox after guest left = %+v", w)
	}
	st, r, _ := ts.do(t, http.MethodPost, "/v1/entries/"+e.id+"/inbox/ack", map[string]any{"ids": []string{w.Envelopes[0].ID}}, reqOpts{token: e.key})
	if st != 200 || r["deleted"] != float64(1) {
		t.Fatalf("entry ack = %d %v", st, r)
	}
	if n := ts.rowCount(t, "SELECT COUNT(*) FROM lands WHERE bridge_id = ?", g.bridgeID); n != 0 {
		t.Fatalf("lands left after last ack = %d", n)
	}
}

func TestCloseGraceEntryLineClosedByService(t *testing.T) {
	ts := graceServer(t, 10*time.Minute)
	e := ts.createEntry(t, "Trattoria Uno", nil, "198.51.100.1:1")
	g := ts.mustJoin(t, e, "amy-bot", "192.0.2.31:1")
	svc := ts.serviceLander(t, e, g.bridgeID)
	cb := createdBridge{bridgeID: g.bridgeID}
	for _, n := range []string{"s-1", "s-2"} {
		if st, r := ts.send(t, e.key, g.bridgeID, ts.encrypt(t, svc, cb, n, "reply")); st >= 300 {
			t.Fatalf("service send = %d %v", st, r)
		}
	}
	_, lst, _ := ts.do(t, http.MethodGet, "/v1/entries/"+e.id+"/bridges", nil, reqOpts{token: e.key})
	if bl, _ := lst["bridges"].([]any); len(bl) != 1 || bl[0].(map[string]any)["undelivered"] != float64(2) {
		t.Fatalf("entry bridges = %v, want undelivered 2", lst)
	}
	if st, r, _ := ts.do(t, http.MethodDelete, "/v1/bridges/"+g.bridgeID, nil, reqOpts{token: e.key}); st != 200 || r["undelivered"] != float64(2) {
		t.Fatalf("line close = %d %v", st, r)
	}
	// The service side never fetches from a line it closed.
	if w := ts.entryInbox(t, e, ""); len(w.Envelopes) != 0 {
		t.Fatalf("service side sees %d envelopes on a line it closed", len(w.Envelopes))
	}
	st, w := ts.inboxStatus(t, g.token, g.bridgeID)
	if st != 200 || len(msgIDs(w)) != 2 || w.Closing == nil {
		t.Fatalf("guest inbox = %d %+v", st, w)
	}
}

func (ts *testServer) rowCount(t *testing.T, q string, args ...any) int {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+ts.dbPath+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var n int
	if err := db.QueryRowContext(context.Background(), q, args...).Scan(&n); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	return n
}

// encryptNoLookup builds a signed envelope from s to r without listing lands
// (which is refused once the bridge is closed).
func (ts *testServer) encryptNoLookup(t *testing.T, s lander, b createdBridge, r lander, msgID, text string) *protocol.Envelope {
	t.Helper()
	env, err := protocol.Encrypt(protocol.EncryptInput{
		BridgeID:       b.bridgeID,
		MsgID:          msgID,
		SenderLandID:   s.landID,
		SenderKeyID:    s.keyID,
		SenderSignPriv: s.k.signPriv,
		LandSetVersion: 2,
		CreatedAt:      "2026-10-29T07:02:00.000Z",
		Plaintext:      []byte(`{"type":"message","text":"` + text + `"}`),
		Recipients:     []protocol.RecipientKey{{LandID: r.landID, KeyID: r.keyID, EncPub: r.k.encPub}},
	})
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	return env
}

// RT-139: the unread count is per bridge and per sender: a key from another
// bridge, or no key, gets nothing.
func TestCloseGraceCountNotLeaked(t *testing.T) {
	ts := graceServer(t, 10*time.Minute)
	_, _, b := ts.pairWithMessages(t, 2)
	x, _, _ := ts.pairWithMessages(t, 0)
	if st, r, _ := ts.do(t, http.MethodGet, "/v1/bridges/"+b.bridgeID+"/undelivered", nil, reqOpts{token: x.token}); st == 200 {
		t.Fatalf("other bridge's key read the count: %d %v", st, r)
	}
	if st, _, _ := ts.do(t, http.MethodGet, "/v1/bridges/"+b.bridgeID+"/undelivered", nil, reqOpts{}); st != http.StatusUnauthorized {
		t.Fatalf("no key = %d, want 401", st)
	}
}
