package store

import (
	"context"
	"database/sql"
)

// GetDeliveries returns the per-recipient delivery status of a message, ordered
// by recipient land id (DEV_SPEC §6.4 GET /messages/{id}/status, §10). The
// result is empty when the message is unknown or its status rows have been
// swept (the handler maps that to 404 after 24h).
func (s *Store) GetDeliveries(ctx context.Context, bridgeID, senderLandID, msgID string) ([]Delivery, error) {
	rows, err := s.read.QueryContext(ctx, `
SELECT recipient_land_id, status, updated_at
FROM delivery_status
WHERE bridge_id = ? AND sender_land_id = ? AND msg_id = ?
ORDER BY recipient_land_id`, bridgeID, senderLandID, msgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Delivery
	for rows.Next() {
		var d Delivery
		if err := rows.Scan(&d.RecipientLandID, &d.Status, &d.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// DropLandInbox deletes all of a land's inbox items and flips its pending
// message deliveries to `dropped` (DEV_SPEC §6.4 DELETE land, §10). Message
// refcounts are decremented and the shared ciphertext / quota are reclaimed when
// they reach zero. Returns the number of items removed.
func (s *Store) DropLandInbox(ctx context.Context, bridgeID, landID string) (int, error) {
	now := s.nowMillis()
	v, err := s.writer.Submit(ctx, func(tx *sql.Tx) (any, error) {
		rows, err := tx.QueryContext(ctx,
			"SELECT id, kind, sender_land_id, msg_id, size_bytes FROM inbox_queue WHERE bridge_id = ? AND recipient_land_id = ?",
			bridgeID, landID)
		if err != nil {
			return nil, err
		}
		type item struct {
			id, kind      string
			sender, msgID string
			size          int64
		}
		var items []item
		for rows.Next() {
			var it item
			var sender, msgID nullable
			if err := rows.Scan(&it.id, &it.kind, &sender, &msgID, &it.size); err != nil {
				rows.Close()
				return nil, err
			}
			it.sender, it.msgID = sender.String, msgID.String
			items = append(items, it)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, err
		}
		rows.Close()

		var blobs []string
		for _, it := range items {
			if _, err := tx.ExecContext(ctx, "DELETE FROM inbox_queue WHERE id = ?", it.id); err != nil {
				return nil, err
			}
			if it.kind != "message" {
				if err := execAffecting(ctx, tx,
					"UPDATE bridges SET queue_bytes = queue_bytes - ? WHERE id = ?", it.size, bridgeID); err != nil {
					return nil, err
				}
				continue
			}
			if _, err := tx.ExecContext(ctx,
				"UPDATE delivery_status SET status = 'dropped', updated_at = ? WHERE bridge_id = ? AND sender_land_id = ? AND msg_id = ? AND recipient_land_id = ?",
				now, bridgeID, it.sender, it.msgID, landID); err != nil {
				return nil, err
			}
			blob, err := s.decRefAndMaybeDelete(ctx, tx, bridgeID, it.sender, it.msgID)
			if err != nil {
				return nil, err
			}
			if blob != "" {
				blobs = append(blobs, blob)
			}
		}
		return dropTxResult{count: len(items), blobs: blobs}, nil
	})
	if err != nil {
		return 0, err
	}
	out := v.(dropTxResult)
	for _, b := range out.blobs {
		s.removeBlob(b)
	}
	return out.count, nil
}

type dropTxResult struct {
	count int
	blobs []string
}

// nullable is a small sql.Scanner for optional TEXT columns.
type nullable struct{ String string }

func (n *nullable) Scan(v any) error {
	switch t := v.(type) {
	case nil:
		n.String = ""
	case string:
		n.String = t
	case []byte:
		n.String = string(t)
	}
	return nil
}
