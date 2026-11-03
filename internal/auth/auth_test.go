package auth

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"strings"
	"testing"
)

func TestGenerateFormat(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 100; i++ {
		k, err := Generate()
		if err != nil {
			t.Fatalf("Generate: %v", err)
		}
		if !strings.HasPrefix(k.Full, Prefix) {
			t.Fatalf("key %q missing %q prefix", k.Full, Prefix)
		}
		body := strings.TrimPrefix(k.Full, Prefix)
		if len(body) != 43 { // 32 bytes -> 43 unpadded base64url chars
			t.Fatalf("key body len = %d, want 43 (%q)", len(body), k.Full)
		}
		if _, err := b64.DecodeString(body); err != nil {
			t.Fatalf("key body not base64url: %v", err)
		}
		if k.Prefix != body[:displayPrefixLen] {
			t.Fatalf("prefix = %q, want %q", k.Prefix, body[:displayPrefixLen])
		}
		if seen[k.Full] {
			t.Fatalf("duplicate key generated: %q", k.Full)
		}
		seen[k.Full] = true
	}
}

func TestHashMatchesHMAC(t *testing.T) {
	pepper := bytes.Repeat([]byte{0x7f}, 32)
	const full = "bbk_abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNO"
	got := Hash(pepper, full)

	m := hmac.New(sha256.New, pepper)
	m.Write([]byte(full))
	want := m.Sum(nil)
	if !hmac.Equal(got, want) {
		t.Fatalf("Hash mismatch")
	}
	// Different pepper -> different hash.
	other := Hash(bytes.Repeat([]byte{0x01}, 32), full)
	if hmac.Equal(got, other) {
		t.Fatalf("hash should depend on pepper")
	}
}

func TestParseBearer(t *testing.T) {
	cases := []struct {
		in      string
		wantTok string
		wantOK  bool
	}{
		{"Bearer bbk_xyz", "bbk_xyz", true},
		{"bearer bbk_xyz", "bbk_xyz", true}, // scheme case-insensitive
		{"BEARER bbk_xyz", "bbk_xyz", true},
		{"Bearer   bbk_xyz  ", "bbk_xyz", true},
		{"Bearer notakey", "", false},
		{"Basic bbk_xyz", "", false},
		{"bbk_xyz", "", false},
		{"", "", false},
		{"Bearer bbk_", "", false}, // empty token body
	}
	for _, c := range cases {
		tok, ok := ParseBearer(c.in)
		if ok != c.wantOK || tok != c.wantTok {
			t.Errorf("ParseBearer(%q) = (%q,%v), want (%q,%v)", c.in, tok, ok, c.wantTok, c.wantOK)
		}
	}
}
