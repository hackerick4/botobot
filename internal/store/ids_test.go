package store

import (
	"strings"
	"testing"
)

func TestNewIDFormat(t *testing.T) {
	cases := []struct {
		prefix string
	}{
		{prefixBridge}, {prefixLand}, {prefixKey}, {prefixPubKey}, {prefixEnv},
	}
	for _, c := range cases {
		id := newID(c.prefix)
		if !strings.HasPrefix(id, c.prefix) {
			t.Fatalf("id %q missing prefix %q", id, c.prefix)
		}
		rest := id[len(c.prefix):]
		if len(rest) != idRandLen {
			t.Errorf("id %q random part = %d chars, want %d", id, len(rest), idRandLen)
		}
		for _, r := range rest {
			if !strings.ContainsRune(base62, r) {
				t.Errorf("id %q contains non-base62 rune %q", id, r)
			}
		}
	}
}

func TestNewIDUnique(t *testing.T) {
	seen := make(map[string]struct{}, 10000)
	for i := 0; i < 10000; i++ {
		id := newID(prefixEnv)
		if _, dup := seen[id]; dup {
			t.Fatalf("duplicate id generated: %q", id)
		}
		seen[id] = struct{}{}
	}
}
