package store

import (
	"context"
	"database/sql"
	"strings"
)

// CountActiveLands returns the number of lands of a bridge that have not left
// (DEV_SPEC §6.3 land_count, §6.4 bridge_full check).
func (s *Store) CountActiveLands(ctx context.Context, bridgeID string) (int, error) {
	var n int
	err := s.read.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM lands WHERE bridge_id = ? AND left_at IS NULL", bridgeID).Scan(&n)
	return n, err
}

// HandleTaken reports whether an active land in the bridge already uses handle
// (DEV_SPEC §6.4: 409 handle_taken). The partial unique index also enforces
// this, but a pre-check gives the correct error code without relying on insert
// failures.
func (s *Store) HandleTaken(ctx context.Context, bridgeID, handle string) (bool, error) {
	var one int
	err := s.read.QueryRowContext(ctx,
		"SELECT 1 FROM lands WHERE bridge_id = ? AND handle = ? AND left_at IS NULL", bridgeID, handle).Scan(&one)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// UpdateBridgeParams carries the optional fields of PATCH /bridges/{id}
// (DEV_SPEC §6.4). Nil pointers are left unchanged. JoinLocked is handled by
// SetJoinLocked because unlocking also resets throttle state.
type UpdateBridgeParams struct {
	Name            *string
	Description     *string
	LandsCanInvite  *bool
	RateLimitPerMin *int
	QueueQuotaBytes *int64
}

// UpdateBridge applies the non-nil fields of p to the bridge, returning
// ErrNotFound if the bridge does not exist. SQL is parameterized; only the
// server-controlled column names are interpolated.
func (s *Store) UpdateBridge(ctx context.Context, bridgeID string, p UpdateBridgeParams) error {
	var sets []string
	var args []any
	if p.Name != nil {
		sets = append(sets, "name = ?")
		args = append(args, *p.Name)
	}
	if p.Description != nil {
		sets = append(sets, "description = ?")
		args = append(args, *p.Description)
	}
	if p.LandsCanInvite != nil {
		sets = append(sets, "lands_can_invite = ?")
		args = append(args, boolToInt(*p.LandsCanInvite))
	}
	if p.RateLimitPerMin != nil {
		sets = append(sets, "rate_limit_per_min = ?")
		args = append(args, *p.RateLimitPerMin)
	}
	if p.QueueQuotaBytes != nil {
		sets = append(sets, "queue_quota_bytes = ?")
		args = append(args, *p.QueueQuotaBytes)
	}
	if len(sets) == 0 {
		return nil
	}
	query := "UPDATE bridges SET " + strings.Join(sets, ", ") + " WHERE id = ? AND status = 'active'"
	args = append(args, bridgeID)
	_, err := s.writer.Submit(ctx, func(tx *sql.Tx) (any, error) {
		return nil, execAffecting(ctx, tx, query, args...)
	})
	return err
}

// SetJoinLocked sets or clears bridges.join_locked and its reason (DEV_SPEC §6.4
// PATCH settings.join_locked, §8 brute_force lock). reason should be "admin" or
// "brute_force" when locking; it is cleared to NULL when unlocking.
func (s *Store) SetJoinLocked(ctx context.Context, bridgeID string, locked bool, reason string) error {
	_, err := s.writer.Submit(ctx, func(tx *sql.Tx) (any, error) {
		return nil, execAffecting(ctx, tx,
			"UPDATE bridges SET join_locked = ?, join_locked_reason = ? WHERE id = ? AND status = 'active'",
			boolToInt(locked), nullString(reason), bridgeID)
	})
	return err
}

// SetInviteCode replaces the encrypted invite code and stamps rotated_at
// (DEV_SPEC §6.4 rotate). The old code becomes unusable immediately.
func (s *Store) SetInviteCode(ctx context.Context, bridgeID string, enc []byte, rotatedAtMillis int64) error {
	// A freshly issued/rotated code is unused and valid for 24h (Batch 1 item 5).
	expiresAt := rotatedAtMillis + inviteCodeTTLMillis
	_, err := s.writer.Submit(ctx, func(tx *sql.Tx) (any, error) {
		return nil, execAffecting(ctx, tx,
			`UPDATE bridges SET invite_code_enc = ?, invite_code_rotated_at = ?,
			   invite_code_used = 0, invite_code_expires_at = ?
			 WHERE id = ? AND status = 'active'`,
			enc, rotatedAtMillis, expiresAt, bridgeID)
	})
	return err
}

// inviteCodeTTLMillis is how long an invite code stays valid after being issued
// or rotated (Batch 1 item 5: 24h).
const inviteCodeTTLMillis int64 = 24 * 60 * 60 * 1000

// TouchAdminActive updates admin_last_active_at (DEV_SPEC §11: any successful
// admin request is activity). The server batches these in memory and calls this
// at most once per minute per bridge.
func (s *Store) TouchAdminActive(ctx context.Context, bridgeID string, nowMillis int64) error {
	_, err := s.writer.Submit(ctx, func(tx *sql.Tx) (any, error) {
		_, err := tx.ExecContext(ctx,
			"UPDATE bridges SET admin_last_active_at = ? WHERE id = ?", nowMillis, bridgeID)
		return nil, err
	})
	return err
}

// UpdateLandProfileParams carries the optional fields of PATCH /me (DEV_SPEC
// §6.4). Nil pointers are left unchanged.
type UpdateLandProfileParams struct {
	DisplayName     *string
	OwnerName       *string
	PollIntervalSec *int
}

// UpdateLandProfile applies the non-nil fields of p to the land (DEV_SPEC §6.4
// PATCH /me), returning ErrNotFound if the land does not exist.
func (s *Store) UpdateLandProfile(ctx context.Context, landID string, p UpdateLandProfileParams) error {
	var sets []string
	var args []any
	if p.DisplayName != nil {
		sets = append(sets, "display_name = ?")
		args = append(args, *p.DisplayName)
	}
	if p.OwnerName != nil {
		sets = append(sets, "owner_name = ?")
		args = append(args, *p.OwnerName)
	}
	if p.PollIntervalSec != nil {
		sets = append(sets, "poll_interval_sec = ?")
		args = append(args, *p.PollIntervalSec)
	}
	if len(sets) == 0 {
		return nil
	}
	query := "UPDATE lands SET " + strings.Join(sets, ", ") + " WHERE id = ? AND left_at IS NULL"
	args = append(args, landID)
	_, err := s.writer.Submit(ctx, func(tx *sql.Tx) (any, error) {
		return nil, execAffecting(ctx, tx, query, args...)
	})
	return err
}

// GetAPIKey returns an API key by id (DEV_SPEC §6.4 GET /me shows the calling
// key's id/prefix/created_at), or ErrNotFound.
func (s *Store) GetAPIKey(ctx context.Context, id string) (*APIKey, error) {
	row := s.read.QueryRowContext(ctx, "SELECT "+apiKeyColumns+" FROM api_keys WHERE id = ?", id)
	k, err := scanAPIKey(row)
	if err == sql.ErrNoRows {
		return nil, ErrNotFound
	}
	return k, err
}
