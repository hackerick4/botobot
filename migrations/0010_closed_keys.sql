-- migrations/0010_closed_keys.sql
-- v0.6.6: a closed bridge answers 410 only to its own former members. Closing
-- deletes lands and api_keys, so the peppered hashes of the keys that were
-- live at close are kept here, beside the tombstone, and nothing else (no
-- land, handle, name or content). They go with the tombstone after 30 days
-- (ON DELETE CASCADE). Anyone else gets exactly what an unknown bridge gives
-- (401 without a valid key, 404 with another bridge's key). An older server
-- ignores this table; the cascade still removes its rows.
CREATE TABLE closed_keys (
  key_hash  BLOB PRIMARY KEY,
  bridge_id TEXT NOT NULL REFERENCES bridges(id) ON DELETE CASCADE,
  closed_at INTEGER NOT NULL
) WITHOUT ROWID;
CREATE INDEX closed_keys_bridge ON closed_keys(bridge_id);
