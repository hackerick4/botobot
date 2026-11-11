package prompts

import (
	"strings"
	"testing"
)

func sampleInfo() Info {
	return Info{
		BridgeName:      "研究小組",
		BridgeID:        "br_ABCDEFGHIJKLMNOPQRSTUV",
		Lang:            "zh-TW",
		InviteLink:      "https://host.example/b/br_ABCDEFGHIJKLMNOPQRSTUV",
		InviteCode:      "482913",
		ClaimToken:      "bbc_SECRETCLAIMTOKENVALUE1234567890abcdefg",
		ClaimExpiresRel: "30 分鐘",
		DLBase:          "https://host.example/dl",
		OwnerHint:       "Rick",
	}
}

func TestCreatorContainsEssentials(t *testing.T) {
	info := sampleInfo()
	got := Creator(info)

	for _, want := range []string{
		info.InviteLink,
		info.ClaimToken,
		info.ClaimExpiresRel,
		info.DLBase + "/bot-bridge-cli-linux-amd64",
		info.DLBase + "/manifest.json",
		"openssl pkeyutl -verify", "| sha256sum -c", "./bot-bridge-cli verify-self", ReleasePubKey,
		"bot-bridge-cli claim",
		"BB_CLAIM_TOKEN='" + info.ClaimToken + "'",
		"--display-name 'BOT NAME' --owner ",
		"每 5 分鐘",
		"ROUTINE_PROMPT",
		"INVITE_MESSAGE_BEGIN",
		"bot-bridge-cli claim --new",
		"https://host.example/join.md",
		SafetyLine,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("creator prompt missing %q\n---\n%s", want, got)
		}
	}
	if n := strings.Count(got, "\n"); n > 20 {
		t.Errorf("creator prompt too long: %d lines\n%s", n, got)
	}
}

func TestInviteShortAndNoToken(t *testing.T) {
	info := sampleInfo()
	got := Invite(info)

	for _, want := range []string{
		"bridge 連結：" + info.InviteLink + "\n",
		"邀請碼（請保密）：" + info.InviteCode + "\n",
		"加入說明：https://host.example/join.md\n",
		SafetyLine,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("invite prompt missing %q\n---\n%s", want, got)
		}
	}
	// Short: intro, link, code, manual, safety (the creator adds the admin line).
	if n := len(strings.Split(got, "\n")); n > 6 {
		t.Errorf("invite prompt has %d lines, want <= 6\n%s", n, got)
	}
	if strings.Contains(got, info.ClaimToken) || strings.Contains(got, "claim") {
		t.Errorf("invite prompt must not carry the claim token or command:\n%s", got)
	}
	// The manual is never asked to supply the admin fingerprint.
	if strings.Contains(got, "管理者指紋：") {
		t.Errorf("invite prompt should not contain a server-made admin fingerprint line")
	}
}

func TestCreatorOmitsInviteCode(t *testing.T) {
	// The invite is forwarded only from claim's INVITE_MESSAGE (which carries the
	// locally computed admin fingerprint), never from the creator prompt.
	info := sampleInfo()
	if got := Creator(info); strings.Contains(got, info.InviteCode) {
		t.Errorf("creator prompt should not carry the invite code:\n%s", got)
	}
}

func TestInviteMessageWithAdminFP(t *testing.T) {
	info := sampleInfo()
	info.AdminFP = "12345 67890 12345 67890 12345 67890"
	got := InviteMessage(info)
	lines := strings.Split(got, "\n")
	if lines[0] != InviteBegin || lines[len(lines)-1] != InviteEnd {
		t.Fatalf("markers wrong:\n%s", got)
	}
	if lines[len(lines)-2] != AdminFPLabel+info.AdminFP {
		t.Errorf("admin line must be last before the end marker:\n%s", got)
	}
	if strings.Contains(got, info.ClaimToken) {
		t.Errorf("invite message leaks the claim token")
	}
}

func TestRoutineLineCLIPath(t *testing.T) {
	got := RoutineLine(Info{BridgeID: "br_X", CLIPath: "/home/u/.local/bin/bot-bridge-cli"})
	if !strings.Contains(got, "執行 /home/u/.local/bin/bot-bridge-cli tick --bridge br_X；") {
		t.Errorf("routine line: %s", got)
	}
}

func TestManualURL(t *testing.T) {
	info := sampleInfo()
	if got := ManualURL(info); got != "https://host.example/join.md" {
		t.Errorf("ManualURL = %q", got)
	}
	info.ManualURL = "https://x.example/m.md"
	if got := ManualURL(info); got != "https://x.example/m.md" {
		t.Errorf("ManualURL override = %q", got)
	}
}

func TestRoutineLineHardcodesBridge(t *testing.T) {
	info := sampleInfo()
	line := RoutineLine(info)
	if !strings.Contains(line, "tick --bridge "+info.BridgeID) {
		t.Errorf("routine line missing hard-coded --bridge: %q", line)
	}
	if !strings.Contains(line, "NO_NEW_MESSAGES") {
		t.Errorf("routine line missing NO_NEW_MESSAGES guard: %q", line)
	}
	// BB_HOME prefix only when a non-default home is set.
	if strings.Contains(line, "BB_HOME=") {
		t.Errorf("routine line should not prefix BB_HOME when unset: %q", line)
	}
	info.BBHome = "/data/bot-a"
	if !strings.Contains(RoutineLine(info), "BB_HOME=/data/bot-a ") {
		t.Errorf("routine line should prefix BB_HOME when set")
	}
}

func TestSuggestHandle(t *testing.T) {
	cases := map[string]string{
		"Sample":    "sample-bot",
		"Amy Chen":  "amy-chen-bot",
		"王小明":       "my-bot", // non-ascii stripped -> fallback
		"":          "my-bot",
		"  -- --  ": "my-bot",
	}
	for in, want := range cases {
		if got := suggestHandle(in); got != want {
			t.Errorf("suggestHandle(%q) = %q, want %q", in, got, want)
		}
	}
}

// Secrets are passed via env vars, never as argv flags (Batch 1 cheap fix), and
// the routine line stays short (per-empty-wake token cost).
func TestPromptsKeepSecretsOutOfArgvAndRoutineShort(t *testing.T) {
	info := sampleInfo()
	c, i := Creator(info), Invite(info)
	for _, bad := range []string{"--token", "--code", "BB_CODE="} {
		if strings.Contains(c, bad) || strings.Contains(i, bad) {
			t.Errorf("prompt still uses %s flag", bad)
		}
	}
	if n := len([]rune(RoutineLine(info))); n > 110 {
		t.Errorf("routine line too long: %d runes", n)
	}
	if shq("a'b") != `'a'\''b'` {
		t.Errorf("shq quoting wrong: %s", shq("a'b"))
	}
}

// Without an owner hint the creator prompt carries no copyable example names.
func TestCreatorNoPlaceholderNames(t *testing.T) {
	info := sampleInfo()
	info.OwnerHint = ""
	c := Creator(info)
	if strings.Contains(c, "'my-bot'") || strings.Contains(c, "你的主人") || !strings.Contains(c, "--display-name 'BOT NAME' --owner 'OWNER NAME'") || !strings.Contains(c, HandleRuleZH) {
		t.Fatalf("creator prompt:\n%s", c)
	}
}

// English creator: English Creator and invite, the /en manual, --lang en on
// claim, and one handle rule shared by both languages (v0.4.9).
func TestEnglishPrompts(t *testing.T) {
	info := sampleInfo()
	info.Lang = "en"
	info.OwnerHint = ""
	info.ClaimExpiresRel = "30 minutes"
	c := Creator(info)
	for _, want := range []string{"https://host.example/en/join.md", "--display-name 'BOT NAME' --owner 'OWNER NAME' --lang en", HandleRuleEN, "claim --new --lang en", SafetyLineEN, "30 minutes"} {
		if !strings.Contains(c, want) {
			t.Errorf("english creator missing %q:\n%s", want, c)
		}
	}
	if strings.ContainsAny(c, "已請主") {
		t.Errorf("english creator has Chinese text:\n%s", c)
	}
	info.AdminFP = "12345 67890"
	i := Invite(info)
	for _, want := range []string{"Bridge link: " + info.InviteLink + "\n", "Invite code (keep private): " + info.InviteCode + "\n", "Join instructions: https://host.example/en/join.md\n", AdminFPLabelEN + "12345 67890"} {
		if !strings.Contains(i, want) {
			t.Errorf("english invite missing %q:\n%s", want, i)
		}
	}
	if strings.Contains(i, info.ClaimToken) {
		t.Error("invite carries the claim token")
	}
	for in, want := range map[string]string{"en": "en", "en-US": "en", "EN": "en", "zh-TW": "zh-TW", "": "zh-TW", "fr": "zh-TW"} {
		if NormLang(in) != want {
			t.Errorf("NormLang(%q) = %q", in, NormLang(in))
		}
	}
	if !strings.Contains(HandleRuleZH, "此 Bot 自己的名稱") || strings.Contains(HandleRuleZH, "太常見") || strings.Contains(HandleRuleZH, "-bot（") {
		t.Error("handle rule is not a single rule")
	}
}

// v0.6.11 (Rick t327u): the Creator prompt hands the invite to the owner as a
// chat message, never a downloaded/uploaded file — the download-txt workaround
// that ordinary-invite creators fell into. Rick 2026-11-10: the invite goes in
// its own message inside a ``` code block, and the BEGIN/END markers are not
// shown to the owner (a bot had merged the notice and invite into one plain
// bubble with the markers still in it).
func TestV0611CreatorInvitePasteAsText(t *testing.T) {
	zh := Creator(sampleInfo())
	if !strings.Contains(zh, "放進 ``` 程式碼區塊") || !strings.Contains(zh, "不要存成 .txt 或附件") {
		t.Errorf("zh Creator step 4 missing the code-block / not-a-file clarification:\n%s", zh)
	}
	en := sampleInfo()
	en.Lang = "en"
	got := Creator(en)
	if !strings.Contains(got, "inside a ``` code block") || !strings.Contains(got, "do not save it as a .txt or attachment") {
		t.Errorf("en Creator step 4 missing the code-block / not-a-file clarification:\n%s", got)
	}
}
