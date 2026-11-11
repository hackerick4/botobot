package store

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"time"
)

// CloseReason values recorded in bridges.close_reason (DEV_SPEC §5, §11).
const (
	CloseAdminLeft        = "admin_left"
	CloseAdminKeysRevoked = "admin_keys_revoked"
	CloseAdminInactive    = "admin_inactive"
	CloseAdminClosed      = "admin_closed"
	// Two-bot bridges (v0.5.0) close when the peer leaves or is removed.
	CloseMemberLeft    = "member_left"
	CloseMemberRemoved = "member_removed"
)

// CloseBridge performs the immediate-shutdown flow (DEV_SPEC §11) in a single
// transaction: it marks the bridge closed with the given reason, blanks the
// tombstone's content columns (DEV_SPEC §5: a closed bridge keeps only id,
// status, closed_at, close_reason), and deletes all land / key / message /
// inbox / delivery data for the bridge. Blob files for the bridge are removed
// after the transaction commits. Closing an already-closed bridge is a no-op.
//
// The 30-day tombstone deletion and the 7-day admin-inactivity trigger are the
// sweeper's job (M5); CloseBridge only implements the close itself, which the
// admin-driven endpoints (DELETE bridge, admin leave, admin last-key revoke)
// invoke directly.
func (s *Store) CloseBridge(ctx context.Context, bridgeID, reason string) error {
	_, err := s.CloseBridgeGrace(ctx, bridgeID, reason, "", 0)
	return err
}

// CloseOutcome reports what a close did with the deliveries the other side
// had not fetched yet (v0.6.4).
type CloseOutcome struct {
	// Undelivered is how many queued messages from the closer the other side
	// had not acked at close time.
	Undelivered int
	// GraceUntil (Unix ms) is set when those messages stay readable by the
	// other side until then; 0 when nothing was kept.
	GraceUntil int64
}

// CloseBridgeGrace closes the bridge like CloseBridge. When closerLandID is set,
// window > 0, and lands other than the closer still have queued messages,
// those message rows (ciphertext and wrapped keys only, still end-to-end
// encrypted) and the lands needed to authenticate and verify them are kept
// until the other side acks them or window ends, whichever comes first
// (FinalizeGrace). Everything else (events, the closer's own inbox, names,
// invite, quota) goes at once, and the bridge is 'closed' for every other
// request. The closer is never let back in.
func (s *Store) CloseBridgeGrace(ctx context.Context, bridgeID, reason, closerLandID string, window time.Duration) (CloseOutcome, error) {
	now := s.nowMillis()
	// The v0.5.0 member reasons live in close_detail (migration 0007): the
	// 0001 CHECK on close_reason cannot be widened without a table rebuild.
	var closeReason, closeDetail any = reason, nil
	if reason == CloseMemberLeft || reason == CloseMemberRemoved {
		closeReason, closeDetail = nil, reason
	}
	var out CloseOutcome
	kept := false
	_, err := s.writer.Submit(ctx, func(tx *sql.Tx) (any, error) {
		out, kept = CloseOutcome{}, false
		// Only act on an active bridge.
		var status string
		if err := tx.QueryRowContext(ctx, "SELECT status FROM bridges WHERE id = ?", bridgeID).Scan(&status); err != nil {
			if err == sql.ErrNoRows {
				return nil, ErrNotFound
			}
			return nil, err
		}
		if status != "active" {
			return nil, nil // already closed; idempotent
		}
		if closerLandID != "" {
			if err := tx.QueryRowContext(ctx, `
SELECT COUNT(*) FROM inbox_queue
WHERE bridge_id = ? AND kind = 'message' AND recipient_land_id <> ? AND expires_at > ?`,
				bridgeID, closerLandID, now).Scan(&out.Undelivered); err != nil {
				return nil, err
			}
		}
		if out.Undelivered > 0 && window > 0 {
			kept = true
			out.GraceUntil = now + window.Milliseconds()
			// Keep only the other side's queued messages and the rows they
			// need; drop the rest now.
			for _, q := range []string{
				"DELETE FROM inbox_queue WHERE bridge_id = ? AND (kind <> 'message' OR recipient_land_id = ?)",
				`DELETE FROM messages WHERE bridge_id = ? AND ? <> '' AND NOT EXISTS (
  SELECT 1 FROM inbox_queue q WHERE q.bridge_id = messages.bridge_id
    AND q.sender_land_id = messages.sender_land_id AND q.msg_id = messages.id)`,
				"DELETE FROM delivery_status WHERE bridge_id = ? AND ? <> ''",
			} {
				if _, err := tx.ExecContext(ctx, q, bridgeID, closerLandID); err != nil {
					return nil, err
				}
			}
			if _, err := tx.ExecContext(ctx, `
UPDATE bridges SET
  status = 'closed', closed_at = ?, close_reason = ?, close_detail = ?,
  name = '', description = '',
  invite_code_enc = NULL, invite_code_rotated_at = NULL,
  join_locked = 0, join_locked_reason = NULL, queue_bytes = 0,
  pending = 0, claim_token_hash = NULL, claim_expires_at = NULL,
  grace_until = ?, grace_closer = ?
WHERE id = ?`, now, closeReason, closeDetail, out.GraceUntil, closerLandID, bridgeID); err != nil {
				return nil, err
			}
			return nil, nil
		}

		if err := purgeBridgeContent(ctx, tx, bridgeID, now); err != nil {
			return nil, err
		}
		// Tombstone: keep id/status/closed_at/close_reason, blank the rest.
		if _, err := tx.ExecContext(ctx, `
UPDATE bridges SET
  status = 'closed', closed_at = ?, close_reason = ?, close_detail = ?,
  name = '', description = '', admin_land_id = NULL,
  invite_code_enc = NULL, invite_code_rotated_at = NULL,
  join_locked = 0, join_locked_reason = NULL, queue_bytes = 0,
  pending = 0, claim_token_hash = NULL, claim_expires_at = NULL,
  grace_until = NULL, grace_closer = NULL
WHERE id = ?`, now, closeReason, closeDetail, bridgeID); err != nil {
			return nil, err
		}
		return nil, nil
	})
	if err != nil {
		return CloseOutcome{}, err
	}
	if !kept {
		out.GraceUntil = 0
		s.removeBridgeBlobs(bridgeID)
	}
	return out, nil
}

// purgeBridgeContent deletes per-bridge content. Deleting lands cascades to
// public_keys and api_keys (ON DELETE CASCADE); messages / inbox / delivery
// are removed explicitly because the bridge row is retained as a tombstone.
func purgeBridgeContent(ctx context.Context, tx *sql.Tx, bridgeID string, now int64) error {
	// v0.6.6: remember which keys belonged to this bridge (hash only) so the
	// tombstone can tell its own former members 410 and everyone else 404.
	if _, err := tx.ExecContext(ctx, `
INSERT OR IGNORE INTO closed_keys (key_hash, bridge_id, closed_at)
SELECT k.key_hash, l.bridge_id, ?
FROM api_keys k JOIN lands l ON l.id = k.land_id
WHERE l.bridge_id = ? AND k.revoked_at IS NULL`, now, bridgeID); err != nil {
		return err
	}
	for _, q := range []string{
		"DELETE FROM lands WHERE bridge_id = ?",
		"DELETE FROM messages WHERE bridge_id = ?",
		"DELETE FROM inbox_queue WHERE bridge_id = ?",
		"DELETE FROM delivery_status WHERE bridge_id = ?",
	} {
		if _, err := tx.ExecContext(ctx, q, bridgeID); err != nil {
			return err
		}
	}
	return nil
}

// removeBridgeBlobs removes the bridge's blob directory (server-generated
// path, DEV_SPEC §16).
func (s *Store) removeBridgeBlobs(bridgeID string) {
	if s.blobDir != "" {
		_ = os.RemoveAll(filepath.Join(s.blobDir, bridgeID))
	}
}

// GraceOpen reports whether landID may still fetch / ack on the closed bridge b
// at nowMS: inside its window and not the land that closed it.
func GraceOpen(b *Bridge, landID string, nowMS int64) bool {
	return b != nil && b.Status == "closed" && b.GraceUntil != nil && nowMS < *b.GraceUntil &&
		b.GraceCloser != nil && landID != "" && landID != *b.GraceCloser
}

// GracePending counts the queued messages still kept for the other side of a
// closed bridge in its window.
func (s *Store) GracePending(ctx context.Context, bridgeID string) (int, error) {
	var n int
	err := s.read.QueryRowContext(ctx, `
SELECT COUNT(*) FROM inbox_queue q JOIN bridges b ON b.id = q.bridge_id
WHERE q.bridge_id = ? AND b.grace_until IS NOT NULL AND q.kind = 'message'
  AND q.recipient_land_id <> b.grace_closer AND q.expires_at > ?`, bridgeID, s.nowMillis()).Scan(&n)
	return n, err
}

// FinalizeGrace ends a closed bridge's last-delivery window: the kept rows,
// lands and keys are deleted and the tombstone is blanked like any other
// closed bridge. A no-op for a bridge outside a window.
func (s *Store) FinalizeGrace(ctx context.Context, bridgeID string) error {
	done := false
	_, err := s.writer.Submit(ctx, func(tx *sql.Tx) (any, error) {
		done = false
		var gu sql.NullInt64
		if err := tx.QueryRowContext(ctx, "SELECT grace_until FROM bridges WHERE id = ? AND status = 'closed'", bridgeID).Scan(&gu); err != nil {
			if err == sql.ErrNoRows {
				return nil, nil
			}
			return nil, err
		}
		if !gu.Valid {
			return nil, nil
		}
		if err := purgeBridgeContent(ctx, tx, bridgeID, s.nowMillis()); err != nil {
			return nil, err
		}
		if _, err := tx.ExecContext(ctx,
			"UPDATE bridges SET admin_land_id = NULL, grace_until = NULL, grace_closer = NULL, queue_bytes = 0 WHERE id = ?", bridgeID); err != nil {
			return nil, err
		}
		done = true
		return nil, nil
	})
	if err == nil && done {
		s.removeBridgeBlobs(bridgeID)
	}
	return err
}

// FinalizeExpiredGrace ends every last-delivery window that has run out (the
// sweeper calls it each pass) and returns how many it ended.
func (s *Store) FinalizeExpiredGrace(ctx context.Context) (int, error) {
	rows, err := s.read.QueryContext(ctx,
		"SELECT id FROM bridges WHERE grace_until IS NOT NULL AND grace_until <= ?", s.nowMillis())
	if err != nil {
		return 0, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return 0, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	n := 0
	for _, id := range ids {
		if err := s.FinalizeGrace(ctx, id); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

// UndeliveredFrom counts the queued messages senderLandID sent on the bridge
// that the other side has not acked yet. Counts only; no content.
func (s *Store) UndeliveredFrom(ctx context.Context, bridgeID, senderLandID string) (int, error) {
	var n int
	err := s.read.QueryRowContext(ctx, `
SELECT COUNT(*) FROM inbox_queue
WHERE bridge_id = ? AND kind = 'message' AND sender_land_id = ? AND recipient_land_id <> ? AND expires_at > ?`,
		bridgeID, senderLandID, senderLandID, s.nowMillis()).Scan(&n)
	return n, err
}
