package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

// Public entries (v0.6.0, migration 0008). One reusable link per service bot;
// every guest that joins through it gets its own two-bot bridge whose admin
// land reuses the entry's public key. See DEV_SPEC §20.

// prefixEntry / prefixEntryLink are the entry id and public link id prefixes.
const (
	prefixEntry     = "en_"
	prefixEntryLink = "pe_"
)

// Entry status values.
const (
	EntryActive   = "active"
	EntryDisabled = "disabled"
	EntryClosed   = "closed"
)

// ErrEntryFull is returned by CreateEntryBridge when the entry already has
// max_open_bridges open bridges. ErrEntryInactive when it is not active.
var (
	ErrEntryFull     = errors.New("store: entry has too many open bridges")
	ErrEntryInactive = errors.New("store: entry is not active")
)

// Entry mirrors the entries table.
type Entry struct {
	ID             string
	LinkID         string
	Name           string
	Status         string
	KeyPrefix      string
	Handle         string
	DisplayName    string
	OwnerName      string
	EncPub         []byte
	SignPub        []byte
	BindingSig     []byte
	Fingerprint    string
	Lang           string
	MaxOpenBridges int
	JoinsPerHour   int
	CreatedAt      int64
	LastActiveAt   int64
	DisabledAt     *int64
	DisabledReason *string
	ClosedAt       *int64
}

// CreateEntryParams carries a new entry. KeyHash is HMAC(pepper, bbe_…).
type CreateEntryParams struct {
	Name           string
	KeyHash        []byte
	KeyPrefix      string
	Handle         string
	DisplayName    string
	OwnerName      string
	EncPub         []byte
	SignPub        []byte
	BindingSig     []byte
	Fingerprint    string
	Lang           string
	MaxOpenBridges int
	JoinsPerHour   int
	CreatedPrefix  string
}

const entryColumns = `id, link_id, name, status, key_prefix, handle, display_name, owner_name,
  enc_pub, sign_pub, binding_sig, fingerprint, lang, max_open_bridges, joins_per_hour,
  created_at, last_active_at, disabled_at, disabled_reason, closed_at`

func scanEntry(sc interface{ Scan(...any) error }) (*Entry, error) {
	var e Entry
	if err := sc.Scan(&e.ID, &e.LinkID, &e.Name, &e.Status, &e.KeyPrefix, &e.Handle,
		&e.DisplayName, &e.OwnerName, &e.EncPub, &e.SignPub, &e.BindingSig, &e.Fingerprint,
		&e.Lang, &e.MaxOpenBridges, &e.JoinsPerHour, &e.CreatedAt, &e.LastActiveAt,
		&e.DisabledAt, &e.DisabledReason, &e.ClosedAt); err != nil {
		return nil, err
	}
	return &e, nil
}

// CreateEntry inserts a new active entry and returns it.
func (s *Store) CreateEntry(ctx context.Context, p CreateEntryParams) (*Entry, error) {
	now := s.nowMillis()
	id, link := newID(prefixEntry), newID(prefixEntryLink)
	_, err := s.writer.Submit(ctx, func(tx *sql.Tx) (any, error) {
		_, err := tx.ExecContext(ctx, `
INSERT INTO entries (id, link_id, name, status, key_hash, key_prefix, handle, display_name, owner_name,
  enc_pub, sign_pub, binding_sig, fingerprint, lang, max_open_bridges, joins_per_hour,
  created_prefix, created_at, last_active_at)
VALUES (?, ?, ?, 'active', ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			id, link, p.Name, p.KeyHash, p.KeyPrefix, p.Handle, p.DisplayName, p.OwnerName,
			p.EncPub, p.SignPub, p.BindingSig, p.Fingerprint, p.Lang, p.MaxOpenBridges, p.JoinsPerHour,
			p.CreatedPrefix, now, now)
		return nil, err
	})
	if err != nil {
		return nil, err
	}
	return s.GetEntry(ctx, id)
}

// GetEntry returns an entry by id, or ErrNotFound.
func (s *Store) GetEntry(ctx context.Context, id string) (*Entry, error) {
	return s.entryWhere(ctx, "id = ?", id)
}

// GetEntryByLink returns an entry by its public link id, or ErrNotFound.
func (s *Store) GetEntryByLink(ctx context.Context, link string) (*Entry, error) {
	return s.entryWhere(ctx, "link_id = ?", link)
}

// GetEntryByKeyHash returns the entry whose key hash matches, or ErrNotFound.
func (s *Store) GetEntryByKeyHash(ctx context.Context, hash []byte) (*Entry, error) {
	return s.entryWhere(ctx, "key_hash = ?", hash)
}

func (s *Store) entryWhere(ctx context.Context, where string, arg any) (*Entry, error) {
	row := s.read.QueryRowContext(ctx, "SELECT "+entryColumns+" FROM entries WHERE "+where, arg)
	e, err := scanEntry(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return e, err
}

// CountEntriesByPrefix counts the not-closed entries created from one network
// prefix (the per-prefix entry cap).
func (s *Store) CountEntriesByPrefix(ctx context.Context, prefix string) (int, error) {
	var n int
	err := s.read.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM entries WHERE created_prefix = ? AND status != 'closed'", prefix).Scan(&n)
	return n, err
}

// UpdateEntryParams are the optional fields of PATCH /v1/entries/{id}.
type UpdateEntryParams struct {
	Name           *string
	Status         *string // active | disabled
	MaxOpenBridges *int
	JoinsPerHour   *int
}

// UpdateEntry applies p to an active or disabled entry.
func (s *Store) UpdateEntry(ctx context.Context, id string, p UpdateEntryParams) error {
	now := s.nowMillis()
	_, err := s.writer.Submit(ctx, func(tx *sql.Tx) (any, error) {
		var status string
		if err := tx.QueryRowContext(ctx, "SELECT status FROM entries WHERE id = ?", id).Scan(&status); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return nil, ErrNotFound
			}
			return nil, err
		}
		if status == EntryClosed {
			return nil, ErrEntryInactive
		}
		if p.Name != nil {
			if _, err := tx.ExecContext(ctx, "UPDATE entries SET name = ? WHERE id = ?", *p.Name, id); err != nil {
				return nil, err
			}
		}
		if p.MaxOpenBridges != nil {
			if _, err := tx.ExecContext(ctx, "UPDATE entries SET max_open_bridges = ? WHERE id = ?", *p.MaxOpenBridges, id); err != nil {
				return nil, err
			}
		}
		if p.JoinsPerHour != nil {
			if _, err := tx.ExecContext(ctx, "UPDATE entries SET joins_per_hour = ? WHERE id = ?", *p.JoinsPerHour, id); err != nil {
				return nil, err
			}
		}
		if p.Status != nil && *p.Status != status {
			var q string
			var args []any
			if *p.Status == EntryDisabled {
				q, args = "UPDATE entries SET status = 'disabled', disabled_at = ?, disabled_reason = 'owner' WHERE id = ?", []any{now, id}
			} else {
				q, args = "UPDATE entries SET status = 'active', disabled_at = NULL, disabled_reason = NULL, last_active_at = ? WHERE id = ?", []any{now, id}
			}
			if _, err := tx.ExecContext(ctx, q, args...); err != nil {
				return nil, err
			}
		}
		return nil, nil
	})
	return err
}

// RotateEntryLink gives the entry a new public link id; the old link stops
// working at once. Open bridges are untouched.
func (s *Store) RotateEntryLink(ctx context.Context, id string) (string, error) {
	link := newID(prefixEntryLink)
	_, err := s.writer.Submit(ctx, func(tx *sql.Tx) (any, error) {
		return nil, execAffecting(ctx, tx,
			"UPDATE entries SET link_id = ? WHERE id = ? AND status != 'closed'", link, id)
	})
	if err != nil {
		return "", err
	}
	return link, nil
}

// CloseEntry closes the entry for good (status closed, profile blanked) and
// returns the ids of its still-open bridges, which the caller closes.
func (s *Store) CloseEntry(ctx context.Context, id string) ([]string, error) {
	now := s.nowMillis()
	_, err := s.writer.Submit(ctx, func(tx *sql.Tx) (any, error) {
		return nil, execAffecting(ctx, tx, `
UPDATE entries SET status = 'closed', closed_at = ?, name = '', display_name = '', owner_name = ''
WHERE id = ? AND status != 'closed'`, now, id)
	})
	if err != nil && !errors.Is(err, ErrNotFound) {
		return nil, err
	}
	return s.ListEntryBridgeIDs(ctx, id)
}

// TouchEntryActive records entry activity (debounced by the caller).
func (s *Store) TouchEntryActive(ctx context.Context, id string, nowMillis int64) error {
	_, err := s.writer.Submit(ctx, func(tx *sql.Tx) (any, error) {
		_, err := tx.ExecContext(ctx, "UPDATE entries SET last_active_at = ? WHERE id = ?", nowMillis, id)
		return nil, err
	})
	return err
}

// CountOpenEntryBridges counts the entry's active bridges.
func (s *Store) CountOpenEntryBridges(ctx context.Context, id string) (int, error) {
	var n int
	err := s.read.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM bridges WHERE entry_id = ? AND status = 'active'", id).Scan(&n)
	return n, err
}

// ListEntryBridgeIDs returns the entry's active bridge ids, oldest first.
func (s *Store) ListEntryBridgeIDs(ctx context.Context, id string) ([]string, error) {
	rows, err := s.read.QueryContext(ctx,
		"SELECT id FROM bridges WHERE entry_id = ? AND status = 'active' ORDER BY created_at, id", id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var b string
		if err := rows.Scan(&b); err != nil {
			return nil, err
		}
		ids = append(ids, b)
	}
	return ids, rows.Err()
}

// EntryCloseLookback (v0.6.5): lines of an entry that closed at most this long
// before the entry itself count as ended by the entry close. A delete-all
// batch closes the lines a few seconds before the entry.
const EntryCloseLookback = 10 * time.Minute

// ClosedEntryBridge is one bridge of an entry that closed after a point in time.
type ClosedEntryBridge struct {
	BridgeID string
	ClosedAt int64
	Reason   string
}

// ListEntryBridgesClosedSince returns the entry's bridges closed after since
// (Unix ms), at most limit, oldest first. Tombstones keep entry_id.
func (s *Store) ListEntryBridgesClosedSince(ctx context.Context, id string, since int64, limit int) ([]ClosedEntryBridge, error) {
	rows, err := s.read.QueryContext(ctx, `
SELECT id, closed_at, COALESCE(close_reason, close_detail, '') FROM bridges
WHERE entry_id = ? AND status = 'closed' AND closed_at > ?
ORDER BY closed_at, id LIMIT ?`, id, since, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ClosedEntryBridge
	for rows.Next() {
		var c ClosedEntryBridge
		if err := rows.Scan(&c.BridgeID, &c.ClosedAt, &c.Reason); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// CreateEntryBridgeParams is one guest join through an entry.
type CreateEntryBridgeParams struct {
	EntryID         string
	RateLimitPerMin int
	QueueQuotaBytes int64

	GuestHandle      string
	GuestDisplayName string
	GuestOwnerName   string
	GuestEncPub      []byte
	GuestSignPub     []byte
	GuestBindingSig  []byte
	GuestFingerprint string
	GuestKeyPrefix   string
	GuestKeyHash     []byte
}

// CreateEntryBridgeResult names the rows created by CreateEntryBridge.
type CreateEntryBridgeResult struct {
	BridgeID    string
	AdminLandID string
	GuestLandID string
	Version     int64
}

// CreateEntryBridge creates, in one transaction, a two-bot bridge for a guest:
// the bridge row (pair = 1, invite used, entry_id set), the service land with
// the entry's public key as admin, the guest land with its key, and the
// guest's API key. It re-checks the entry is active and under its cap inside
// the transaction, so concurrent joins cannot overshoot max_open_bridges.
func (s *Store) CreateEntryBridge(ctx context.Context, p CreateEntryBridgeParams) (*CreateEntryBridgeResult, error) {
	now := s.nowMillis()
	res := &CreateEntryBridgeResult{
		BridgeID:    newID(prefixBridge),
		AdminLandID: newID(prefixLand),
		GuestLandID: newID(prefixLand),
		Version:     2,
	}
	adminKey, guestKey, apiKeyID := newID(prefixPubKey), newID(prefixPubKey), newID(prefixKey)
	_, err := s.writer.Submit(ctx, func(tx *sql.Tx) (any, error) {
		e, err := scanEntry(tx.QueryRowContext(ctx, "SELECT "+entryColumns+" FROM entries WHERE id = ?", p.EntryID))
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return nil, ErrNotFound
			}
			return nil, err
		}
		if e.Status != EntryActive {
			return nil, ErrEntryInactive
		}
		var open int
		if err := tx.QueryRowContext(ctx,
			"SELECT COUNT(*) FROM bridges WHERE entry_id = ? AND status = 'active'", p.EntryID).Scan(&open); err != nil {
			return nil, err
		}
		if open >= e.MaxOpenBridges {
			return nil, ErrEntryFull
		}
		if _, err := tx.ExecContext(ctx, `
INSERT INTO bridges (id, name, description, status, invite_code_enc, invite_code_rotated_at, invite_code_used,
  max_lands, land_set_version, lands_can_invite, join_locked, rate_limit_per_min,
  queue_quota_bytes, queue_bytes, admin_last_active_at, created_at, pending, pair, entry_id)
VALUES (?, ?, '', 'active', NULL, NULL, 1, 2, ?, 0, 0, ?, ?, 0, ?, ?, 0, 1, ?)`,
			res.BridgeID, e.Name, res.Version, p.RateLimitPerMin, p.QueueQuotaBytes, now, now, p.EntryID); err != nil {
			return nil, err
		}
		lands := []struct {
			id, role, handle, display, owner, key string
			enc, sign, bind                       []byte
			fp                                    string
		}{
			{res.AdminLandID, "admin", e.Handle, e.DisplayName, e.OwnerName, adminKey, e.EncPub, e.SignPub, e.BindingSig, e.Fingerprint},
			{res.GuestLandID, "member", p.GuestHandle, p.GuestDisplayName, p.GuestOwnerName, guestKey, p.GuestEncPub, p.GuestSignPub, p.GuestBindingSig, p.GuestFingerprint},
		}
		for i, l := range lands {
			// The guest joins a moment after the service land so ordering by
			// joined_at keeps the admin first.
			if _, err := tx.ExecContext(ctx, `
INSERT INTO lands (id, bridge_id, role, handle, display_name, owner_name, active_key_id, poll_interval_sec, joined_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				l.id, res.BridgeID, l.role, l.handle, l.display, l.owner, l.key, DefaultPollIntervalSec, now+int64(i)); err != nil {
				return nil, err
			}
			if _, err := tx.ExecContext(ctx, `
INSERT INTO public_keys (id, land_id, enc_pub, sign_pub, binding_sig, fingerprint, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?)`, l.key, l.id, l.enc, l.sign, l.bind, l.fp, now); err != nil {
				return nil, err
			}
		}
		if _, err := tx.ExecContext(ctx, "UPDATE bridges SET admin_land_id = ? WHERE id = ?", res.AdminLandID, res.BridgeID); err != nil {
			return nil, err
		}
		if _, err := tx.ExecContext(ctx,
			"INSERT INTO api_keys (id, land_id, prefix, key_hash, created_at) VALUES (?, ?, ?, ?, ?)",
			apiKeyID, res.GuestLandID, p.GuestKeyPrefix, p.GuestKeyHash, now); err != nil {
			return nil, err
		}
		return nil, nil
	})
	if err != nil {
		return nil, err
	}
	return res, nil
}

// EntryInboxItem is one leased inbox row of an entry's aggregated inbox.
type EntryInboxItem struct {
	BridgeID string
	InboxItem
}

// entryLineOpen selects the entry's lines whose service land may still fetch:
// open ones, and closed ones in their last-delivery window that a guest (not
// the service land) closed (v0.6.4). It takes one parameter: now (Unix ms).
const entryLineOpen = `(b.status = 'active' OR (b.grace_until > ? AND b.grace_closer IS NOT b.admin_land_id))`

// EntryHasInbox reports, with a read-only query, whether any of the entry's
// open bridges has a deliverable row for the service land. It lets an empty
// wake skip the write transaction entirely.
func (s *Store) EntryHasInbox(ctx context.Context, entryID string) (bool, error) {
	now := s.nowMillis()
	var one int
	err := s.read.QueryRowContext(ctx, `
SELECT 1 FROM bridges b JOIN inbox_queue q ON q.bridge_id = b.id AND q.recipient_land_id = b.admin_land_id
WHERE b.entry_id = ? AND `+entryLineOpen+` AND q.expires_at > ?
  AND (q.lease_until IS NULL OR q.lease_until < ?)
LIMIT 1`, entryID, now, now, now).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

// FetchEntryInbox leases up to limit deliverable rows addressed to the service
// land across all the entry's open bridges, oldest first, in one transaction.
func (s *Store) FetchEntryInbox(ctx context.Context, entryID string, limit int) ([]EntryInboxItem, error) {
	if limit < 1 {
		limit = 50
	}
	now := s.nowMillis()
	leaseUntil := now + s.lease.Milliseconds()
	type pending struct {
		item     EntryInboxItem
		blobPath string
	}
	v, err := s.writer.Submit(ctx, func(tx *sql.Tx) (any, error) {
		rows, err := tx.QueryContext(ctx, `
SELECT q.bridge_id, q.id, q.kind, q.sender_land_id, q.msg_id, q.wrapped_key, q.event_json,
  q.enqueued_at, q.expires_at, q.fetch_count
FROM bridges b JOIN inbox_queue q ON q.bridge_id = b.id AND q.recipient_land_id = b.admin_land_id
WHERE b.entry_id = ? AND `+entryLineOpen+` AND q.expires_at > ?
  AND (q.lease_until IS NULL OR q.lease_until < ?)
ORDER BY q.enqueued_at, q.id
LIMIT ?`, entryID, now, now, now, limit)
		if err != nil {
			return nil, err
		}
		var items []pending
		for rows.Next() {
			var (
				it      EntryInboxItem
				sender  sql.NullString
				msgID   sql.NullString
				wrapped []byte
				event   []byte
			)
			if err := rows.Scan(&it.BridgeID, &it.ID, &it.Kind, &sender, &msgID, &wrapped, &event,
				&it.EnqueuedAt, &it.ExpiresAt, &it.FetchCount); err != nil {
				rows.Close()
				return nil, err
			}
			it.SenderLandID, it.MsgID, it.WrappedKey, it.EventJSON = sender.String, msgID.String, wrapped, event
			it.FetchCount++
			items = append(items, pending{item: it})
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, err
		}
		rows.Close()
		for i := range items {
			if _, err := tx.ExecContext(ctx,
				"UPDATE inbox_queue SET lease_until = ?, fetch_count = fetch_count + 1 WHERE id = ?",
				leaseUntil, items[i].item.ID); err != nil {
				return nil, err
			}
			if items[i].item.Kind != "message" {
				continue
			}
			var blob sql.NullString
			if err := tx.QueryRowContext(ctx,
				"SELECT header_json, signature, ciphertext, blob_path FROM messages WHERE bridge_id = ? AND sender_land_id = ? AND id = ?",
				items[i].item.BridgeID, items[i].item.SenderLandID, items[i].item.MsgID).Scan(
				&items[i].item.HeaderJSON, &items[i].item.Signature, &items[i].item.Ciphertext, &blob); err != nil {
				return nil, err
			}
			items[i].blobPath = blob.String
		}
		// Polling marks every service land of the entry as polled.
		if len(items) > 0 {
			if _, err := tx.ExecContext(ctx, `
UPDATE lands SET last_polled_at = ? WHERE id IN (
  SELECT admin_land_id FROM bridges WHERE entry_id = ? AND status = 'active')`, now, entryID); err != nil {
				return nil, err
			}
		}
		return items, nil
	})
	if err != nil {
		return nil, err
	}
	pendings := v.([]pending)
	out := make([]EntryInboxItem, 0, len(pendings))
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

// EntryInboxOwners maps inbox row ids to their bridge for an entry ack: only
// rows addressed to the service land of one of the entry's open bridges are
// returned; anything else is silently left out.
func (s *Store) EntryInboxOwners(ctx context.Context, entryID string, ids []string) (map[string][]string, error) {
	out := map[string][]string{}
	if len(ids) == 0 {
		return out, nil
	}
	args := make([]any, 0, len(ids)+2)
	args = append(args, entryID, s.nowMillis())
	for _, id := range ids {
		args = append(args, id)
	}
	rows, err := s.read.QueryContext(ctx, `
SELECT q.id, q.bridge_id FROM inbox_queue q JOIN bridges b ON b.id = q.bridge_id AND q.recipient_land_id = b.admin_land_id
WHERE b.entry_id = ? AND `+entryLineOpen+` AND q.id IN (`+strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")+`)`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id, br string
		if err := rows.Scan(&id, &br); err != nil {
			return nil, err
		}
		out[br] = append(out[br], id)
	}
	return out, rows.Err()
}

// ListInactiveEntries returns active entries whose service bot has not polled
// or acted for at least the inactivity window.
func (s *Store) ListInactiveEntries(ctx context.Context, inactivity time.Duration) ([]string, error) {
	cutoff := s.nowMillis() - inactivity.Milliseconds()
	rows, err := s.read.QueryContext(ctx,
		"SELECT id FROM entries WHERE status = 'active' AND last_active_at <= ?", cutoff)
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

// DisableEntryInactive disables an entry after its service bot went quiet.
func (s *Store) DisableEntryInactive(ctx context.Context, id string) error {
	now := s.nowMillis()
	_, err := s.writer.Submit(ctx, func(tx *sql.Tx) (any, error) {
		_, err := tx.ExecContext(ctx,
			"UPDATE entries SET status = 'disabled', disabled_at = ?, disabled_reason = 'inactive' WHERE id = ? AND status = 'active'", now, id)
		return nil, err
	})
	return err
}
