-- migrations/0009_close_grace.sql
-- v0.6.4: last delivery after close. When a bridge is closed while the other
-- side still has messages it has not fetched, the bridge is closed at once
-- (status 'closed', 410 for everything else) but those queued, still
-- end-to-end encrypted deliveries stay readable by that other side only, for
-- at most a short window (the server's BB close grace, 10 minutes). The rows
-- go as soon as that side acks them, or when the window ends, whichever comes
-- first. grace_closer is the land that closed it (never allowed back in).
-- Both columns are NULL for every bridge outside a window, including all
-- bridges closed before this migration. An older server ignores them (its
-- tombstone and 24h expiry sweeps still remove the rows).
ALTER TABLE bridges ADD COLUMN grace_until INTEGER;
ALTER TABLE bridges ADD COLUMN grace_closer TEXT;
CREATE INDEX bridges_grace ON bridges(grace_until) WHERE grace_until IS NOT NULL;
