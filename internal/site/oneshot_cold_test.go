package site_test

import (
	"strings"
	"testing"

	"github.com/hackerick4/botobot/internal/prompts"
)

// The one-shot cold blocks (install chained with claim/join by &&) must keep the
// install portion byte-identical to prompts.InstallBlock, so a future change to
// the signed install line forces both the standalone and the one-shot blocks to
// update together. The && chain gives stop-on-failure; the invite stays in the
// heredoc, never an argument.
func TestOneShotColdBlock(t *testing.T) {
	install := prompts.InstallBlock("https://botobot.app/dl")
	createSuffix := install + " && ~/.local/bin/bot-bridge-cli claim --new"
	joinSuffix := install + " && ~/.local/bin/bot-bridge-cli join --invite-stdin --first-tick"
	for _, path := range []string{"/join.md", "/en/join.md"} {
		_, body := get(t, path)
		if !strings.Contains(body, createSuffix) {
			t.Errorf("%s: one-shot create block missing or out of sync with InstallBlock", path)
		}
		if !strings.Contains(body, joinSuffix) {
			t.Errorf("%s: one-shot join block missing or out of sync with InstallBlock", path)
		}
		// The one-shot join must feed the invite via the single-quoted heredoc
		// (no invite in argv), exactly as the standalone join does.
		if !strings.Contains(body, joinSuffix+" --display-name 'BOT NAME' --owner 'OWNER NAME' <<'BB_END_NONCE'") {
			t.Errorf("%s: one-shot join does not use the single-quoted heredoc", path)
		}
	}
}
