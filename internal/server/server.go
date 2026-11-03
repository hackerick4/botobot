// Package server wires the HTTP routes, logging/recovery middleware, auth
// middleware, and the bridge-admin endpoints (M3) plus the operational
// endpoints (/healthz, /readyz). Handlers must never log request content; all
// access logging is centralised in Middleware (DEV_SPEC §15).
package server

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/hackerick4/botobot/internal/clock"
	"github.com/hackerick4/botobot/internal/config"
	"github.com/hackerick4/botobot/internal/geo"
	"github.com/hackerick4/botobot/internal/invite"
	"github.com/hackerick4/botobot/internal/ratelimit"
	"github.com/hackerick4/botobot/internal/site"
	"github.com/hackerick4/botobot/internal/store"
)

// keyCacheTTL bounds how long a resolved API key stays cached between the
// mutation-driven clears (DEV_SPEC §3.2). keyCacheMax caps its size.
const (
	keyCacheTTL = 30 * time.Second
	keyCacheMax = 4096
)

// HTTP server hardening (DEV_SPEC §16). The read/write/idle timeouts bound slow
// clients; maxHeaderBytes is modest because every bot-bridge request header is
// tiny (a Bearer token, Content-Type, and a short forwarding header).
const (
	readHeaderTimeout = 10 * time.Second
	readTimeout       = 120 * time.Second
	writeTimeout      = 120 * time.Second
	idleTimeout       = 120 * time.Second
	maxHeaderBytes    = 64 << 10 // 64 KiB
)

// ReadyFunc reports whether the server is ready to serve (e.g. DB writable).
type ReadyFunc func(ctx context.Context) error

// Server holds the dependencies needed to build the HTTP handler.
type Server struct {
	cfg    *config.Config
	logger *slog.Logger
	clock  clock.Clock
	ready  ReadyFunc

	store  *store.Store
	pepper []byte

	keyCache      *keyCache
	throttle      *invite.Throttler
	otherLimiter  *ratelimit.Limiter
	createLimiter *ratelimit.Limiter
	joinLimiter   *ratelimit.Limiter // per-IP POST /join (DEV_SPEC §6 /join)
	sendLimiter   *ratelimit.Dynamic // per-land /send, rate = bridge.rate_limit_per_min
	inboxLimiter  *ratelimit.Limiter // per-land GET /inbox
	ackLimiter    *ratelimit.Limiter // per-land POST /inbox/ack
	admin         *adminActivity

	// Public entry (v0.6.0).
	entryAct                       *entryActivity
	entryCreateLimiter             *ratelimit.Limiter // POST /v1/entries per prefix
	entryJoinPrefixLimiter         *ratelimit.Limiter // guest joins per prefix, all entries
	entryJoinPerEntryPrefixLimiter *ratelimit.Limiter // guest joins per prefix per entry
	entryJoinLimiter               *ratelimit.Dynamic // guest joins per entry (joins_per_hour)
	entrySendLimiter               *ratelimit.Limiter // sends per entry across its bridges
	entryInboxLimiter              *ratelimit.Limiter // aggregated inbox polls per entry
	jitter                         func()             // join-failure response jitter; injectable for tests
}

// Rate-limit burst sizes (DEV_SPEC §9). The per-minute rates come from config;
// these are the fixed burst allowances for each per-land bucket.
const (
	sendBurst  = 10 // /send burst (DEV_SPEC §9: 30 msg/min/land, burst 10)
	inboxBurst = 10 // GET /inbox burst (DEV_SPEC §9: 6/min/land, burst 10)
	ackBurst   = 20 // POST /inbox/ack burst (DEV_SPEC §9: 60/min/land, burst 20)
)

// Option customises a Server.
type Option func(*Server)

// WithReady sets the readiness probe used by /readyz.
func WithReady(fn ReadyFunc) Option {
	return func(s *Server) { s.ready = fn }
}

// WithStore attaches the persistence layer and enables the v1 API routes. Without
// it, only /healthz and /readyz are served (M0 behaviour).
func WithStore(st *store.Store) Option {
	return func(s *Server) { s.store = st }
}

// WithJoinJitter overrides the join-failure response jitter (DEV_SPEC §8). Tests
// pass a no-op to avoid real delays (CLAUDE.md rule 8: no time.Sleep in tests).
func WithJoinJitter(fn func()) Option {
	return func(s *Server) { s.jitter = fn }
}

// New builds a Server. The returned Handler is fully wrapped with middleware.
func New(cfg *config.Config, logger *slog.Logger, clk clock.Clock, opts ...Option) *Server {
	s := &Server{
		cfg:    cfg,
		logger: logger,
		clock:  clk,
		ready:  func(context.Context) error { return nil },
		pepper: cfg.APIKeyPepper,
		jitter: defaultJitter,
	}
	for _, opt := range opts {
		opt(s)
	}
	s.keyCache = newKeyCache(clk, keyCacheTTL, keyCacheMax)
	s.throttle = invite.NewThrottler(clk)
	s.otherLimiter = ratelimit.New(cfg.RateOtherPerMin, time.Minute, cfg.RateOtherPerMin, clk)
	s.createLimiter = ratelimit.New(cfg.RateCreateBridgePerHourPerIP, time.Hour, cfg.RateCreateBridgePerHourPerIP, clk)
	s.joinLimiter = ratelimit.New(cfg.JoinCreatePerHour, time.Hour, cfg.JoinCreatePerHour, clk)
	s.sendLimiter = ratelimit.NewDynamic(clk)
	s.inboxLimiter = ratelimit.New(cfg.RateInboxPerMin, time.Minute, inboxBurst, clk)
	s.ackLimiter = ratelimit.New(cfg.RateAckPerMin, time.Minute, ackBurst, clk)
	s.admin = newAdminActivity()
	s.entryAct = &entryActivity{last: make(map[string]time.Time)}
	s.entryCreateLimiter = ratelimit.New(cfg.EntryCreatePerHour, time.Hour, cfg.EntryCreatePerHour, clk)
	s.entryJoinPrefixLimiter = ratelimit.New(cfg.EntryJoinPerPrefixPerHour, time.Hour, cfg.EntryJoinPerPrefixPerHour, clk)
	s.entryJoinPerEntryPrefixLimiter = ratelimit.New(cfg.EntryJoinPerPrefixPerEntryPerHour, time.Hour, cfg.EntryJoinPerPrefixPerEntryPerHour, clk)
	s.entryJoinLimiter = ratelimit.NewDynamic(clk)
	s.entrySendLimiter = ratelimit.New(entrySendPerMin, time.Minute, entrySendBurst, clk)
	s.entryInboxLimiter = ratelimit.New(entryInboxPerMin, time.Minute, entryInboxBurst, clk)
	return s
}

// ClearKeyCache invalidates all cached API-key lookups. The M5 sweeper calls
// this after closing a bridge for admin inactivity (DEV_SPEC §11) so the keys
// it deleted are not served from the cache; the admin-driven close endpoints
// clear it inline for the same reason.
func (s *Server) ClearKeyCache() { s.keyCache.clear() }

// route is one registered endpoint. Handler() is built solely from routes(), so
// the full route set is enumerable (TestLogsHaveNoSecrets walks it, keeping new
// routes covered automatically — DEV_SPEC §15, §16).
type route struct {
	method  string
	pattern string
	handler http.HandlerFunc
}

// Handler returns the root http.Handler with routes and middleware applied.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	for _, rt := range s.routes() {
		mux.HandleFunc(rt.method+" "+rt.pattern, rt.handler)
	}
	mw := NewMiddleware(s.logger, s.clock, s.cfg.APIKeyPepper, s.cfg.TrustProxyHeader, s.cfg.TrustedProxies)
	return mw.Wrap(mux)
}

// HTTPServer builds the *http.Server with the DEV_SPEC §16 hardening timeouts
// and a modest MaxHeaderBytes. Construction lives here (not in cmd/) so the
// timeouts are unit-tested (TestHTTPServerTimeouts).
func (s *Server) HTTPServer() *http.Server {
	return &http.Server{
		Addr:              s.cfg.ListenAddr,
		Handler:           s.Handler(),
		ReadHeaderTimeout: readHeaderTimeout,
		ReadTimeout:       readTimeout,
		WriteTimeout:      writeTimeout,
		IdleTimeout:       idleTimeout,
		MaxHeaderBytes:    maxHeaderBytes,
	}
}

// routes returns every registered endpoint. /healthz and /readyz are always
// present; the v1 API is added only when a store is attached (M0 vs later).
func (s *Server) routes() []route {
	rs := []route{
		{"GET", "/healthz", s.handleHealthz},
		{"GET", "/readyz", s.handleReadyz},
	}
	// Public website (botobot.app landing page, /llms.txt, robots, sitemap).
	// "/" picks the language: ?lang= or the lang cookie, else Traditional
	// Chinese for Taiwan IPs (offline DB-IP lookup), else English.
	rs = append(rs, route{"GET", "/{$}", site.Root(func(r *http.Request) bool { return geo.IsTW(s.clientIP(r)) })})
	for _, pg := range site.Pages {
		pattern := pg.Path
		if pattern == "/" {
			pattern = "/{$}" // exact root only, not a catch-all
		}
		rs = append(rs, route{"GET", pattern, site.Handler(pg)})
	}
	if s.store != nil {
		rs = append(rs, s.v1Routes()...)
	}
	return rs
}

// v1Routes lists the bridge-admin, message/inbox, and /me endpoints (DEV_SPEC §6.4).
func (s *Server) v1Routes() []route {
	return []route{
		// Unauthenticated.
		{"POST", "/v1/bridges", s.handleCreateBridge},
		{"POST", "/v1/bridges/{bridge_id}/join", s.handleJoin},
		{"POST", "/v1/bridges/{bridge_id}/claim", s.handleClaim},
		{"GET", "/b/{bridge_id}", s.handleBridgePage},

		// Public entry (v0.6.0, DEV_SPEC §20).
		{"POST", "/v1/entries", s.handleCreateEntry},
		{"POST", "/v1/e/{link_id}/join", s.handleEntryJoin},
		{"GET", "/e/{link_id}", s.handleEntryPage},
		{"GET", "/v1/entries/{entry_id}", s.entryAuthed(s.entryOtherLimit, s.handleGetEntry)},
		{"PATCH", "/v1/entries/{entry_id}", s.entryAuthed(s.entryOtherLimit, s.handlePatchEntry)},
		{"DELETE", "/v1/entries/{entry_id}", s.entryAuthed(s.entryOtherLimit, s.handleDeleteEntry)},
		{"POST", "/v1/entries/{entry_id}/rotate-link", s.entryAuthed(s.entryOtherLimit, s.handleRotateEntryLink)},
		{"GET", "/v1/entries/{entry_id}/bridges", s.entryAuthed(s.entryOtherLimit, s.handleListEntryBridges)},
		{"GET", "/v1/entries/{entry_id}/inbox", s.entryAuthed(s.entryInboxLimit, s.handleEntryInbox)},
		{"POST", "/v1/entries/{entry_id}/inbox/ack", s.entryAuthed(s.entryAckLimit, s.handleEntryAck)},

		// Public one-call onboarding (DEV_SPEC §6 /join).
		{"GET", "/join", s.handleJoinUsage},
		{"POST", "/join", s.handleJoinCreate},

		// Bridge-scoped, land-authenticated.
		{"GET", "/v1/bridges/{bridge_id}", s.authed(false, true, s.handleGetBridge)},
		{"PATCH", "/v1/bridges/{bridge_id}", s.authed(true, true, s.handlePatchBridge)},
		{"DELETE", "/v1/bridges/{bridge_id}", s.authed(true, true, s.handleDeleteBridge)},
		{"GET", "/v1/bridges/{bridge_id}/invite-code", s.authed(false, true, s.handleGetInviteCode)},
		{"POST", "/v1/bridges/{bridge_id}/invite-code/rotate", s.authed(true, true, s.handleRotateInviteCode)},
		{"GET", "/v1/bridges/{bridge_id}/lands", s.authed(false, true, s.handleListLands)},
		{"DELETE", "/v1/bridges/{bridge_id}/lands/{land_id}", s.authed(false, true, s.handleDeleteLand)},

		// Messages & inbox (M4). Each uses its own per-land rate-limit bucket
		// (DEV_SPEC §9) instead of the generic "other" limiter.
		{"POST", "/v1/bridges/{bridge_id}/send", s.authedRL(false, true, s.sendRateLimit, s.handleSend)},
		{"GET", "/v1/bridges/{bridge_id}/inbox", s.authedGrace(s.inboxRateLimit, s.handleInbox)},
		{"POST", "/v1/bridges/{bridge_id}/inbox/ack", s.authedGrace(s.ackRateLimit, s.handleAck)},
		// v0.6.4: how many of my messages the other side has not read yet.
		{"GET", "/v1/bridges/{bridge_id}/undelivered", s.authed(false, true, s.handleUndelivered)},
		{"GET", "/v1/bridges/{bridge_id}/messages/{msg_id}/status", s.authed(false, true, s.handleMessageStatus)},

		// Self ("/me"), land-authenticated (bridge resolved from the API key).
		{"GET", "/v1/me", s.authed(false, false, s.handleGetMe)},
		{"PATCH", "/v1/me", s.authed(false, false, s.handlePatchMe)},
		{"PUT", "/v1/me/public-key", s.authed(false, false, s.handlePutPublicKey)},
		{"POST", "/v1/me/api-keys", s.authed(false, false, s.handleCreateAPIKey)},
		{"DELETE", "/v1/me/api-keys/{key_id}", s.authed(false, false, s.handleDeleteAPIKey)},
		{"POST", "/v1/me/ping", s.authed(false, false, s.handlePing)},
	}
}

// clientIP resolves the caller's IP with proxy-header anti-spoofing
// (DEV_SPEC §8, §16) using the configured trust header and trusted-proxy set.
func (s *Server) clientIP(r *http.Request) string {
	return resolveClientIP(r, s.cfg.TrustProxyHeader, s.cfg.TrustedProxies)
}

// handleHealthz is a liveness probe: no auth, no data access (DEV_SPEC §6.4).
// It also reports schema_version (the highest applied migration) so a deploy
// can confirm its migrations ran without shell access; no data is exposed.
func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	out := map[string]any{"ok": true}
	if s.store != nil {
		if v, err := s.store.SchemaVersion(r.Context()); err == nil {
			out["schema_version"] = v
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// handleReadyz reports readiness; returns 503 when the readiness probe fails.
func (s *Server) handleReadyz(w http.ResponseWriter, r *http.Request) {
	if err := s.ready(r.Context()); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"ok": false, "reason": "not_ready"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// defaultJitter sleeps a random 100–300 ms to mask invite-code timing
// (DEV_SPEC §8). Randomness is from crypto/rand.
func defaultJitter() {
	var b [2]byte
	if _, err := rand.Read(b[:]); err != nil {
		time.Sleep(200 * time.Millisecond)
		return
	}
	ms := 100 + int(binary.BigEndian.Uint16(b[:]))%201 // 100..300
	time.Sleep(time.Duration(ms) * time.Millisecond)
}

// writeJSON serialises v as JSON with the given status. On encode failure it
// has already committed the status; nothing secret is ever written here.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// writeError emits the standard error envelope (DEV_SPEC §6.1). The message
// must be human-readable and must not leak internal detail.
func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]any{
		"error": map[string]any{"code": code, "message": message},
	})
}
