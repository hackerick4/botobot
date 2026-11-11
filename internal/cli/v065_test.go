package cli

import (
	"strings"
	"testing"
)

// v0.6.5 round-12 copy: neutral, plain, no 主人/你 in the new owner lines.
func noOwnerWords(t *testing.T, s string) {
	t.Helper()
	if strings.Contains(s, "主人") || strings.Contains(s, "你") || strings.Contains(s, "bridge_id") {
		t.Fatalf("owner line not neutral: %q", s)
	}
}

func TestPairEndedNotice(t *testing.T) {
	bc := &bridgeConfig{BridgeID: "br_x", Name: "研究小組"}
	if got := pairEndedNotice(bc, true); got != "NOTIFY_OWNER: 「研究小組」已關閉，雙方都無法再傳訊息。其他連線不受影響。" {
		t.Fatalf("closed: %q", got)
	}
	if got := pairEndedNotice(bc, false); got != "NOTIFY_OWNER: 已離開「研究小組」，其他連線不受影響。" {
		t.Fatalf("left: %q", got)
	}
	bc.Lang = "en"
	if got := pairEndedNotice(bc, true); got != `NOTIFY_OWNER: "研究小組" is closed. Neither side can send messages on it any more. Other lines are not affected.` {
		t.Fatalf("en: %q", got)
	}
	noOwnerWords(t, pairEndedNotice(&bridgeConfig{BridgeID: "br_y"}, true))
}

func TestUpdateNotice(t *testing.T) {
	cases := []struct {
		res  updateResult
		en   bool
		want string
	}{
		{updateResult{Status: "updated", Current: "v0.6.4", Latest: "v0.6.5"}, false, "NOTIFY_OWNER: bot-bridge 已更新到 v0.6.5（官方版本）。"},
		{updateResult{Status: "up_to_date", Current: "v0.6.5"}, false, "NOTIFY_OWNER: bot-bridge 已是最新版本（v0.6.5），不需要更新。"},
		{updateResult{Status: "not_modified", Current: "v0.6.5"}, false, "NOTIFY_OWNER: bot-bridge 已是最新版本（v0.6.5），不需要更新。"},
		{updateResult{Status: "up_to_date", Current: "v0.6.0", Latest: "v0.6.4"}, false, "NOTIFY_OWNER: bot-bridge 已是最新版本（v0.6.4），不需要更新。"},
		{updateResult{Status: "available", Current: "v0.6.4", Latest: "v0.6.5"}, false, "NOTIFY_OWNER: bot-bridge 有新版本（v0.6.5）可以更新。"},
		{updateResult{Status: "updated", Current: "v0.6.4", Latest: "v0.6.5"}, true, "NOTIFY_OWNER: bot-bridge was updated to v0.6.5 (the official version)."},
		{updateResult{Status: "up_to_date", Current: "v0.6.5"}, true, "NOTIFY_OWNER: bot-bridge is already the latest version (v0.6.5). No update needed."},
	}
	for _, c := range cases {
		got := updateNotice(&c.res, c.en)
		if got != c.want {
			t.Fatalf("%+v: %q", c.res, got)
		}
		noOwnerWords(t, got)
	}
	if allEnglish(nil) || !allEnglish([]*bridgeConfig{{Lang: "en"}}) || allEnglish([]*bridgeConfig{{Lang: "en"}, {Lang: "zh"}}) {
		t.Fatal("allEnglish")
	}
}

func TestEntryCreatedNoticeSaysWhere(t *testing.T) {
	zh := strings.Join(entryCreatedNotice("Desk", "https://botobot.app/e/x", false), "\n")
	if !strings.Contains(zh, "有新的 Bot 加入時，會在這個對話裡簡短通知，不必另外查看。") || strings.Contains(zh, "會在這裡通知") {
		t.Fatalf("zh: %s", zh)
	}
	noOwnerWords(t, zh)
	en := strings.Join(entryCreatedNotice("Desk", "https://botobot.app/e/x", true), "\n")
	if !strings.Contains(en, "When a new bot joins, a short notice appears in this chat") {
		t.Fatalf("en: %s", en)
	}
}

func TestLinesEnded(t *testing.T) {
	three := 3
	if linesEnded(&three, 0) != 3 || linesEnded(nil, 2) != 2 {
		t.Fatal("linesEnded")
	}
}

// Rick 2026-11-03 20:41: the audit lines carry no 主人.
func TestReleasedNowLineNeutral(t *testing.T) {
	n := releaseNotice{ID: "h_x", Hash: "0123456789abcdef", ReleasedMS: 1}
	zh, en := releasedNowLine(n, false), releasedNowLine(n, true)
	if !strings.HasSuffix(zh, "經確認後執行。若未經同意，請要求助理停止並檢查紀錄。") || !strings.HasSuffix(en, "after it was confirmed. If this was not agreed to, ask the assistant to stop and check the log.") {
		t.Fatalf("%q / %q", zh, en)
	}
	noOwnerWords(t, zh)
	if strings.Contains(strings.ToLower(strings.TrimPrefix(en, "NOTIFY_OWNER: ")), "owner") {
		t.Fatalf("en: %q", en)
	}
	if l := releaseNoticeLine(n); strings.Contains(l, "主人") || !strings.HasSuffix(l, "若未經同意，請要求助理停止並檢查紀錄。") {
		t.Fatalf("tick notice: %q", l)
	}
}
