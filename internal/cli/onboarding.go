package cli

// onboarding.go: what `join` and `claim` print after success so the published
// manual (https://botobot.app/join.md) can stay generic. The bot copies the
// routine line from here (bridge id and any non-default BB_HOME filled in), and
// the admin fingerprint from the invite is compared against the key the CLI
// actually received, computed locally (not a server-asserted value).

import (
	"crypto/ed25519"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/hackerick4/botobot/internal/prompts"
	"github.com/hackerick4/botobot/internal/protocol"
)

// Admin fingerprint check results.
const (
	fpMatch       = "match"
	fpMismatch    = "mismatch"
	fpNotGiven    = "not_given"
	fpUnavailable = "unavailable"
)

var shellSafe = regexp.MustCompile(`^[A-Za-z0-9_./~+-]+$`)

// routinePrompt returns the exact one-line routine prompt for this bot: one
// routine ticks every bridge in its folder (v0.5.1). BB_HOME is included only
// when this session uses a non-default state directory.
func (s *session) routinePrompt() string {
	info := prompts.Info{CLIPath: s.app.routineCLIPath(), BBHome: s.homeValue()}
	return prompts.RoutineLine(info)
}

// homeValue is this session's folder, shell-quoted, when it is not the default
// one (so commands for this bot need BB_HOME=… in front); otherwise "".
func (s *session) homeValue() string {
	home := s.ks.home
	if abs, err := filepath.Abs(home); err == nil {
		home = abs
	}
	if def, err := resolveHome("", ""); err == nil && filepath.Clean(def) == filepath.Clean(home) {
		return ""
	}
	if shellSafe.MatchString(home) {
		return home
	}
	return "'" + strings.ReplaceAll(home, "'", `'\''`) + "'"
}

// homePrefix is "BB_HOME=… " for a non-default folder, else "".
func (s *session) homePrefix() string {
	if h := s.homeValue(); h != "" {
		return "BB_HOME=" + h + " "
	}
	return ""
}

// routineCLIPath returns this binary's absolute path (shell-quoted when
// needed). Since v0.5.3 it is always used in the routine line, even when
// `bot-bridge-cli` on PATH is the same file: a routine may run with a different
// PATH, and must never run a different, older copy. "" only when unknown.
func (a *app) routineCLIPath() string {
	if a.exe == nil {
		return ""
	}
	exe, err := a.exe()
	if err != nil || exe == "" {
		return ""
	}
	if r, err := filepath.EvalSymlinks(exe); err == nil {
		exe = r
	}
	if abs, err := filepath.Abs(exe); err == nil {
		exe = abs
	}
	if shellSafe.MatchString(exe) {
		return exe
	}
	return "'" + strings.ReplaceAll(exe, "'", `'\''`) + "'"
}

// cliCmd is how a printed NEXT line starts: BB_HOME for a non-default folder,
// then this binary's absolute path, like ROUTINE_PROMPT (v0.5.4 item 3), so the
// bot runs the same copy in the same folder. Falls back to "bot-bridge-cli"
// when the path is unknown.
func (s *session) cliCmd() string {
	exe := s.app.routineCLIPath()
	if exe == "" {
		exe = "bot-bridge-cli"
	}
	return s.homePrefix() + exe
}

// localFingerprint computes this land's fingerprint from its own private keys,
// so the admin line in an invite never depends on a server-supplied value.
func localFingerprint(km *keyMaterial) (string, error) {
	encPub, err := protocol.PublicEncKey(km.EncPriv)
	if err != nil {
		return "", err
	}
	if len(km.SignPriv) != ed25519.PrivateKeySize {
		return "", errBadSignKey
	}
	signPub := ed25519.PrivateKey(km.SignPriv).Public().(ed25519.PublicKey)
	return protocol.Fingerprint(signPub, encPub), nil
}

type fpError string

func (e fpError) Error() string { return string(e) }

const errBadSignKey = fpError("invalid signing key")

// inviteMessage renders the complete invite (link, code, manual, safety line,
// admin fingerprint) that claim and invite-code give the owner to forward.
// server is the bridge server origin; the manual is same-origin. The raw output
// wraps it in the INVITE_MESSAGE markers (inviteBlockLines).
func inviteMessage(server, link, code, adminFP, lang string) string {
	return prompts.Invite(prompts.Info{
		Lang:       lang,
		InviteLink: link,
		InviteCode: code,
		ManualURL:  prompts.ManualFor(server, lang),
		AdminFP:    adminFP,
	})
}

// normFP keeps only the digits of a fingerprint so spacing and line breaks in
// a forwarded invite do not matter.
func normFP(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// adminOf returns the admin land other than self, or nil.
func adminOf(lc *landsCache, selfID string) *cachedLand {
	if lc == nil {
		return nil
	}
	for i := range lc.Lands {
		l := &lc.Lands[i]
		if l.Role == "admin" && l.LandID != selfID {
			return l
		}
	}
	return nil
}

// checkAdminFP compares the expected fingerprint from the invite with the
// admin key this CLI received. It returns the locally computed fingerprint and
// one of the fp* results.
func checkAdminFP(lc *landsCache, selfID, expect string) (string, string) {
	adm := adminOf(lc, selfID)
	if adm == nil || adm.KeyID == "" || !bindingOK(adm) {
		return "", fpUnavailable
	}
	got := computeFingerprint(adm)
	switch {
	case strings.TrimSpace(expect) == "":
		return got, fpNotGiven
	case len(normFP(expect)) > 0 && normFP(expect) == normFP(got):
		return got, fpMatch
	default:
		return got, fpMismatch
	}
}

// Owner-facing notices (v0.5.2): only what to do and what to watch out for.
// Security details (admin, fingerprint, polling) stay in bot-facing output.
const (
	claimNoticeZH  = "bridge 已建立。下一則是邀請，請整段私下傳給對方，請對方直接貼給自己的 Grok Bot。"
	claimNoticeEN  = "Bridge created. The next message is the invite: send all of it privately to the other person, to paste as is into their own Grok Bot."
	ownerCautionZH = "注意：寄信、付款或涉及個資的動作，送出前一律先確認。"
	ownerCautionEN = "Note: emails, payments and anything with personal data are always confirmed before sending."
)

// maxClaimNameRunes caps the bridge name quoted in the claim notice.
const maxClaimNameRunes = 40

// claimNoticeName is the bridge name as quoted in the owner's claim notice
// (v0.5.5): sanitised, quote marks removed so it cannot close the quote and
// add its own sentence, capped. "" when there is no usable name.
func claimNoticeName(name string) string {
	name = sanitizeDisplayName(name)
	if name == "?" {
		return ""
	}
	name = strings.NewReplacer("「", "", "」", "", "『", "", "』", "", "\"", "", "“", "", "”", "").Replace(name)
	name = strings.Join(strings.Fields(name), " ")
	if r := []rune(name); len(r) > maxClaimNameRunes {
		name = string(r[:maxClaimNameRunes]) + "…"
	}
	return name
}

// claimNotice is the first owner notice after a claim. With two bridges the
// owner can tell them apart by name (v0.5.5).
func claimNotice(name, lang string) string {
	n := claimNoticeName(name)
	switch {
	case lang == "en" && n != "":
		return "\"" + n + "\" created. The next message is the invite: send all of it privately to the other person, to paste as is into their own Grok Bot."
	case lang == "en":
		return claimNoticeEN
	case n != "":
		return "「" + n + "」已建立。下一則是邀請，請整段私下傳給對方，請對方直接貼給自己的 Grok Bot。"
	}
	return claimNoticeZH
}

// claimPendingNotice is the rare notice when the invite code could not be
// fetched yet.
func claimPendingNotice(name, lang string) string {
	n := claimNoticeName(name)
	switch {
	case lang == "en" && n != "":
		return "\"" + n + "\" created; the invite follows."
	case lang == "en":
		return "Bridge created; the invite follows."
	case n != "":
		return "「" + n + "」已建立，邀請稍後補上。"
	}
	return "bridge 已建立，邀請稍後補上。"
}

// joinOwnerNotice is the plain-language notice for the owner after `join`
// (one or two NOTIFY_OWNER lines). The fingerprint result itself is in the
// JSON line (admin_fingerprint_check) for the bot.
func joinOwnerNotice(bridgeID, name, adminFP, check string, lang ...string) string {
	n := sanitizeDisplayName(name)
	en := len(lang) > 0 && prompts.IsEnglish(lang[0])
	if check == fpMismatch {
		if en {
			return "NOTIFY_OWNER: Joining failed: the invite may have been tampered with, so the bridge is locked. Ask the other person for a new invite."
		}
		return "NOTIFY_OWNER: 加入失敗：邀請可能遭竄改，bridge 已鎖定。請向對方索取新邀請。"
	}
	if en {
		where := "the bridge"
		if n != "" && n != "?" {
			where = "\"" + n + "\""
		}
		return "NOTIFY_OWNER: Joined " + where + ".\nNOTIFY_OWNER: " + ownerCautionEN
	}
	where := " bridge"
	if n != "" && n != "?" {
		where = "「" + n + "」"
	}
	return "NOTIFY_OWNER: 已加入" + where + "。\nNOTIFY_OWNER: " + ownerCautionZH
}

// maxInviteBytes bounds what join --invite-stdin reads.
const maxInviteBytes = 16 << 10

var (
	inviteLinkRe = regexp.MustCompile(`https?://[A-Za-z0-9.:\-\[\]]+(/[A-Za-z0-9._~\-/]*)?/b/br_[A-Za-z0-9_\-]+`)
	inviteCodeRe = regexp.MustCompile(`(^|[^0-9])([0-9]{6})([^0-9]|$)`)
)

// parseInvite extracts the bridge link, the 6-digit invite code, and the admin
// fingerprint from a forwarded invite message (join --invite-stdin). It is
// strict: exactly one distinct bridge link and one distinct code on a labeled
// line, so an ambiguous paste stops instead of guessing. The fingerprint is
// optional (empty when the invite has no 管理者指紋 line).
// inviteTerminatorRe matches a line that could end a shell heredoc carrying the
// invite: the old fixed marker, any BB_END_… nonce marker, EOF, or any other
// bare *END* word. A real invite never has such a line.
var inviteTerminatorRe = regexp.MustCompile(`^(EOF|[A-Za-z0-9_]*END[A-Za-z0-9_]*)$`)

// inviteShellChars are characters a forwarded invite never contains but shell
// commands typically do. The invite is refused if any line has one.
const inviteShellChars = "`$;|&<>\\"

// checkInviteSafe refuses invite text that carries a heredoc terminator line or
// shell syntax. Such text was altered on the way (or is an attack that tried to
// break out of the heredoc); nothing in it may be run or used.
//
// v0.5.6: the invite may come with or without its INVITE_MESSAGE_BEGIN/END
// lines and with chat text around it. The invite itself (between the markers,
// or from its first line to its fingerprint line) stays strict: no shell
// characters at all. Chat text outside it may use ordinary punctuation such as
// ";" and "&", but a line that looks like shell (a terminator, #!, ` $ | \ < >,
// &&, or a line that starts with a command) is still refused.
// hasHiddenControl reports control characters (NUL, ESC, a carriage return
// inside the line, DEL) or bidi overrides that can hide text from the
// owner. Tabs and emoji joiners are fine.
func hasHiddenControl(line string) bool {
	for _, r := range line {
		switch {
		case r == '\t':
		case r < 0x20 || r == 0x7f:
			return true
		case r >= 0x202a && r <= 0x202e, r >= 0x2066 && r <= 0x2069:
			return true
		}
	}
	return false
}

func checkInviteSafe(text string) error {
	lines := strings.Split(text, "\n")
	for i := range lines {
		lines[i] = strings.TrimSpace(strings.TrimSuffix(lines[i], "\r"))
	}
	from, to, err := inviteSpan(lines)
	if err != nil {
		return err
	}
	for i, line := range lines {
		if hasHiddenControl(line) {
			return fpError(fmt.Sprintf("line %d of the invite has control characters", i+1))
		}
		if line == "" || line == prompts.InviteBegin || line == prompts.InviteEnd {
			continue // the markers claim prints around the invite, if forwarded along
		}
		if inviteTerminatorRe.MatchString(line) {
			return fpError(fmt.Sprintf("line %d of the invite looks like a heredoc end marker", i+1))
		}
		if strings.HasPrefix(line, "#!") {
			return fpError(fmt.Sprintf("line %d of the invite contains shell characters", i+1))
		}
		// Lines that carry invite fields are strict wherever they are.
		inside := (i >= from && i <= to) || looksLikeInviteField(line)
		if inside && strings.ContainsAny(line, inviteShellChars) {
			return fpError(fmt.Sprintf("line %d of the invite contains shell characters", i+1))
		}
		if !inside && chatLineLooksLikeShell(line) {
			return fpError(fmt.Sprintf("line %d next to the invite looks like a shell command", i+1))
		}
	}
	return nil
}

// looksLikeInviteField reports whether a line carries a bridge link, an invite
// code or a fingerprint label, or is an invite's first line.
func looksLikeInviteField(line string) bool {
	low := strings.ToLower(line)
	return strings.Contains(line, "/b/br_") || strings.Contains(line, "邀請碼") || strings.Contains(low, "invite code") ||
		strings.Contains(line, prompts.AdminFPLabel) || strings.Contains(low, "admin fingerprint") || inviteHeaderRe.MatchString(line)
}

// inviteHeaderRe matches the first line of an invite (zh or en template).
var inviteHeaderRe = regexp.MustCompile(`(?i)bot-bridge (邀請|invite)`)

// inviteSpan returns the line range [from, to] that holds the invite itself:
// between the INVITE_MESSAGE markers when present, else from the invite's
// first line to its admin fingerprint line (or its last template line), else
// the whole text. Repeated or out-of-order markers are refused.
func inviteSpan(lines []string) (from, to int, err error) {
	begin, end := -1, -1
	for i, l := range lines {
		switch l {
		case prompts.InviteBegin:
			if begin >= 0 {
				return 0, 0, fpError("more than one INVITE_MESSAGE_BEGIN line; paste exactly one invite")
			}
			begin = i
		case prompts.InviteEnd:
			if end >= 0 {
				return 0, 0, fpError("more than one INVITE_MESSAGE_END line; paste exactly one invite")
			}
			end = i
		}
	}
	switch {
	case begin >= 0 && end >= 0 && end < begin:
		return 0, 0, fpError("INVITE_MESSAGE_END comes before INVITE_MESSAGE_BEGIN")
	case begin >= 0 && end >= 0:
		return begin + 1, end - 1, nil
	case begin >= 0:
		return begin + 1, len(lines) - 1, nil
	case end >= 0:
		return 0, end - 1, nil
	}
	head := -1
	for i, l := range lines {
		if inviteHeaderRe.MatchString(l) {
			head = i
			break
		}
	}
	if head < 0 {
		return 0, len(lines) - 1, nil
	}
	last := min(head+4, len(lines)-1) // header, link, code, manual, safety
	for i := head + 1; i < len(lines) && i <= head+8; i++ {
		low := strings.ToLower(lines[i])
		if strings.Contains(lines[i], prompts.AdminFPLabel) || strings.Contains(low, "admin fingerprint") {
			last = i
			break
		}
	}
	return head, last, nil
}

// chatShellCmds are command words that, at the start of a chat line next to
// the invite, mark it as shell rather than chat.
var chatShellCmds = map[string]bool{
	"sh": true, "bash": true, "zsh": true, "rm": true, "curl": true, "wget": true, "cat": true, "echo": true,
	"cd": true, "chmod": true, "chown": true, "sudo": true, "eval": true, "exec": true, "export": true,
	"python": true, "python3": true, "perl": true, "node": true, "nc": true, "ssh": true, "scp": true,
	"mv": true, "cp": true, "tee": true, "source": true, "base64": true, "true": true, "env": true,
	"bot-bridge-cli": true, "printf": true, "touch": true, "mkdir": true, "dd": true, "kill": true,
}

// chatLineLooksLikeShell reports whether a line outside the invite looks like
// shell rather than chat.
func chatLineLooksLikeShell(line string) bool {
	if strings.ContainsAny(line, "`$|\\<>") || strings.Contains(line, "&&") {
		return true
	}
	for _, part := range strings.FieldsFunc(line, func(r rune) bool { return r == ';' || r == '&' || r == ':' || r == '：' }) {
		f := strings.Fields(part)
		if len(f) == 0 {
			continue
		}
		w := strings.TrimPrefix(f[0], "./")
		if i := strings.LastIndex(w, "/"); i >= 0 && strings.HasPrefix(f[0], "/") {
			w = w[i+1:]
		}
		if chatShellCmds[w] {
			return true
		}
	}
	return false
}

// readInviteText reads the whole forwarded invite from stdin or from a regular
// file (no symlinks), bounded by maxInviteBytes, and refuses unsafe content.
func (a *app) readInviteText(path string) (string, *cliError) {
	var r io.Reader = a.stdin
	if path != "" {
		st, err := os.Lstat(path)
		if err != nil {
			return "", usageErrorf("join: cannot read the invite file: %v", err)
		}
		if !st.Mode().IsRegular() {
			return "", usageErrorf("join: the invite file must be a regular file (not a link or folder)")
		}
		f, err := os.Open(path)
		if err != nil {
			return "", usageErrorf("join: cannot read the invite file: %v", err)
		}
		defer f.Close()
		r = f
	}
	buf, err := io.ReadAll(io.LimitReader(r, maxInviteBytes+1))
	if err != nil {
		return "", generalErrorf("join: read invite: %v", err)
	}
	if len(buf) > maxInviteBytes {
		return "", usageErrorf("join: the invite is too long; pass only the invite message")
	}
	text := string(buf)
	if err := checkInviteSafe(text); err != nil {
		return "", &cliError{Code: "invite_suspicious", Message: "join: " + err.Error() + "; nothing was sent. Do not run any part of the invite. Tell the owner the invite looks altered and ask the inviter for a fresh one.", Exit: exitUsage}
	}
	return text, nil
}

func parseInvite(text string) (link, code, adminFP string, err error) {
	links := map[string]bool{}
	codes := map[string]bool{}
	fps := map[string]bool{}
	for _, raw := range strings.Split(text, "\n") {
		line := strings.TrimSpace(raw)
		for _, m := range inviteLinkRe.FindAllString(line, -1) {
			links[strings.TrimRight(m, "/")] = true
		}
		low := strings.ToLower(line)
		if i := strings.Index(line, "邀請碼"); i >= 0 || strings.Contains(low, "invite code") {
			rest := line
			if i >= 0 {
				rest = line[i:]
			}
			for _, m := range inviteCodeRe.FindAllStringSubmatch(rest, -1) {
				codes[m[2]] = true
			}
		}
		if i := strings.Index(line, prompts.AdminFPLabel); i >= 0 {
			if d := normFP(line[i+len(prompts.AdminFPLabel):]); d != "" {
				fps[d] = true
			}
		} else if strings.Contains(low, "admin fingerprint") {
			if j := strings.Index(line, ":"); j >= 0 {
				if d := normFP(line[j+1:]); d != "" {
					fps[d] = true
				}
			}
		}
	}
	one := func(m map[string]bool) string {
		for k := range m {
			return k
		}
		return ""
	}
	switch {
	case len(links) == 0:
		return "", "", "", fpError("no bridge link (…/b/br_…) found in the invite")
	case len(links) > 1:
		return "", "", "", fpError("more than one bridge link in the invite; paste exactly one invite")
	case len(codes) == 0:
		return "", "", "", fpError("no 6-digit invite code (邀請碼) found in the invite")
	case len(codes) > 1:
		return "", "", "", fpError("more than one invite code in the invite; paste exactly one invite")
	case len(fps) > 1:
		return "", "", "", fpError("more than one admin fingerprint in the invite; paste exactly one invite")
	}
	return one(links), one(codes), one(fps), nil
}

// inviteBlock wraps an invite in the INVITE_MESSAGE markers for raw output.
func inviteBlock(msg string) string {
	return prompts.InviteBegin + "\n" + msg + "\n" + prompts.InviteEnd
}

// printRoutine prints the ROUTINE_PROMPT line and what to do with it. prior is
// how many bridges this bot's folder held before this one: with none, the bot
// creates its one routine; otherwise the routine it already has covers the new
// bridge too (v0.5.1), and an older per-bridge routine is merged into it.
func (s *session) printRoutine(routine string, prior int, lang string, hadEntry bool) {
	s.pr.raw("ROUTINE_PROMPT: " + routine)
	home := ""
	if i := strings.Index(routine, "BB_HOME="); i >= 0 {
		if f := strings.Fields(routine[i:]); len(f) > 0 {
			home = f[0]
		}
	}
	en := prompts.IsEnglish(lang)
	switch {
	// v0.6.1: a bot with a public entry already runs the routine, even when
	// no visitor line is open yet.
	case hadEntry && en:
		s.pr.raw("NEXT: this bot already has a public entry, so it already has its \"bot-bridge inbox\" routine: keep using it (the same routine also checks this one); do not create another. Only if that routine is missing, create it every 5 minutes with only the text after ROUTINE_PROMPT: above.")
	case hadEntry:
		s.pr.raw("NEXT: 此 Bot 已有公開入口，所以已有「bot-bridge inbox」routine：沿用它（同一個 routine 會一併檢查這裡），不要另建。只有找不到這個 routine 時，才建立每 5 分鐘一次的 routine，提示詞只放上面 ROUTINE_PROMPT: 後的那一行。")
	// v0.6.2: earlier bridges that are all closed (not yet forgotten) are
	// not "other bridges": the bot may have deleted its routine already.
	case prior > 0 && s.ks.liveCount() <= 1 && en:
		s.pr.raw("NEXT: check this bot's routines: if it has a \"bot-bridge inbox\" routine, keep using it (it also checks this bridge); do not create another. If it has none, create it every 5 minutes with only the text after ROUTINE_PROMPT: above.")
	case prior > 0 && s.ks.liveCount() <= 1:
		s.pr.raw("NEXT: 查看此 Bot 的 routine：已有「bot-bridge inbox」routine 時沿用（同一個 routine 會一併檢查這個 bridge），不要另建；沒有時，建立每 5 分鐘一次的這個 routine，提示詞只放上面 ROUTINE_PROMPT: 後的那一行。")
	// prior > 0 means this folder already had a bridge; whether a routine
	// exists is the bot's to check (the CLI cannot see routines, v0.5.3).
	case prior > 0 && en:
		s.pr.raw("NEXT: this bot has other bridges. Check its routines: if it has a \"bot-bridge inbox\" routine, that one routine also checks this bridge; do not create another. If it has none, create it every 5 minutes with only the text after ROUTINE_PROMPT: above. If old routines have --bridge in the prompt, keep one, change it to the line above, and delete the rest.")
	case prior > 0:
		s.pr.raw("NEXT: 此 Bot 還有別的 bridge。查看它的 routine：已有「bot-bridge inbox」routine 時，同一個 routine 會一併檢查這個 bridge，不要另建；沒有時，建立每 5 分鐘一次的這個 routine，提示詞只放上面 ROUTINE_PROMPT: 後的那一行。舊 routine 的提示詞含 --bridge 時，留一個並改成上面這一行，其餘刪除。")
	case en && home != "":
		s.pr.raw("NEXT: create one routine named \"bot-bridge inbox\" every 5 minutes whose prompt is only the text after ROUTINE_PROMPT: above. It covers every bridge of this bot. To create or join another bridge later, put the same " + home + " in front of the command.")
	case en:
		s.pr.raw("NEXT: create one routine named \"bot-bridge inbox\" every 5 minutes whose prompt is only the text after ROUTINE_PROMPT: above. It covers every bridge of this bot.")
	case home != "":
		s.pr.raw("NEXT: 建立每 5 分鐘一次、名為「bot-bridge inbox」的 routine，提示詞只放上面 ROUTINE_PROMPT: 後的那一行。一個 routine 會檢查此 Bot 的所有 bridge。之後再建立或加入 bridge 時，指令前加上同一個 " + home + "。")
	default:
		s.pr.raw("NEXT: 建立每 5 分鐘一次、名為「bot-bridge inbox」的 routine，提示詞只放上面 ROUTINE_PROMPT: 後的那一行。一個 routine 會檢查此 Bot 的所有 bridge。")
	}
}
