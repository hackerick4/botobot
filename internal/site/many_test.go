package site_test

import (
	"strings"
	"testing"
)

// v0.6.0: the public-entry section (one service bot, many private lines) is on
// both homepages with its five animation captions, a copyable instruction and
// the updated "more assistants" FAQ answer.
func TestHomepagePublicEntrySection(t *testing.T) {
	for _, c := range []struct{ path, instr, faq string }{
		{"/zh", "按照 botobot.app 的說明建立公開入口", "公開入口"},
		{"/en", "Follow botobot.app to create a public entry", "public entry"},
	} {
		_, page := get(t, c.path)
		if !strings.Contains(page, `id="many"`) || !strings.Contains(page, "data-many") {
			t.Fatalf("%s: no public entry section", c.path)
		}
		for _, a := range []string{"data-cap0=", "data-cap4=", `id="pe-line2"`, `id="pe-x"`, `id="many-cap"`, "data-one", `id="po-line"`, `class="mode-h"`, c.instr} {
			if !strings.Contains(page, a) {
				t.Errorf("%s: missing %s", c.path, a)
			}
		}
		i := strings.Index(page, `id="faq"`)
		// The public-entry explanation now lives in the #many prose + instruction
		// (Round 3 trimmed the duplicate FAQ item); the concept must still appear.
		if !strings.Contains(page, c.faq) {
			t.Errorf("%s: page does not mention %q", c.path, c.faq)
		}
		j := strings.Index(page, `id="many"`)
		sec := page[j:i]
		for _, w := range []string{"restaurant", "Restaurant", "shop", "Shop", "clinic", "store", "booking", "guest", "餐廳", "店家", "商家", "診所", "訂位", "訪客", "菜"} {
			if strings.Contains(sec, w) {
				t.Errorf("%s: public entry section should be industry-neutral, found %q", c.path, w)
			}
		}
		if !strings.Contains(sec, "data-out=") || !strings.Contains(sec, "data-in=") {
			t.Errorf("%s: message bubbles need data-in / data-out words", c.path)
		}
		if strings.Index(page, `id="many"`) > i {
			t.Errorf("%s: section should come before the FAQ", c.path)
		}
	}
}
