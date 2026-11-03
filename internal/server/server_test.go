package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/hackerick4/botobot/internal/clock"
	"github.com/hackerick4/botobot/internal/config"
)

func testConfig() *config.Config {
	return &config.Config{
		APIKeyPepper:     bytes.Repeat([]byte{0x01}, 32),
		InviteKey:        bytes.Repeat([]byte{0x02}, 32),
		TrustProxyHeader: "",
		LogLevel:         "info",
		LogFormat:        "json",
	}
}

// newTestServer builds a server writing logs into buf.
func newTestServer(t *testing.T, buf *bytes.Buffer, opts ...Option) http.Handler {
	t.Helper()
	logger := NewLogger("info", "json", buf)
	srv := New(testConfig(), logger, clock.Real{}, opts...)
	return srv.Handler()
}

func TestHealthz(t *testing.T) {
	var buf bytes.Buffer
	h := newTestServer(t, &buf)

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("healthz status = %d, want 200", rec.Code)
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("healthz body not JSON: %v", err)
	}
	if body["ok"] != true {
		t.Errorf("healthz body = %v, want {ok:true}", body)
	}
}

func TestReadyzDefaultReady(t *testing.T) {
	var buf bytes.Buffer
	h := newTestServer(t, &buf)
	req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("readyz status = %d, want 200", rec.Code)
	}
}

func TestReadyzNotReady(t *testing.T) {
	var buf bytes.Buffer
	h := newTestServer(t, &buf, WithReady(func(context.Context) error {
		return errors.New("db not writable")
	}))
	req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("readyz status = %d, want 503", rec.Code)
	}
	// The failure reason text must not leak internal error detail.
	if strings.Contains(rec.Body.String(), "db not writable") {
		t.Errorf("readyz body leaked internal error: %s", rec.Body.String())
	}
}

func TestRequestIDHeaderAndLog(t *testing.T) {
	var buf bytes.Buffer
	h := newTestServer(t, &buf)

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	reqID := rec.Header().Get("X-Request-Id")
	if reqID == "" {
		t.Fatal("missing X-Request-Id header")
	}
	// The access log line must carry the same request_id.
	var line map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &line); err != nil {
		t.Fatalf("log line not JSON (%v): %s", err, buf.String())
	}
	if line["request_id"] != reqID {
		t.Errorf("log request_id = %v, header = %q", line["request_id"], reqID)
	}
	if line["route"] != "GET /healthz" {
		t.Errorf("log route = %v, want GET /healthz", line["route"])
	}
	if line["msg"] != "request" {
		t.Errorf("log msg = %v, want request", line["msg"])
	}
}

// TestLogsHaveNoSecrets verifies the middleware never logs the Authorization
// header, the API key, invite codes (even wrong guesses), or query values
// (DEV_SPEC §15, CLAUDE.md rule 5).
func TestLogsHaveNoSecrets(t *testing.T) {
	var buf bytes.Buffer
	h := newTestServer(t, &buf)

	const (
		apiKey     = "bbk_SUPERSECRETAPIKEYVALUE1234567890abcdefg"
		inviteCode = "482913"
	)
	req := httptest.NewRequest(http.MethodGet, "/healthz?code="+inviteCode+"&secret="+apiKey, nil)
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("X-Forwarded-For", "203.0.113.9")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	logs := buf.String()
	for _, secret := range []string{apiKey, inviteCode, "Authorization", "Bearer"} {
		if strings.Contains(logs, secret) {
			t.Errorf("log output contains secret/forbidden token %q:\n%s", secret, logs)
		}
	}
	// Raw client IP must not appear; only the hashed form may.
	if strings.Contains(logs, "203.0.113.9") {
		t.Errorf("log output contains raw client IP:\n%s", logs)
	}
}

func TestPanicRecovery(t *testing.T) {
	var buf bytes.Buffer
	logger := NewLogger("info", "json", &buf)
	srv := New(testConfig(), logger, clock.Real{})

	mux := http.NewServeMux()
	mux.HandleFunc("GET /boom", func(http.ResponseWriter, *http.Request) {
		panic("kaboom")
	})
	mw := NewMiddleware(srv.logger, srv.clock, srv.cfg.APIKeyPepper, srv.cfg.TrustProxyHeader, srv.cfg.TrustedProxies)
	h := mw.Wrap(mux)

	// Drive secrets through the request too: the recovered-panic log path must
	// not leak the Authorization header or an invite code (DEV_SPEC §15, §16).
	const apiKey = "bbk_PANICPATHSECRET1234567890abcdefghi"
	req := httptest.NewRequest(http.MethodGet, "/boom?code=482913", nil)
	req.Header.Set("Authorization", "Bearer "+apiKey)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("panic status = %d, want 500", rec.Code)
	}
	// Recovery must log a panic entry; the body must not leak the panic value.
	logs := buf.String()
	if !strings.Contains(logs, "panic recovered") {
		t.Errorf("expected panic log entry, got: %s", logs)
	}
	if strings.Contains(rec.Body.String(), "kaboom") {
		t.Errorf("response body leaked panic value: %s", rec.Body.String())
	}
	for _, secret := range []string{apiKey, "482913", "Authorization", "Bearer"} {
		if strings.Contains(logs, secret) {
			t.Errorf("panic log leaked secret %q:\n%s", secret, logs)
		}
	}
}

func TestResolveClientIP(t *testing.T) {
	// A sample extra trusted proxy network (a load balancer subnet).
	_, lbNet, _ := net.ParseCIDR("192.0.2.0/24")
	trusted := []*net.IPNet{lbNet}

	newReq := func(remote, xff string) *http.Request {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.RemoteAddr = remote
		if xff != "" {
			r.Header.Set("X-Forwarded-For", xff)
		}
		return r
	}

	cases := []struct {
		name   string
		remote string
		xff    string
		header string // trust header name ("" = disabled)
		want   string
	}{
		// Anti-spoofing: with no trust header the forwarding header is ignored.
		{"header disabled ignores xff", "10.0.0.1:5555", "1.1.1.1", "", "10.0.0.1"},
		// Loopback peer is trusted (Caddy on the same host): honour the header.
		{"loopback peer honours xff", "127.0.0.1:40000", "203.0.113.7", "X-Forwarded-For", "203.0.113.7"},
		// Untrusted direct peer: the header is ignored even when present.
		{"untrusted peer ignores xff", "10.0.0.1:5555", "203.0.113.7", "X-Forwarded-For", "10.0.0.1"},
		// Multi-hop: strip our own proxies (loopback) from the right.
		{"multi-hop strips loopback", "127.0.0.1:40000", "203.0.113.7, 127.0.0.1", "X-Forwarded-For", "203.0.113.7"},
		// Extra trusted proxy net is stripped from the right too.
		{"strips configured proxy", "192.0.2.9:40000", "203.0.113.7, 192.0.2.9", "X-Forwarded-For", "203.0.113.7"},
		// Garbage header value falls back to the (trusted) peer.
		{"garbage falls back to peer", "127.0.0.1:40000", "not-an-ip", "X-Forwarded-For", "127.0.0.1"},
		// IPv6 client via a loopback proxy.
		{"ipv6 client", "[::1]:40000", "2001:db8::1", "X-Forwarded-For", "2001:db8::1"},
		// Chain entirely of trusted proxies falls back to the peer.
		{"all-trusted chain falls back", "127.0.0.1:40000", "127.0.0.1, 192.0.2.9", "X-Forwarded-For", "127.0.0.1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := resolveClientIP(newReq(tc.remote, tc.xff), tc.header, trusted)
			if got != tc.want {
				t.Errorf("resolveClientIP(%q, xff=%q, header=%q) = %q, want %q",
					tc.remote, tc.xff, tc.header, got, tc.want)
			}
		})
	}
}

// TestResolveClientIPCFHeader covers a single-value forwarding header
// (CF-Connecting-IP) rather than an X-Forwarded-For list.
func TestResolveClientIPCFHeader(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.RemoteAddr = "127.0.0.1:40000"
	r.Header.Set("CF-Connecting-IP", "198.51.100.23")
	if ip := resolveClientIP(r, "CF-Connecting-IP", nil); ip != "198.51.100.23" {
		t.Errorf("CF-Connecting-IP resolved = %q, want 198.51.100.23", ip)
	}
	// Spoofed from an untrusted peer: ignored.
	r.RemoteAddr = "203.0.113.200:9"
	if ip := resolveClientIP(r, "CF-Connecting-IP", nil); ip != "203.0.113.200" {
		t.Errorf("spoofed CF-Connecting-IP resolved = %q, want 203.0.113.200", ip)
	}
}

// TestHTTPServerTimeouts verifies the DEV_SPEC §16 hardening values are set on
// the constructed *http.Server.
func TestHTTPServerTimeouts(t *testing.T) {
	cfg := testConfig()
	cfg.ListenAddr = "127.0.0.1:8080"
	srv := New(cfg, NewLogger("info", "json", &bytes.Buffer{}), clock.Real{})
	hs := srv.HTTPServer()

	if hs.ReadHeaderTimeout != 10*time.Second {
		t.Errorf("ReadHeaderTimeout = %s, want 10s", hs.ReadHeaderTimeout)
	}
	if hs.ReadTimeout != 120*time.Second {
		t.Errorf("ReadTimeout = %s, want 120s", hs.ReadTimeout)
	}
	if hs.WriteTimeout != 120*time.Second {
		t.Errorf("WriteTimeout = %s, want 120s", hs.WriteTimeout)
	}
	if hs.IdleTimeout != 120*time.Second {
		t.Errorf("IdleTimeout = %s, want 120s", hs.IdleTimeout)
	}
	if hs.MaxHeaderBytes <= 0 || hs.MaxHeaderBytes > 1<<20 {
		t.Errorf("MaxHeaderBytes = %d, want modest (0 < n <= 1 MiB)", hs.MaxHeaderBytes)
	}
	if hs.Addr != cfg.ListenAddr {
		t.Errorf("Addr = %q, want %q", hs.Addr, cfg.ListenAddr)
	}
	if hs.Handler == nil {
		t.Error("Handler is nil")
	}
}

// TestNoMetricsAndNoCORS confirms /metrics is not served on the public listener
// (it binds only to BB_METRICS_ADDR, DEV_SPEC §6.4) and that no CORS headers are
// emitted (DEV_SPEC §16: no browser use).
func TestNoMetricsAndNoCORS(t *testing.T) {
	var buf bytes.Buffer
	h := newTestServer(t, &buf)

	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("GET /metrics on public listener = %d, want 404 (must not be exposed)", rec.Code)
	}

	req = httptest.NewRequest(http.MethodGet, "/healthz", nil)
	req.Header.Set("Origin", "https://evil.example")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	for _, hName := range []string{
		"Access-Control-Allow-Origin",
		"Access-Control-Allow-Methods",
		"Access-Control-Allow-Headers",
		"Access-Control-Allow-Credentials",
	} {
		if v := rec.Header().Get(hName); v != "" {
			t.Errorf("response emitted CORS header %s=%q, want none", hName, v)
		}
	}
}

func TestHashIPStableAndTruncated(t *testing.T) {
	pepper := bytes.Repeat([]byte{0x09}, 32)
	a := hashIP(pepper, "198.51.100.7")
	b := hashIP(pepper, "198.51.100.7")
	if a != b {
		t.Errorf("hashIP not stable: %q vs %q", a, b)
	}
	if len(a) != 12 {
		t.Errorf("hashIP length = %d, want 12", len(a))
	}
	if strings.Contains(a, "198.51.100.7") {
		t.Errorf("hashIP leaked raw ip: %q", a)
	}
	if hashIP(pepper, "") != "" {
		t.Errorf("hashIP of empty should be empty")
	}
}

// Ensure the Fake clock is usable for duration measurement in the middleware.
func TestMiddlewareUsesInjectedClock(t *testing.T) {
	var buf bytes.Buffer
	logger := NewLogger("info", "json", &buf)
	clk := clock.NewFake(time.Unix(1000, 0))
	cfg := testConfig()
	mw := NewMiddleware(logger, clk, cfg.APIKeyPepper, cfg.TrustProxyHeader, cfg.TrustedProxies)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /x", func(w http.ResponseWriter, _ *http.Request) {
		clk.Advance(1500 * time.Millisecond)
		w.WriteHeader(http.StatusTeapot)
	})
	h := mw.Wrap(mux)

	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	var line map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &line); err != nil {
		t.Fatalf("log not JSON: %v", err)
	}
	if line["duration_ms"].(float64) != 1500 {
		t.Errorf("duration_ms = %v, want 1500", line["duration_ms"])
	}
	if line["status"].(float64) != float64(http.StatusTeapot) {
		t.Errorf("status = %v, want 418", line["status"])
	}
}
