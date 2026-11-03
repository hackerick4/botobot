package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hackerick4/botobot/internal/config"
)

// "/" picks the page language from the client IP as seen through the trusted
// local proxy (Caddy on loopback). A forwarding header from any other peer is
// ignored, so it cannot be used to pick (or probe) a language.
func TestRootLanguageByGeoIP(t *testing.T) {
	ts := newIntegServer(t, func(c *config.Config) { c.TrustProxyHeader = "X-Forwarded-For" })
	cases := []struct {
		name, peer, xff, cookie, path, want string
	}{
		{"TW via trusted proxy", "127.0.0.1:5000", "168.95.1.1", "", "/", "zh-Hant"},
		{"TW IPv6 via trusted proxy", "127.0.0.1:5000", "2001:b000::1", "", "/", "zh-Hant"},
		{"non-TW via trusted proxy", "127.0.0.1:5000", "8.8.8.8", "", "/", "en"},
		{"spoofed chain: rightmost untrusted hop wins", "127.0.0.1:5000", "168.95.1.1, 8.8.8.8", "", "/", "en"},
		{"spoofed XFF from untrusted peer", "8.8.4.4:5000", "168.95.1.1", "", "/", "en"},
		{"no proxy, non-TW peer", "8.8.4.4:5000", "", "", "/", "en"},
		{"cookie en beats TW", "127.0.0.1:5000", "168.95.1.1", "en", "/", "en"},
		{"cookie zh beats non-TW", "127.0.0.1:5000", "8.8.8.8", "zh", "/", "zh-Hant"},
		{"?lang=en beats cookie zh", "127.0.0.1:5000", "168.95.1.1", "zh", "/?lang=en", "en"},
		{"?lang=zh on non-TW", "127.0.0.1:5000", "8.8.8.8", "", "/?lang=zh", "zh-Hant"},
		{"bogus ?lang ignored", "127.0.0.1:5000", "8.8.8.8", "", "/?lang=xx", "en"},
		{"/zh is always zh", "127.0.0.1:5000", "8.8.8.8", "", "/zh", "zh-Hant"},
		{"/en is always en", "127.0.0.1:5000", "168.95.1.1", "", "/en", "en"},
	}
	for _, c := range cases {
		req := httptest.NewRequest("GET", c.path, nil)
		req.RemoteAddr = c.peer
		if c.xff != "" {
			req.Header.Set("X-Forwarded-For", c.xff)
		}
		if c.cookie != "" {
			req.AddCookie(&http.Cookie{Name: "lang", Value: c.cookie})
		}
		rec := httptest.NewRecorder()
		ts.h.ServeHTTP(rec, req)
		if rec.Code != 200 {
			t.Fatalf("%s: status %d", c.name, rec.Code)
		}
		if got := rec.Header().Get("Content-Language"); got != c.want {
			t.Errorf("%s: Content-Language %q, want %q", c.name, got, c.want)
		}
		if c.path == "/" || strings.HasPrefix(c.path, "/?") {
			if v := rec.Header().Get("Vary"); !strings.Contains(v, "Cookie") {
				t.Errorf("%s: Vary %q lacks Cookie", c.name, v)
			}
			if cc := rec.Header().Get("Cache-Control"); !strings.HasPrefix(cc, "private") {
				t.Errorf("%s: Cache-Control %q, want private", c.name, cc)
			}
		}
		set := rec.Header().Get("Set-Cookie")
		if strings.Contains(c.path, "lang=en") || strings.Contains(c.path, "lang=zh") {
			lang := c.path[strings.Index(c.path, "lang=")+5:]
			if !strings.HasPrefix(set, "lang="+lang) || !strings.Contains(set, "Secure") || !strings.Contains(set, "SameSite=Lax") {
				t.Errorf("%s: Set-Cookie %q", c.name, set)
			}
		} else if set != "" {
			t.Errorf("%s: unexpected Set-Cookie %q", c.name, set)
		}
	}
}
