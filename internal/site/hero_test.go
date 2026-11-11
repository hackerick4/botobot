package site_test

import (
	"regexp"
	"strings"
	"testing"
)

var reHeroDot = regexp.MustCompile(`class="dot" data-step="\d" data-cap="([^"]+)"`)

// The hero shows each AI assistant on its own person's computer, reaching the
// other bot through the botobot.app cloud (Rick t158u). The cloud only relays:
// no wording may suggest it keeps messages. Six steps; the JS requires six dots.
func TestHeroComputersAndCloud(t *testing.T) {
	cases := []struct {
		path string
		want []string
		caps []string
		bad  []string
	}{
		{"/zh",
			[]string{"Amy 的電腦", "Bob 的電腦", "botobot.app 雲端"},
			[]string{"AI 助理各在自己的電腦上", "Amy 交代要辦的事", "訊息加密後送出", "雲端只轉送，讀不到內容", "寄信、付款前先問過", "回覆原路送回"},
			[]string{"儲存", "保存", "存放", "BotoBot 只轉送密文"}},
		{"/en",
			[]string{"Amy&#39;s computer", "Bob&#39;s computer", "botobot.app cloud"},
			nil,
			[]string{"store", "stores", "stored", "saved", "kept in the cloud"}},
	}
	for _, c := range cases {
		_, body := get(t, c.path)
		a := strings.Index(body, `<div class="hero-explain">`)
		b := strings.Index(body, `<!-- ░░ STORY`)
		if a < 0 || b < a {
			t.Fatalf("%s: hero block not found", c.path)
		}
		hero := body[a:b]
		for _, w := range c.want {
			if !strings.Contains(hero, w) && !strings.Contains(hero, strings.ReplaceAll(w, "&#39;", "'")) {
				t.Errorf("%s hero: missing %q", c.path, w)
			}
		}
		dots := reHeroDot.FindAllStringSubmatch(hero, -1)
		if len(dots) != 6 {
			t.Fatalf("%s hero: %d step dots, want 6", c.path, len(dots))
		}
		if strings.Count(hero, `class="sb-panel"`) != 6 {
			t.Errorf("%s hero: storyboard must have 6 panels", c.path)
		}
		for i, w := range c.caps {
			if dots[i][1] != w {
				t.Errorf("%s step %d caption = %q, want %q", c.path, i+1, dots[i][1], w)
			}
		}
		low := strings.ToLower(visibleText(hero))
		for _, w := range c.bad {
			if regexp.MustCompile(`(?i)(^|[^a-z])` + regexp.QuoteMeta(w) + `([^a-z]|$)`).MatchString(low) {
				t.Errorf("%s hero: wording %q implies storage or is outdated", c.path, w)
			}
		}
	}
}
