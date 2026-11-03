-- migrations/0008_public_entries.sql
-- v0.6.0: public entry. A service bot (shop, clinic, restaurant) publishes one
-- reusable link. Every guest bot that joins through it gets its own private
-- two-bot bridge (pair = 1) with the service bot; guests never share a bridge.
--
-- entries holds the service side once: its land profile, its public keys and
-- the HMAC of its entry key (bbe_…). Each bridge created through the entry
-- carries entry_id; its admin land reuses the entry's public key, and the entry
-- key authenticates as that admin land on those bridges only.
--
-- link_id is the public id in the link (/e/<link_id>); rotating it kills the
-- old link without touching the entry or its open bridges.
-- Existing bridges keep entry_id NULL and their old rules; nothing is changed.
CREATE TABLE entries (
  id               TEXT PRIMARY KEY,                 -- en_…
  link_id          TEXT NOT NULL UNIQUE,             -- pe_… (public)
  name             TEXT NOT NULL,
  status           TEXT NOT NULL DEFAULT 'active'
                   CHECK (status IN ('active','disabled','closed')),
  key_hash         BLOB NOT NULL UNIQUE,             -- HMAC(pepper, bbe_…)
  key_prefix       TEXT NOT NULL,
  handle           TEXT NOT NULL,
  display_name     TEXT NOT NULL,
  owner_name       TEXT NOT NULL DEFAULT '',
  enc_pub          BLOB NOT NULL,
  sign_pub         BLOB NOT NULL,
  binding_sig      BLOB NOT NULL,
  fingerprint      TEXT NOT NULL,
  lang             TEXT NOT NULL DEFAULT '',
  max_open_bridges INTEGER NOT NULL,
  joins_per_hour   INTEGER NOT NULL,
  created_prefix   TEXT NOT NULL DEFAULT '',          -- HMAC(pepper, /24) hex
  created_at       INTEGER NOT NULL,
  last_active_at   INTEGER NOT NULL,
  disabled_at      INTEGER,
  disabled_reason  TEXT,
  closed_at        INTEGER
);
CREATE INDEX entries_status ON entries(status);
CREATE INDEX entries_created_prefix ON entries(created_prefix, status);

ALTER TABLE bridges ADD COLUMN entry_id TEXT REFERENCES entries(id);
CREATE INDEX bridges_entry ON bridges(entry_id, status) WHERE entry_id IS NOT NULL;
