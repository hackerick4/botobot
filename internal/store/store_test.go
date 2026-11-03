package store

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/hackerick4/botobot/internal/clock"
)

// epoch is a fixed instant used by the fake clock so every test is deterministic.
var epoch = time.Date(2026, 10, 29, 7, 0, 0, 0, time.UTC)

type env struct {
	s   *Store
	clk *clock.Fake
	dir string
}

// setup opens a store in a fresh temp dir with a fake clock. The caller may
// override Options fields via mutate.
func setup(t *testing.T, mutate func(*Options)) *env {
	t.Helper()
	dir := t.TempDir()
	clk := clock.NewFake(epoch)
	opts := Options{
		Path:    filepath.Join(dir, "bridge.db"),
		BlobDir: filepath.Join(dir, "blobs"),
		Clock:   clk,
		Flush:   time.Millisecond, // keep batches snappy in tests
	}
	if mutate != nil {
		mutate(&opts)
	}
	s, err := Open(context.Background(), opts)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return &env{s: s, clk: clk, dir: dir}
}

// reopen closes the current store and opens a new one on the same files, used to
// prove durability across restarts.
func (e *env) reopen(t *testing.T) {
	t.Helper()
	if err := e.s.Close(); err != nil {
		t.Fatalf("close before reopen: %v", err)
	}
	s, err := Open(context.Background(), Options{
		Path:    filepath.Join(e.dir, "bridge.db"),
		BlobDir: filepath.Join(e.dir, "blobs"),
		Clock:   e.clk,
		Flush:   time.Millisecond,
	})
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	e.s = s
	t.Cleanup(func() { _ = s.Close() })
}

// seedBridge creates a bridge (with the given byte quota) plus n member lands,
// returning the bridge id and the land ids.
func seedBridge(t *testing.T, s *Store, quota int64, n int) (string, []string) {
	t.Helper()
	ctx := context.Background()
	b, err := s.CreateBridge(ctx, CreateBridgeParams{
		Name:            "ACME x Dana",
		MaxLands:        32,
		LandsCanInvite:  true,
		RateLimitPerMin: 30,
		QueueQuotaBytes: quota,
	})
	if err != nil {
		t.Fatalf("CreateBridge: %v", err)
	}
	var lands []string
	for i := 0; i < n; i++ {
		role := "member"
		if i == 0 {
			role = "admin"
		}
		landID, _, err := s.CreateLand(ctx, CreateLandParams{
			BridgeID:    b.ID,
			Role:        role,
			Handle:      handleFor(i),
			EncPub:      bytes.Repeat([]byte{byte(i + 1)}, 32),
			SignPub:     bytes.Repeat([]byte{byte(i + 100)}, 32),
			BindingSig:  bytes.Repeat([]byte{byte(i + 1)}, 64),
			Fingerprint: "00000 00000 00000 00000 00000 00000",
		})
		if err != nil {
			t.Fatalf("CreateLand %d: %v", i, err)
		}
		lands = append(lands, landID)
	}
	return b.ID, lands
}

func handleFor(i int) string {
	return "bot-" + string(rune('a'+i))
}

// enqueueTo is a small helper to enqueue a message of the given ciphertext size
// to the named recipients.
func enqueueTo(t *testing.T, s *Store, bridgeID, sender, msgID string, cipher []byte, size int64, recips []string) *EnqueueResult {
	t.Helper()
	wraps := make([]RecipientWrap, len(recips))
	for i, r := range recips {
		wraps[i] = RecipientWrap{LandID: r, WrappedKey: bytes.Repeat([]byte{byte(i + 1)}, 80)}
	}
	res, err := s.EnqueueMessage(context.Background(), EnqueueMessageParams{
		BridgeID:     bridgeID,
		SenderLandID: sender,
		MsgID:        msgID,
		HeaderJSON:   []byte(`{"v":"bb-e2e-v1","msg_id":"` + msgID + `"}`),
		Ciphertext:   cipher,
		SizeBytes:    size,
		Recipients:   wraps,
	})
	if err != nil {
		t.Fatalf("EnqueueMessage %q: %v", msgID, err)
	}
	return res
}

func countRows(t *testing.T, s *Store, query string, args ...any) int {
	t.Helper()
	var n int
	if err := s.read.QueryRowContext(context.Background(), query, args...).Scan(&n); err != nil {
		t.Fatalf("count query %q: %v", query, err)
	}
	return n
}

// --- T2.1: migrations ---

func TestMigrationsAppliedOnce(t *testing.T) {
	e := setup(t, nil)
	// All schema tables must exist.
	for _, tbl := range []string{"bridges", "lands", "public_keys", "api_keys", "messages", "inbox_queue", "delivery_status", "schema_migrations"} {
		if countRows(t, e.s, "SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?", tbl) != 1 {
			t.Errorf("table %q missing after migrate", tbl)
		}
	}
	migs, err := loadMigrations()
	if err != nil {
		t.Fatalf("loadMigrations: %v", err)
	}
	want := len(migs)
	if v := countRows(t, e.s, "SELECT COUNT(*) FROM schema_migrations"); v != want {
		t.Errorf("schema_migrations rows = %d, want %d", v, want)
	}
	// Reopening must not re-apply or error.
	e.reopen(t)
	if v := countRows(t, e.s, "SELECT COUNT(*) FROM schema_migrations"); v != want {
		t.Errorf("after reopen schema_migrations rows = %d, want %d", v, want)
	}
}

func TestWALModeAndForeignKeys(t *testing.T) {
	e := setup(t, nil)
	var mode string
	if err := e.s.read.QueryRow("PRAGMA journal_mode").Scan(&mode); err != nil {
		t.Fatalf("pragma journal_mode: %v", err)
	}
	if mode != "wal" {
		t.Errorf("journal_mode = %q, want wal", mode)
	}
	var fk int
	if err := e.s.read.QueryRow("PRAGMA foreign_keys").Scan(&fk); err != nil {
		t.Fatalf("pragma foreign_keys: %v", err)
	}
	if fk != 1 {
		t.Errorf("foreign_keys = %d, want 1", fk)
	}
}

// --- T2.3 / T2.4: enqueue -> lease -> ack -> delete ---

func TestEnqueueLeaseAckDelete(t *testing.T) {
	ctx := context.Background()
	e := setup(t, nil)
	bridgeID, lands := seedBridge(t, e.s, 1<<30, 3) // admin + 2 members
	sender := lands[0]
	recips := []string{lands[1], lands[2]}

	res := enqueueTo(t, e.s, bridgeID, sender, "c-0001", []byte("ciphertext-and-tag"), 500, recips)
	if !res.Created || len(res.Deliveries) != 2 {
		t.Fatalf("enqueue result = %+v", res)
	}
	if qb, _ := e.s.GetQueueBytes(ctx, bridgeID); qb != 500 {
		t.Errorf("queue_bytes after enqueue = %d, want 500", qb)
	}
	if rc := countRows(t, e.s, "SELECT refcount FROM messages WHERE id='c-0001'"); rc != 2 {
		t.Errorf("refcount = %d, want 2", rc)
	}

	// Recipient 1 fetches and acks.
	items, err := e.s.FetchInbox(ctx, bridgeID, recips[0], 50)
	if err != nil || len(items) != 1 {
		t.Fatalf("FetchInbox r0: items=%d err=%v", len(items), err)
	}
	if items[0].Kind != "message" || !bytes.Equal(items[0].Ciphertext, []byte("ciphertext-and-tag")) {
		t.Errorf("fetched item wrong: %+v", items[0])
	}
	if items[0].FetchCount != 1 {
		t.Errorf("fetch_count = %d, want 1", items[0].FetchCount)
	}
	ack, err := e.s.Ack(ctx, bridgeID, recips[0], []string{items[0].ID})
	if err != nil || ack.Deleted != 1 {
		t.Fatalf("Ack r0: %+v err=%v", ack, err)
	}
	// Message still present (refcount 1), quota unchanged.
	if rc := countRows(t, e.s, "SELECT refcount FROM messages WHERE id='c-0001'"); rc != 1 {
		t.Errorf("refcount after one ack = %d, want 1", rc)
	}
	if qb, _ := e.s.GetQueueBytes(ctx, bridgeID); qb != 500 {
		t.Errorf("queue_bytes after one ack = %d, want 500 (shared ciphertext)", qb)
	}
	// delivery_status for recipient 0 is delivered.
	deliveries, _ := e.s.GetDeliveries(ctx, bridgeID, sender, "c-0001")
	if statusOf(deliveries, recips[0]) != "delivered" {
		t.Errorf("r0 delivery status = %q, want delivered", statusOf(deliveries, recips[0]))
	}

	// Recipient 2 fetches and acks -> message and ciphertext deleted, quota freed.
	items2, _ := e.s.FetchInbox(ctx, bridgeID, recips[1], 50)
	if len(items2) != 1 {
		t.Fatalf("FetchInbox r1 items=%d", len(items2))
	}
	if _, err := e.s.Ack(ctx, bridgeID, recips[1], []string{items2[0].ID}); err != nil {
		t.Fatalf("Ack r1: %v", err)
	}
	if n := countRows(t, e.s, "SELECT COUNT(*) FROM messages WHERE id='c-0001'"); n != 0 {
		t.Errorf("message rows after all acks = %d, want 0", n)
	}
	if n := countRows(t, e.s, "SELECT COUNT(*) FROM inbox_queue"); n != 0 {
		t.Errorf("inbox rows after all acks = %d, want 0", n)
	}
	if qb, _ := e.s.GetQueueBytes(ctx, bridgeID); qb != 0 {
		t.Errorf("queue_bytes after all acks = %d, want 0", qb)
	}
}

func statusOf(ds []Delivery, land string) string {
	for _, d := range ds {
		if d.RecipientLandID == land {
			return d.Status
		}
	}
	return ""
}

// Ack of an unknown / already-deleted id is not an error (DEV_SPEC §6.4).
func TestAckUnknownIsIdempotent(t *testing.T) {
	ctx := context.Background()
	e := setup(t, nil)
	bridgeID, lands := seedBridge(t, e.s, 1<<30, 2)
	enqueueTo(t, e.s, bridgeID, lands[0], "c-1", []byte("x"), 100, []string{lands[1]})
	items, _ := e.s.FetchInbox(ctx, bridgeID, lands[1], 50)
	ack, err := e.s.Ack(ctx, bridgeID, lands[1], []string{items[0].ID, "env_doesnotexist000000000000"})
	if err != nil {
		t.Fatalf("Ack: %v", err)
	}
	if ack.Deleted != 1 || len(ack.Unknown) != 1 {
		t.Errorf("ack = %+v, want deleted=1 unknown=1", ack)
	}
}

// --- lease expiry re-delivery (fake clock) ---

func TestLeaseHidesThenReappears(t *testing.T) {
	ctx := context.Background()
	e := setup(t, func(o *Options) { o.Lease = 5 * time.Minute })
	bridgeID, lands := seedBridge(t, e.s, 1<<30, 2)
	enqueueTo(t, e.s, bridgeID, lands[0], "c-1", []byte("x"), 100, []string{lands[1]})

	first, _ := e.s.FetchInbox(ctx, bridgeID, lands[1], 50)
	if len(first) != 1 {
		t.Fatalf("first fetch = %d items, want 1", len(first))
	}
	// Immediately re-fetch: leased, so nothing.
	again, _ := e.s.FetchInbox(ctx, bridgeID, lands[1], 50)
	if len(again) != 0 {
		t.Fatalf("leased re-fetch = %d items, want 0", len(again))
	}
	// Advance past the lease: it reappears with an incremented fetch_count.
	e.clk.Advance(5*time.Minute + time.Second)
	third, _ := e.s.FetchInbox(ctx, bridgeID, lands[1], 50)
	if len(third) != 1 {
		t.Fatalf("post-lease fetch = %d items, want 1", len(third))
	}
	if third[0].FetchCount != 2 {
		t.Errorf("fetch_count = %d, want 2", third[0].FetchCount)
	}
}

// --- TTL expiry ---

func TestTTLExpiry(t *testing.T) {
	ctx := context.Background()
	e := setup(t, func(o *Options) { o.TTL = 24 * time.Hour })
	bridgeID, lands := seedBridge(t, e.s, 1<<30, 2)
	enqueueTo(t, e.s, bridgeID, lands[0], "c-1", []byte("x"), 400, []string{lands[1]})

	// Before TTL: nothing expires.
	if r, err := e.s.ExpireInbox(ctx); err != nil || r.Deleted != 0 {
		t.Fatalf("early ExpireInbox = %+v err=%v", r, err)
	}
	// After TTL: the item expires, quota frees, status becomes expired.
	e.clk.Advance(24*time.Hour + time.Minute)
	r, err := e.s.ExpireInbox(ctx)
	if err != nil || r.Deleted != 1 {
		t.Fatalf("ExpireInbox = %+v err=%v", r, err)
	}
	if n := countRows(t, e.s, "SELECT COUNT(*) FROM inbox_queue"); n != 0 {
		t.Errorf("inbox rows after expiry = %d, want 0", n)
	}
	if n := countRows(t, e.s, "SELECT COUNT(*) FROM messages"); n != 0 {
		t.Errorf("message rows after expiry = %d, want 0", n)
	}
	if qb, _ := e.s.GetQueueBytes(ctx, bridgeID); qb != 0 {
		t.Errorf("queue_bytes after expiry = %d, want 0", qb)
	}
	ds, _ := e.s.GetDeliveries(ctx, bridgeID, lands[0], "c-1")
	if statusOf(ds, lands[1]) != "expired" {
		t.Errorf("delivery status = %q, want expired", statusOf(ds, lands[1]))
	}
	// A fetch after expiry returns nothing.
	items, _ := e.s.FetchInbox(ctx, bridgeID, lands[1], 50)
	if len(items) != 0 {
		t.Errorf("fetch after expiry = %d items, want 0", len(items))
	}
}

// --- quota increments / decrements ---

func TestQuotaEnforcedAndReclaimed(t *testing.T) {
	ctx := context.Background()
	e := setup(t, nil)
	bridgeID, lands := seedBridge(t, e.s, 1000, 2) // 1000-byte quota
	sender, recip := lands[0], []string{lands[1]}

	enqueueTo(t, e.s, bridgeID, sender, "c-1", bytes.Repeat([]byte("a"), 10), 600, recip)
	if qb, _ := e.s.GetQueueBytes(ctx, bridgeID); qb != 600 {
		t.Fatalf("queue_bytes = %d, want 600", qb)
	}
	// Second 600-byte message would exceed the 1000 quota.
	_, err := e.s.EnqueueMessage(ctx, EnqueueMessageParams{
		BridgeID: bridgeID, SenderLandID: sender, MsgID: "c-2",
		HeaderJSON: []byte(`{"msg_id":"c-2"}`), Ciphertext: []byte("y"), SizeBytes: 600,
		Recipients: []RecipientWrap{{LandID: recip[0], WrappedKey: bytes.Repeat([]byte{1}, 80)}},
	})
	if err != ErrQuotaExceeded {
		t.Fatalf("second enqueue err = %v, want ErrQuotaExceeded", err)
	}
	// Ack the first (single recipient -> refcount 0 -> freed).
	items, _ := e.s.FetchInbox(ctx, bridgeID, recip[0], 50)
	if _, err := e.s.Ack(ctx, bridgeID, recip[0], []string{items[0].ID}); err != nil {
		t.Fatalf("Ack: %v", err)
	}
	if qb, _ := e.s.GetQueueBytes(ctx, bridgeID); qb != 0 {
		t.Fatalf("queue_bytes after ack = %d, want 0", qb)
	}
	// Now the second fits.
	enqueueTo(t, e.s, bridgeID, sender, "c-2", []byte("y"), 600, recip)
	if qb, _ := e.s.GetQueueBytes(ctx, bridgeID); qb != 600 {
		t.Errorf("queue_bytes after re-enqueue = %d, want 600", qb)
	}
}

// --- idempotency ---

func TestEnqueueIdempotentAndConflict(t *testing.T) {
	ctx := context.Background()
	e := setup(t, nil)
	bridgeID, lands := seedBridge(t, e.s, 1<<30, 2)
	sender, recip := lands[0], []string{lands[1]}

	first := enqueueTo(t, e.s, bridgeID, sender, "c-1", []byte("same"), 300, recip)
	// Identical resend -> idempotent (Created=false), no extra quota/rows.
	second := enqueueTo(t, e.s, bridgeID, sender, "c-1", []byte("same"), 300, recip)
	if second.Created {
		t.Errorf("identical resend Created = true, want false")
	}
	if first.ExpiresAt != second.ExpiresAt {
		t.Errorf("idempotent expires_at differs: %d vs %d", first.ExpiresAt, second.ExpiresAt)
	}
	if qb, _ := e.s.GetQueueBytes(ctx, bridgeID); qb != 300 {
		t.Errorf("queue_bytes after idempotent resend = %d, want 300", qb)
	}
	if n := countRows(t, e.s, "SELECT COUNT(*) FROM inbox_queue"); n != 1 {
		t.Errorf("inbox rows after idempotent resend = %d, want 1", n)
	}
	// Conflicting content under the same msg_id -> error.
	_, err := e.s.EnqueueMessage(ctx, EnqueueMessageParams{
		BridgeID: bridgeID, SenderLandID: sender, MsgID: "c-1",
		HeaderJSON: []byte(`{"msg_id":"c-1","x":1}`), Ciphertext: []byte("different"), SizeBytes: 999,
		Recipients: []RecipientWrap{{LandID: recip[0], WrappedKey: bytes.Repeat([]byte{1}, 80)}},
	})
	if err != ErrDuplicateMsgID {
		t.Errorf("conflicting resend err = %v, want ErrDuplicateMsgID", err)
	}
}

// --- events ---

func TestEnqueueEventQuotaAndFetch(t *testing.T) {
	ctx := context.Background()
	e := setup(t, nil)
	bridgeID, lands := seedBridge(t, e.s, 1<<30, 2)
	ev := []byte(`{"type":"land_joined","land_set_version":2}`)
	ids, err := e.s.EnqueueEvent(ctx, bridgeID, []string{lands[0], lands[1]}, ev)
	if err != nil || len(ids) != 2 {
		t.Fatalf("EnqueueEvent ids=%v err=%v", ids, err)
	}
	if qb, _ := e.s.GetQueueBytes(ctx, bridgeID); qb != int64(2*len(ev)) {
		t.Errorf("queue_bytes = %d, want %d", qb, 2*len(ev))
	}
	items, _ := e.s.FetchInbox(ctx, bridgeID, lands[0], 50)
	if len(items) != 1 || items[0].Kind != "event" || !bytes.Equal(items[0].EventJSON, ev) {
		t.Fatalf("fetched event wrong: %+v", items)
	}
	// Ack frees the event bytes.
	if _, err := e.s.Ack(ctx, bridgeID, lands[0], []string{items[0].ID}); err != nil {
		t.Fatalf("Ack event: %v", err)
	}
	if qb, _ := e.s.GetQueueBytes(ctx, bridgeID); qb != int64(len(ev)) {
		t.Errorf("queue_bytes after one event ack = %d, want %d", qb, len(ev))
	}
}

// --- persistence across close/reopen ---

func TestPersistsAcrossReopen(t *testing.T) {
	ctx := context.Background()
	e := setup(t, nil)
	bridgeID, lands := seedBridge(t, e.s, 1<<30, 2)
	enqueueTo(t, e.s, bridgeID, lands[0], "c-1", []byte("persist-me"), 777, []string{lands[1]})

	e.reopen(t)

	if _, err := e.s.GetBridge(ctx, bridgeID); err != nil {
		t.Fatalf("GetBridge after reopen: %v", err)
	}
	if qb, _ := e.s.GetQueueBytes(ctx, bridgeID); qb != 777 {
		t.Errorf("queue_bytes after reopen = %d, want 777", qb)
	}
	items, err := e.s.FetchInbox(ctx, bridgeID, lands[1], 50)
	if err != nil || len(items) != 1 {
		t.Fatalf("FetchInbox after reopen: items=%d err=%v", len(items), err)
	}
	if !bytes.Equal(items[0].Ciphertext, []byte("persist-me")) {
		t.Errorf("ciphertext after reopen = %q", items[0].Ciphertext)
	}
}

// --- blob spill for large envelopes ---

func TestLargeCiphertextSpillsToBlob(t *testing.T) {
	ctx := context.Background()
	e := setup(t, nil)
	bridgeID, lands := seedBridge(t, e.s, 1<<30, 2)
	big := bytes.Repeat([]byte("Z"), blobThreshold+1024)

	enqueueTo(t, e.s, bridgeID, lands[0], "c-big", big, int64(len(big)), []string{lands[1]})

	// The DB stores a blob_path and NULL ciphertext.
	var blobPath string
	var cipherNull bool
	row := e.s.read.QueryRowContext(ctx, "SELECT blob_path, ciphertext IS NULL FROM messages WHERE id='c-big'")
	if err := row.Scan(&blobPath, &cipherNull); err != nil {
		t.Fatalf("scan message: %v", err)
	}
	if blobPath == "" || !cipherNull {
		t.Fatalf("expected blob spill: blob_path=%q ciphertext_null=%v", blobPath, cipherNull)
	}
	if _, err := os.Stat(blobPath); err != nil {
		t.Fatalf("blob file missing: %v", err)
	}
	// Fetch reconstitutes the full ciphertext from the blob.
	items, _ := e.s.FetchInbox(ctx, bridgeID, lands[1], 50)
	if len(items) != 1 || !bytes.Equal(items[0].Ciphertext, big) {
		t.Fatalf("blob-backed fetch mismatch (len=%d)", len(items))
	}
	// Full ack removes the blob file.
	if _, err := e.s.Ack(ctx, bridgeID, lands[1], []string{items[0].ID}); err != nil {
		t.Fatalf("Ack: %v", err)
	}
	if _, err := os.Stat(blobPath); !os.IsNotExist(err) {
		t.Errorf("blob file still present after refcount 0: err=%v", err)
	}
}
