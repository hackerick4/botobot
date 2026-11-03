package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
)

// RecipientWrap pairs a recipient land with its sealed content key (80 bytes,
// DEV_SPEC §6.4 wrapped_keys).
type RecipientWrap struct {
	LandID     string
	WrappedKey []byte
}

// EnqueueMessageParams is one /send after validation (DEV_SPEC §6.4 step 6).
// SizeBytes is the quota-charged size (header + ciphertext + wrapped keys) and
// is accounted once against the bridge regardless of recipient count (§19).
type EnqueueMessageParams struct {
	BridgeID     string
	SenderLandID string
	MsgID        string
	HeaderJSON   []byte
	Signature    []byte // 64-byte Ed25519 envelope signature (§7.5), stored for GET /inbox
	Ciphertext   []byte // includes the 16-byte AEAD tag
	// CiphertextSHA256 is SHA-256(Ciphertext). Used for the idempotency compare
	// (DEV_SPEC §6.4 step 7) without reading a spilled blob back. When empty it
	// is computed from Ciphertext.
	CiphertextSHA256 []byte
	SizeBytes        int64
	Recipients       []RecipientWrap
}

// EnqueueResult reports the outcome of an enqueue. Created is false when the
// call was idempotent (same (bridge, sender, msg_id) with identical content).
type EnqueueResult struct {
	MsgID      string
	ExpiresAt  int64
	Deliveries []Delivery
	Created    bool
}

// EnqueueMessage stores a message once and fans it out to each recipient's
// inbox, charging the bridge quota once and setting refcount to the recipient
// count (DEV_SPEC §5, §9, §19). Large ciphertext is spilled to a blob file.
//
// Idempotency (DEV_SPEC §6.4 step 7): re-sending identical content returns the
// original result; a conflicting msg_id returns ErrDuplicateMsgID.
func (s *Store) EnqueueMessage(ctx context.Context, p EnqueueMessageParams) (*EnqueueResult, error) {
	now := s.nowMillis()
	expiresAt := now + s.ttl.Milliseconds()

	// Fix the ciphertext hash once so it is persisted and used for idempotency
	// (DEV_SPEC §6.4 step 7) without re-reading a spilled blob.
	if len(p.CiphertextSHA256) == 0 {
		h := sha256.Sum256(p.Ciphertext)
		p.CiphertextSHA256 = h[:]
	}

	// Idempotency / conflict pre-check (also protects the blob file from being
	// clobbered by a conflicting resend). A resend is idempotent only when both
	// the signed header and the ciphertext hash match the stored message
	// (DEV_SPEC §6.4 step 7: "same header + ciphertext hash"); anything else
	// under the same (bridge, sender, msg_id) is a conflict.
	if existing, err := s.getMessageMeta(ctx, p.BridgeID, p.SenderLandID, p.MsgID); err == nil {
		if bytes.Equal(existing.headerJSON, p.HeaderJSON) && bytes.Equal(existing.ctHash, p.CiphertextSHA256) {
			deliveries, derr := s.GetDeliveries(ctx, p.BridgeID, p.SenderLandID, p.MsgID)
			if derr != nil {
				return nil, derr
			}
			return &EnqueueResult{MsgID: p.MsgID, ExpiresAt: existing.expiresAt, Deliveries: deliveries, Created: false}, nil
		}
		return nil, ErrDuplicateMsgID
	} else if !errors.Is(err, ErrNotFound) {
		return nil, err
	}

	// Spill large ciphertext to a blob file before the DB op.
	var inline []byte
	var blobPath string
	if s.blobDir != "" && int64(len(p.Ciphertext)) > s.blobThreshold {
		blobPath = s.blobPathFor(p.BridgeID, p.SenderLandID, p.MsgID)
		if err := s.writeBlob(blobPath, p.Ciphertext); err != nil {
			return nil, err
		}
	} else {
		inline = p.Ciphertext
	}

	v, err := s.writer.Submit(ctx, func(tx *sql.Tx) (any, error) {
		return s.enqueueMessageTx(ctx, tx, p, inline, blobPath, now, expiresAt)
	})
	if err != nil {
		s.removeBlob(blobPath)
		return nil, err
	}
	return v.(*EnqueueResult), nil
}

func (s *Store) enqueueMessageTx(ctx context.Context, tx *sql.Tx, p EnqueueMessageParams, inline []byte, blobPath string, now, expiresAt int64) (any, error) {
	// Guard against a race that inserted the same message between the pre-check
	// and here (single-writer makes this rare, but stay correct).
	var exists int
	if err := tx.QueryRowContext(ctx,
		"SELECT 1 FROM messages WHERE bridge_id = ? AND sender_land_id = ? AND id = ?",
		p.BridgeID, p.SenderLandID, p.MsgID).Scan(&exists); err == nil {
		return nil, ErrDuplicateMsgID
	} else if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}

	// Quota (DEV_SPEC §9: 507 when the bridge byte quota would be exceeded).
	var queueBytes, quota int64
	if err := tx.QueryRowContext(ctx,
		"SELECT queue_bytes, queue_quota_bytes FROM bridges WHERE id = ?", p.BridgeID).Scan(&queueBytes, &quota); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	if queueBytes+p.SizeBytes > quota {
		return nil, ErrQuotaExceeded
	}

	// Per-recipient inbox item cap (DEV_SPEC §9: 507 when full).
	for _, r := range p.Recipients {
		var n int
		if err := tx.QueryRowContext(ctx,
			"SELECT COUNT(*) FROM inbox_queue WHERE recipient_land_id = ? AND bridge_id = ?",
			r.LandID, p.BridgeID).Scan(&n); err != nil {
			return nil, err
		}
		if n >= s.maxInboxItemsPerLand {
			return nil, ErrInboxFull
		}
	}

	// Store the ciphertext once.
	if _, err := tx.ExecContext(ctx, `
INSERT INTO messages (id, bridge_id, sender_land_id, header_json, signature, ciphertext_sha256,
  ciphertext, blob_path, size_bytes, refcount, created_at, expires_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		p.MsgID, p.BridgeID, p.SenderLandID, p.HeaderJSON, p.Signature, p.CiphertextSHA256,
		inline, nullString(blobPath), p.SizeBytes, len(p.Recipients), now, expiresAt); err != nil {
		return nil, err
	}

	// Fan out: one inbox row + one delivery_status row per recipient.
	deliveries := make([]Delivery, 0, len(p.Recipients))
	for _, r := range p.Recipients {
		envID := newID(prefixEnv)
		if _, err := tx.ExecContext(ctx, `
INSERT INTO inbox_queue (id, bridge_id, recipient_land_id, kind, sender_land_id, msg_id,
  wrapped_key, size_bytes, enqueued_at, expires_at, fetch_count)
VALUES (?, ?, ?, 'message', ?, ?, ?, ?, ?, ?, 0)`,
			envID, p.BridgeID, r.LandID, p.SenderLandID, p.MsgID, r.WrappedKey,
			int64(len(r.WrappedKey)), now, expiresAt); err != nil {
			return nil, err
		}
		if _, err := tx.ExecContext(ctx, `
INSERT INTO delivery_status (bridge_id, sender_land_id, msg_id, recipient_land_id, status, updated_at)
VALUES (?, ?, ?, ?, 'queued', ?)`,
			p.BridgeID, p.SenderLandID, p.MsgID, r.LandID, now); err != nil {
			return nil, err
		}
		deliveries = append(deliveries, Delivery{RecipientLandID: r.LandID, Status: "queued", UpdatedAt: now})
	}

	// Charge the quota once for the shared ciphertext (DEV_SPEC §19).
	if err := execAffecting(ctx, tx,
		"UPDATE bridges SET queue_bytes = queue_bytes + ? WHERE id = ?", p.SizeBytes, p.BridgeID); err != nil {
		return nil, err
	}

	return &EnqueueResult{MsgID: p.MsgID, ExpiresAt: expiresAt, Deliveries: deliveries, Created: true}, nil
}

// EnqueueEvent fans a plaintext event (DEV_SPEC §6.4 event types) out to the
// given recipients, charging each event row against the bridge quota. It
// returns the created inbox ids. Recipients at their item cap are skipped (an
// event must not fail the triggering request); such skips are rare in practice.
func (s *Store) EnqueueEvent(ctx context.Context, bridgeID string, recipientLandIDs []string, eventJSON []byte) ([]string, error) {
	now := s.nowMillis()
	expiresAt := now + s.ttl.Milliseconds()
	size := int64(len(eventJSON))

	v, err := s.writer.Submit(ctx, func(tx *sql.Tx) (any, error) {
		var quota, queueBytes int64
		if err := tx.QueryRowContext(ctx,
			"SELECT queue_bytes, queue_quota_bytes FROM bridges WHERE id = ?", bridgeID).Scan(&queueBytes, &quota); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return nil, ErrNotFound
			}
			return nil, err
		}
		ids := make([]string, 0, len(recipientLandIDs))
		for _, rid := range recipientLandIDs {
			var n int
			if err := tx.QueryRowContext(ctx,
				"SELECT COUNT(*) FROM inbox_queue WHERE recipient_land_id = ? AND bridge_id = ?",
				rid, bridgeID).Scan(&n); err != nil {
				return nil, err
			}
			if n >= s.maxInboxItemsPerLand {
				continue
			}
			if queueBytes+size > quota {
				return nil, ErrQuotaExceeded
			}
			envID := newID(prefixEnv)
			if _, err := tx.ExecContext(ctx, `
INSERT INTO inbox_queue (id, bridge_id, recipient_land_id, kind, event_json, size_bytes,
  enqueued_at, expires_at, fetch_count)
VALUES (?, ?, ?, 'event', ?, ?, ?, ?, 0)`,
				envID, bridgeID, rid, eventJSON, size, now, expiresAt); err != nil {
				return nil, err
			}
			queueBytes += size
			ids = append(ids, envID)
		}
		if len(ids) > 0 {
			if err := execAffecting(ctx, tx,
				"UPDATE bridges SET queue_bytes = queue_bytes + ? WHERE id = ?", size*int64(len(ids)), bridgeID); err != nil {
				return nil, err
			}
		}
		return ids, nil
	})
	if err != nil {
		return nil, err
	}
	return v.([]string), nil
}

// UnexpiredEventJSON returns the event_json of every unexpired event item in a
// recipient's inbox (DEV_SPEC §11 re-delivery): the admin's GET /inbox uses it
// to learn which land_joined notices are still queued so it does not rebuild a
// duplicate of one that has not yet expired. event_json stays opaque to the
// store; the caller interprets it. The row count is bounded by the per-land
// inbox item cap.
func (s *Store) UnexpiredEventJSON(ctx context.Context, bridgeID, recipientLandID string, nowMillis int64) ([][]byte, error) {
	rows, err := s.read.QueryContext(ctx,
		"SELECT event_json FROM inbox_queue WHERE recipient_land_id = ? AND bridge_id = ? AND kind = 'event' AND expires_at > ?",
		recipientLandID, bridgeID, nowMillis)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out [][]byte
	for rows.Next() {
		var ev []byte
		if err := rows.Scan(&ev); err != nil {
			return nil, err
		}
		out = append(out, ev)
	}
	return out, rows.Err()
}

// FetchInbox leases up to limit available items for a recipient (DEV_SPEC §6.4
// GET /inbox, §10 lease): items whose lease is free and that have not expired,
// ordered by (enqueued_at, id). It sets lease_until = now + lease, increments
// fetch_count, and updates the land's last_polled_at. Blob-backed ciphertext is
// loaded after the transaction commits.
func (s *Store) FetchInbox(ctx context.Context, bridgeID, recipientLandID string, limit int) ([]InboxItem, error) {
	if limit < 1 {
		limit = 50
	}
	now := s.nowMillis()
	leaseUntil := now + s.lease.Milliseconds()

	type pending struct {
		item     InboxItem
		blobPath string
	}

	v, err := s.writer.Submit(ctx, func(tx *sql.Tx) (any, error) {
		// Touch last_polled_at (no error if the land row is absent).
		if _, err := tx.ExecContext(ctx,
			"UPDATE lands SET last_polled_at = ? WHERE id = ?", now, recipientLandID); err != nil {
			return nil, err
		}

		rows, err := tx.QueryContext(ctx, `
SELECT id, kind, sender_land_id, msg_id, wrapped_key, event_json, enqueued_at, expires_at, fetch_count
FROM inbox_queue
WHERE recipient_land_id = ? AND bridge_id = ? AND expires_at > ?
  AND (lease_until IS NULL OR lease_until < ?)
ORDER BY enqueued_at, id
LIMIT ?`, recipientLandID, bridgeID, now, now, limit)
		if err != nil {
			return nil, err
		}

		var items []pending
		var ids []string
		for rows.Next() {
			var (
				it      InboxItem
				sender  sql.NullString
				msgID   sql.NullString
				wrapped []byte
				event   []byte
			)
			if err := rows.Scan(&it.ID, &it.Kind, &sender, &msgID, &wrapped, &event,
				&it.EnqueuedAt, &it.ExpiresAt, &it.FetchCount); err != nil {
				rows.Close()
				return nil, err
			}
			it.SenderLandID = sender.String
			it.MsgID = msgID.String
			it.WrappedKey = wrapped
			it.EventJSON = event
			it.FetchCount++ // reflect the increment we are about to apply
			items = append(items, pending{item: it})
			ids = append(ids, it.ID)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, err
		}
		rows.Close()

		// Apply the lease and fetch_count increment.
		for _, id := range ids {
			if _, err := tx.ExecContext(ctx,
				"UPDATE inbox_queue SET lease_until = ?, fetch_count = fetch_count + 1 WHERE id = ?",
				leaseUntil, id); err != nil {
				return nil, err
			}
		}

		// Resolve message bodies (header + inline ciphertext / blob path).
		for i := range items {
			if items[i].item.Kind != "message" {
				continue
			}
			var (
				header []byte
				sig    []byte
				cipher []byte
				blob   sql.NullString
			)
			if err := tx.QueryRowContext(ctx,
				"SELECT header_json, signature, ciphertext, blob_path FROM messages WHERE bridge_id = ? AND sender_land_id = ? AND id = ?",
				bridgeID, items[i].item.SenderLandID, items[i].item.MsgID).Scan(&header, &sig, &cipher, &blob); err != nil {
				return nil, err
			}
			items[i].item.HeaderJSON = header
			items[i].item.Signature = sig
			items[i].item.Ciphertext = cipher
			items[i].blobPath = blob.String
		}
		return items, nil
	})
	if err != nil {
		return nil, err
	}

	pendings := v.([]pending)
	out := make([]InboxItem, 0, len(pendings))
	for _, p := range pendings {
		if p.item.Kind == "message" && p.blobPath != "" {
			data, err := s.readBlob(p.blobPath)
			if err != nil {
				return nil, err
			}
			p.item.Ciphertext = data
		}
		out = append(out, p.item)
	}
	return out, nil
}

// AckResult reports how many items were deleted and which ids were unknown
// (already deleted / not owned) — unknown ids are not an error (DEV_SPEC §6.4).
type AckResult struct {
	Deleted int
	Unknown []string
}

// Ack hard-deletes the caller's inbox items (DEV_SPEC §6.4 ack, §10). For
// message items it sets delivery_status=delivered and decrements the message
// refcount, deleting the shared ciphertext (and crediting the quota back) when
// the refcount reaches zero. For event items it credits the event's bytes back.
func (s *Store) Ack(ctx context.Context, bridgeID, recipientLandID string, ids []string) (*AckResult, error) {
	now := s.nowMillis()

	v, err := s.writer.Submit(ctx, func(tx *sql.Tx) (any, error) {
		res := &AckResult{}
		var blobs []string
		var joinedLandIDs []string // land_joined events acked here (watermark, §11)
		for _, id := range ids {
			var (
				kind   string
				sender sql.NullString
				msgID  sql.NullString
				size   int64
				event  []byte
			)
			err := tx.QueryRowContext(ctx,
				"SELECT kind, sender_land_id, msg_id, size_bytes, event_json FROM inbox_queue WHERE id = ? AND recipient_land_id = ? AND bridge_id = ?",
				id, recipientLandID, bridgeID).Scan(&kind, &sender, &msgID, &size, &event)
			if errors.Is(err, sql.ErrNoRows) {
				res.Unknown = append(res.Unknown, id)
				continue
			}
			if err != nil {
				return nil, err
			}
			if _, err := tx.ExecContext(ctx, "DELETE FROM inbox_queue WHERE id = ?", id); err != nil {
				return nil, err
			}
			res.Deleted++

			if kind != "message" {
				// Event: credit its bytes back to the quota.
				if err := execAffecting(ctx, tx,
					"UPDATE bridges SET queue_bytes = queue_bytes - ? WHERE id = ?", size, bridgeID); err != nil {
					return nil, err
				}
				if lid := ackedJoinLandID(event); lid != "" {
					joinedLandIDs = append(joinedLandIDs, lid)
				}
				continue
			}

			// Message: mark delivered and decrement the refcount.
			if _, err := tx.ExecContext(ctx,
				"UPDATE delivery_status SET status = 'delivered', updated_at = ? WHERE bridge_id = ? AND sender_land_id = ? AND msg_id = ? AND recipient_land_id = ?",
				now, bridgeID, sender.String, msgID.String, recipientLandID); err != nil {
				return nil, err
			}
			blob, err := s.decRefAndMaybeDelete(ctx, tx, bridgeID, sender.String, msgID.String)
			if err != nil {
				return nil, err
			}
			if blob != "" {
				blobs = append(blobs, blob)
			}
		}
		// Re-delivery watermark (DEV_SPEC §11): when the admin acks land_joined
		// notices (original or rebuilt), advance admin_joins_acked_through to the
		// latest acked land's joined_at so those joins are never rebuilt again.
		if len(joinedLandIDs) > 0 {
			if err := advanceAdminJoinsWatermark(ctx, tx, bridgeID, recipientLandID, joinedLandIDs); err != nil {
				return nil, err
			}
		}
		return ackTxResult{res: res, blobs: blobs}, nil
	})
	if err != nil {
		return nil, err
	}
	out := v.(ackTxResult)
	for _, b := range out.blobs {
		s.removeBlob(b)
	}
	return out.res, nil
}

type ackTxResult struct {
	res   *AckResult
	blobs []string
}

// decRefAndMaybeDelete decrements a message's refcount and, if it hits zero,
// deletes the message, credits its size back to the bridge quota, and returns
// the blob path (if any) for the caller to remove after commit.
func (s *Store) decRefAndMaybeDelete(ctx context.Context, tx *sql.Tx, bridgeID, senderLandID, msgID string) (string, error) {
	if _, err := tx.ExecContext(ctx,
		"UPDATE messages SET refcount = refcount - 1 WHERE bridge_id = ? AND sender_land_id = ? AND id = ?",
		bridgeID, senderLandID, msgID); err != nil {
		return "", err
	}
	var (
		refcount int
		size     int64
		blob     sql.NullString
	)
	err := tx.QueryRowContext(ctx,
		"SELECT refcount, size_bytes, blob_path FROM messages WHERE bridge_id = ? AND sender_land_id = ? AND id = ?",
		bridgeID, senderLandID, msgID).Scan(&refcount, &size, &blob)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if refcount > 0 {
		return "", nil
	}
	if _, err := tx.ExecContext(ctx,
		"DELETE FROM messages WHERE bridge_id = ? AND sender_land_id = ? AND id = ?",
		bridgeID, senderLandID, msgID); err != nil {
		return "", err
	}
	if err := execAffecting(ctx, tx,
		"UPDATE bridges SET queue_bytes = queue_bytes - ? WHERE id = ?", size, bridgeID); err != nil {
		return "", err
	}
	return blob.String, nil
}

// messageMeta is the subset of a message row used for idempotency checks.
type messageMeta struct {
	headerJSON []byte
	ctHash     []byte
	size       int64
	expiresAt  int64
}

// getMessageMeta reads a message's identity fields, or ErrNotFound.
func (s *Store) getMessageMeta(ctx context.Context, bridgeID, senderLandID, msgID string) (*messageMeta, error) {
	var m messageMeta
	err := s.read.QueryRowContext(ctx,
		"SELECT header_json, ciphertext_sha256, size_bytes, expires_at FROM messages WHERE bridge_id = ? AND sender_land_id = ? AND id = ?",
		bridgeID, senderLandID, msgID).Scan(&m.headerJSON, &m.ctHash, &m.size, &m.expiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &m, nil
}

// ackedJoinLandID returns the land_id of a land_joined event blob, or "" if the
// blob is not a land_joined event. It is the one place the store peeks inside an
// otherwise-opaque event_json, kept to the minimal shape it needs (DEV_SPEC §11).
func ackedJoinLandID(eventJSON []byte) string {
	if len(eventJSON) == 0 {
		return ""
	}
	var ev struct {
		Type string `json:"type"`
		Land struct {
			LandID string `json:"land_id"`
		} `json:"land"`
	}
	if err := json.Unmarshal(eventJSON, &ev); err != nil {
		return ""
	}
	if ev.Type != "land_joined" {
		return ""
	}
	return ev.Land.LandID
}

// advanceAdminJoinsWatermark moves bridges.admin_joins_acked_through forward to
// the greatest joined_at among the acked land_joined lands, but only when the
// acking recipient is the bridge admin. The UPDATE guard keeps it monotonic
// (DEV_SPEC §11).
func advanceAdminJoinsWatermark(ctx context.Context, tx *sql.Tx, bridgeID, recipientLandID string, landIDs []string) error {
	var admin sql.NullString
	err := tx.QueryRowContext(ctx, "SELECT admin_land_id FROM bridges WHERE id = ?", bridgeID).Scan(&admin)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if !admin.Valid || admin.String != recipientLandID {
		return nil
	}
	var maxJoined int64
	for _, lid := range landIDs {
		var jat int64
		e := tx.QueryRowContext(ctx, "SELECT joined_at FROM lands WHERE id = ?", lid).Scan(&jat)
		if errors.Is(e, sql.ErrNoRows) {
			continue
		}
		if e != nil {
			return e
		}
		if jat > maxJoined {
			maxJoined = jat
		}
	}
	if maxJoined == 0 {
		return nil
	}
	_, err = tx.ExecContext(ctx,
		"UPDATE bridges SET admin_joins_acked_through = ? WHERE id = ? AND admin_joins_acked_through < ?",
		maxJoined, bridgeID, maxJoined)
	return err
}

// nullString maps "" to a NULL for optional TEXT columns.
func nullString(s string) any {
	if s == "" {
		return nil
	}
	return s
}
