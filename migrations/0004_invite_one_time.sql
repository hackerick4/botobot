-- migrations/0004_invite_one_time.sql
-- One-time, 24h-expiring invite codes (Batch 1 item 5).
--
-- An invite code is now single-use: it is consumed on a successful join and
-- expires 24h after it was issued/rotated. A correct-but-used or correct-but-
-- expired code fails exactly like a wrong code (401 join_failed) so neither
-- condition leaks. The admin issues a fresh code with invite-code rotate.
--
-- invite_code_used is set to 1 when a join consumes the code; a rotate resets it
-- to 0. invite_code_expires_at is Unix ms (invite_code_rotated_at + 24h); NULL
-- means no expiry (legacy rows before this migration keep working until rotated).
ALTER TABLE bridges ADD COLUMN invite_code_used       INTEGER NOT NULL DEFAULT 0;
ALTER TABLE bridges ADD COLUMN invite_code_expires_at INTEGER;
