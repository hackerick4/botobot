package cli

import (
	"strings"
	"testing"
)

func TestSanitizeFilename(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"quote.png", "quote.png"},
		{"a/b/c.txt", "c.txt"},
		{"../../etc/passwd", "passwd"},
		{`..\..\windows\system32\evil.dll`, "evil.dll"},
		{"/absolute/path/file", "file"},
		{"with\x00null.bin", "withnull.bin"},
		{"tab\tand\nnewline.txt", "tabandnewline.txt"},
		{"..", "attachment"},
		{".", "attachment"},
		{"", "attachment"},
		{"/", "attachment"},
		{"only/dir/", "attachment"}, // trailing separator leaves empty basename
		{"   spaced.txt   ", "spaced.txt"},
		{"a\x7fdel.txt", "adel.txt"},
	}
	for _, c := range cases {
		if got := sanitizeFilename(c.in); got != c.want {
			t.Errorf("sanitizeFilename(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestSanitizeFilenameNeverEscapes asserts the result is always a single,
// separator-free component (DEV_SPEC §16 path-traversal defence).
func TestSanitizeFilenameNeverEscapes(t *testing.T) {
	inputs := []string{"../x", "a/../../b", `c:\windows\x`, "foo/bar", "..", "...."}
	for _, in := range inputs {
		got := sanitizeFilename(in)
		if got == "" || got == "." || got == ".." {
			t.Errorf("sanitizeFilename(%q) = %q: must be a real filename", in, got)
		}
		for _, r := range got {
			if r == '/' || r == '\\' {
				t.Errorf("sanitizeFilename(%q) = %q: contains a path separator", in, got)
			}
		}
	}
}

func TestSanitizePeerText(t *testing.T) {
	// Invisible characters are written as \u escapes so the source has no literal
	// bidi/zero-width bytes (Go rejects a literal U+FEFF mid-file, and literal
	// invisibles are easy to mangle on edit).
	cases := []struct {
		name, in, want string
	}{
		{"plain", "hello world", "hello world"},
		{"keep newline+tab", "a\nb\tc", "a\nb\tc"},
		{"strip RLO/PDF override", "a\u202eevil\u202cb", "aevilb"},
		{"strip isolates", "x\u2066y\u2069z", "xyz"},
		{"strip zero-width + BOM", "a\u200bb\u200dc\ufeffd", "abcd"},
		{"strip ALM/LRM", "a\u061c\u200eb", "ab"},
		{"strip ANSI color", "red\x1b[31mtext\x1b[0mend", "redtextend"},
		{"strip ANSI cursor", "a\x1b[2Jb", "ab"},
		{"strip C0 except nl/tab", "a\x01\x07b", "ab"},
		{"strip C1", "a\u0085\u009fb", "ab"},
		{"strip DEL", "a\x7fb", "ab"},
	}
	for _, c := range cases {
		if got := sanitizePeerText(c.in); got != c.want {
			t.Errorf("%s: sanitizePeerText(%q) = %q, want %q", c.name, c.in, got, c.want)
		}
	}
}

func TestSanitizeDisplayName(t *testing.T) {
	if got := sanitizeDisplayName("Rick\nbot"); got != "Rick bot" {
		t.Errorf("newline not flattened: %q", got)
	}
	if got := sanitizeDisplayName("  spaced  "); got != "spaced" {
		t.Errorf("not trimmed: %q", got)
	}
	if got := sanitizeDisplayName("\u202eevil"); got != "evil" {
		t.Errorf("bidi not stripped: %q", got)
	}
	if got := sanitizeDisplayName(""); got != "?" {
		t.Errorf("empty name should fall back: %q", got)
	}
	long := strings.Repeat("x", 200)
	if got := sanitizeDisplayName(long); len([]rune(got)) != maxNameRunes {
		t.Errorf("name not capped: len=%d", len([]rune(got)))
	}
}

func TestTruncateRunes(t *testing.T) {
	if s, trunc := truncateRunes("short"); trunc || s != "short" {
		t.Errorf("short text should not truncate: %q %v", s, trunc)
	}
	long := strings.Repeat("あ", maxTextRunes+50)
	s, trunc := truncateRunes(long)
	if !trunc || len([]rune(s)) != maxTextRunes {
		t.Errorf("long text truncation wrong: len=%d trunc=%v", len([]rune(s)), trunc)
	}
}
