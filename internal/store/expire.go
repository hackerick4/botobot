package store

import (
	"context"
	"database/sql"
)

// ExpireResult reports a sweep's effect (DEV_SPEC §10 TTL).
type ExpireResult struct {
	Deleted int
}

// ExpireInbox deletes every inbox item whose TTL has passed (expires_at <= now),
// across all bridges, and applies the same bookkeeping as an ack except the
// terminal status is `expired` rather than `delivered` (DEV_SPEC §10): message
// items decrement the shared message's refcount (deleting it and crediting the
// quota at zero) and flip delivery_status to `expired`; event items credit their
// bytes back. This is the query the M5 sweeper goroutine will call on a timer.
func (s *Store) ExpireInbox(ctx context.Context) (*ExpireResult, error) {
	now := s.nowMillis()

	v, err := s.writer.Submit(ctx, func(tx *sql.Tx) (any, error) {
		rows, err := tx.QueryContext(ctx, `
SELECT id, bridge_id, recipient_land_id, kind, sender_land_id, msg_id, size_bytes
FROM inbox_queue WHERE expires_at <= ?`, now)
		if err != nil {
			return nil, err
		}
		type expired struct {
			id, bridgeID, recipient, kind string
			sender, msgID                 sql.NullString
			size                          int64
		}
		var list []expired
		for rows.Next() {
			var e expired
			if err := rows.Scan(&e.id, &e.bridgeID, &e.recipient, &e.kind, &e.sender, &e.msgID, &e.size); err != nil {
				rows.Close()
				return nil, err
			}
			list = append(list, e)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, err
		}
		rows.Close()

		res := &ExpireResult{}
		var blobs []string
		for _, e := range list {
			if _, err := tx.ExecContext(ctx, "DELETE FROM inbox_queue WHERE id = ?", e.id); err != nil {
				return nil, err
			}
			res.Deleted++
			if e.kind != "message" {
				if err := execAffecting(ctx, tx,
					"UPDATE bridges SET queue_bytes = queue_bytes - ? WHERE id = ?", e.size, e.bridgeID); err != nil {
					return nil, err
				}
				continue
			}
			if _, err := tx.ExecContext(ctx,
				"UPDATE delivery_status SET status = 'expired', updated_at = ? WHERE bridge_id = ? AND sender_land_id = ? AND msg_id = ? AND recipient_land_id = ?",
				now, e.bridgeID, e.sender.String, e.msgID.String, e.recipient); err != nil {
				return nil, err
			}
			blob, err := s.decRefAndMaybeDelete(ctx, tx, e.bridgeID, e.sender.String, e.msgID.String)
			if err != nil {
				return nil, err
			}
			if blob != "" {
				blobs = append(blobs, blob)
			}
		}
		return expireTxResult{res: res, blobs: blobs}, nil
	})
	if err != nil {
		return nil, err
	}
	out := v.(expireTxResult)
	for _, b := range out.blobs {
		s.removeBlob(b)
	}
	return out.res, nil
}

type expireTxResult struct {
	res   *ExpireResult
	blobs []string
}
