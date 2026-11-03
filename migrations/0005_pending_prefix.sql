-- migrations/0005_pending_prefix.sql
-- Per-prefix cap on unclaimed pending bridges (Batch 1 item 6 anti-abuse).
--
-- POST /join is rate-limited per source /24 (IPv4) or /64 (IPv6) prefix, and the
-- number of simultaneously-pending (unclaimed) bridges per prefix is capped.
-- pending_prefix stores HMAC(pepper, "bb-prefix\n"||prefix) hex so the raw prefix
-- is not retained, and it is cleared when the bridge is claimed (it only matters
-- while pending). NULL for non-/join bridges and claimed bridges.
ALTER TABLE bridges ADD COLUMN pending_prefix TEXT;
CREATE INDEX bridges_pending_prefix ON bridges(pending_prefix) WHERE pending = 1;
