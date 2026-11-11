package site_test

import (
	"strings"
	"testing"

	"github.com/hackerick4/botobot/internal/prompts"
)

// Rick 2026-11-05 (t311u): bot-bridge is for any AI assistant that meets the
// capability bar (DEV_SPEC §13.0), not Grok Bot only.
func TestV0610GeneralAssistants(t *testing.T) {
	want := map[string][]string{
		"/zh":         {"<p>能在自己的電腦執行指令、照說明安裝，並建立定期檢查的 AI 助理都可以。例如 Grok Bot、OpenAI Dot。</p>", `<p class="cta-label">傳給 AI 助理：</p>`, "已複製，貼到 AI 助理即可", `<span class="nowrap">按照 botobot.app 的說明，</span><wbr><span class="nowrap">開一條通道</span>`, "<summary>如何關上通道？</summary>"},
		"/en":         {"<p>Any AI assistant that can run commands on its own computer, follow the setup manual and create a recurring check. For example Grok Bot or OpenAI Dot.</p>", `<p class="cta-label">Send this to an AI assistant:</p>`, "Copied. Paste it into an AI assistant.", `<span class="nowrap">Open a channel</span> <span class="nowrap">following botobot.app</span>`, "<summary>How to close a channel</summary>"},
		"/join.md":    {"主人說「按照 botobot.app 的說明，開一條通道」（舊說法「建立一個 bot-bridge」）就是要建立", "名稱「通道：對方名稱」", "## 4. 建立每 5 分鐘一次的 routine（必做）", "平台的排程最短只能每小時", "不限平台，例如 Grok Bot、OpenAI Dot。需要四項能力", "其他平台改說該平台的移除方式。"},
		"/en/join.md": {"## 4. Create the routine (every 5 minutes, required)", "If the platform's shortest interval is hourly", "on any platform, such as Grok Bot or OpenAI Dot. A bot needs four things", "On other platforms, describe that platform's way to remove it."},
		"/llms.txt":   {"\"Open a channel following botobot.app\" (older wording: \"Create a bot-bridge\"", "## The routine (required)", "stop and tell your owner; never switch to a longer interval", "between AI assistants on any platform, such as Grok Bot or OpenAI Dot.", "elsewhere, the platform's own way"},
		"/delete.md":  {"「參考 https://botobot.app/delete 關上通道」（舊說法「…刪除對接」）"},
		"/agent.md":   {"This file is for AI assistants (bots) that can run shell commands on their own computer"},
	}
	ban := []string{"目前只支援 Grok Bot", "Grok Bot only", "支援 Grok Bot", "Works with Grok Bot", "傳給 Grok Bot", "Send this to Grok Bot", "交給 Grok Bot", "to Grok Bot", "貼到 Grok Bot", "into Grok Bot", "their own Grok Bot", "自己的 Grok Bot", "flag-grok", "for AI bots such as Grok Bot", "（例如 Grok Bot）。只在"}
	for path, ws := range want {
		_, body := get(t, path)
		for _, w := range ws {
			if !strings.Contains(body, w) {
				t.Errorf("%s missing %q", path, w)
			}
		}
		for _, b := range ban {
			if strings.Contains(body, b) {
				t.Errorf("%s still has Grok-only %q", path, b)
			}
		}
	}
	// The site demo shows the CLI's own invite first line.
	zhInv := strings.SplitN(prompts.Invite(prompts.Info{InviteLink: "x", InviteCode: "1"}), "\n", 2)[0]
	enInv := strings.SplitN(prompts.Invite(prompts.Info{InviteLink: "x", InviteCode: "1", Lang: "en"}), "\n", 2)[0]
	if strings.Contains(zhInv+enInv, "Grok") {
		t.Errorf("invite header names a platform: %q / %q", zhInv, enInv)
	}
	_, zh := get(t, "/zh")
	_, en := get(t, "/en")
	if !strings.Contains(zh, zhInv) || !strings.Contains(en, enInv) {
		t.Errorf("site demo invite != CLI invite: %q %q", zhInv, enInv)
	}
}
