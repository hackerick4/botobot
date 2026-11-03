package store

import (
	"context"
	"database/sql"
	"errors"
)

// InsertAPIKeyParams records a new API key. Only the HMAC hash is stored; the
// plaintext key is returned to the caller once and never persisted (DEV_SPEC §5,
// §16).
type InsertAPIKeyParams struct {
	LandID  string
	Prefix  string // first 8 chars after 'bbk_', for display
	KeyHash []byte // HMAC-SHA256(pepper, full key)
}

// InsertAPIKey stores a new API key hash and returns its id.
func (s *Store) InsertAPIKey(ctx context.Context, p InsertAPIKeyParams) (string, error) {
	now := s.nowMillis()
	id := newID(prefixKey)
	_, err := s.writer.Submit(ctx, func(tx *sql.Tx) (any, error) {
		_, err := tx.ExecContext(ctx, `
INSERT INTO api_keys (id, land_id, prefix, key_hash, created_at) VALUES (?, ?, ?, ?, ?)`,
			id, p.LandID, p.Prefix, p.KeyHash, now)
		return nil, err
	})
	if err != nil {
		return "", err
	}
	return id, nil
}

const apiKeyColumns = `id, land_id, prefix, key_hash, created_at, last_used_at, revoked_at`

// GetAPIKeyByHash looks up a non-revoked API key by its hash (DEV_SPEC §6.1 auth,
// §16 constant-time: the caller computes the HMAC; lookup is by exact hash).
// Returns ErrNotFound when no live key matches.
func (s *Store) GetAPIKeyByHash(ctx context.Context, hash []byte) (*APIKey, error) {
	row := s.read.QueryRowContext(ctx,
		"SELECT "+apiKeyColumns+" FROM api_keys WHERE key_hash = ? AND revoked_at IS NULL", hash)
	k, err := scanAPIKey(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return k, err
}

// TouchAPIKeyUsed updates last_used_at. Best-effort; callers may batch this.
func (s *Store) TouchAPIKeyUsed(ctx context.Context, id string) error {
	now := s.nowMillis()
	_, err := s.writer.Submit(ctx, func(tx *sql.Tx) (any, error) {
		_, err := tx.ExecContext(ctx, "UPDATE api_keys SET last_used_at = ? WHERE id = ?", now, id)
		return nil, err
	})
	return err
}

// RevokeAPIKey marks one key revoked (DEV_SPEC §6.4 DELETE /me/api-keys/{id}).
func (s *Store) RevokeAPIKey(ctx context.Context, id string) error {
	now := s.nowMillis()
	_, err := s.writer.Submit(ctx, func(tx *sql.Tx) (any, error) {
		return nil, execAffecting(ctx, tx,
			"UPDATE api_keys SET revoked_at = ? WHERE id = ? AND revoked_at IS NULL", now, id)
	})
	return err
}

// RevokeLandAPIKeys revokes every live key of a land (used on leave/kick and on
// key rotation with revoke_others) and returns how many were revoked.
func (s *Store) RevokeLandAPIKeys(ctx context.Context, landID string, exceptID string) (int, error) {
	now := s.nowMillis()
	v, err := s.writer.Submit(ctx, func(tx *sql.Tx) (any, error) {
		res, err := tx.ExecContext(ctx,
			"UPDATE api_keys SET revoked_at = ? WHERE land_id = ? AND revoked_at IS NULL AND id != ?",
			now, landID, exceptID)
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

// CountActiveAPIKeys returns the number of live keys for a land (DEV_SPEC §6.4:
// revoking the admin's last key closes the bridge).
func (s *Store) CountActiveAPIKeys(ctx context.Context, landID string) (int, error) {
	var n int
	err := s.read.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM api_keys WHERE land_id = ? AND revoked_at IS NULL", landID).Scan(&n)
	return n, err
}

func scanAPIKey(sc interface{ Scan(...any) error }) (*APIKey, error) {
	var k APIKey
	if err := sc.Scan(
		&k.ID, &k.LandID, &k.Prefix, &k.KeyHash, &k.CreatedAt, &k.LastUsedAt, &k.RevokedAt,
	); err != nil {
		return nil, err
	}
	return &k, nil
}
