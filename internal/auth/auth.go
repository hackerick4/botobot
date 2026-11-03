// Package auth handles bot-bridge API keys (DEV_SPEC §5, §6.1, §16): generating
// a key, deriving its display prefix, and computing the HMAC-SHA256 hash that is
// the only representation stored server-side. The plaintext key is shown to the
// land exactly once (on create / join / rotate) and never persisted.
//
// Keys are formatted "bbk_" + 43 base64url (unpadded) characters, which encode
// 32 bytes of crypto/rand entropy (DEV_SPEC §6.1). The stored hash is
// HMAC-SHA256(pepper, full-key); comparison is by exact hash, so a stolen
// database alone cannot reconstruct a usable key without the pepper.
package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"strings"
)

// Prefix is the literal "bbk_" scheme prefix of every API key.
const Prefix = "bbk_"

// EntryPrefix is the scheme prefix of a public-entry key (v0.6.0): the service
// bot's one key for all bridges created through its entry.
const EntryPrefix = "bbe_"

// keyBytes is the number of random bytes behind a key (32 => 43 base64url chars).
const keyBytes = 32

// displayPrefixLen is how many characters after "bbk_" are stored for display
// (DEV_SPEC §5 api_keys.prefix: "first 8 chars after 'bbk_'").
const displayPrefixLen = 8

// b64 is unpadded base64url, matching the "<43 chars base64url>" form (§6.1).
var b64 = base64.RawURLEncoding

// Key is a freshly generated API key: the full secret shown once to the land and
// the non-secret display prefix recorded alongside its hash.
type Key struct {
	// Full is the complete "bbk_…" secret. Never log or store it.
	Full string
	// Prefix is the first displayPrefixLen characters after "bbk_".
	Prefix string
}

// Generate creates a new API key from crypto/rand.
func Generate() (Key, error) {
	buf := make([]byte, keyBytes)
	if _, err := rand.Read(buf); err != nil {
		return Key{}, fmt.Errorf("auth: read random: %w", err)
	}
	full := Prefix + b64.EncodeToString(buf)
	return Key{Full: full, Prefix: DisplayPrefix(full)}, nil
}

// GenerateEntry creates a new public-entry key ("bbe_" + 43 base64url).
func GenerateEntry() (Key, error) {
	buf := make([]byte, keyBytes)
	if _, err := rand.Read(buf); err != nil {
		return Key{}, fmt.Errorf("auth: read random: %w", err)
	}
	full := EntryPrefix + b64.EncodeToString(buf)
	return Key{Full: full, Prefix: DisplayPrefix(full)}, nil
}

// IsEntryKey reports whether a bearer token is a public-entry key.
func IsEntryKey(tok string) bool { return strings.HasPrefix(tok, EntryPrefix) }

// DisplayPrefix returns the first displayPrefixLen characters after "bbk_", or
// the whole suffix if it is shorter. It never returns secret-length material.
func DisplayPrefix(full string) string {
	s := strings.TrimPrefix(strings.TrimPrefix(full, Prefix), EntryPrefix)
	if len(s) > displayPrefixLen {
		return s[:displayPrefixLen]
	}
	return s
}

// Hash returns HMAC-SHA256(pepper, full) — the value stored in api_keys.key_hash
// and looked up on every authenticated request (DEV_SPEC §5, §6.1).
func Hash(pepper []byte, full string) []byte {
	m := hmac.New(sha256.New, pepper)
	m.Write([]byte(full))
	return m.Sum(nil)
}

// ParseBearer extracts the token from an Authorization header value of the form
// "Bearer bbk_…" (DEV_SPEC §6.1). It reports ok=false when the header is absent,
// uses a non-Bearer scheme, or the token lacks the "bbk_" prefix. The scheme is
// matched case-insensitively per RFC 7235; the token itself is case-sensitive.
func ParseBearer(header string) (token string, ok bool) {
	const scheme = "bearer "
	if len(header) <= len(scheme) || !strings.EqualFold(header[:len(scheme)], scheme) {
		return "", false
	}
	tok := strings.TrimSpace(header[len(scheme):])
	if (!strings.HasPrefix(tok, Prefix) && !strings.HasPrefix(tok, EntryPrefix)) || len(tok) <= len(Prefix) {
		return "", false
	}
	return tok, true
}
