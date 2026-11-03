// Package invite handles bot-bridge invite codes (DEV_SPEC §8): generating a
// random 6-digit code (with weak codes excluded), encrypting it at rest under
// the server's BB_INVITE_KEY so it can still be shown to authorised lands, and
// the in-memory brute-force throttling state machine (throttle.go).
//
// Invite codes are secret: they are never logged, and comparison is
// constant-time (DEV_SPEC §8, §15, §16).
package invite

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/binary"
	"fmt"
	"io"

	"golang.org/x/crypto/chacha20poly1305"
)

// CodeLen is the number of decimal digits in an invite code (DEV_SPEC §8).
const CodeLen = 6

// Generate returns a uniformly random 6-digit code from crypto/rand with weak
// codes excluded (DEV_SPEC §8). Generate reads from crypto/rand.Reader.
func Generate() (string, error) { return generate(rand.Reader) }

// generate is Generate with an injectable randomness source for tests.
func generate(rnd io.Reader) (string, error) {
	for {
		n, err := uniformUint32(rnd, 1_000_000)
		if err != nil {
			return "", err
		}
		code := fmt.Sprintf("%06d", n)
		if !IsWeak(code) {
			return code, nil
		}
	}
}

// uniformUint32 returns a uniformly distributed value in [0, max) using
// rejection sampling so there is no modulo bias (DEV_SPEC §8).
func uniformUint32(rnd io.Reader, max uint32) (uint32, error) {
	// Largest multiple of max that fits in uint32; reject anything at/above it.
	limit := (uint32(0xffffffff) / max) * max
	var b [4]byte
	for {
		if _, err := io.ReadFull(rnd, b[:]); err != nil {
			return 0, fmt.Errorf("invite: read random: %w", err)
		}
		v := binary.BigEndian.Uint32(b[:])
		if v < limit {
			return v % max, nil
		}
	}
}

// IsWeak reports whether a 6-digit code is excluded (DEV_SPEC §8): all digits
// identical (111111), strictly consecutive ascending (123456) or descending
// (987654), or first three digits equal to the last three (123123).
func IsWeak(code string) bool {
	if len(code) != CodeLen {
		return true
	}
	allSame, asc, desc := true, true, true
	for i := 1; i < CodeLen; i++ {
		if code[i] != code[0] {
			allSame = false
		}
		if code[i] != code[i-1]+1 {
			asc = false
		}
		if code[i] != code[i-1]-1 {
			desc = false
		}
	}
	if allSame || asc || desc {
		return true
	}
	return code[:3] == code[3:] // 123123
}

// Encrypt seals a code with XChaCha20-Poly1305 under key (the 32-byte
// BB_INVITE_KEY), binding it to bridgeID as associated data (DEV_SPEC §8). The
// stored blob is nonce(24) || ciphertext; a fresh random nonce is used each time.
func Encrypt(key []byte, bridgeID, code string) ([]byte, error) {
	aead, err := chacha20poly1305.NewX(key)
	if err != nil {
		return nil, fmt.Errorf("invite: new xchacha: %w", err)
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, fmt.Errorf("invite: read nonce: %w", err)
	}
	ct := aead.Seal(nil, nonce, []byte(code), []byte(bridgeID))
	out := make([]byte, 0, len(nonce)+len(ct))
	out = append(out, nonce...)
	out = append(out, ct...)
	return out, nil
}

// Decrypt opens a blob produced by Encrypt, verifying the bridgeID binding
// (DEV_SPEC §8).
func Decrypt(key []byte, bridgeID string, enc []byte) (string, error) {
	aead, err := chacha20poly1305.NewX(key)
	if err != nil {
		return "", fmt.Errorf("invite: new xchacha: %w", err)
	}
	ns := aead.NonceSize()
	if len(enc) < ns {
		return "", fmt.Errorf("invite: ciphertext too short")
	}
	nonce, ct := enc[:ns], enc[ns:]
	pt, err := aead.Open(nil, nonce, ct, []byte(bridgeID))
	if err != nil {
		return "", fmt.Errorf("invite: open: %w", err)
	}
	return string(pt), nil
}

// Equal reports whether two codes match in constant time (DEV_SPEC §8, §16).
func Equal(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}
