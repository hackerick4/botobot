-- migrations/0007_pair_bridges.sql
-- v0.5.0: each new bridge connects exactly two bots (the admin and one peer).
-- pair = 1 marks such a bridge: a third land is refused (bridge_full), the
-- invite cannot be rotated once the peer has joined, and either side leaving
-- (or the admin removing the peer) closes the bridge for both.
-- Existing bridges keep pair = 0 and their old rules (grandfathered); nothing
-- is closed or changed by this migration.
ALTER TABLE bridges ADD COLUMN pair INTEGER NOT NULL DEFAULT 0;
-- close_reason carries a CHECK from 0001 that only knows the admin reasons.
-- Rebuilding bridges would cascade through lands, so the two new reasons
-- (member_left, member_removed) go into close_detail instead, with
-- close_reason left NULL. Readers use COALESCE(close_reason, close_detail).
ALTER TABLE bridges ADD COLUMN close_detail TEXT
  CHECK (close_detail IS NULL OR close_detail IN ('member_left','member_removed'));
