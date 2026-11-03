package protocol

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"

	"golang.org/x/crypto/curve25519"
)

// keyBindingPrefix is the exact 10-byte domain-separation prefix for key
// binding signatures (DEV_SPEC §7.6): "bb-key-v1\n".
const keyBindingPrefix = "bb-key-v1\n"

// b64 is standard base64 with padding (RFC 4648 §4), used both on the JSON wire
// and inside SigInput (DEV_SPEC §6.1, §7.5).
var b64 = base64.StdEncoding

// SigInput builds the exact bytes signed for an envelope (DEV_SPEC §7.5). Each
// line is terminated by a single '\n' (0x0A); the output is UTF-8 with no BOM.
// No JSON canonicalization is used, to avoid cross-language differences.
//
// Layout:
//
//	bb-e2e-v1
//	<bridge_id>
//	<msg_id>
//	<sender_land_id>
//	<sender_key_id>
//	<land_set_version decimal>
//	<created_at RFC3339 original string>
//	<reply_to or empty string>
//	<mentions joined by "," in original order, or empty string>
//	<base64(nonce)>
//	<base64(ciphertext_sha256)>
//	<recipients count decimal>
//	<land_id>:<key_id>:<base64(wrapped_key_sha256)>   # one per recipient, sorted by land_id
//
// Recipients are emitted in the order they appear in h.Recipients, which must
// already be sorted by land_id (Encrypt guarantees this; Validate checks it).
func SigInput(h *Header) []byte {
	var b strings.Builder

	writeLine := func(s string) {
		b.WriteString(s)
		b.WriteByte('\n')
	}

	writeLine(h.V)
	writeLine(h.BridgeID)
	writeLine(h.MsgID)
	writeLine(h.SenderLandID)
	writeLine(h.SenderKeyID)
	writeLine(strconv.FormatInt(h.LandSetVersion, 10))
	writeLine(h.CreatedAt)
	if h.ReplyTo != nil {
		writeLine(*h.ReplyTo)
	} else {
		writeLine("")
	}
	writeLine(strings.Join(h.Mentions, ","))
	writeLine(b64.EncodeToString(h.Nonce))
	writeLine(b64.EncodeToString(h.CiphertextSHA256))
	writeLine(strconv.Itoa(len(h.Recipients)))
	for _, r := range h.Recipients {
		writeLine(r.LandID + ":" + r.KeyID + ":" + b64.EncodeToString(r.WrappedKeySHA256))
	}

	return []byte(b.String())
}

// Sign returns the Ed25519 signature over SigInput(h) using the sender's
// signing private key (DEV_SPEC §7.4 step 5, §7.5). Pure Ed25519, not ph.
func Sign(signPriv ed25519.PrivateKey, h *Header) []byte {
	return ed25519.Sign(signPriv, SigInput(h))
}

// Verify reports whether sig is a valid Ed25519 signature over SigInput(h)
// under signPub (DEV_SPEC §7.8 step 2).
func Verify(signPub ed25519.PublicKey, h *Header, sig []byte) bool {
	if len(signPub) != ed25519.PublicKeySize || len(sig) != SignatureSize {
		return false
	}
	return ed25519.Verify(signPub, SigInput(h), sig)
}

// BindingInput returns the exact bytes signed for a key binding (DEV_SPEC §7.6):
// "bb-key-v1\n" || enc_pub(32) || sign_pub(32).
func BindingInput(encPub, signPub []byte) []byte {
	out := make([]byte, 0, len(keyBindingPrefix)+KeySize+KeySize)
	out = append(out, keyBindingPrefix...)
	out = append(out, encPub...)
	out = append(out, signPub...)
	return out
}

// SignBinding produces binding_sig = Ed25519(sign_priv, BindingInput)
// (DEV_SPEC §6.3, §7.6). encPub and signPub must each be 32 bytes.
func SignBinding(signPriv ed25519.PrivateKey, encPub, signPub []byte) ([]byte, error) {
	if len(encPub) != KeySize || len(signPub) != KeySize {
		return nil, fmt.Errorf("binding: enc_pub and sign_pub must be %d bytes", KeySize)
	}
	return ed25519.Sign(signPriv, BindingInput(encPub, signPub)), nil
}

// VerifyBinding checks that bindingSig binds encPub to signPub under signPub
// itself (DEV_SPEC §7.6). The server calls this on create/join/rekey.
func VerifyBinding(encPub, signPub, bindingSig []byte) bool {
	if len(encPub) != KeySize || len(signPub) != KeySize || len(bindingSig) != SignatureSize {
		return false
	}
	return ed25519.Verify(ed25519.PublicKey(signPub), BindingInput(encPub, signPub), bindingSig)
}

// Fingerprint computes the 30-digit key fingerprint (DEV_SPEC §7.7):
// d = SHA-256(sign_pub || enc_pub); take d[0:30] as six 5-byte groups; each
// group is a big-endian 40-bit integer mod 100000, zero-padded to 5 digits;
// groups are joined by single spaces.
func Fingerprint(signPub, encPub []byte) string {
	h := sha256.New()
	h.Write(signPub)
	h.Write(encPub)
	d := h.Sum(nil)

	groups := make([]string, 6)
	for i := 0; i < 6; i++ {
		g := d[i*5 : i*5+5]
		var v uint64
		for _, bb := range g {
			v = v<<8 | uint64(bb)
		}
		groups[i] = fmt.Sprintf("%05d", v%100000)
	}
	return strings.Join(groups, " ")
}

// PublicEncKey derives the 32-byte X25519 public key from a 32-byte X25519
// private key. Helper for building key material and test vectors.
func PublicEncKey(encPriv []byte) ([]byte, error) {
	if len(encPriv) != KeySize {
		return nil, fmt.Errorf("enc_priv must be %d bytes", KeySize)
	}
	var priv, pub [KeySize]byte
	copy(priv[:], encPriv)
	curve25519.ScalarBaseMult(&pub, &priv)
	return pub[:], nil
}
