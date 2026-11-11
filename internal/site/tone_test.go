package site_test

import (
	"strings"
	"testing"
)

// The public copy stays plain (Rick t409u): no AI-marketing vocabulary, in
// either language, on any served owner-facing page. The site is already clean of
// these; this test locks that so a regression fails cheaply. Banned tokens are
// ones that never legitimately appear in this product's plain copy.
func TestNoMarketingTone(t *testing.T) {
	pages := []string{
		"/zh", "/en", "/join.md", "/en/join.md",
		"/delete", "/en/delete", "/llms.txt",
	}
	// Chinese marketing adjectives / slogans (exact substrings).
	zhBanned := []string{
		"無縫", "一站式", "賦能", "打造", "無憂", "暢享", "一鍵", "極速",
		"卓越", "智能化", "助力", "輕鬆", "強大", "賦予", "煥新", "顛覆",
		"盡在掌握", "輕而易舉",
	}
	// English marketing words (matched case-insensitively as substrings).
	enBanned := []string{
		"seamless", "effortless", "powerful", "unlock", "empower", "elevate",
		"supercharge", "cutting-edge", "revolutioniz", "game-chang", "frictionless",
		"blazing", "best-in-class", "one-stop", "next-gen", "serves many",
	}
	for _, p := range pages {
		_, body := get(t, p)
		for _, b := range zhBanned {
			if strings.Contains(body, b) {
				t.Errorf("%s has AI-marketing token %q", p, b)
			}
		}
		low := strings.ToLower(body)
		for _, b := range enBanned {
			if strings.Contains(low, b) {
				t.Errorf("%s has AI-marketing token %q", p, b)
			}
		}
	}
}

// Round 2 (Rick t409u): the "Bot to Bot. Do to Done." slogan is retired from the
// page. It must not appear as the hero <h1>, in the <title>/og:title, or in the
// meta description; the hero <h1> must be a plain descriptive heading, not that
// slogan. The BotoBot wordmark (the brand name) still identifies the site.
func TestTaglineRetired(t *testing.T) {
	for _, p := range []string{"/zh", "/en"} {
		_, body := get(t, p)
		if strings.Contains(body, "Do to Done") || strings.Contains(body, "Bot to Bot.") {
			t.Errorf("%s: the retired slogan still appears somewhere on the page", p)
		}
		// Hero h1 exists and is not the slogan.
		i := strings.Index(body, `class="hero-h1"`)
		if i < 0 {
			t.Fatalf("%s: no hero h1", p)
		}
		h1 := body[i : i+strings.Index(body[i:], "</h1>")]
		if strings.Contains(h1, "Do to Done") {
			t.Errorf("%s: hero h1 is still the slogan", p)
		}
		// The BotoBot brand name is still present (header/footer wordmark).
		if !strings.Contains(body, "BotoBot") {
			t.Errorf("%s: BotoBot brand name missing", p)
		}
	}
}
