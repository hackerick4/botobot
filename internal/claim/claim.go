// Package claim handles the one-time admin claim token used by the public
// /join flow (DEV_SPEC §6 /join, claim). POST /join returns a claim token to
// the caller once; the caller later proves possession of it at
// POST /v1/bridges/{id}/claim to become the bridge's admin land.
//
// The token is a secret, treated like an API key: it is generated from
// crypto/rand with >=128 bits of entropy, only its HMAC (with the server pepper,
// domain-separated) is stored, comparison is constant-time, and it is never
// logged (CLAUDE.md rule 5, DEV_SPEC §15, §16).
package claim

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"strings"
)

// Prefix is the literal scheme prefix of every claim token ("bbc_" = bot-bridge
// claim), mirroring the "bbk_" API-key convention (DEV_SPEC §6.1).
const Prefix = "bbc_"

// tokenBytes is the number of random bytes behind a token. 32 bytes = 256 bits,
// comfortably above the 128-bit minimum (DEV_SPEC §6 /join claim token).
const tokenBytes = 32

// domain is the HMAC domain-separation label so a claim-token hash can never
// collide with an API-key hash computed under the same pepper.
const domain = "bb-claim-v1\n"

// b64 is unpadded base64url, keeping the token URL-safe and copy-paste friendly.
var b64 = base64.RawURLEncoding

// Generate returns a fresh URL-safe claim token from crypto/rand. The returned
// string is the full "bbc_…" secret shown to the caller exactly once.
func Generate() (string, error) {
	buf := make([]byte, tokenBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("claim: read random: %w", err)
	}
	return Prefix + b64.EncodeToString(buf), nil
}

// Hash returns HMAC-SHA256(pepper, domain || token) — the value stored in
// bridges.claim_token_hash and compared on claim (DEV_SPEC §6 /join).
func Hash(pepper []byte, token string) []byte {
	m := hmac.New(sha256.New, pepper)
	m.Write([]byte(domain))
	m.Write([]byte(token))
	return m.Sum(nil)
}

// Equal reports whether the presented token hashes to stored, in constant time
// (DEV_SPEC §8, §16). A nil/short stored hash never matches.
func Equal(pepper []byte, token string, stored []byte) bool {
	if len(stored) != sha256.Size {
		return false
	}
	return subtle.ConstantTimeCompare(Hash(pepper, token), stored) == 1
}

// Valid reports whether s has the expected token shape (prefix + non-empty
// suffix). It is a cheap structural check; possession is proven by Equal.
func Valid(s string) bool {
	return strings.HasPrefix(s, Prefix) && len(s) > len(Prefix)
}
