package server

import (
	"bytes"
	"net/http/httptest"
	"strings"
	"testing"
)

// The website is served at exact paths only; "/" must not become a catch-all
// that hides API 404s/405s (botobot.app move, DEV_SPEC §19).
func TestSiteRoutes(t *testing.T) {
	var buf bytes.Buffer
	h := newTestServer(t, &buf)
	for _, c := range []struct {
		method, path string
		code         int
		ctype        string
	}{
		{"GET", "/", 200, "text/html"},
		{"HEAD", "/", 200, "text/html"},
		{"GET", "/en", 200, "text/html"},
		{"GET", "/site.css", 200, "text/css"},
		{"GET", "/js/main.js", 200, "text/javascript"},
		{"GET", "/fonts/fraunces.woff2", 200, "font/woff2"},
		{"GET", "/fonts/noto-serif-tc.woff2", 200, "font/woff2"},
		{"GET", "/OFL.txt", 200, "text/plain"},
		{"GET", "/llms.txt", 200, "text/plain"},
		{"GET", "/agent.md", 200, "text/markdown"},
		{"GET", "/robots.txt", 200, "text/plain"},
		{"GET", "/sitemap.xml", 200, "application/xml"},
		{"GET", "/og.png", 200, "image/png"},
		{"GET", "/nope", 404, ""},
		{"GET", "/index.html.bak", 404, ""},
		{"GET", "/en/", 404, ""},
		{"POST", "/", 405, ""},
		{"POST", "/en", 405, ""},
	} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(c.method, c.path, nil))
		if rec.Code != c.code {
			t.Errorf("%s %s = %d, want %d", c.method, c.path, rec.Code, c.code)
		}
		if c.ctype != "" && !strings.HasPrefix(rec.Header().Get("Content-Type"), c.ctype) {
			t.Errorf("%s %s content-type %q", c.method, c.path, rec.Header().Get("Content-Type"))
		}
	}
}
