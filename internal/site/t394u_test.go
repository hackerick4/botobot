package site_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/hackerick4/botobot/internal/site"
)

// TestT394uNoTimePromise: the homepage CTA keeps the honest "no sign-up" line
// but drops the "about a minute" time promise (real onboarding spans several
// assistant turns and a poll, so a one-minute claim is not truthful).
func TestT394uNoTimePromise(t *testing.T) {
	for path, want := range map[string]struct{ keep, drop string }{
		"/zh": {keep: "不需註冊", drop: "約 1 分鐘"},
		"/en": {keep: "No sign-up.", drop: "About a minute"},
	} {
		_, body := get(t, path)
		if !strings.Contains(body, want.keep) {
			t.Errorf("%s dropped the honest CTA line %q", path, want.keep)
		}
		if strings.Contains(body, want.drop) {
			t.Errorf("%s still makes the %q time promise", path, want.drop)
		}
	}
}

// TestT394uDemoIntermediateStates: the chat demo now shows the real PROGRESS
// states the CLI prints (creating -> wait for peer; joined-no-recv -> connected)
// instead of jumping straight to "connected". Copy must match the CLI wording.
func TestT394uDemoIntermediateStates(t *testing.T) {
	for path, want := range map[string][]string{
		"/zh": {"正在建立通道…", "通道已建立，等待對方加入。", "還差一步：啟用收信…"},
		"/en": {"Creating the channel…", "Channel created; waiting for the other side to join.", "One more step: enable receiving…"},
	} {
		_, body := get(t, path)
		for _, w := range want {
			if !strings.Contains(body, w) {
				t.Errorf("%s demo missing intermediate state %q", path, w)
			}
		}
	}
	// No invented states: the demo must not claim it is "encrypting".
	for _, path := range []string{"/zh", "/en"} {
		_, body := get(t, path)
		for _, bad := range []string{"加密中", "Encrypting", "等待核准", "Waiting for approval"} {
			if strings.Contains(body, bad) {
				t.Errorf("%s demo invents a fake state %q", path, bad)
			}
		}
	}
}

// TestT394uWaitPeerDocumented: the manuals document the wait_peer tick status
// and its honest owner-facing copy, alongside the other PROGRESS lines.
func TestT394uWaitPeerDocumented(t *testing.T) {
	for path, want := range map[string][]string{
		"/join.md": {
			"wait_peer", "通道已建立，等待對方加入。",
			"正在建立通道…", "正在加入通道…",
			"已加入。還差一步：啟用收信…",
			"INVITE_FILE", "不是 JSON 的 `\"invite\"` 欄位",
			"掃描 JSON 行，不要假設第一行就是 JSON",
		},
		"/en/join.md": {
			"wait_peer", "Channel created; waiting for the other side to join.",
			"Creating the channel…", "Joining the channel…",
			"Joined. One step left: enable receiving…",
			"INVITE_FILE", "never the JSON `\"invite\"` field",
			"scan for JSON lines; do not assume the first line is JSON",
		},
		"/llms.txt": {
			"wait_peer", "Channel created; waiting for the other side to join.",
			"INVITE_FILE", "The invite is that INVITE_MESSAGE block, never the JSON `\"invite\"` field",
			"scan for JSON lines; do not assume the first line is JSON",
		},
	} {
		_, body := get(t, path)
		for _, w := range want {
			if !strings.Contains(body, w) {
				t.Errorf("%s missing %q", path, w)
			}
		}
	}
}

// TestT394uDedicatedBotNoPreUpdate: an already-installed dedicated bot goes
// straight to claim/join; the dedicated-bot section must not tell it to run
// update/verify-self first (that contradicts the section-1 warm path).
func TestT394uDedicatedBotNoPreUpdate(t *testing.T) {
	for path, want := range map[string]string{
		"/join.md":    "CLI 已裝好就直接進行，不用先 update／verify-self",
		"/en/join.md": "go straight to it when the CLI is already installed; no update/verify-self first",
		"/llms.txt":   "no update/verify-self first when the CLI is already installed",
	} {
		_, body := get(t, path)
		if !strings.Contains(body, want) {
			t.Errorf("%s dedicated-bot copy missing warm-path wording %q", path, want)
		}
	}
}

// TestT394uIconsServedAndLinked: favicon.ico and apple-touch-icon.png are
// served with the right content type and linked from both home pages.
func TestT394uIconsServedAndLinked(t *testing.T) {
	for path, ct := range map[string]string{
		"/favicon.ico":          "image/x-icon",
		"/apple-touch-icon.png": "image/png",
	} {
		res, body := get(t, path)
		if res.StatusCode != http.StatusOK || len(body) == 0 {
			t.Errorf("%s: status %d len %d", path, res.StatusCode, len(body))
		}
		if res.Header.Get("Content-Type") != ct {
			t.Errorf("%s: content-type %q, want %q", path, res.Header.Get("Content-Type"), ct)
		}
	}
	for _, page := range []string{"/zh", "/en"} {
		_, body := get(t, page)
		for _, link := range []string{`href="/favicon.ico"`, `rel="apple-touch-icon" href="/apple-touch-icon.png"`} {
			if !strings.Contains(body, link) {
				t.Errorf("%s does not link %q", page, link)
			}
		}
	}
}

// TestT394uNoWakeLanguage: the polling-only product never advertises a webhook
// or one-click wake. (The platform "approval tap" copy is unrelated and stays.)
func TestT394uNoWakeLanguage(t *testing.T) {
	for _, p := range site.Pages {
		if !strings.HasPrefix(p.ContentType, "text/") {
			continue
		}
		_, body := get(t, p.Path)
		for _, bad := range []string{"webhook", "Webhook", "一鍵", "one-click", "One-click", "one click"} {
			if strings.Contains(body, bad) {
				t.Errorf("%s advertises wake/webhook language %q", p.Path, bad)
			}
		}
	}
}

// TestT394uSitemapLastmod: the sitemap reflects this copy pass (2026-11-09).
func TestT394uSitemapLastmod(t *testing.T) {
	_, body := get(t, "/sitemap.xml")
	if !strings.Contains(body, "<lastmod>2026-11-09</lastmod>") {
		t.Error("sitemap.xml not bumped to 2026-11-09")
	}
	if strings.Contains(body, "<lastmod>2026-11-01</lastmod>") {
		t.Error("sitemap.xml still has stale 2026-11-01 lastmod")
	}
}
