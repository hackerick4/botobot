package site_test

import (
	"strings"
	"testing"
)

// v0.6.11 (Rick t327u/t326u): the published manuals carry the onboarding-UX
// fixes — invite pasted as chat text (no download/upload .txt for an ordinary
// invite), a scoped send note, a dedicated-bot description that does not read
// as "ask about every send", and never claiming the channel is ready before
// the routine (inbox) exists.
func TestV0611ManualOnboardingUX(t *testing.T) {
	for path, wants := range map[string][]string{
		"/join.md": {
			"邀請訊息（含邀請碼）是要交給主人的",
			"整段當一般聊天文字貼給主人（不要存成 .txt 或附件、也不要請主人下載或上傳檔案）",
			"這是此 Bot 用自己的寫檔工具存下貼來的文字，不是要主人下載或上傳檔案",
			"一般訊息直接收發，只有真正要主人決定的事才問主人",
			"routine 建好（收件匣開始運作）之前，不要說通道「已就緒」",
			"一般訊息可直接收發；只有寄信、付款或涉及個資的動作，送出前才需先確認",
		},
		"/en/join.md": {
			"paste the whole thing to your owner as ordinary chat text",
			"do not save it as a .txt or attachment, and do not ask your owner to download or upload a file",
			"This is this bot saving the pasted text with its own file tool, not asking your owner to download or upload a file.",
			"sends and receives ordinary messages directly, asks the owner only when something genuinely needs their decision",
			`do not tell your owner the channel is "ready" or "open"`,
			"ordinary messages are sent and received directly; only emails, payments and anything with personal data are confirmed before sending",
		},
	} {
		_, body := get(t, path)
		for _, w := range wants {
			if !strings.Contains(body, w) {
				t.Errorf("%s missing %q", path, w)
			}
		}
		// The old any-outbound phrasing must be gone from both manuals.
		if strings.Contains(body, "送出前一律先確認") {
			t.Errorf("%s still has the any-outbound 送出前一律先確認 phrasing", path)
		}
	}
	// The optional --invite-file command itself must still be documented.
	_, zh := get(t, "/join.md")
	if !strings.Contains(zh, "bot-bridge-cli join --invite-file ~/bb-invite.txt") {
		t.Error("/join.md dropped the optional --invite-file command")
	}
}
