package store

import (
	"context"
	"database/sql"
	"time"
)

// This file holds the periodic maintenance queries the M5 sweeper runs on a
// timer (DEV_SPEC §3.2 b–e, §5 tombstone, §10 delivery-status retention, §11
// 7-day admin inactivity). ExpireInbox (DEV_SPEC §3.2 a, §10 TTL) lives in
// expire.go. Every mutation flows through the single writer; time comes from the
// injected clock so these are deterministic under a fake clock in tests.

// SweepOrphanMessages deletes any message whose refcount has dropped to zero but
// which was not already removed inline (DEV_SPEC §3.2 b). The ack and TTL-expiry
// paths normally delete a message the moment its last inbox row is cleared, so
// this is a defensive backstop; when it does find a stray row it still credits
// the bytes back to the bridge quota and removes the blob file. Returns the
// number of messages deleted.
func (s *Store) SweepOrphanMessages(ctx context.Context) (int, error) {
	v, err := s.writer.Submit(ctx, func(tx *sql.Tx) (any, error) {
		rows, err := tx.QueryContext(ctx,
			"SELECT bridge_id, sender_land_id, id, size_bytes, blob_path FROM messages WHERE refcount <= 0")
		if err != nil {
			return nil, err
		}
		type orphan struct {
			bridgeID, sender, msgID string
			size                    int64
			blob                    sql.NullString
		}
		var list []orphan
		for rows.Next() {
			var o orphan
			if err := rows.Scan(&o.bridgeID, &o.sender, &o.msgID, &o.size, &o.blob); err != nil {
				rows.Close()
				return nil, err
			}
			list = append(list, o)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, err
		}
		rows.Close()

		var blobs []string
		for _, o := range list {
			if _, err := tx.ExecContext(ctx,
				"DELETE FROM messages WHERE bridge_id = ? AND sender_land_id = ? AND id = ?",
				o.bridgeID, o.sender, o.msgID); err != nil {
				return nil, err
			}
			if err := execAffecting(ctx, tx,
				"UPDATE bridges SET queue_bytes = queue_bytes - ? WHERE id = ?", o.size, o.bridgeID); err != nil {
				return nil, err
			}
			if o.blob.Valid && o.blob.String != "" {
				blobs = append(blobs, o.blob.String)
			}
		}
		return sweepMsgResult{count: len(list), blobs: blobs}, nil
	})
	if err != nil {
		return 0, err
	}
	out := v.(sweepMsgResult)
	for _, b := range out.blobs {
		s.removeBlob(b)
	}
	return out.count, nil
}

type sweepMsgResult struct {
	count int
	blobs []string
}

// SweepDeliveryStatus deletes delivery_status rows whose last update is older
// than retain (DEV_SPEC §10 / D6: per-recipient status is kept 24h). Returns the
// number of rows deleted.
func (s *Store) SweepDeliveryStatus(ctx context.Context, retain time.Duration) (int, error) {
	cutoff := s.nowMillis() - retain.Milliseconds()
	v, err := s.writer.Submit(ctx, func(tx *sql.Tx) (any, error) {
		res, err := tx.ExecContext(ctx, "DELETE FROM delivery_status WHERE updated_at <= ?", cutoff)
		if err != nil {
			return nil, err
		}
		n, err := res.RowsAffected()
		return int(n), err
	})
	if err != nil {
		return 0, err
	}
	return v.(int), nil
}

// SweepTombstones deletes closed-bridge tombstone rows older than retain
// (DEV_SPEC §5: a closed bridge is kept 30 days to answer 410, then removed).
// Returns the number of tombstones deleted.
func (s *Store) SweepTombstones(ctx context.Context, retain time.Duration) (int, error) {
	cutoff := s.nowMillis() - retain.Milliseconds()
	v, err := s.writer.Submit(ctx, func(tx *sql.Tx) (any, error) {
		res, err := tx.ExecContext(ctx,
			"DELETE FROM bridges WHERE status = 'closed' AND closed_at IS NOT NULL AND closed_at <= ?", cutoff)
		if err != nil {
			return nil, err
		}
		n, err := res.RowsAffected()
		return int(n), err
	})
	if err != nil {
		return 0, err
	}
	return v.(int), nil
}

// SweepExpiredPending deletes /join-created bridges still awaiting their first
// claim whose claim_expires_at has passed (DEV_SPEC §6 claim expiry). A pending
// bridge has no lands, keys, messages, inbox, or blobs (joining it is refused
// with bridge_pending, and claim is the only way to add the first land), so the
// row is removed outright with no tombstone. Returns the number deleted.
func (s *Store) SweepExpiredPending(ctx context.Context) (int, error) {
	now := s.nowMillis()
	v, err := s.writer.Submit(ctx, func(tx *sql.Tx) (any, error) {
		res, err := tx.ExecContext(ctx,
			"DELETE FROM bridges WHERE pending = 1 AND claim_expires_at IS NOT NULL AND claim_expires_at <= ?", now)
		if err != nil {
			return nil, err
		}
		n, err := res.RowsAffected()
		return int(n), err
	})
	if err != nil {
		return 0, err
	}
	return v.(int), nil
}

// ListInactiveAdminBridges returns the ids of active bridges whose admin has had
// no successful API call for at least inactivity (DEV_SPEC §11 condition 3:
// now - admin_last_active_at exceeds the window). The sweeper closes each with
// reason admin_inactive. This is a pure read.
func (s *Store) ListInactiveAdminBridges(ctx context.Context, inactivity time.Duration) ([]string, error) {
	cutoff := s.nowMillis() - inactivity.Milliseconds()
	// pending = 0 excludes unclaimed /join bridges: those are governed by the
	// claim expiry (SweepExpiredPending), not the 7-day admin-inactivity rule.
	// Bridges of a public entry (v0.6.0) follow the entry's activity: the
	// service bot polls one aggregated inbox, not each bridge.
	rows, err := s.read.QueryContext(ctx,
		`SELECT id FROM bridges WHERE status = 'active' AND pending = 0 AND (
  (entry_id IS NULL AND admin_last_active_at <= ?) OR
  (entry_id IS NOT NULL AND entry_id IN (SELECT id FROM entries WHERE last_active_at <= ?)))`, cutoff, cutoff)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}
