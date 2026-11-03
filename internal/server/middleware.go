package server

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"log/slog"
	"net"
	"net/http"
	"runtime/debug"

	"github.com/hackerick4/botobot/internal/clock"
)

// ctxKey is an unexported context key type to avoid collisions.
type ctxKey int

const (
	ctxRequestID ctxKey = iota
	ctxLandID
	ctxBridgeID
)

// RequestID returns the request id stored on the context, or "" if absent.
func RequestID(ctx context.Context) string {
	if v, ok := ctx.Value(ctxRequestID).(string); ok {
		return v
	}
	return ""
}

// WithLandID / WithBridgeID let later handlers attach identifiers that the
// logging middleware will include. Only non-secret identifiers belong here.
func WithLandID(ctx context.Context, landID string) context.Context {
	return context.WithValue(ctx, ctxLandID, landID)
}

func WithBridgeID(ctx context.Context, bridgeID string) context.Context {
	return context.WithValue(ctx, ctxBridgeID, bridgeID)
}

func stringFromCtx(ctx context.Context, key ctxKey) string {
	if v, ok := ctx.Value(key).(string); ok {
		return v
	}
	return ""
}

// statusRecorder wraps http.ResponseWriter to capture the status code and the
// number of bytes written for logging. It never buffers or inspects the body.
type statusRecorder struct {
	http.ResponseWriter
	status      int
	bytes       int
	wroteHeader bool
}

func (r *statusRecorder) WriteHeader(code int) {
	if r.wroteHeader {
		return
	}
	r.status = code
	r.wroteHeader = true
	r.ResponseWriter.WriteHeader(code)
}

func (r *statusRecorder) Write(b []byte) (int, error) {
	if !r.wroteHeader {
		r.WriteHeader(http.StatusOK)
	}
	n, err := r.ResponseWriter.Write(b)
	r.bytes += n
	return n, err
}

// Flush implements http.Flusher when the underlying writer supports it.
func (r *statusRecorder) Flush() {
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// newRequestID returns a 32-char hex request id from crypto/rand.
func newRequestID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand failure is fatal in practice; fall back to a marker so
		// the request can still be served and correlated as degraded.
		return "req-rand-unavailable"
	}
	return hex.EncodeToString(b[:])
}

// ipTrusted reports whether ip is a trusted proxy source: loopback (Caddy on
// the same host, DEV_SPEC §18) or inside a configured BB_TRUSTED_PROXIES net.
func ipTrusted(ip net.IP, trusted []*net.IPNet) bool {
	if ip == nil {
		return false
	}
	if ip.IsLoopback() {
		return true
	}
	for _, n := range trusted {
		if n != nil && n.Contains(ip) {
			return true
		}
	}
	return false
}

// resolveClientIP determines the client IP with proxy-header anti-spoofing
// (DEV_SPEC §8, §16). The host portion only is returned — never the port.
//
//   - trustHeader empty: the forwarding header is ignored entirely; the direct
//     peer (RemoteAddr) is authoritative. This stops a client spoofing its IP
//     by sending X-Forwarded-For / CF-Connecting-IP.
//   - trustHeader set: the header is honoured only when the direct peer is a
//     trusted proxy (loopback or a BB_TRUSTED_PROXIES net). From the header's
//     value the right-most address that is not itself a trusted proxy is taken
//     (so a chain of our own proxies is stripped). A value that does not parse
//     as an IP, or a chain that is entirely trusted proxies, falls back to the
//     peer address.
func resolveClientIP(r *http.Request, trustHeader string, trusted []*net.IPNet) string {
	peer := r.RemoteAddr
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		peer = host
	}
	if trustHeader == "" {
		return peer
	}
	if !ipTrusted(net.ParseIP(peer), trusted) {
		return peer // untrusted direct peer: never trust a forwarding header
	}
	v := r.Header.Get(trustHeader)
	if v == "" {
		return peer
	}
	parts := splitAndTrim(v)
	for i := len(parts) - 1; i >= 0; i-- {
		ip := net.ParseIP(parts[i])
		if ip == nil {
			return peer // garbage in the chain: fall back to the trusted peer
		}
		if ipTrusted(ip, trusted) {
			continue // strip our own proxies
		}
		return ip.String()
	}
	return peer // every hop was a trusted proxy
}

func splitAndTrim(s string) []string {
	var out []string
	start := 0
	for i := 0; i <= len(s); i++ {
		if i == len(s) || s[i] == ',' {
			seg := s[start:i]
			for len(seg) > 0 && (seg[0] == ' ' || seg[0] == '\t') {
				seg = seg[1:]
			}
			for len(seg) > 0 && (seg[len(seg)-1] == ' ' || seg[len(seg)-1] == '\t') {
				seg = seg[:len(seg)-1]
			}
			if seg != "" {
				out = append(out, seg)
			}
			start = i + 1
		}
	}
	return out
}

// hashIP returns HMAC-SHA256(pepper, ip) truncated to the first 12 hex chars
// (DEV_SPEC §15). Raw IPs are never logged.
func hashIP(pepper []byte, ip string) string {
	if ip == "" {
		return ""
	}
	m := hmac.New(sha256.New, pepper)
	m.Write([]byte(ip))
	return hex.EncodeToString(m.Sum(nil))[:12]
}

// Middleware assigns a request id, records access logs, and recovers panics.
// Per DEV_SPEC §15 it logs ONLY non-secret metadata: request_id, method, route
// pattern (no query), status, duration, response size, bridge_id, land_id, and
// a hashed IP. It never logs headers, bodies, or secrets.
type Middleware struct {
	logger  *slog.Logger
	clock   clock.Clock
	pepper  []byte
	trustHd string
	trusted []*net.IPNet
}

// NewMiddleware builds the access-log/recovery middleware.
func NewMiddleware(logger *slog.Logger, clk clock.Clock, pepper []byte, trustHeader string, trusted []*net.IPNet) *Middleware {
	return &Middleware{logger: logger, clock: clk, pepper: pepper, trustHd: trustHeader, trusted: trusted}
}

// Wrap returns next wrapped with request-id assignment, panic recovery, and
// access logging.
func (m *Middleware) Wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reqID := newRequestID()
		w.Header().Set("X-Request-Id", reqID)
		// RT-107 (v0.6.0): every response, the plain-text /b/ and /e/ pages
		// included, is served as declared; browsers must not sniff it as HTML.
		w.Header().Set("X-Content-Type-Options", "nosniff")
		ctx := context.WithValue(r.Context(), ctxRequestID, reqID)
		r = r.WithContext(ctx)

		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		start := m.clock.Now()

		defer func() {
			if rv := recover(); rv != nil {
				// Recover: log the stack only, never the request (§15).
				m.logger.Error("panic recovered",
					slog.String("request_id", reqID),
					slog.Any("panic", rv),
					slog.String("stack", string(debug.Stack())),
				)
				if !rec.wroteHeader {
					writeError(rec, http.StatusInternalServerError, "internal", "internal error")
				}
			}

			dur := m.clock.Now().Sub(start)
			attrs := []slog.Attr{
				slog.String("request_id", reqID),
				slog.String("method", r.Method),
				slog.String("route", routePattern(r)),
				slog.Int("status", rec.status),
				slog.Int64("duration_ms", dur.Milliseconds()),
				slog.Int("bytes", rec.bytes),
			}
			if ip := hashIP(m.pepper, resolveClientIP(r, m.trustHd, m.trusted)); ip != "" {
				attrs = append(attrs, slog.String("ip_hash", ip))
			}
			if bid := stringFromCtx(r.Context(), ctxBridgeID); bid != "" {
				attrs = append(attrs, slog.String("bridge_id", bid))
			}
			if lid := stringFromCtx(r.Context(), ctxLandID); lid != "" {
				attrs = append(attrs, slog.String("land_id", lid))
			}
			m.logger.LogAttrs(r.Context(), slog.LevelInfo, "request", attrs...)
		}()

		next.ServeHTTP(rec, r)
	})
}

// routePattern returns the matched ServeMux pattern (e.g.
// "GET /v1/bridges/{bridge_id}/inbox") without any query string, so path
// parameters and query values never reach the logs (§15). Falls back to a
// generic marker when the request did not match a registered route.
func routePattern(r *http.Request) string {
	if r.Pattern != "" {
		return r.Pattern
	}
	return "unmatched"
}
