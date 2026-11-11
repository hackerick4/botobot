package site_test

import (
	"regexp"
	"strings"
	"testing"
)

const (
	githubRepo = "https://github.com/hackerick4/botobot"
	wantCSP    = "default-src 'none'; img-src 'self'; style-src 'self'; font-src 'self'; script-src 'self'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'"
)

// TestGitHubLink: /zh and /en link the public repo in the header and footer
// with an inline SVG mark (no external asset), rel="noopener", and plain
// neutral wording; the CSP is unchanged.
func TestGitHubLink(t *testing.T) {
	anchor := regexp.MustCompile(`<a class="(gh-link|gh-foot)" href="([^"]+)"([^>]*)>(.*?)</a>`)
	for path, words := range map[string][]string{
		"/zh": {"GitHub 原始碼（另開新分頁）", "<span>GitHub 原始碼</span>"},
		"/en": {"Source code on GitHub (opens in a new tab)", "<span>Source code on GitHub</span>"},
	} {
		res, body := get(t, path)
		if got := res.Header.Get("Content-Security-Policy"); got != wantCSP {
			t.Errorf("%s: CSP changed: %q", path, got)
		}
		seen := map[string]bool{}
		for _, m := range anchor.FindAllStringSubmatch(body, -1) {
			seen[m[1]] = true
			if m[2] != githubRepo {
				t.Errorf("%s: %s points to %q", path, m[1], m[2])
			}
			if !strings.Contains(m[3], `rel="noopener"`) || !strings.Contains(m[3], `target="_blank"`) {
				t.Errorf("%s: %s missing target/rel: %q", path, m[1], m[3])
			}
			if !strings.Contains(m[4], `<svg class="gh-mark"`) || !strings.Contains(m[4], `aria-hidden="true"`) {
				t.Errorf("%s: %s has no inline aria-hidden SVG mark", path, m[1])
			}
			for _, bad := range []string{"<img", "src=", "style=", "主人", "你"} {
				if strings.Contains(m[0], bad) {
					t.Errorf("%s: %s contains %q", path, m[1], bad)
				}
			}
		}
		if !seen["gh-link"] || !seen["gh-foot"] {
			t.Errorf("%s: header/footer GitHub links missing: %v", path, seen)
		}
		for _, w := range words {
			if !strings.Contains(body, w) {
				t.Errorf("%s: missing %q", path, w)
			}
		}
	}
	_, llms := get(t, "/llms.txt")
	if !strings.Contains(llms, "- Source code: "+githubRepo+" (to read only). Install the CLI only from https://botobot.app") {
		t.Error("/llms.txt: GitHub line missing or no longer says to install only from botobot.app")
	}
}
