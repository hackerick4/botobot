package server

import (
	"encoding/hex"
	"net/http"
	"sync"
	"time"

	"github.com/hackerick4/botobot/internal/auth"
	"github.com/hackerick4/botobot/internal/clock"
	"github.com/hackerick4/botobot/internal/store"
)

// authContext is the resolved caller for an authenticated request.
type authContext struct {
	keyID  string
	land   *store.Land
	bridge *store.Bridge
	// entry is set when a public-entry key (bbe_…) authenticated as the
	// service land of one of its bridges (v0.6.0); keyID is then empty.
	entry *store.Entry
}

// handlerFunc is an authenticated handler. It returns a non-nil *apiErr to have
// the wrapper emit the standard error envelope; nil means it wrote its own
// success response.
type handlerFunc func(w http.ResponseWriter, r *http.Request, ac *authContext) *apiErr

// ---- API key cache (DEV_SPEC §3.2: api_key_hash -> land) ----

// keyCache is a small, TTL-bounded cache of resolved API keys (DEV_SPEC §3.2,
// T3.1 "small cache"). It stores only the key_id/land_id/bridge_id mapping, not
// the land/bridge rows, which are re-read fresh so role/settings/status changes
// take effect at once. Every mutation that can invalidate a key (revoke, kick,
// close, rotate) calls clear(), so a cache hit always reflects a still-valid
// key between mutations; the short TTL is a backstop.
type keyCache struct {
	mu  sync.Mutex
	clk clock.Clock
	ttl time.Duration
	max int
	m   map[string]keyCacheEntry
}

type keyCacheEntry struct {
	keyID, landID, bridgeID string
	exp                     time.Time
}

func newKeyCache(clk clock.Clock, ttl time.Duration, max int) *keyCache {
	return &keyCache{clk: clk, ttl: ttl, max: max, m: make(map[string]keyCacheEntry)}
}

func (c *keyCache) get(hashHex string) (keyCacheEntry, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.m[hashHex]
	if !ok {
		return keyCacheEntry{}, false
	}
	if !c.clk.Now().Before(e.exp) {
		delete(c.m, hashHex)
		return keyCacheEntry{}, false
	}
	return e, true
}

func (c *keyCache) put(hashHex, keyID, landID, bridgeID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.m) >= c.max {
		c.m = make(map[string]keyCacheEntry) // simplest bounded eviction
	}
	c.m[hashHex] = keyCacheEntry{keyID: keyID, landID: landID, bridgeID: bridgeID, exp: c.clk.Now().Add(c.ttl)}
}

func (c *keyCache) clear() {
	c.mu.Lock()
	c.m = make(map[string]keyCacheEntry)
	c.mu.Unlock()
}

// authenticateGrace resolves the Authorization header to a caller (DEV_SPEC
// §6.1). It returns errUnauthorized for a missing/invalid/revoked key and a 410
// for a key whose bridge is closed; with grace set, a land of a closed bridge
// that is still in its last-delivery window (and did not close it) is let in
// (v0.6.4: only GET inbox and POST inbox/ack use this).
func (s *Server) authenticateGrace(r *http.Request, grace bool) (*authContext, *apiErr) {
	token, ok := auth.ParseBearer(r.Header.Get("Authorization"))
	if !ok {
		return nil, errUnauthorized
	}
	if auth.IsEntryKey(token) {
		return s.authenticateEntryOnBridge(r, token)
	}
	hashHex := hex.EncodeToString(auth.Hash(s.pepper, token))

	ctx := r.Context()
	var keyID, landID, bridgeID string
	if e, hit := s.keyCache.get(hashHex); hit {
		keyID, landID, bridgeID = e.keyID, e.landID, e.bridgeID
	} else {
		k, err := s.store.GetAPIKeyByHash(ctx, auth.Hash(s.pepper, token))
		if err != nil {
			return nil, errUnauthorized // not found / revoked are indistinguishable
		}
		l, err := s.store.GetLand(ctx, k.LandID)
		if err != nil {
			return nil, errUnauthorized
		}
		keyID, landID, bridgeID = k.ID, k.LandID, l.BridgeID
		s.keyCache.put(hashHex, keyID, landID, bridgeID)
	}

	land, err := s.store.GetLand(ctx, landID)
	if err != nil {
		return nil, errUnauthorized
	}
	bridge, err := s.store.GetBridge(ctx, bridgeID)
	if err != nil {
		return nil, errUnauthorized
	}
	if bridge.Status == "closed" && !(grace && store.GraceOpen(bridge, land.ID, s.clock.Now().UnixMilli())) {
		return nil, closedErr(bridge)
	}
	return &authContext{keyID: keyID, land: land, bridge: bridge}, nil
}

// closedErr builds the 410 bridge_closed error with closed_at / close_reason
// (DEV_SPEC §6.1).
func closedErr(b *store.Bridge) *apiErr {
	extra := map[string]any{}
	if b.ClosedAt != nil {
		extra["closed_at"] = rfc3339(*b.ClosedAt)
	}
	if b.CloseReason != nil {
		extra["close_reason"] = *b.CloseReason
	}
	// v0.6.1: a public-entry line tells its joining bot whether the whole
	// entry closed or only this line (both sides of the line knew the entry).
	// v0.6.5: a line closed shortly before its entry (the same delete-all
	// batch closes the lines first) also counts as ended by the entry close.
	if b.EntryID != nil && b.EntryClosedAt != nil && b.ClosedAt != nil &&
		*b.ClosedAt >= *b.EntryClosedAt-store.EntryCloseLookback.Milliseconds() {
		extra["entry_closed"] = true
	}
	return &apiErr{status: http.StatusGone, code: "bridge_closed", msg: "bridge is closed", extra: extra}
}

// limitFunc decides whether an authenticated caller may proceed, returning the
// retry-after wait when throttled (DEV_SPEC §9). It runs after authentication so
// the per-land bucket can be keyed by land id and read bridge settings.
type limitFunc func(ac *authContext) (ok bool, retryAfter time.Duration)

// authed wraps an authenticated handler using the generic per-land "other"
// request limit (DEV_SPEC §9). See authedRL for endpoints with their own bucket.
func (s *Server) authed(requireAdmin, bridgeScoped bool, fn handlerFunc) http.HandlerFunc {
	return s.authedRL(requireAdmin, bridgeScoped, s.otherRateLimit, fn)
}

// authedRL wraps an authenticated handler with an explicit rate-limit function.
// bridgeScoped routes carry a {bridge_id} path value that must equal the
// caller's bridge (else 404), and a closed such bridge yields 410 even to a
// stale key. requireAdmin enforces the admin role (403). A successful admin
// request records activity (DEV_SPEC §11).
func (s *Server) authedRL(requireAdmin, bridgeScoped bool, limit limitFunc, fn handlerFunc) http.HandlerFunc {
	return s.authedOpt(requireAdmin, bridgeScoped, false, limit, fn)
}

// authedGrace is authedRL for the two routes the other side of a just-closed
// bridge may still use during its last-delivery window: GET inbox and POST
// inbox/ack (v0.6.4). Everything else on a closed bridge stays 410.
func (s *Server) authedGrace(limit limitFunc, fn handlerFunc) http.HandlerFunc {
	return s.authedOpt(false, true, true, limit, fn)
}

func (s *Server) authedOpt(requireAdmin, bridgeScoped, grace bool, limit limitFunc, fn handlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Closed bridges answer 410 to their own former members even though the
		// keys were deleted on close (DEV_SPEC §6.1, §11). v0.6.6: anyone else
		// falls through to normal auth and gets what an unknown bridge gives
		// (401 without a valid key, 404 with another bridge's key), so the
		// tombstone does not tell outsiders that the id ever existed.
		if bridgeScoped {
			if b, err := s.store.GetBridge(r.Context(), r.PathValue("bridge_id")); err == nil && b.Status == "closed" {
				open := b.GraceUntil != nil && s.clock.Now().UnixMilli() < *b.GraceUntil
				if b.GraceUntil != nil && !open {
					// The window ran out: end it now rather than wait for the sweeper.
					if err := s.store.FinalizeGrace(r.Context(), b.ID); err == nil {
						s.keyCache.clear()
					}
				}
				if (!grace || !open) && s.formerMember(r, b) {
					s.writeAPIError(w, closedErr(b))
					return
				}
			}
		}

		ac, aerr := s.authenticateGrace(r, grace)
		if aerr != nil {
			s.writeAPIError(w, aerr)
			return
		}
		// Attach identifiers for the access log (mutate in place so the outer
		// logging middleware, which holds this same *Request, observes them).
		*r = *r.WithContext(WithBridgeID(WithLandID(r.Context(), ac.land.ID), ac.bridge.ID))

		if bridgeScoped && r.PathValue("bridge_id") != ac.bridge.ID {
			s.writeAPIError(w, errNotFound)
			return
		}
		if ok, ra := limit(ac); !ok {
			s.writeAPIError(w, rateLimited(ra))
			return
		}
		if requireAdmin && ac.land.Role != "admin" {
			s.writeAPIError(w, errForbidden)
			return
		}

		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		if e := fn(rec, r, ac); e != nil {
			s.writeAPIError(rec, e)
			return
		}
		if ac.land.Role == "admin" {
			s.touchAdmin(r, ac.bridge.ID)
		}
		if ac.entry != nil {
			s.touchEntry(r, ac.entry.ID)
		}
	}
}

// ---- Per-endpoint rate-limit functions (DEV_SPEC §9) ----

func (s *Server) otherRateLimit(ac *authContext) (bool, time.Duration) {
	return s.otherLimiter.Allow(ac.land.ID)
}

func (s *Server) sendRateLimit(ac *authContext) (bool, time.Duration) {
	if ac.entry != nil {
		// One service bot answering many guests: a shared per-entry bucket on
		// top of the per-bridge one (v0.6.0).
		if ok, ra := s.entrySendLimiter.Allow(ac.entry.ID); !ok {
			return false, ra
		}
	}
	return s.sendLimiter.Allow(ac.land.ID, ac.bridge.RateLimitPerMin, sendBurst)
}

func (s *Server) inboxRateLimit(ac *authContext) (bool, time.Duration) {
	return s.inboxLimiter.Allow(ac.land.ID)
}

func (s *Server) ackRateLimit(ac *authContext) (bool, time.Duration) {
	return s.ackLimiter.Allow(ac.land.ID)
}

// ---- Admin activity tracking (DEV_SPEC §11) ----

// adminActivity debounces admin_last_active_at writes to at most once per minute
// per bridge (DEV_SPEC §11).
type adminActivity struct {
	mu   sync.Mutex
	last map[string]time.Time
}

func newAdminActivity() *adminActivity { return &adminActivity{last: make(map[string]time.Time)} }

// touchAdmin records that the admin of bridgeID was just active, flushing to the
// DB at most once per minute per bridge.
func (s *Server) touchAdmin(r *http.Request, bridgeID string) {
	now := s.clock.Now()
	s.admin.mu.Lock()
	last, ok := s.admin.last[bridgeID]
	due := !ok || now.Sub(last) >= time.Minute
	if due {
		s.admin.last[bridgeID] = now
	}
	s.admin.mu.Unlock()
	if !due {
		return
	}
	if err := s.store.TouchAdminActive(r.Context(), bridgeID, now.UnixMilli()); err != nil {
		s.logger.Warn("touch admin active failed", "bridge_id", bridgeID, "err_code", storeErr(err).code)
	}
}

// formerMember reports whether the request's land key belongs (or belonged at
// close) to the closed bridge b. Entry keys are handled by
// authenticateEntryOnBridge, which answers 410 only for the entry's own line.
func (s *Server) formerMember(r *http.Request, b *store.Bridge) bool {
	token, ok := auth.ParseBearer(r.Header.Get("Authorization"))
	if !ok || auth.IsEntryKey(token) {
		return false
	}
	hash := auth.Hash(s.pepper, token)
	if k, err := s.store.GetAPIKeyByHash(r.Context(), hash); err == nil {
		if l, err := s.store.GetLand(r.Context(), k.LandID); err == nil {
			return l.BridgeID == b.ID
		}
	}
	id, err := s.store.ClosedKeyBridge(r.Context(), hash)
	return err == nil && id == b.ID
}
