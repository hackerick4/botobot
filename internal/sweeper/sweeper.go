// Package sweeper runs bot-bridge's periodic lifecycle maintenance (DEV_SPEC
// §3.2 "Sweeper goroutine", §5, §10, §11). On each pass it:
//
//	(a) deletes inbox items past their TTL, recording message deliveries as
//	    `expired` (DEV_SPEC §10);
//	(b) deletes messages whose refcount reached zero and their blob files
//	    (DEV_SPEC §3.2 b);
//	(c) deletes delivery_status rows older than 24h (DEV_SPEC §10 / D6);
//	(d) closes bridges whose admin has been inactive past the window, with
//	    reason admin_inactive (DEV_SPEC §11 condition 3);
//	(e) deletes closed-bridge tombstones older than 30 days (DEV_SPEC §5).
//
// It never retains content beyond the 24h hard TTL (DEV_SPEC §1.1 G4): steps
// (a)–(c) are the mechanism that enforces that. All time comes from an injected
// clock.Clock (via the store) so RunOnce is deterministic in tests without
// time.Sleep (CLAUDE.md rule 8); the loop cadence in Run uses a wall-clock
// ticker, which needs no faking because tests drive RunOnce directly.
package sweeper

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/hackerick4/botobot/internal/store"
)

// Fixed retention windows set by the spec (not configurable): delivery status is
// kept 24h (DEV_SPEC §10 / D6) and a closed bridge's tombstone 30 days (§5).
const (
	DeliveryStatusRetention = 24 * time.Hour
	TombstoneRetention      = 30 * 24 * time.Hour
)

// Options configures a Sweeper.
type Options struct {
	// Store is the persistence layer; its injected clock governs all timing.
	Store *store.Store
	// Logger receives one line per non-empty pass; nil uses slog.Default().
	Logger *slog.Logger
	// Interval is how often Run performs a pass (BB_SWEEP_INTERVAL); <=0 -> 60s.
	Interval time.Duration
	// AdminInactivity is the idle window after which a bridge is closed
	// (BB_ADMIN_INACTIVITY); <=0 -> 168h (7 days).
	AdminInactivity time.Duration
	// OnBridgeClosed, if set, is called after a bridge is closed by the
	// inactivity check, letting the server drop any cached key material whose
	// rows were just deleted (DEV_SPEC §11).
	OnBridgeClosed func(bridgeID string)
}

// Sweeper performs periodic lifecycle maintenance.
type Sweeper struct {
	opts Options
}

// New builds a Sweeper, applying defaults for unset options.
func New(opts Options) *Sweeper {
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	if opts.Interval <= 0 {
		opts.Interval = 60 * time.Second
	}
	if opts.AdminInactivity <= 0 {
		opts.AdminInactivity = 168 * time.Hour
	}
	return &Sweeper{opts: opts}
}

// Result reports what a single pass did (used by tests and logging).
type Result struct {
	GraceEnded     int      // last-delivery windows ended (step a0, v0.6.4)
	ExpiredInbox   int      // inbox items deleted at TTL (step a)
	OrphanMessages int      // refcount-0 messages deleted (step b)
	DeliveryStatus int      // delivery_status rows deleted (step c)
	ClosedInactive []string // bridges closed for admin inactivity (step d)
	Tombstones     int      // tombstones deleted at 30 days (step e)
	PendingDeleted int      // unclaimed /join bridges deleted at claim expiry (step f)
	// EntriesDisabled counts public entries disabled for inactivity (d2).
	EntriesDisabled int
}

// RunOnce performs one maintenance pass (DEV_SPEC §3.2 a–e). Steps run in order
// so that TTL expiry drops refcounts before the orphan-message sweep, and the
// inactivity close precedes the tombstone GC (a bridge just tombstoned is far
// newer than 30 days, so it is never GC'd in the same pass). A failure returns
// immediately with the partial Result; the caller logs and retries next tick.
func (sw *Sweeper) RunOnce(ctx context.Context) (Result, error) {
	var res Result
	var err error
	st := sw.opts.Store

	// (a0) end last-delivery windows of closed bridges that ran out (v0.6.4).
	if res.GraceEnded, err = st.FinalizeExpiredGrace(ctx); err != nil {
		return res, err
	}

	// (a) TTL: delete expired inbox items; message deliveries -> `expired`.
	exp, err := st.ExpireInbox(ctx)
	if err != nil {
		return res, err
	}
	res.ExpiredInbox = exp.Deleted

	// (b) delete refcount-0 messages + blob files (defensive backstop).
	if res.OrphanMessages, err = st.SweepOrphanMessages(ctx); err != nil {
		return res, err
	}

	// (c) delete delivery_status rows older than 24h.
	if res.DeliveryStatus, err = st.SweepDeliveryStatus(ctx, DeliveryStatusRetention); err != nil {
		return res, err
	}

	// (d) close bridges whose admin has been inactive past the window.
	ids, err := st.ListInactiveAdminBridges(ctx, sw.opts.AdminInactivity)
	if err != nil {
		return res, err
	}
	for _, id := range ids {
		if err := st.CloseBridge(ctx, id, store.CloseAdminInactive); err != nil {
			if errors.Is(err, store.ErrNotFound) {
				continue // closed or removed between listing and now
			}
			return res, err
		}
		res.ClosedInactive = append(res.ClosedInactive, id)
		if sw.opts.OnBridgeClosed != nil {
			sw.opts.OnBridgeClosed(id)
		}
	}

	// (d2) a public entry whose service bot went quiet stops taking new
	// guests (v0.6.0); its bridges were closed in (d) above.
	if eids, err := st.ListInactiveEntries(ctx, sw.opts.AdminInactivity); err == nil {
		for _, id := range eids {
			_ = st.DisableEntryInactive(ctx, id)
		}
		res.EntriesDisabled = len(eids)
	} else {
		return res, err
	}

	// (e) delete closed-bridge tombstones older than 30 days.
	if res.Tombstones, err = st.SweepTombstones(ctx, TombstoneRetention); err != nil {
		return res, err
	}

	// (f) delete unclaimed /join (pending) bridges past their claim expiry.
	if res.PendingDeleted, err = st.SweepExpiredPending(ctx); err != nil {
		return res, err
	}

	return res, nil
}

// Run performs a pass immediately, then every Interval until ctx is cancelled
// (DEV_SPEC §3.2, §18 graceful shutdown stops the sweeper). It returns when ctx
// is done; a pass already in progress is allowed to finish.
func (sw *Sweeper) Run(ctx context.Context) {
	sw.runLogged(ctx)
	t := time.NewTicker(sw.opts.Interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			sw.runLogged(ctx)
		}
	}
}

// runLogged runs one pass and logs its effect (nothing when the pass was a
// no-op, to keep idle logs quiet). Errors are logged at warn; they carry no
// secrets (DB/system errors only, DEV_SPEC §15).
func (sw *Sweeper) runLogged(ctx context.Context) {
	res, err := sw.RunOnce(ctx)
	if err != nil {
		if ctx.Err() != nil {
			return // shutting down
		}
		sw.opts.Logger.Warn("sweep pass failed", "err", err.Error())
		return
	}
	if res.ExpiredInbox == 0 && res.OrphanMessages == 0 && res.DeliveryStatus == 0 &&
		len(res.ClosedInactive) == 0 && res.Tombstones == 0 && res.PendingDeleted == 0 {
		return
	}
	sw.opts.Logger.Info("sweep pass",
		"expired_inbox", res.ExpiredInbox,
		"orphan_messages", res.OrphanMessages,
		"delivery_status", res.DeliveryStatus,
		"closed_inactive", len(res.ClosedInactive),
		"tombstones", res.Tombstones,
		"pending_deleted", res.PendingDeleted,
	)
}
