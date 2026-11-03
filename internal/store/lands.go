package store

import (
	"context"
	"database/sql"
	"errors"
)

// CreateLandParams carries the fields for a new land plus its first public key.
// The land and its key are inserted atomically and active_key_id is set to the
// new key (DEV_SPEC §5, §6.4 create/join).
type CreateLandParams struct {
	BridgeID        string
	Role            string // admin | member
	Handle          string
	DisplayName     string
	OwnerName       string
	PollIntervalSec int

	EncPub      []byte // 32
	SignPub     []byte // 32
	BindingSig  []byte // 64
	Fingerprint string

	// ConsumeInvite atomically consumes the bridge's one-time invite code in the
	// same transaction as the insert (red-team RT-07: concurrent joins with one
	// code). If the code was already used, nothing is inserted and
	// ErrInviteConsumed is returned.
	ConsumeInvite bool
}

// CreateLand inserts a land and its initial public key in one transaction,
// returning the land id and key id.
func (s *Store) CreateLand(ctx context.Context, p CreateLandParams) (landID, keyID string, err error) {
	now := s.nowMillis()
	landID = newID(prefixLand)
	keyID = newID(prefixPubKey)
	poll := p.PollIntervalSec
	if poll == 0 {
		poll = DefaultPollIntervalSec
	}
	_, err = s.writer.Submit(ctx, func(tx *sql.Tx) (any, error) {
		if p.ConsumeInvite {
			res, err := tx.ExecContext(ctx,
				"UPDATE bridges SET invite_code_used = 1 WHERE id = ? AND status = 'active' AND invite_code_used = 0",
				p.BridgeID)
			if err != nil {
				return nil, err
			}
			if n, err := res.RowsAffected(); err != nil {
				return nil, err
			} else if n == 0 {
				return nil, ErrInviteConsumed
			}
		}
		if _, err := tx.ExecContext(ctx, `
INSERT INTO lands (id, bridge_id, role, handle, display_name, owner_name, active_key_id,
  poll_interval_sec, joined_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			landID, p.BridgeID, p.Role, p.Handle, p.DisplayName, p.OwnerName, keyID, poll, now); err != nil {
			return nil, err
		}
		if _, err := tx.ExecContext(ctx, `
INSERT INTO public_keys (id, land_id, enc_pub, sign_pub, binding_sig, fingerprint, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?)`,
			keyID, landID, p.EncPub, p.SignPub, p.BindingSig, p.Fingerprint, now); err != nil {
			return nil, err
		}
		return nil, nil
	})
	if err != nil {
		return "", "", err
	}
	return landID, keyID, nil
}

const landColumns = `id, bridge_id, role, handle, display_name, owner_name, active_key_id,
  poll_interval_sec, last_polled_at, joined_at, left_at, left_reason`

// GetLand returns a land by id, or ErrNotFound.
func (s *Store) GetLand(ctx context.Context, id string) (*Land, error) {
	row := s.read.QueryRowContext(ctx, "SELECT "+landColumns+" FROM lands WHERE id = ?", id)
	l, err := scanLand(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return l, err
}

// ListActiveLands returns all lands of a bridge that have not left, ordered by
// id for stable output (DEV_SPEC §6.4 GET /lands).
func (s *Store) ListActiveLands(ctx context.Context, bridgeID string) ([]*Land, error) {
	rows, err := s.read.QueryContext(ctx,
		"SELECT "+landColumns+" FROM lands WHERE bridge_id = ? AND left_at IS NULL ORDER BY id", bridgeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Land
	for rows.Next() {
		l, err := scanLand(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// LandsJoinedAfter returns the active member lands of a bridge whose joined_at
// is strictly greater than afterMillis, ordered by (joined_at, id). It drives
// the admin land_joined re-delivery (DEV_SPEC §11): the admin role is excluded
// (it never notifies itself) and lands that have left are excluded, so a land
// that joined then left is never reported as joined.
func (s *Store) LandsJoinedAfter(ctx context.Context, bridgeID string, afterMillis int64) ([]*Land, error) {
	rows, err := s.read.QueryContext(ctx,
		"SELECT "+landColumns+" FROM lands WHERE bridge_id = ? AND role = 'member' AND left_at IS NULL AND joined_at > ? ORDER BY joined_at, id",
		bridgeID, afterMillis)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Land
	for rows.Next() {
		l, err := scanLand(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// SetLandActiveKey points a land at a new active public key (DEV_SPEC §6.4
// PUT /me/public-key). The caller inserts the key and marks the old one
// superseded via InsertPublicKey/SupersedeKey.
func (s *Store) SetLandActiveKey(ctx context.Context, landID, keyID string) error {
	_, err := s.writer.Submit(ctx, func(tx *sql.Tx) (any, error) {
		return nil, execAffecting(ctx, tx, "UPDATE lands SET active_key_id = ? WHERE id = ?", keyID, landID)
	})
	return err
}

// MarkLandLeft records that a land left/was kicked (DEV_SPEC §6.4 DELETE land).
// Its inbox cleanup and delivery_status transitions are handled separately.
func (s *Store) MarkLandLeft(ctx context.Context, landID, reason string) error {
	now := s.nowMillis()
	_, err := s.writer.Submit(ctx, func(tx *sql.Tx) (any, error) {
		return nil, execAffecting(ctx, tx,
			"UPDATE lands SET left_at = ?, left_reason = ? WHERE id = ? AND left_at IS NULL", now, reason, landID)
	})
	return err
}

func scanLand(sc interface{ Scan(...any) error }) (*Land, error) {
	var l Land
	if err := sc.Scan(
		&l.ID, &l.BridgeID, &l.Role, &l.Handle, &l.DisplayName, &l.OwnerName, &l.ActiveKeyID,
		&l.PollIntervalSec, &l.LastPolledAt, &l.JoinedAt, &l.LeftAt, &l.LeftReason,
	); err != nil {
		return nil, err
	}
	return &l, nil
}

// DefaultPollIntervalSec is the poll interval a new land reports: the 5-minute
// routine the manuals tell bots to create.
const DefaultPollIntervalSec = 300
