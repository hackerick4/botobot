package site_test

import (
	"strings"
	"testing"

	"github.com/hackerick4/botobot/internal/prompts"
)

// v0.6.3: the site says where the official download is, in plain words, and
// the manuals publish the release key and the SHA-256 list for bots.
func TestV063OfficialDownloadCopy(t *testing.T) {
	for path, want := range map[string][]string{
		"/zh": {"怎麼確認是官方版本？", "只從 botobot.app 下載，其他網址都不是官方版本。Grok Bot 安裝時會自動核對，不符就停止安裝。", `<p class="foot-tag">只從 botobot.app 下載，其他網址都不是官方版本。</p>`},
		"/en": {"How to tell the official version", "Download only from botobot.app. Other addresses are not official. Grok Bot checks the download when installing and stops if it does not match.", `<p class="foot-tag">Download only from botobot.app. Other addresses are not official.</p>`},
	} {
		_, body := get(t, path)
		for _, w := range want {
			if !strings.Contains(body, w) {
				t.Errorf("%s missing %q", path, w)
			}
		}
		i := strings.Index(body, `id="faq-official"`)
		if i < 0 {
			t.Fatalf("%s: no faq-official", path)
		}
		item := body[i : i+strings.Index(body[i:], "</details>")]
		for _, bad := range []string{"https://", "主人", "你", "SHA", "openssl", "key"} {
			if strings.Contains(item, bad) {
				t.Errorf("%s faq-official has %q", path, bad)
			}
		}
	}
	for _, path := range []string{"/join.md", "/en/join.md", "/llms.txt"} {
		_, body := get(t, path)
		for _, w := range []string{"117a95e2967bf881", "iNGy6U+ZD9Se4uDfiDY8U1AUYz6bgB3CdjBDQJZmpks=", "https://botobot.app/dl/cli/SHA256SUMS", "https://botobot.app/dl/manifest.json", "verify-self"} {
			if !strings.Contains(body, w) {
				t.Errorf("%s missing %q", path, w)
			}
		}
		if strings.Contains(body, "dl/bot-bridge-cli-linux-amd64.sha256") {
			t.Errorf("%s still installs with the unsigned .sha256 next to the binary", path)
		}
	}
	// The manuals show exactly the command the server's creator_prompt uses.
	for _, path := range []string{"/join.md", "/en/join.md"} {
		_, body := get(t, path)
		if !strings.Contains(body, "    "+prompts.InstallBlock("https://botobot.app/dl")+"\n") {
			t.Errorf("%s: install command differs from prompts.InstallBlock", path)
		}
	}
	_, zh := get(t, "/join.md")
	_, en := get(t, "/en/join.md")
	for _, w := range []string{"「下載的 bot-bridge 程式和官方版本不符，沒有安裝。只從 botobot.app 下載。」", "bridge 上的連結、別人傳來的檔案、其他網域（包括看起來很像的網址）都不是官方版本"} {
		if !strings.Contains(zh, w) {
			t.Errorf("join.md missing %q", w)
		}
	}
	for _, w := range []string{`"The downloaded bot-bridge program does not match the official one, so it was not installed. Download it only from botobot.app."`, "other domains (including lookalike addresses) are not official"} {
		if !strings.Contains(en, w) {
			t.Errorf("en/join.md missing %q", w)
		}
	}
}
