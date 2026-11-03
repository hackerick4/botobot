package sweeper

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/hackerick4/botobot/internal/clock"
	"github.com/hackerick4/botobot/internal/store"
)

var epoch = time.Date(2026, 10, 29, 7, 0, 0, 0, time.UTC)

// newStore opens a store on a temp dir with the given fake clock.
func newStore(t *testing.T, clk clock.Clock) *store.Store {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(context.Background(), store.Options{
		Path:    filepath.Join(dir, "bridge.db"),
		BlobDir: filepath.Join(dir, "blobs"),
		Clock:   clk,
		Flush:   time.Millisecond,
	})
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func makeBridge(t *testing.T, st *store.Store) string {
	t.Helper()
	b, err := st.CreateBridge(context.Background(), store.CreateBridgeParams{
		Name: "ACME x Dana", MaxLands: 32, LandsCanInvite: true,
		RateLimitPerMin: 30, QueueQuotaBytes: 1 << 30,
	})
	if err != nil {
		t.Fatalf("CreateBridge: %v", err)
	}
	return b.ID
}

// --- M5 T5.2 / §17.1: admin 7-day inactivity close, injected clock ---

func TestRunOnceClosesInactiveAdmin(t *testing.T) {
	ctx := context.Background()
	clk := clock.NewFake(epoch)
	st := newStore(t, clk)

	idle := makeBridge(t, st)
	busy := makeBridge(t, st)

	var closed []string
	sw := New(Options{
		Store:           st,
		AdminInactivity: 7 * 24 * time.Hour,
		OnBridgeClosed:  func(id string) { closed = append(closed, id) },
	})

	// Within the window nothing closes.
	clk.Advance(7 * 24 * time.Hour)
	clk.Advance(-time.Second) // epoch + 7d - 1s
	res, err := sw.RunOnce(ctx)
	if err != nil {
		t.Fatalf("RunOnce early: %v", err)
	}
	if len(res.ClosedInactive) != 0 {
		t.Fatalf("closed too early: %v", res.ClosedInactive)
	}

	// Cross the window, but keep `busy`'s admin active (as regular polling would).
	clk.Advance(2 * time.Second) // epoch + 7d + 1s
	if err := st.TouchAdminActive(ctx, busy, clk.Now().UnixMilli()); err != nil {
		t.Fatalf("TouchAdminActive: %v", err)
	}

	res, err = sw.RunOnce(ctx)
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if len(res.ClosedInactive) != 1 || res.ClosedInactive[0] != idle {
		t.Fatalf("ClosedInactive = %v, want [%s]", res.ClosedInactive, idle)
	}
	if len(closed) != 1 || closed[0] != idle {
		t.Fatalf("OnBridgeClosed callbacks = %v, want [%s]", closed, idle)
	}

	// The idle bridge is a closed tombstone; the busy one stays active.
	if b, err := st.GetBridge(ctx, idle); err != nil || b.Status != "closed" || b.CloseReason == nil || *b.CloseReason != store.CloseAdminInactive {
		t.Fatalf("idle bridge = %+v err = %v, want closed/admin_inactive", b, err)
	}
	if b, err := st.GetBridge(ctx, busy); err != nil || b.Status != "active" {
		t.Fatalf("busy bridge = %+v err = %v, want active", b, err)
	}
}

// --- M5 T5.1: tombstone GC runs after the inactivity close (DEV_SPEC §5) ---

func TestRunOnceGCsTombstonesAfter30Days(t *testing.T) {
	ctx := context.Background()
	clk := clock.NewFake(epoch)
	st := newStore(t, clk)

	id := makeBridge(t, st)
	if err := st.CloseBridge(ctx, id, store.CloseAdminClosed); err != nil {
		t.Fatalf("CloseBridge: %v", err)
	}

	sw := New(Options{Store: st, AdminInactivity: 7 * 24 * time.Hour})

	// One day later: still a tombstone (answers 410).
	clk.Advance(24 * time.Hour)
	if res, err := sw.RunOnce(ctx); err != nil || res.Tombstones != 0 {
		t.Fatalf("early pass tombstones = %d err = %v, want 0", res.Tombstones, err)
	}
	if _, err := st.GetBridge(ctx, id); err != nil {
		t.Fatalf("tombstone gone too early: %v", err)
	}

	// Past 30 days: GC'd.
	clk.Advance(30 * 24 * time.Hour)
	res, err := sw.RunOnce(ctx)
	if err != nil || res.Tombstones != 1 {
		t.Fatalf("GC pass tombstones = %d err = %v, want 1", res.Tombstones, err)
	}
	if _, err := st.GetBridge(ctx, id); err != store.ErrNotFound {
		t.Fatalf("GetBridge after GC = %v, want ErrNotFound", err)
	}
}

// --- §6 /join: unclaimed pending bridges deleted at claim expiry ---

func makePendingBridge(t *testing.T, st *store.Store, clk clock.Clock, claimTTL time.Duration) string {
	t.Helper()
	exp := clk.Now().Add(claimTTL).UnixMilli()
	b, err := st.CreateBridge(context.Background(), store.CreateBridgeParams{
		Name: "pending", MaxLands: 32, LandsCanInvite: true,
		RateLimitPerMin: 30, QueueQuotaBytes: 1 << 30,
		Pending: true, ClaimTokenHash: make([]byte, 32), ClaimExpiresAt: &exp,
	})
	if err != nil {
		t.Fatalf("CreateBridge pending: %v", err)
	}
	return b.ID
}

func TestRunOnceDeletesExpiredPending(t *testing.T) {
	ctx := context.Background()
	clk := clock.NewFake(epoch)
	st := newStore(t, clk)

	id := makePendingBridge(t, st, clk, time.Hour)
	sw := New(Options{Store: st, AdminInactivity: 7 * 24 * time.Hour})

	// Before expiry: the pending bridge survives and is not closed for inactivity.
	clk.Advance(30 * time.Minute)
	res, err := sw.RunOnce(ctx)
	if err != nil || res.PendingDeleted != 0 {
		t.Fatalf("early pass pending_deleted = %d err = %v, want 0", res.PendingDeleted, err)
	}
	if len(res.ClosedInactive) != 0 {
		t.Fatalf("pending bridge must not be closed for admin inactivity: %v", res.ClosedInactive)
	}
	if _, err := st.GetBridge(ctx, id); err != nil {
		t.Fatalf("pending bridge gone too early: %v", err)
	}

	// Past the claim expiry: deleted entirely (no tombstone).
	clk.Advance(time.Hour)
	res, err = sw.RunOnce(ctx)
	if err != nil || res.PendingDeleted != 1 {
		t.Fatalf("expiry pass pending_deleted = %d err = %v, want 1", res.PendingDeleted, err)
	}
	if _, err := st.GetBridge(ctx, id); err != store.ErrNotFound {
		t.Fatalf("GetBridge after pending sweep = %v, want ErrNotFound", err)
	}
}
