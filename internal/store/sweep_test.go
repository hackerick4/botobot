package store

import (
	"bytes"
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// --- M5 T5.1: delivery_status 24h retention (DEV_SPEC §10 / D6) ---

func TestSweepDeliveryStatus(t *testing.T) {
	ctx := context.Background()
	e := setup(t, nil)
	bridgeID, lands := seedBridge(t, e.s, 1<<30, 3)
	enqueueTo(t, e.s, bridgeID, lands[0], "c-0001", []byte("ciphertext-and-tag"), 500, []string{lands[1], lands[2]})

	// Two queued delivery rows, both stamped at epoch.
	if n := countRows(t, e.s, "SELECT COUNT(*) FROM delivery_status"); n != 2 {
		t.Fatalf("delivery_status rows = %d, want 2", n)
	}

	// Just before 24h: nothing is old enough yet.
	e.clk.Advance(24*time.Hour - time.Second)
	if n, err := e.s.SweepDeliveryStatus(ctx, 24*time.Hour); err != nil || n != 0 {
		t.Fatalf("early sweep deleted = %d err = %v, want 0", n, err)
	}
	if n := countRows(t, e.s, "SELECT COUNT(*) FROM delivery_status"); n != 2 {
		t.Fatalf("delivery_status rows after early sweep = %d, want 2", n)
	}

	// Past 24h: both rows are collected.
	e.clk.Advance(2 * time.Second)
	if n, err := e.s.SweepDeliveryStatus(ctx, 24*time.Hour); err != nil || n != 2 {
		t.Fatalf("sweep deleted = %d err = %v, want 2", n, err)
	}
	if n := countRows(t, e.s, "SELECT COUNT(*) FROM delivery_status"); n != 0 {
		t.Fatalf("delivery_status rows after sweep = %d, want 0", n)
	}
}

// --- M5 T5.1: refcount-0 message + blob cleanup (DEV_SPEC §3.2 b) ---

func TestSweepOrphanMessages(t *testing.T) {
	ctx := context.Background()
	e := setup(t, nil)
	bridgeID, lands := seedBridge(t, e.s, 1<<30, 2)
	enqueueTo(t, e.s, bridgeID, lands[0], "c-0001", []byte("ciphertext-and-tag"), 500, []string{lands[1]})

	if qb, _ := e.s.GetQueueBytes(ctx, bridgeID); qb != 500 {
		t.Fatalf("queue_bytes = %d, want 500", qb)
	}

	// Force the anomaly the backstop exists for: a message left at refcount 0
	// without being deleted inline.
	if _, err := e.s.writer.Submit(ctx, func(tx *sql.Tx) (any, error) {
		_, err := tx.ExecContext(ctx, "UPDATE messages SET refcount = 0 WHERE id = 'c-0001'")
		return nil, err
	}); err != nil {
		t.Fatalf("force refcount 0: %v", err)
	}

	n, err := e.s.SweepOrphanMessages(ctx)
	if err != nil || n != 1 {
		t.Fatalf("SweepOrphanMessages = %d err = %v, want 1", n, err)
	}
	if m := countRows(t, e.s, "SELECT COUNT(*) FROM messages WHERE id = 'c-0001'"); m != 0 {
		t.Fatalf("orphan message still present (%d rows)", m)
	}
	if qb, _ := e.s.GetQueueBytes(ctx, bridgeID); qb != 0 {
		t.Fatalf("queue_bytes after orphan sweep = %d, want 0 (reclaimed)", qb)
	}
}

// --- M5 T5.1: 30-day tombstone GC (DEV_SPEC §5) ---

func TestSweepTombstones(t *testing.T) {
	ctx := context.Background()
	e := setup(t, nil)
	bridgeID, _ := seedBridge(t, e.s, 1<<30, 1)

	if err := e.s.CloseBridge(ctx, bridgeID, CloseAdminClosed); err != nil {
		t.Fatalf("CloseBridge: %v", err)
	}
	// Tombstone exists and answers as closed immediately after close.
	if b, err := e.s.GetBridge(ctx, bridgeID); err != nil || b.Status != "closed" {
		t.Fatalf("after close GetBridge status = %v err = %v", b, err)
	}

	// Before 30 days: the tombstone is retained (so other lands still get 410).
	e.clk.Advance(30*24*time.Hour - time.Second)
	if n, err := e.s.SweepTombstones(ctx, 30*24*time.Hour); err != nil || n != 0 {
		t.Fatalf("early tombstone sweep = %d err = %v, want 0", n, err)
	}
	if _, err := e.s.GetBridge(ctx, bridgeID); err != nil {
		t.Fatalf("tombstone removed too early: %v", err)
	}

	// Past 30 days: the tombstone row is deleted.
	e.clk.Advance(2 * time.Second)
	if n, err := e.s.SweepTombstones(ctx, 30*24*time.Hour); err != nil || n != 1 {
		t.Fatalf("tombstone sweep = %d err = %v, want 1", n, err)
	}
	if _, err := e.s.GetBridge(ctx, bridgeID); err != ErrNotFound {
		t.Fatalf("GetBridge after GC = %v, want ErrNotFound", err)
	}
}

// --- M5 T5.2: list bridges whose admin is inactive (DEV_SPEC §11 cond. 3) ---

func TestListInactiveAdminBridges(t *testing.T) {
	ctx := context.Background()
	e := setup(t, nil)
	idle, _ := seedBridge(t, e.s, 1<<30, 1)
	active, _ := seedBridge(t, e.s, 1<<30, 1)

	// Move the clock just past the 7-day window, then mark `active`'s admin busy.
	e.clk.Advance(7*24*time.Hour + time.Second)
	if err := e.s.TouchAdminActive(ctx, active, e.s.nowMillis()); err != nil {
		t.Fatalf("TouchAdminActive: %v", err)
	}

	ids, err := e.s.ListInactiveAdminBridges(ctx, 7*24*time.Hour)
	if err != nil {
		t.Fatalf("ListInactiveAdminBridges: %v", err)
	}
	if len(ids) != 1 || ids[0] != idle {
		t.Fatalf("inactive = %v, want [%s] (active bridge kept)", ids, idle)
	}
}

// --- M5 acceptance: after close, no bridge content remains and the blob dir
// for the bridge is gone (DEV_SPEC §5, §11, §16) ---

func TestCloseRemovesAllContentAndBlobDir(t *testing.T) {
	ctx := context.Background()
	e := setup(t, nil)
	bridgeID, lands := seedBridge(t, e.s, 1<<30, 2)

	// A large message spills to a blob file so we can prove the dir is removed.
	big := bytes.Repeat([]byte("Z"), blobThreshold+1024)
	enqueueTo(t, e.s, bridgeID, lands[0], "c-big", big, int64(len(big)), []string{lands[1]})

	blobBridgeDir := filepath.Join(e.dir, "blobs", bridgeID)
	if entries, err := os.ReadDir(blobBridgeDir); err != nil || len(entries) == 0 {
		t.Fatalf("expected a blob file under %s (err=%v)", blobBridgeDir, err)
	}

	if err := e.s.CloseBridge(ctx, bridgeID, CloseAdminClosed); err != nil {
		t.Fatalf("CloseBridge: %v", err)
	}

	// No content rows for this bridge survive the close.
	for _, tbl := range []string{"lands", "messages", "inbox_queue", "delivery_status"} {
		if n := countRows(t, e.s, "SELECT COUNT(*) FROM "+tbl+" WHERE bridge_id = ?", bridgeID); n != 0 {
			t.Errorf("%s still has %d rows for bridge after close", tbl, n)
		}
	}
	// public_keys / api_keys cascade from lands (ON DELETE CASCADE).
	if n := countRows(t, e.s, "SELECT COUNT(*) FROM public_keys"); n != 0 {
		t.Errorf("public_keys still has %d rows after close", n)
	}
	if n := countRows(t, e.s, "SELECT COUNT(*) FROM api_keys"); n != 0 {
		t.Errorf("api_keys still has %d rows after close", n)
	}

	// The bridge row remains only as a content-free tombstone.
	b, err := e.s.GetBridge(ctx, bridgeID)
	if err != nil {
		t.Fatalf("GetBridge after close: %v", err)
	}
	if b.Status != "closed" || b.Name != "" || b.QueueBytes != 0 || b.InviteCodeEnc != nil {
		t.Fatalf("tombstone not blanked: %+v", b)
	}

	// The bridge's blob directory is gone.
	if _, err := os.Stat(blobBridgeDir); !os.IsNotExist(err) {
		t.Fatalf("blob dir still present after close: err=%v", err)
	}
}
