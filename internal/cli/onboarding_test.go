package cli

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// claimFresh creates a bridge via POST /join and claims it into a new home.
// It returns the invite link, invite code, the claim output and the admin home.
func claimFresh(t *testing.T, url string) (link, code, out, homeA string) {
	t.Helper()
	created := postJoin(t, url)
	link, _ = created["invite_link"].(string)
	code, _ = created["invite_code"].(string)
	token, _ := created["claim_token"].(string)
	homeA = t.TempDir()
	out, errOut, rc := run(t, "claim", "--home", homeA, "--link", link, "--token", token, "--handle", "sample-bot", "--owner", "Rick")
	if rc != exitOK {
		t.Fatalf("claim exit %d, stderr=%s", rc, errOut)
	}
	return link, code, out, homeA
}

func rawLine(out, prefix string) string {
	for _, l := range strings.Split(out, "\n") {
		if strings.HasPrefix(l, prefix) {
			return strings.TrimPrefix(l, prefix)
		}
	}
	return ""
}

// claim prints the routine line (bridge id and non-default BB_HOME filled in),
// the owner notice with the fingerprint, and the admin line for the invite.
func TestClaimPrintsOnboarding(t *testing.T) {
	url := startTestServer(t)
	_, _, out, homeA := claimFresh(t, url)
	c := findLine(t, out, "claimed")
	br, fp := c["bridge_id"].(string), c["fingerprint"].(string)
	want := "執行 BB_HOME=" + homeA + " bot-bridge-cli tick；"
	_ = br
	if r := rawLine(out, "ROUTINE_PROMPT: "); !strings.HasPrefix(r, want) || c["routine_prompt"] != r {
		t.Errorf("routine line = %q (json %q), want prefix %q", r, c["routine_prompt"], want)
	}
	msg := inviteBlockOf(out)
	if msg == "" || c["invite_message"] != nil || strings.Count(out, "邀請碼（請保密）：") != 1 {
		t.Fatalf("missing INVITE_MESSAGE block:\n%s", out)
	}
	if !strings.HasSuffix(msg, "\n管理者指紋："+fp) || !strings.Contains(msg, "邀請碼（請保密）：") {
		t.Errorf("invite block = %q", msg)
	}
	// TOFU without prompts: the owner is never asked to compare fingerprints.
	if n := rawLine(out, "NOTIFY_OWNER: "); strings.Contains(n, fp) || !strings.HasSuffix(n, "已建立。下一則是邀請，請整段私下傳給對方，請對方直接貼給自己的 AI 助理。") || !strings.Contains(out, "NOTIFY_OWNER: "+ownerCautionZH) || strings.Contains(out, "NOTIFY_OWNER: bridge br_") {
		t.Errorf("owner notice = %q", n)
	}
}

// A fresh claim (no prior routine) prints ready-to-paste ROUTINE_CREATE params
// so the assistant can create the "bot-bridge inbox" routine in one turn: valid
// JSON with the fixed name, the 5-minute cadence, and the exact ROUTINE_PROMPT
// text as the prompt (warm08 claim->routine turn cut).
func TestClaimPrintsRoutineCreateParams(t *testing.T) {
	url := startTestServer(t)
	_, code, out, _ := claimFresh(t, url)
	line := rawLine(out, "ROUTINE_CREATE: ")
	if line == "" {
		t.Fatalf("no ROUTINE_CREATE line:\n%s", out)
	}
	spec, err := parseRoutineCreate(line)
	if err != nil {
		t.Fatalf("ROUTINE_CREATE not valid JSON (%v): %q", err, line)
	}
	if spec.Name != "bot-bridge inbox" || spec.EveryMinutes != 5 || spec.PollIntervalSec != 300 {
		t.Errorf("ROUTINE_CREATE params = %+v", spec)
	}
	// A fresh bot (no prior bridge, no entry) creates outright: no reuse to confirm.
	if spec.ReuseIfConfirmed {
		t.Errorf("fresh claim: reuse_if_confirmed should be false, got %+v", spec)
	}
	// The prompt must be exactly the ROUTINE_PROMPT line (and the JSON result).
	if rp := rawLine(out, "ROUTINE_PROMPT: "); spec.Prompt != rp || spec.Prompt == "" {
		t.Errorf("ROUTINE_CREATE prompt %q != ROUTINE_PROMPT %q", spec.Prompt, rp)
	}
	// It must sit between ROUTINE_PROMPT and the NEXT prose.
	if strings.Index(out, "ROUTINE_CREATE: ") < strings.Index(out, "ROUTINE_PROMPT: ") {
		t.Error("ROUTINE_CREATE printed before ROUTINE_PROMPT")
	}
	// No secrets ever: the line is only the tick command, no invite code.
	if strings.Contains(line, "邀請碼") || (code != "" && strings.Contains(line, code)) {
		t.Error("ROUTINE_CREATE must not carry the invite code")
	}
}

type routineCreateSpec struct {
	Name             string `json:"name"`
	EveryMinutes     int    `json:"every_minutes"`
	PollIntervalSec  int    `json:"poll_interval_sec"`
	Prompt           string `json:"prompt"`
	ReuseIfConfirmed bool   `json:"reuse_if_confirmed"`
}

func parseRoutineCreate(line string) (routineCreateSpec, error) {
	var s routineCreateSpec
	err := json.Unmarshal([]byte(line), &s)
	return s, err
}

// warm09 fix: the ROUTINE_CREATE line must ALSO appear when the bot has a prior
// bridge (returning users / test bots), with reuse_if_confirmed:true so the
// assistant still confirms an existing enabled routine before creating — the
// earlier "fresh only" gate meant the line almost never showed for real bots.
func TestRoutineCreateReuseFlagOnPriorBridge(t *testing.T) {
	url := startTestServer(t)
	_, _, _, home := claimFresh(t, url)

	// Second bridge for the same bot (same home) -> prior > 0.
	o2, e, c := run(t, "claim", "--new", "--server", url, "--home", home,
		"--name", "Second", "--owner", "Rick")
	if c != exitOK {
		t.Fatalf("second claim: %d %s", c, e)
	}
	line := rawLine(o2, "ROUTINE_CREATE: ")
	if line == "" {
		t.Fatalf("prior-bridge claim did not print ROUTINE_CREATE:\n%s", o2)
	}
	spec, err := parseRoutineCreate(line)
	if err != nil {
		t.Fatalf("ROUTINE_CREATE not valid JSON (%v): %q", err, line)
	}
	if !spec.ReuseIfConfirmed {
		t.Errorf("prior-bridge claim: reuse_if_confirmed should be true, got %+v", spec)
	}
	if spec.Name != "bot-bridge inbox" || spec.EveryMinutes != 5 || spec.PollIntervalSec != 300 {
		t.Errorf("ROUTINE_CREATE params = %+v", spec)
	}
	if rp := rawLine(o2, "ROUTINE_PROMPT: "); spec.Prompt != rp || spec.Prompt == "" {
		t.Errorf("ROUTINE_CREATE prompt %q != ROUTINE_PROMPT %q", spec.Prompt, rp)
	}
	// The conditional-reuse NEXT prose must still be present (no duplicate induced).
	if !strings.Contains(o2, "NEXT:") {
		t.Errorf("prior-bridge claim missing NEXT reuse prose:\n%s", o2)
	}
}

// inviteBlock returns the text between the INVITE_MESSAGE markers.
func inviteBlockOf(out string) string {
	i := strings.Index(out, "INVITE_MESSAGE_BEGIN\n")
	j := strings.Index(out, "\nINVITE_MESSAGE_END")
	if i < 0 || j < i {
		return ""
	}
	return out[i+len("INVITE_MESSAGE_BEGIN\n") : j]
}

// The primary join path: the whole forwarded invite on stdin. The admin gets
// one NOTIFY_OWNER line when the member joins, and the used code is never
// offered again; a rotate (owner-requested) prints a fresh complete invite.
func TestJoinInviteStdinFlow(t *testing.T) {
	url := startTestServer(t)
	_, _, out, homeA := claimFresh(t, url)
	br := findLine(t, out, "claimed")["bridge_id"].(string)
	invite := "主人轉來：\n" + inviteBlockOf(out) + "\n"

	homeB := t.TempDir()
	jout, errOut, rc := runStdin(t, invite, "join", "--home", homeB, "--invite-stdin", "--handle", "amy-bot", "--owner", "Amy Lin")
	if rc != exitOK {
		t.Fatalf("join --invite-stdin exit %d: %s", rc, errOut)
	}
	if j := findLine(t, jout, "joined"); j["admin_fingerprint_check"] != fpMatch {
		t.Fatalf("joined = %v", j)
	}

	// Admin tick: one owner line naming the new member.
	tout, errOut, rc := run(t, "tick", "--home", homeA, "--bridge", br)
	if rc != exitOK {
		t.Fatalf("tick exit %d: %s", rc, errOut)
	}
	if n := rawLine(tout, "NOTIFY_OWNER: "); !strings.Contains(n, "amy-bot（Amy Lin）已加入「研究小組」") {
		t.Errorf("admin join notice = %q\n%s", n, tout)
	}
	if strings.Count(tout, "已加入") != 1 {
		t.Errorf("join notice should appear once:\n%s", tout)
	}
	tout, _, _ = run(t, "tick", "--home", homeA, "--bridge", br)
	if strings.Contains(tout, "已加入") {
		t.Errorf("join notice repeated on the next tick:\n%s", tout)
	}
	// The member does not get the admin-only line.
	mout, _, _ := run(t, "tick", "--home", homeB, "--bridge", br)
	if strings.Contains(mout, "已加入") {
		t.Errorf("member got the admin join notice:\n%s", mout)
	}

	// The one-time code is used: invite-code offers no dead invite.
	iout, errOut, rc := run(t, "invite-code", "--home", homeA, "--bridge", br)
	if rc != exitOK {
		t.Fatalf("invite-code exit %d: %s", rc, errOut)
	}
	if inviteBlockOf(iout) != "" || findLine(t, iout, "invite_code")["usable"] != false {
		t.Errorf("used code offered again:\n%s", iout)
	}

	// The rest covers a pre-v0.5.0 multi-member bridge (grandfathered).
	makeLegacy(t, url, br)
	// Rotate is held; once released it prints a fresh complete invite.
	rout, _, rc := runApproved(t, "invite-code", "--home", homeA, "--bridge", br, "--rotate")
	if rc != exitOK {
		t.Fatalf("rotate exit %d:\n%s", rc, rout)
	}
	fresh := inviteBlockOf(rout)
	if fresh == "" || fresh == inviteBlockOf(out) || !strings.Contains(fresh, "管理者指紋：") {
		t.Fatalf("rotate should print a new invite block:\n%s", rout)
	}
	homeC := t.TempDir()
	if _, errOut, rc := runStdin(t, fresh, "join", "--home", homeC, "--invite-stdin", "--handle", "bob-bot", "--owner", "Bob"); rc != exitOK {
		t.Fatalf("second member join exit %d: %s", rc, errOut)
	}
	// The first invite cannot be reused.
	homeD := t.TempDir()
	if _, _, rc := runStdin(t, invite, "join", "--home", homeD, "--invite-stdin", "--handle", "eve-bot", "--owner", "Eve"); rc == exitOK {
		t.Fatalf("old invite joined again")
	}

	// A member leaving: one short admin-only owner line, once.
	run(t, "tick", "--home", homeA, "--bridge", br) // consume bob-bot's join
	if _, errOut, rc := runApproved(t, "leave", "--home", homeC, "--bridge", br); rc != exitOK {
		t.Fatalf("leave exit %d: %s", rc, errOut)
	}
	tout, _, _ = run(t, "tick", "--home", homeA, "--bridge", br)
	if n := rawLine(tout, "NOTIFY_OWNER: "); n != "bob-bot（Bob）已離開「研究小組」。" || strings.Count(tout, "已離開") != 1 {
		t.Errorf("admin leave notice = %q\n%s", n, tout)
	}
	if tout, _, _ = run(t, "tick", "--home", homeA, "--bridge", br); strings.Contains(tout, "已離開") {
		t.Errorf("leave notice repeated:\n%s", tout)
	}
	if mout, _, _ = run(t, "tick", "--home", homeB, "--bridge", br); strings.Contains(mout, "已離開") {
		t.Errorf("member got the admin leave notice:\n%s", mout)
	}
}

func TestParseInvite(t *testing.T) {
	good := "bot-bridge 邀請。請將整段交給 AI 助理：\nbridge 連結：https://botobot.app/b/br_ABCDEFGHIJKLMNOPQRSTUV\n邀請碼（請保密）：482913\n加入說明：https://botobot.app/join.md\n安全：x\n管理者指紋：12345 67890 12345 67890 12345 67890"
	l, c, fp, err := parseInvite(good)
	if err != nil || l != "https://botobot.app/b/br_ABCDEFGHIJKLMNOPQRSTUV" || c != "482913" || fp != "123456789012345678901234567890" {
		t.Fatalf("parseInvite = %q %q %q %v", l, c, fp, err)
	}
	for name, bad := range map[string]string{
		"no link":   "邀請碼：482913",
		"no code":   "https://botobot.app/b/br_ABCDEFGHIJKLMNOPQRSTUV",
		"two links": good + "\nhttps://evil.example/b/br_ZZZZZZZZZZZZZZZZZZZZZZ",
		"two codes": good + "\n邀請碼：111111",
		"two fps":   good + "\n管理者指紋：99999 99999 99999 99999 99999 99999",
	} {
		if _, _, _, err := parseInvite(bad); err == nil {
			t.Errorf("%s: want error", name)
		}
	}
	// Repeating the same invite twice is not ambiguous.
	if _, _, _, err := parseInvite(good + "\n" + good); err != nil {
		t.Errorf("duplicate paste: %v", err)
	}
}

func TestJoinInviteStdinRefusesMixedFlags(t *testing.T) {
	_, errOut, rc := runStdin(t, "x", "join", "--home", t.TempDir(), "--invite-stdin", "--link", "https://h/b/br_x", "--handle", "amy-bot")
	if rc != exitUsage || !strings.Contains(errOut, "--invite-stdin") {
		t.Fatalf("rc=%d err=%s", rc, errOut)
	}
}

func TestJoinAdminFingerprintCheck(t *testing.T) {
	url := startTestServer(t)

	// match: the line copied from the invite, with the label and odd spacing.
	link, code, out, _ := claimFresh(t, url)
	adminFP := findLine(t, out, "claimed")["fingerprint"].(string)
	homeB := t.TempDir()
	out, errOut, rc := run(t, "join", "--home", homeB, "--link", link, "--code", code,
		"--handle", "amy-bot", "--owner", "Amy", "--admin-fp", "管理者指紋： "+strings.ReplaceAll(adminFP, " ", "  "))
	if rc != exitOK {
		t.Fatalf("join exit %d, stderr=%s", rc, errOut)
	}
	j := findLine(t, out, "joined")
	if j["admin_fingerprint_check"] != fpMatch || j["admin_fingerprint"] != adminFP {
		t.Fatalf("joined = %v", j)
	}
	if r := rawLine(out, "ROUTINE_PROMPT: "); !strings.HasPrefix(r, "執行 BB_HOME="+homeB+" bot-bridge-cli tick；") {
		t.Errorf("missing routine line: %q", out)
	}
	if n := rawLine(out, "NOTIFY_OWNER: "); n != "已加入「研究小組」。" || strings.Contains(out, j["fingerprint"].(string)+"。") || !strings.Contains(out, "NOTIFY_OWNER: "+ownerCautionZH) {
		t.Errorf("owner notice = %q", n)
	}
	// Matched: the admin is trusted, so the member can send right away.
	if _, errOut, rc = run(t, "send", "--home", homeB, "hello"); rc != exitOK {
		t.Fatalf("send after matched join exit %d: %s", rc, errOut)
	}

	// mismatch: error exit, no routine line, nothing trusted (send is paused).
	link, code, _, _ = claimFresh(t, url)
	homeC := t.TempDir()
	out, errOut, rc = run(t, "join", "--home", homeC, "--link", link, "--code", code,
		"--handle", "eve-bot", "--admin-fp", "11111 22222 33333 44444 55555 66666")
	if rc == exitOK || !strings.Contains(errOut, "admin_fingerprint_mismatch") {
		t.Fatalf("mismatch join rc=%d stderr=%s", rc, errOut)
	}
	if j := findLine(t, out, "joined"); j["admin_fingerprint_check"] != fpMismatch || j["routine_prompt"] != nil {
		t.Fatalf("mismatch joined = %v", j)
	}
	if strings.Contains(out, "ROUTINE_PROMPT") {
		t.Fatalf("mismatch must not print a routine line:\n%s", out)
	}
	if _, _, rc = run(t, "send", "--home", homeC, "hello"); rc == exitOK {
		t.Fatal("send must be paused after an admin fingerprint mismatch")
	}

	// not_given: still joins, reports the fingerprint for the owner to confirm.
	link, code, out, _ = claimFresh(t, url)
	adminFP = findLine(t, out, "claimed")["fingerprint"].(string)
	t.Setenv("BB_ADMIN_FP", "")
	out, errOut, rc = run(t, "join", "--home", t.TempDir(), "--link", link, "--code", code, "--handle", "bob-bot")
	if rc != exitOK {
		t.Fatalf("join exit %d, stderr=%s", rc, errOut)
	}
	if j := findLine(t, out, "joined"); j["admin_fingerprint_check"] != fpNotGiven {
		t.Fatalf("not_given joined = %v", j)
	}
	if n := rawLine(out, "NOTIFY_OWNER: "); strings.Contains(n, adminFP) || strings.Contains(n, "確認") {
		t.Errorf("owner notice should not ask the owner to compare: %q", n)
	}
}

// The routine line uses this binary's absolute path when bot-bridge-cli on PATH
// is missing or a different file, and the bare name when PATH finds this file.
func TestRoutineCLIPath(t *testing.T) {
	dir := t.TempDir()
	bin := dir + "/bot-bridge-cli"
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	a := &app{exe: func() (string, error) { return bin, nil }}
	t.Setenv("PATH", t.TempDir())
	if got := a.routineCLIPath(); got != bin {
		t.Errorf("not on PATH: got %q, want %q", got, bin)
	}
	t.Setenv("PATH", dir)
	// v0.5.3: the absolute path even when PATH has the same file.
	if got := a.routineCLIPath(); got != bin {
		t.Errorf("on PATH: got %q, want %q", got, bin)
	}
	if got := (&app{}).routineCLIPath(); got != "" {
		t.Errorf("no exe: got %q", got)
	}
}

// An expired or used claim token gets one clear next step, not a retry.
func TestClaimExpiredMessage(t *testing.T) {
	url := startTestServer(t)
	created := postJoin(t, url)
	link, _ := created["invite_link"].(string)
	_, errOut, rc := run(t, "claim", "--home", t.TempDir(), "--link", link, "--token", "bbc_"+strings.Repeat("A", 43), "--handle", "sample-bot", "--owner", "Rick")
	if rc == exitOK || !strings.Contains(errOut, "claim --new") || !strings.Contains(errOut, "30 minutes") {
		t.Fatalf("rc=%d err=%s", rc, errOut)
	}
}
