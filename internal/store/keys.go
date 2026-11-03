package store

import (
	"context"
	"database/sql"
	"errors"
)

// InsertPublicKeyParams is a new public key for an existing land (rekey path).
type InsertPublicKeyParams struct {
	LandID      string
	EncPub      []byte
	SignPub     []byte
	BindingSig  []byte
	Fingerprint string
}

// InsertPublicKey adds a public key and returns its id (DEV_SPEC §6.4 rekey).
// It does not change lands.active_key_id; call SetLandActiveKey for that.
func (s *Store) InsertPublicKey(ctx context.Context, p InsertPublicKeyParams) (string, error) {
	now := s.nowMillis()
	id := newID(prefixPubKey)
	_, err := s.writer.Submit(ctx, func(tx *sql.Tx) (any, error) {
		_, err := tx.ExecContext(ctx, `
INSERT INTO public_keys (id, land_id, enc_pub, sign_pub, binding_sig, fingerprint, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?)`,
			id, p.LandID, p.EncPub, p.SignPub, p.BindingSig, p.Fingerprint, now)
		return nil, err
	})
	if err != nil {
		return "", err
	}
	return id, nil
}

// SupersedeKey timestamps a key as superseded (DEV_SPEC §6.4: old key superseded
// on rekey; CLI keeps the old private key 24h to decrypt in-flight messages).
func (s *Store) SupersedeKey(ctx context.Context, keyID string) error {
	now := s.nowMillis()
	_, err := s.writer.Submit(ctx, func(tx *sql.Tx) (any, error) {
		return nil, execAffecting(ctx, tx,
			"UPDATE public_keys SET superseded_at = ? WHERE id = ? AND superseded_at IS NULL", now, keyID)
	})
	return err
}

const pubKeyColumns = `id, land_id, enc_pub, sign_pub, binding_sig, fingerprint, created_at, superseded_at`

// GetPublicKey returns a public key by id, or ErrNotFound.
func (s *Store) GetPublicKey(ctx context.Context, id string) (*PublicKey, error) {
	row := s.read.QueryRowContext(ctx, "SELECT "+pubKeyColumns+" FROM public_keys WHERE id = ?", id)
	k, err := scanPublicKey(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return k, err
}

func scanPublicKey(sc interface{ Scan(...any) error }) (*PublicKey, error) {
	var k PublicKey
	if err := sc.Scan(
		&k.ID, &k.LandID, &k.EncPub, &k.SignPub, &k.BindingSig, &k.Fingerprint, &k.CreatedAt, &k.SupersededAt,
	); err != nil {
		return nil, err
	}
	return &k, nil
}
