-- migrations/0001_init.sql
-- Initial bot-bridge schema (DEV_SPEC §5). Times are Unix milliseconds (UTC).
-- IDs are a prefix + 22-char base62 (>=128 bit, crypto/rand). All binary
-- columns hold raw bytes. Connection pragmas (WAL, foreign_keys, busy_timeout,
-- synchronous=NORMAL) are set by the store when opening, not here.

CREATE TABLE bridges (
  id                    TEXT PRIMARY KEY,              -- br_xxxxxxxxxxxxxxxxxxxxxx
  name                  TEXT NOT NULL,                 -- 1..80 chars
  description           TEXT NOT NULL DEFAULT '',      -- 0..500 chars
  status                TEXT NOT NULL CHECK (status IN ('active','closed')),
  admin_land_id         TEXT,                          -- set right after creator land row
  invite_code_enc       BLOB,                          -- AEAD(server INVITE_KEY, code); NULL after close
  invite_code_rotated_at INTEGER,
  max_lands             INTEGER NOT NULL DEFAULT 32,
  land_set_version      INTEGER NOT NULL DEFAULT 1,
  lands_can_invite      INTEGER NOT NULL DEFAULT 1,    -- 0/1: members may read invite code
  join_locked           INTEGER NOT NULL DEFAULT 0,    -- 0/1
  join_locked_reason    TEXT,                          -- 'admin' | 'brute_force'
  rate_limit_per_min    INTEGER NOT NULL DEFAULT 30,   -- 1..600
  queue_quota_bytes     INTEGER NOT NULL DEFAULT 2147483648,
  queue_bytes           INTEGER NOT NULL DEFAULT 0,
  admin_last_active_at  INTEGER NOT NULL,
  created_at            INTEGER NOT NULL,
  closed_at             INTEGER,
  close_reason          TEXT CHECK (close_reason IN ('admin_left','admin_keys_revoked','admin_inactive','admin_closed'))
);

CREATE TABLE lands (
  id                TEXT PRIMARY KEY,                  -- land_xxx
  bridge_id         TEXT NOT NULL REFERENCES bridges(id) ON DELETE CASCADE,
  role              TEXT NOT NULL CHECK (role IN ('admin','member')),
  handle            TEXT NOT NULL,                     -- ^[a-z0-9][a-z0-9_-]{1,31}$, unique per bridge
  display_name      TEXT NOT NULL DEFAULT '',
  owner_name        TEXT NOT NULL DEFAULT '',          -- unverified
  active_key_id     TEXT,
  poll_interval_sec INTEGER NOT NULL DEFAULT 1800,     -- 60..86400
  last_polled_at    INTEGER,
  joined_at         INTEGER NOT NULL,
  left_at           INTEGER,
  left_reason       TEXT CHECK (left_reason IN ('left','kicked','bridge_closed'))
);
CREATE UNIQUE INDEX lands_bridge_handle ON lands(bridge_id, handle) WHERE left_at IS NULL;
CREATE INDEX lands_bridge_active ON lands(bridge_id) WHERE left_at IS NULL;

CREATE TABLE public_keys (
  id            TEXT PRIMARY KEY,                      -- pk_xxx
  land_id       TEXT NOT NULL REFERENCES lands(id) ON DELETE CASCADE,
  enc_pub       BLOB NOT NULL CHECK (length(enc_pub) = 32),   -- X25519
  sign_pub      BLOB NOT NULL CHECK (length(sign_pub) = 32),  -- Ed25519
  binding_sig   BLOB NOT NULL CHECK (length(binding_sig) = 64),
  fingerprint   TEXT NOT NULL,                         -- 30 digits, see §7.7
  created_at    INTEGER NOT NULL,
  superseded_at INTEGER
);

CREATE TABLE api_keys (
  id           TEXT PRIMARY KEY,                       -- key_xxx
  land_id      TEXT NOT NULL REFERENCES lands(id) ON DELETE CASCADE,
  prefix       TEXT NOT NULL,                          -- first 8 chars after 'bbk_', for display
  key_hash     BLOB NOT NULL UNIQUE,                   -- HMAC-SHA256(API_KEY_PEPPER, full key)
  created_at   INTEGER NOT NULL,
  last_used_at INTEGER,
  revoked_at   INTEGER
);

-- ciphertext stored once per message; inbox rows reference it
CREATE TABLE messages (
  id                TEXT NOT NULL,                     -- client msg_id (unique per sender)
  bridge_id         TEXT NOT NULL REFERENCES bridges(id) ON DELETE CASCADE,
  sender_land_id    TEXT NOT NULL,
  header_json       BLOB NOT NULL,                     -- signed header (§7.4), opaque to server except validation
  signature         BLOB,                              -- Ed25519 envelope signature (§7.5); returned by GET /inbox
  ciphertext_sha256 BLOB,                              -- SHA-256(ciphertext) (§7.4), for idempotency compare without touching blobs
  ciphertext        BLOB,                              -- NULL if stored in blob file
  blob_path         TEXT,
  size_bytes        INTEGER NOT NULL,                  -- header + ciphertext (+ wrapped keys)
  refcount          INTEGER NOT NULL,                  -- pending inbox rows
  created_at        INTEGER NOT NULL,
  expires_at        INTEGER NOT NULL,                  -- created_at + 24h
  PRIMARY KEY (bridge_id, sender_land_id, id)
);
CREATE INDEX messages_expires ON messages(expires_at);

CREATE TABLE inbox_queue (
  id                TEXT PRIMARY KEY,                  -- env_xxx
  bridge_id         TEXT NOT NULL REFERENCES bridges(id) ON DELETE CASCADE,
  recipient_land_id TEXT NOT NULL,
  kind              TEXT NOT NULL CHECK (kind IN ('message','event')),
  sender_land_id    TEXT,                              -- kind=message
  msg_id            TEXT,                              -- kind=message
  wrapped_key       BLOB,                              -- kind=message, 80 bytes
  event_json        BLOB,                              -- kind=event (plaintext metadata only)
  size_bytes        INTEGER NOT NULL,
  enqueued_at       INTEGER NOT NULL,
  expires_at        INTEGER NOT NULL,
  lease_until       INTEGER,                           -- fetched but not acked
  fetch_count       INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX inbox_recipient ON inbox_queue(recipient_land_id, enqueued_at, id);
CREATE INDEX inbox_expires ON inbox_queue(expires_at);

CREATE TABLE delivery_status (
  bridge_id         TEXT NOT NULL,
  sender_land_id    TEXT NOT NULL,
  msg_id            TEXT NOT NULL,
  recipient_land_id TEXT NOT NULL,
  status            TEXT NOT NULL CHECK (status IN ('queued','delivered','expired','dropped')),
  updated_at        INTEGER NOT NULL,
  PRIMARY KEY (bridge_id, sender_land_id, msg_id, recipient_land_id)
);
CREATE INDEX delivery_status_updated ON delivery_status(updated_at);

CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY, applied_at INTEGER NOT NULL);
