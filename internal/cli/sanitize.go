package cli

import (
	"regexp"
	"strings"
	"time"
	"unicode"

	"github.com/hackerick4/botobot/internal/protocol"
)

// maxTextRunes is the per-message display cap (Batch 1 cheap fixes): long peer
// text is truncated in the rendered output and the full text is written to a
// file the output points at.
const maxTextRunes = 4000

// maxNameRunes caps a sanitized display name (Batch 1 cheap fixes).
const maxNameRunes = 64

// ansiRe matches ANSI escape sequences (CSI "ESC [ … final", OSC "ESC ] … BEL/ST",
// and lone two-byte "ESC X" forms) so terminal control sequences in untrusted
// peer text cannot move the cursor, recolour, or inject into a terminal that
// displays the routine output (Batch 1 cheap fixes).
var ansiRe = regexp.MustCompile("\x1b\\[[0-9;?]*[ -/]*[@-~]|\x1b\\][^\x07\x1b]*(?:\x07|\x1b\\\\)|\x1b[@-Z\\\\-_]")

// isStrippable reports whether r is a dangerous invisible/control rune that must
// be removed from untrusted peer text (Batch 1 cheap fixes): bidirectional
// overrides/isolates, zero-width characters, the Arabic letter mark, C0 controls
// (except TAB/newline, handled by the caller), DEL, and C1 controls.
func isStrippable(r rune) bool {
	switch r {
	// Bidi controls: LRE RLE PDF LRO RLO, LRI RLI FSI PDI, LRM RLM, ALM.
	case 0x202A, 0x202B, 0x202C, 0x202D, 0x202E,
		0x2066, 0x2067, 0x2068, 0x2069,
		0x200E, 0x200F, 0x061C:
		return true
	// Zero-width: ZWSP ZWNJ ZWJ, word joiner, BOM/ZWNBSP.
	case 0x200B, 0x200C, 0x200D, 0x2060, 0xFEFF:
		return true
	}
	switch {
	case r == 0x7f: // DEL
		return true
	case r < 0x20: // C0 controls (caller decides TAB/newline)
		return true
	case r >= 0x80 && r <= 0x9f: // C1 controls
		return true
	case r >= 0xE0000 && r <= 0xE007F: // Unicode tag characters (invisible ASCII smuggling)
		return true
	}
	return false
}

// sanitizePeerText cleans untrusted peer message text for display (Batch 1 cheap
// fixes): ANSI escapes are removed, and bidi/zero-width/C0/C1 controls are
// stripped, keeping only TAB and newline among the controls. Content is
// otherwise preserved (the bytes are shown verbatim, never executed).
func sanitizePeerText(s string) string {
	s = ansiRe.ReplaceAllString(s, "")
	return strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' {
			return r
		}
		// U+2028/U+2029 render as line breaks: turn them into real newlines so
		// the per-line gutter applies (P0 A0 hardening).
		if r == 0x2028 || r == 0x2029 {
			return '\n'
		}
		if isStrippable(r) {
			return -1
		}
		return r
	}, s)
}

// sanitizeDisplayName cleans an untrusted display name: like sanitizePeerText but
// also removing newlines and TAB (a name is a single line), collapsing to a
// single trimmed line, and capping the length (Batch 1 cheap fixes).
func sanitizeDisplayName(s string) string {
	s = ansiRe.ReplaceAllString(s, "")
	s = strings.Map(func(r rune) rune {
		if r == '\n' || r == '\r' || r == '\t' || r == 0x2028 || r == 0x2029 {
			return ' '
		}
		if isStrippable(r) {
			return -1
		}
		return r
	}, s)
	s = strings.TrimSpace(s)
	if n := []rune(s); len(n) > maxNameRunes {
		s = string(n[:maxNameRunes])
	}
	if s == "" {
		return "?"
	}
	return s
}

// truncateRunes returns s capped at maxTextRunes runes and whether it was cut
// (Batch 1 cheap fixes).
func truncateRunes(s string) (string, bool) {
	r := []rune(s)
	if len(r) <= maxTextRunes {
		return s, false
	}
	return string(r[:maxTextRunes]), true
}

// sanitizeFilename reduces an attacker-controlled attachment name to a safe
// basename for writing under files/<msg_id>/ (DEV_SPEC §16: "only the basename,
// strip control characters, prevent path traversal"). The sender's name is
// untrusted remote data, so this must never allow escaping the message's files
// directory.
//
// Rules:
//   - split on both '/' and '\' and keep only the final component (defeats
//     "a/b", "../../etc/passwd", and Windows-style "..\\x");
//   - drop ASCII control characters (< 0x20 and DEL) and the path separators
//     themselves;
//   - reject "", ".", and ".." (which are not real filenames) with a fallback.
func sanitizeFilename(name string) string {
	// Keep only the final path component, treating both separators as such.
	if i := strings.LastIndexAny(name, `/\`); i >= 0 {
		name = name[i+1:]
	}
	// Remove control characters and any residual separators.
	cleaned := strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f || r == '/' || r == '\\' {
			return -1
		}
		return r
	}, name)
	cleaned = strings.TrimSpace(cleaned)
	switch cleaned {
	case "", ".", "..":
		return "attachment"
	}
	return cleaned
}

// oneLine reduces any untrusted value to a single, control-free line for use in
// a tick/poll control line (meta, attachment, event, NOTIFY): ANSI escapes are
// removed, every control/format/line-separator rune becomes nothing or a space,
// and the result is length-capped. It is the A0 fix (EXFIL_DEFENSE.md §2.1): no
// peer- or server-supplied value may introduce a line break into the output.
func oneLine(s string, max int) string {
	s = ansiRe.ReplaceAllString(s, "")
	s = strings.Map(func(r rune) rune {
		switch {
		case r == '\n' || r == '\r' || r == '\t' || r == 0x2028 || r == 0x2029 || r == 0x85:
			return ' '
		case isStrippable(r) || unicode.Is(unicode.Cf, r):
			return -1
		}
		return r
	}, s)
	s = strings.TrimSpace(s)
	if r := []rune(s); max > 0 && len(r) > max {
		s = string(r[:max])
	}
	return s
}

// mimeRe is the strict shape accepted for an attachment MIME type shown in tick
// output (RFC 6838 restricted-name chars, no parameters).
var mimeRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9!#$&^_.+-]{0,63}/[A-Za-z0-9][A-Za-z0-9!#$&^_.+-]{0,63}$`)

// safeMime returns m if it is a well-formed type/subtype, else
// "application/octet-stream".
func safeMime(m string) string {
	if mimeRe.MatchString(m) {
		return m
	}
	return "application/octet-stream"
}

// safeSentAt returns the peer's self-reported sent_at only if it parses as an
// RFC 3339 timestamp; anything else is shown as "invalid".
func safeSentAt(s string) string {
	if s == "" {
		return "unknown"
	}
	if _, err := time.Parse(time.RFC3339Nano, s); err != nil {
		return "invalid"
	}
	return s
}

// safeMsgRef returns a msg_id-shaped reference (reply_to) or "invalid".
func safeMsgRef(s string) string {
	if protocol.ValidMsgID(s) {
		return s
	}
	return "invalid"
}

// handleShapeRe mirrors the server's handle rule; a handle from a (possibly
// malicious) server that does not match is shown as "?".
var handleShapeRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{1,31}$`)

func safeHandle(h string) string {
	if handleShapeRe.MatchString(h) {
		return h
	}
	return "?"
}

// checkProfileNames applies the server's display/owner name rules locally so a
// bad name fails fast with a usage error (EXFIL_DEFENSE.md P0).
func checkProfileNames(displayName, owner string) *cliError {
	if d := strings.ToLower(strings.TrimSpace(displayName)); placeholderBotNames[d] || strings.ContainsAny(displayName, "<>") {
		return &cliError{Code: "placeholder_name", Exit: exitUsage, Message: "--display-name " + displayName +
			" is an example, not a name. Nothing was sent. Use this bot's own name (the name its owner calls it), or leave --display-name out"}
	}
	if _, err := protocol.NormalizeProfileName("--display-name", displayName); err != nil {
		return usageErrorf("%v", err)
	}
	if _, err := protocol.NormalizeProfileName("--owner", owner); err != nil {
		return usageErrorf("%v", err)
	}
	return nil
}

// maxPeerNameRunes caps a peer's display name in owner-facing lines (v0.5.4).
const maxPeerNameRunes = 24

// peerName is a peer bot's self-declared display name made safe to show the
// owner: it must pass the server's profile-name rules again (letters, digits,
// spaces and -_.'’·&() only, so no newlines, brackets, quotes or control-line
// look-alikes), and is cut to maxPeerNameRunes. It returns "" when the name is
// missing or unusable, or is just the handle, so callers fall back to the
// handle (v0.5.4 item 1).
func peerName(displayName, handle string) string {
	n, err := protocol.NormalizeProfileName("display_name", displayName)
	if err != nil || n == "" || strings.EqualFold(n, handle) || reservedPeerNames[strings.ToLower(strings.ReplaceAll(n, " ", ""))] {
		return ""
	}
	if r := []rune(n); len(r) > maxPeerNameRunes {
		n = strings.TrimSpace(string(r[:maxPeerNameRunes])) + "…"
	}
	return n
}

// reservedPeerNames are words the CLI's own lines use for this bot and its
// owner; a peer calling itself one of them is shown by its handle (RT-86).
// Keys are lowercase with spaces removed.
var reservedPeerNames = map[string]bool{
	"此bot": true, "thisbot": true, "本bot": true, "主人": true, "owner": true, "theowner": true,
	"管理者": true, "admin": true, "bot-bridge": true, "botbridge": true, "system": true, "系統": true,
}

// peerLabelFor is peerLabel, except that a peer using this bot's own display
// name is shown by its handle, so it cannot pass itself off as this bot.
func peerLabelFor(displayName, handle, owner, selfName string) string {
	if selfName != "" && strings.EqualFold(strings.TrimSpace(displayName), strings.TrimSpace(selfName)) {
		displayName = ""
	}
	return peerLabel(displayName, handle, owner)
}

// peerLabel is how a peer bot is named to the owner: its display name (or, if
// it has none, its handle), then the owner name in full-width brackets, e.g.
// 「BB 測試 C（Rick Chiu）」. The handle stays in the JSON for bots.
func peerLabel(displayName, handle, owner string) string {
	who := peerName(displayName, handle)
	if who == "" {
		who = safeHandle(handle)
	}
	if po, err := protocol.NormalizeProfileName("owner_name", owner); err == nil && po != "" {
		who += "（" + po + "）"
	}
	return who
}
