// Package site serves the static public website for botobot.app: a bilingual
// landing page, /llms.txt (instructions for AI agents), the /join.md and
// /en/join.md manuals, the /delete and /en/delete removal manuals, robots.txt, sitemap.xml
// and a few assets. Everything is embedded in the server binary, so the website
// ships with the same release artifact and needs no extra hosting.
package site

import (
	"bytes"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"net/http"
	"strconv"
	"strings"
)

//go:embed static/*
var files embed.FS

// Page is one served file.
type Page struct {
	Path        string // URL path, e.g. "/llms.txt"
	File        string // file under static/
	ContentType string
}

// Pages lists every public website path.
var Pages = []Page{
	{"/zh", "index.html", "text/html; charset=utf-8"},
	{"/en", "en.html", "text/html; charset=utf-8"},
	{"/site.css", "site.css", "text/css; charset=utf-8"},
	{"/js/main.js", "js/main.js", "text/javascript; charset=utf-8"},
	{"/js/LICENSES.txt", "js/LICENSES.txt", "text/plain; charset=utf-8"},
	{"/fonts/fraunces.woff2", "fonts/fraunces.woff2", "font/woff2"},
	{"/fonts/instrument-sans.woff2", "fonts/instrument-sans.woff2", "font/woff2"},
	{"/fonts/instrument-sans-sb.woff2", "fonts/instrument-sans-sb.woff2", "font/woff2"},
	{"/fonts/noto-serif-tc.woff2", "fonts/noto-serif-tc.woff2", "font/woff2"},
	{"/fonts/noto-sans-tc.woff2", "fonts/noto-sans-tc.woff2", "font/woff2"},
	{"/OFL.txt", "OFL.txt", "text/plain; charset=utf-8"},
	{"/llms.txt", "llms.txt", "text/plain; charset=utf-8"},
	{"/agent.md", "llms.txt", "text/markdown; charset=utf-8"},
	{"/join.md", "join.md", "text/markdown; charset=utf-8"},
	{"/en/join.md", "en/join.md", "text/markdown; charset=utf-8"},
	{"/delete", "delete.md", "text/plain; charset=utf-8"},
	{"/en/delete", "en/delete.md", "text/plain; charset=utf-8"},
	{"/delete.md", "delete.md", "text/markdown; charset=utf-8"},
	{"/en/delete.md", "en/delete.md", "text/markdown; charset=utf-8"},
	{"/robots.txt", "robots.txt", "text/plain; charset=utf-8"},
	{"/sitemap.xml", "sitemap.xml", "application/xml; charset=utf-8"},
	{"/favicon.svg", "favicon.svg", "image/svg+xml"},
	{"/favicon.ico", "favicon.ico", "image/x-icon"},
	{"/apple-touch-icon.png", "apple-touch-icon.png", "image/png"},
	{"/og.png", "og.png", "image/png"},
}

// csp is strict: everything comes from 'self' and only the asset kinds the
// pages actually use are allowed. No inline styles/scripts ('unsafe-inline' is
// absent) and no 'unsafe-eval' (the hero is a GSAP-driven inline SVG; GSAP
// needs neither eval nor a canvas). CSS and JS are served as external
// same-origin files. Inline JSON-LD (<script type="application/ld+json">) is a
// data block, not executed, so script-src does not apply to it.
const csp = "default-src 'none'; img-src 'self'; style-src 'self'; font-src 'self'; script-src 'self'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'"

// assetVersion returns a short content hash of an embedded file. HTML pages
// reference CSS/JS as "/site.css?v=<hash>", so a deploy that changes an asset
// also changes its URL and no browser can pair new HTML with a stale asset.
func assetVersion(file string) string {
	b, err := files.ReadFile("static/" + file)
	if err != nil {
		panic("site: missing embedded file " + file)
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])[:12]
}

// versioned is the asset-version placeholder table applied to HTML pages.
var versioned = map[string]string{
	"__CSSV__": "site.css",
	"__JSV__":  "js/main.js",
}

// Handler returns the handler for one page. It panics at startup if the
// embedded file is missing (a build error, caught by tests).
//
// Caching: HTML is "no-cache" (always revalidated via ETag) so a deploy is
// picked up immediately. CSS/JS requested with a ?v= content hash are
// immutable for a year; everything else is cached for five minutes.
func Handler(p Page) http.HandlerFunc { return handler(p, false) }

// zhPage and enPage are the two language versions of the landing page.
var (
	zhPage = Page{"/zh", "index.html", "text/html; charset=utf-8"}
	enPage = Page{"/en", "en.html", "text/html; charset=utf-8"}
)

// langFromQuery returns "en" or "zh" from ?lang=, or "".
func langFromQuery(r *http.Request) string {
	switch v := r.URL.Query().Get("lang"); v {
	case "en", "zh":
		return v
	}
	return ""
}

// Root serves "/": English by default, Traditional Chinese for visitors in
// Taiwan. An explicit choice wins: ?lang=en|zh first, then the lang cookie set
// by the language toggle, then isTW (an offline GeoIP lookup of the client
// address). The response is private and varies by Cookie, so no shared cache
// can serve one language to everyone. /zh and /en stay the canonical URLs.
func Root(isTW func(*http.Request) bool) http.HandlerFunc {
	zh, en := handler(zhPage, true), handler(enPage, true)
	return func(w http.ResponseWriter, r *http.Request) {
		lang := langFromQuery(r)
		if lang == "" {
			if c, err := r.Cookie("lang"); err == nil && (c.Value == "en" || c.Value == "zh") {
				lang = c.Value
			}
		}
		if lang == "" && isTW != nil && isTW(r) {
			lang = "zh"
		}
		if lang == "zh" {
			zh(w, r)
			return
		}
		en(w, r)
	}
}

func handler(p Page, varies bool) http.HandlerFunc {
	body, err := files.ReadFile("static/" + p.File)
	if err != nil {
		panic("site: missing embedded file " + p.File)
	}
	isHTML := strings.HasPrefix(p.ContentType, "text/html")
	contentLang := ""
	if isHTML {
		contentLang = "en"
		if p.File == "index.html" {
			contentLang = "zh-Hant"
		}
	}
	if isHTML {
		for ph, f := range versioned {
			body = bytes.ReplaceAll(body, []byte(ph), []byte(assetVersion(f)))
		}
	}
	sum := sha256.Sum256(body)
	etag := `"` + hex.EncodeToString(sum[:])[:16] + `"`
	isVersioned := p.File == "site.css" || p.File == "js/main.js"
	return func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("ETag", etag)
		setLang := ""
		if isHTML {
			setLang = langFromQuery(r)
		}
		if setLang != "" {
			// The language toggle links carry ?lang=; remember the choice.
			http.SetCookie(w, &http.Cookie{Name: "lang", Value: setLang, Path: "/", MaxAge: 365 * 24 * 3600, Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode})
		}
		if varies {
			h.Add("Vary", "Cookie")
		}
		switch {
		case isHTML && (varies || setLang != ""):
			h.Set("Cache-Control", "private, no-cache")
		case isHTML:
			h.Set("Cache-Control", "no-cache")
		case isVersioned && r.URL.Query().Get("v") != "":
			h.Set("Cache-Control", "public, max-age=31536000, immutable")
		default:
			h.Set("Cache-Control", "public, max-age=300")
		}
		if r.Header.Get("If-None-Match") == etag {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		h.Set("Content-Type", p.ContentType)
		if contentLang != "" {
			h.Set("Content-Language", contentLang)
		}
		h.Set("Content-Length", strconv.Itoa(len(body)))
		h.Set("Content-Security-Policy", csp)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		w.WriteHeader(http.StatusOK)
		if r.Method != http.MethodHead {
			_, _ = w.Write(body)
		}
	}
}
