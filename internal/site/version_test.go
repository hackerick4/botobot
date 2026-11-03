package site

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAssetVersioningAndCaching(t *testing.T) {
	for _, p := range Pages {
		if p.Path != "/" && p.Path != "/en" {
			continue
		}
		rec := httptest.NewRecorder()
		Handler(p)(rec, httptest.NewRequest(http.MethodGet, p.Path, nil))
		body := rec.Body.String()
		if strings.Contains(body, "__CSSV__") || strings.Contains(body, "__JSV__") {
			t.Fatalf("%s: unreplaced asset version placeholder", p.Path)
		}
		for _, ref := range []string{`href="/site.css?v=` + assetVersion("site.css") + `"`, `src="/js/main.js?v=` + assetVersion("js/main.js") + `"`} {
			if !strings.Contains(body, ref) {
				t.Errorf("%s: missing versioned ref %s", p.Path, ref)
			}
		}
		if cc := rec.Header().Get("Cache-Control"); cc != "no-cache" {
			t.Errorf("%s: Cache-Control = %q, want no-cache", p.Path, cc)
		}
		etag := rec.Header().Get("ETag")
		req := httptest.NewRequest(http.MethodGet, p.Path, nil)
		req.Header.Set("If-None-Match", etag)
		rec2 := httptest.NewRecorder()
		Handler(p)(rec2, req)
		if rec2.Code != http.StatusNotModified {
			t.Errorf("%s: revalidation code %d, want 304", p.Path, rec2.Code)
		}
	}
	js := Page{"/js/main.js", "js/main.js", "text/javascript; charset=utf-8"}
	rec := httptest.NewRecorder()
	Handler(js)(rec, httptest.NewRequest(http.MethodGet, "/js/main.js?v=x", nil))
	if cc := rec.Header().Get("Cache-Control"); !strings.Contains(cc, "immutable") {
		t.Errorf("versioned main.js Cache-Control = %q", cc)
	}
}
