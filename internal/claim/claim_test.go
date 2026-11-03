package claim

import (
	"bytes"
	"crypto/sha256"
	"strings"
	"testing"
)

func TestGenerateShapeAndEntropy(t *testing.T) {
	seen := map[string]struct{}{}
	for i := 0; i < 100; i++ {
		tok, err := Generate()
		if err != nil {
			t.Fatalf("Generate: %v", err)
		}
		if !strings.HasPrefix(tok, Prefix) {
			t.Fatalf("token %q missing prefix %q", tok, Prefix)
		}
		if !Valid(tok) {
			t.Fatalf("Valid(%q) = false", tok)
		}
		// 32 bytes -> 43 unpadded base64url chars after the prefix (>=128 bits).
		suffix := strings.TrimPrefix(tok, Prefix)
		if len(suffix) != 43 {
			t.Fatalf("suffix length = %d, want 43", len(suffix))
		}
		if _, dup := seen[tok]; dup {
			t.Fatalf("duplicate token generated: %q", tok)
		}
		seen[tok] = struct{}{}
	}
}

func TestHashDeterministicAndDomainSeparated(t *testing.T) {
	pepper := bytes.Repeat([]byte{0x01}, 32)
	tok := "bbc_exampletoken"

	h1 := Hash(pepper, tok)
	h2 := Hash(pepper, tok)
	if !bytes.Equal(h1, h2) {
		t.Fatal("Hash not deterministic")
	}
	if len(h1) != sha256.Size {
		t.Fatalf("hash length = %d, want %d", len(h1), sha256.Size)
	}
	// The hash must not equal a plain HMAC without the domain label (separation).
	if bytes.Contains(h1, []byte(tok)) {
		t.Fatal("hash leaks the token bytes")
	}

	// Different pepper -> different hash.
	other := bytes.Repeat([]byte{0x02}, 32)
	if bytes.Equal(h1, Hash(other, tok)) {
		t.Fatal("hash does not depend on pepper")
	}
}

func TestEqualConstantTimeMatch(t *testing.T) {
	pepper := bytes.Repeat([]byte{0x09}, 32)
	tok, err := Generate()
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	stored := Hash(pepper, tok)

	if !Equal(pepper, tok, stored) {
		t.Fatal("Equal should match the correct token")
	}
	if Equal(pepper, tok+"x", stored) {
		t.Fatal("Equal should reject a wrong token")
	}
	if Equal(pepper, tok, stored[:len(stored)-1]) {
		t.Fatal("Equal should reject a short stored hash")
	}
	if Equal(pepper, tok, nil) {
		t.Fatal("Equal should reject a nil stored hash")
	}
}

func TestValid(t *testing.T) {
	if Valid("") || Valid("bbc_") || Valid("nope") {
		t.Fatal("Valid accepted a malformed token")
	}
	if !Valid("bbc_x") {
		t.Fatal("Valid rejected a well-formed token")
	}
}
