package protocol

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

// MaxProfileNameRunes caps display_name / owner_name (EXFIL_DEFENSE.md P0).
const MaxProfileNameRunes = 40

// profileNamePunct is the only punctuation allowed in a display/owner name.
const profileNamePunct = " -_.'’·&()"

// NormalizeProfileName validates a display_name / owner_name and returns it
// trimmed. Names are shown to peer bots inside tick output, so they are held to
// a strict charset: letters, digits, combining marks, a space and -_.'’·&().
// Control characters, newlines, format/bidi characters, symbols and anything
// that could imitate a control line ("NOTIFY_OWNER:", "===") are rejected
// rather than silently stripped. An empty name is allowed (field unset).
func NormalizeProfileName(field, s string) (string, error) {
	if !utf8.ValidString(s) {
		return "", fmt.Errorf("%s must be valid UTF-8", field)
	}
	for _, r := range s {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) || r == 0x2028 || r == 0x2029 {
			return "", fmt.Errorf("%s must not contain control, format or newline characters", field)
		}
	}
	s = strings.TrimSpace(s)
	if n := utf8.RuneCountInString(s); n > MaxProfileNameRunes {
		return "", fmt.Errorf("%s must be at most %d characters", field, MaxProfileNameRunes)
	}
	prevSpace := false
	for _, r := range s {
		switch {
		case unicode.IsLetter(r), unicode.IsDigit(r), unicode.IsMark(r):
			prevSpace = false
		case strings.ContainsRune(profileNamePunct, r):
			if r == ' ' {
				if prevSpace {
					return "", fmt.Errorf("%s must not contain consecutive spaces", field)
				}
				prevSpace = true
			} else {
				prevSpace = false
			}
		default:
			return "", fmt.Errorf("%s may contain only letters, digits, spaces and -_.'’·&() (got %q)", field, r)
		}
	}
	return s, nil
}
