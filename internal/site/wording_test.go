package site_test

import (
	"html"
	"regexp"
	"strings"
	"testing"
)

var (
	reHidden = regexp.MustCompile(`(?s)<script\b[^>]*>.*?</script>|<style\b[^>]*>.*?</style>|<!--.*?-->`)
	reAttr   = regexp.MustCompile(`(?:content|aria-label|data-cap|alt|title)="([^"]*)"`)
	reTag    = regexp.MustCompile(`<[^>]+>`)
	reEnPron = regexp.MustCompile(`\b(?:you|your|yours|we|our|us|I|I’m|I’ll|me|my|mine)\b`)
)

// visibleText returns the text a visitor can see or hear: element text plus
// meta, aria-label, data-cap, alt and title values.
func visibleText(page string) string {
	s := reHidden.ReplaceAllString(page, "")
	var b strings.Builder
	for _, m := range reAttr.FindAllStringSubmatch(s, -1) {
		b.WriteString(m[1] + "\n")
	}
	b.WriteString(reTag.ReplaceAllString(s, "\n"))
	return html.UnescapeString(b.String())
}

// Visitor-facing copy uses neutral product wording without pronouns that
// address the reader or speak as the product (Dana, copy pass v7). The H1 and
// the CTA one-liner are fixed and contain none.
func TestHomepageNeutralWording(t *testing.T) {
	_, zh := get(t, "/zh")
	for _, l := range strings.Split(visibleText(zh), "\n") {
		// v0.5.2 (Dana t156u): the demo caption addresses the visitor.
		if strings.TrimSpace(l) == "你與 Grok Bot" {
			continue
		}
		if strings.ContainsAny(l, "你您我") || strings.Contains(l, "他的") {
			t.Errorf("/zh pronoun: %q", strings.TrimSpace(l))
		}
	}
	_, en := get(t, "/en")
	for _, l := range strings.Split(visibleText(en), "\n") {
		if m := reEnPron.FindString(l); m != "" {
			t.Errorf("/en pronoun %q: %q", m, strings.TrimSpace(l))
		}
	}
}
