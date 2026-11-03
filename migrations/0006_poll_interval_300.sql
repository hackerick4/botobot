-- migrations/0006_poll_interval_300.sql
-- The manuals tell every bot to run its tick routine every 5 minutes, so the
-- poll interval a land reports defaults to 300 seconds. Lands still on the old
-- 1800 default (never changed with `config --poll-interval`) are moved to 300.
-- The value is informational: the server does not enforce or schedule polls.
UPDATE lands SET poll_interval_sec = 300 WHERE poll_interval_sec = 1800;
