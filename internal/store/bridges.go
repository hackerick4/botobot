package store

import (
	"context"
	"database/sql"
	"errors"
)

// CreateBridgeParams carries the fields a caller sets when creating a bridge.
// The server fills the rest (status=active, land_set_version=1, counters=0) and
// generates the id. admin_land_id is set separately once the creator land row
// exists (DEV_SPEC §5).
type CreateBridgeParams struct {
	Name            string
	Description     string
	InviteCodeEnc   []byte
	MaxLands        int
	LandsCanInvite  bool
	RateLimitPerMin int
	QueueQuotaBytes int64

	// Pending/ClaimTokenHash/ClaimExpiresAt mark a /join-created bridge that
	// awaits its first admin claim (DEV_SPEC §6 /join). A normal create leaves
	// them zero: pending=0, no claim token (NULL), no claim expiry.
	// PendingPrefix is HMAC(pepper, prefix) hex for the per-prefix pending cap
	// (Batch 1 item 6); empty for non-/join bridges.
	Pending        bool
	ClaimTokenHash []byte
	ClaimExpiresAt *int64
	PendingPrefix  string

	// Pair creates a two-bot bridge (v0.5.0).
	Pair bool
}

// CreateBridge inserts a new active bridge and returns it. created_at,
// admin_last_active_at, and invite_code_rotated_at are set to now.
func (s *Store) CreateBridge(ctx context.Context, p CreateBridgeParams) (*Bridge, error) {
	now := s.nowMillis()
	b := &Bridge{
		ID:                  newID(prefixBridge),
		Name:                p.Name,
		Description:         p.Description,
		Status:              "active",
		InviteCodeEnc:       p.InviteCodeEnc,
		InviteCodeRotatedAt: &now,
		MaxLands:            p.MaxLands,
		LandSetVersion:      1,
		LandsCanInvite:      p.LandsCanInvite,
		RateLimitPerMin:     p.RateLimitPerMin,
		QueueQuotaBytes:     p.QueueQuotaBytes,
		QueueBytes:          0,
		AdminLastActiveAt:   now,
		CreatedAt:           now,
		Pending:             p.Pending,
		ClaimTokenHash:      p.ClaimTokenHash,
		ClaimExpiresAt:      p.ClaimExpiresAt,
		Pair:                p.Pair,
	}
	var pendingPrefix any
	if p.PendingPrefix != "" {
		pendingPrefix = p.PendingPrefix
	}
	_, err := s.writer.Submit(ctx, func(tx *sql.Tx) (any, error) {
		_, err := tx.ExecContext(ctx, `
INSERT INTO bridges (id, name, description, status, invite_code_enc, invite_code_rotated_at,
  max_lands, land_set_version, lands_can_invite, join_locked, rate_limit_per_min,
  queue_quota_bytes, queue_bytes, admin_last_active_at, created_at,
  pending, claim_token_hash, claim_expires_at, pending_prefix, pair)
VALUES (?, ?, ?, 'active', ?, ?, ?, 1, ?, 0, ?, ?, 0, ?, ?, ?, ?, ?, ?, ?)`,
			b.ID, b.Name, b.Description, b.InviteCodeEnc, now,
			b.MaxLands, boolToInt(b.LandsCanInvite), b.RateLimitPerMin,
			b.QueueQuotaBytes, now, now,
			boolToInt(b.Pending), b.ClaimTokenHash, b.ClaimExpiresAt, pendingPrefix, boolToInt(b.Pair))
		return nil, err
	})
	if err != nil {
		return nil, err
	}
	return b, nil
}

// bridgeColumns is the canonical select list for scanBridge.
const bridgeColumns = `id, name, description, status, admin_land_id, invite_code_enc,
  invite_code_rotated_at, invite_code_used, invite_code_expires_at,
  max_lands, land_set_version, lands_can_invite, join_locked,
  join_locked_reason, rate_limit_per_min, queue_quota_bytes, queue_bytes,
  admin_last_active_at, admin_joins_acked_through, created_at, closed_at, COALESCE(close_reason, close_detail),
  pending, claim_token_hash, claim_expires_at, pair, entry_id,
  (SELECT e.closed_at FROM entries e WHERE e.id = bridges.entry_id AND e.status = 'closed'),
  grace_until, grace_closer`

// GetBridge returns the bridge by id, or ErrNotFound.
func (s *Store) GetBridge(ctx context.Context, id string) (*Bridge, error) {
	row := s.read.QueryRowContext(ctx, "SELECT "+bridgeColumns+" FROM bridges WHERE id = ?", id)
	b, err := scanBridge(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return b, err
}

// GetQueueBytes returns the current queued byte total for a bridge (the quota
// accumulator, DEV_SPEC §5). Useful for the /bridge view and tests.
func (s *Store) GetQueueBytes(ctx context.Context, bridgeID string) (int64, error) {
	var n int64
	err := s.read.QueryRowContext(ctx, "SELECT queue_bytes FROM bridges WHERE id = ?", bridgeID).Scan(&n)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrNotFound
	}
	return n, err
}

// SetAdminLandID records the creator land as the bridge admin (DEV_SPEC §5:
// admin_land_id set right after the creator land row is inserted).
func (s *Store) SetAdminLandID(ctx context.Context, bridgeID, landID string) error {
	_, err := s.writer.Submit(ctx, func(tx *sql.Tx) (any, error) {
		return nil, execAffecting(ctx, tx, "UPDATE bridges SET admin_land_id = ? WHERE id = ?", landID, bridgeID)
	})
	return err
}

// BumpLandSetVersion increments and returns the bridge's land_set_version
// (DEV_SPEC §6.4: join / leave / rekey all bump it).
func (s *Store) BumpLandSetVersion(ctx context.Context, bridgeID string) (int64, error) {
	v, err := s.writer.Submit(ctx, func(tx *sql.Tx) (any, error) {
		if err := execAffecting(ctx, tx,
			"UPDATE bridges SET land_set_version = land_set_version + 1 WHERE id = ?", bridgeID); err != nil {
			return nil, err
		}
		var ver int64
		if err := tx.QueryRowContext(ctx,
			"SELECT land_set_version FROM bridges WHERE id = ?", bridgeID).Scan(&ver); err != nil {
			return nil, err
		}
		return ver, nil
	})
	if err != nil {
		return 0, err
	}
	return v.(int64), nil
}

// ClaimBridge activates a pending /join bridge (DEV_SPEC §6 claim): it records
// the claimant as the admin land, clears the one-time claim token and expiry,
// clears the pending flag, and stamps admin_last_active_at = now. The WHERE
// pending = 1 guard makes a second claim a no-op (ErrNotFound), so a consumed or
// already-claimed token cannot re-activate the bridge.
func (s *Store) ClaimBridge(ctx context.Context, bridgeID, adminLandID string) error {
	now := s.nowMillis()
	_, err := s.writer.Submit(ctx, func(tx *sql.Tx) (any, error) {
		return nil, execAffecting(ctx, tx, `
UPDATE bridges SET
  pending = 0, claim_token_hash = NULL, claim_expires_at = NULL, pending_prefix = NULL,
  admin_land_id = ?, admin_last_active_at = ?
WHERE id = ? AND pending = 1`, adminLandID, now, bridgeID)
	})
	return err
}

// CountPendingBridges returns how many bridges are awaiting their first claim
// (DEV_SPEC §6 /join global cap). Pure read.
func (s *Store) CountPendingBridges(ctx context.Context) (int, error) {
	var n int
	err := s.read.QueryRowContext(ctx, "SELECT COUNT(*) FROM bridges WHERE pending = 1").Scan(&n)
	return n, err
}

// CountPendingBridgesByPrefix returns how many unclaimed pending bridges were
// created from the given source prefix hash (Batch 1 item 6 per-prefix cap).
func (s *Store) CountPendingBridgesByPrefix(ctx context.Context, prefixHash string) (int, error) {
	var n int
	err := s.read.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM bridges WHERE pending = 1 AND pending_prefix = ?", prefixHash).Scan(&n)
	return n, err
}

// scanBridge reads a bridge row from a *sql.Row or *sql.Rows.
func scanBridge(sc interface{ Scan(...any) error }) (*Bridge, error) {
	var b Bridge
	var landsCanInvite, joinLocked, pending, inviteUsed, pair int
	if err := sc.Scan(
		&b.ID, &b.Name, &b.Description, &b.Status, &b.AdminLandID, &b.InviteCodeEnc,
		&b.InviteCodeRotatedAt, &inviteUsed, &b.InviteCodeExpiresAt,
		&b.MaxLands, &b.LandSetVersion, &landsCanInvite, &joinLocked,
		&b.JoinLockedReason, &b.RateLimitPerMin, &b.QueueQuotaBytes, &b.QueueBytes,
		&b.AdminLastActiveAt, &b.AdminJoinsAckedThrough, &b.CreatedAt, &b.ClosedAt, &b.CloseReason,
		&pending, &b.ClaimTokenHash, &b.ClaimExpiresAt, &pair, &b.EntryID, &b.EntryClosedAt,
		&b.GraceUntil, &b.GraceCloser,
	); err != nil {
		return nil, err
	}
	b.LandsCanInvite = landsCanInvite != 0
	b.JoinLocked = joinLocked != 0
	b.Pending = pending != 0
	b.InviteCodeUsed = inviteUsed != 0
	b.Pair = pair != 0
	return &b, nil
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// execAffecting runs a statement expected to touch exactly one row, returning
// ErrNotFound when it touches none.
func execAffecting(ctx context.Context, tx *sql.Tx, query string, args ...any) error {
	res, err := tx.ExecContext(ctx, query, args...)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// SetBridgeLegacy turns a bridge into a pre-v0.5.0 multi-member bridge
// (pair = 0, the given max_lands). Only tests call it, to model bridges that
// existed before migration 0007; the server never does.
func (s *Store) SetBridgeLegacy(ctx context.Context, bridgeID string, maxLands int) error {
	_, err := s.writer.Submit(ctx, func(tx *sql.Tx) (any, error) {
		_, err := tx.ExecContext(ctx, "UPDATE bridges SET pair = 0, max_lands = ? WHERE id = ?", maxLands, bridgeID)
		return nil, err
	})
	return err
}
