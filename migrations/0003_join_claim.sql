-- migrations/0003_join_claim.sql
-- Public, unauthenticated /join flow (product request): one curl call
-- creates a PENDING bridge (no lands yet) plus a one-time admin claim token, and
-- returns copy-paste prompts so onboarding needs no manual key copying.
--
-- A bridge created via /join starts PENDING: it has an invite code but no admin
-- land. The caller claims it with POST /v1/bridges/{id}/claim, supplying a land
-- registration (keys generated on the bot's own machine) plus the claim token;
-- the claimant then becomes the admin land and the bridge becomes a normal
-- active bridge. Unclaimed pending bridges past claim_expires_at are deleted
-- entirely by the sweeper (no tombstone — nobody has joined yet).
--
-- The claim token is a secret: only its HMAC (with the server pepper, domain
-- separated) is stored, compared in constant time, and never logged (§15, §16).
-- bridges.status stays CHECK (active|closed); "pending" is a separate flag so no
-- table rebuild is needed to widen the status enum (DEV_SPEC §19).
ALTER TABLE bridges ADD COLUMN pending          INTEGER NOT NULL DEFAULT 0; -- 1 while awaiting first claim
ALTER TABLE bridges ADD COLUMN claim_token_hash BLOB;                       -- HMAC(pepper, token); NULL once claimed
ALTER TABLE bridges ADD COLUMN claim_expires_at INTEGER;                    -- Unix ms; pending bridge deleted after this
CREATE INDEX bridges_pending ON bridges(pending) WHERE pending = 1;
