// Package protocol implements the bot-bridge wire format and end-to-end
// encryption (DEV_SPEC §7), shared by the server and the CLI so that both
// sides agree on exactly one implementation of the envelope, the signature
// input bytes, the fingerprint, and the encrypt/decrypt steps.
//
// The protocol identifier is "bb-e2e-v1". The reference Python prototype uses
// an incompatible "bb-test-v1" format that must not be reused.
//
// Binary fields in the JSON wire format are standard base64 (RFC 4648 §4, with
// padding), matching encoding/json's default []byte encoding (DEV_SPEC §6.1).
package protocol

import (
	"fmt"
	"regexp"
	"time"
)

// ProtocolVersion is the value of Header.V and the first line of SigInput
// (DEV_SPEC §7.1, §7.5).
const ProtocolVersion = "bb-e2e-v1"

// Fixed byte lengths used throughout the protocol (DEV_SPEC §7).
const (
	NonceSize      = 24 // XChaCha20-Poly1305 nonce
	KeySize        = 32 // content key K, X25519 pub, Ed25519 pub
	SHA256Size     = 32
	SignatureSize  = 64 // Ed25519 signature / key binding signature
	WrappedKeySize = 80 // crypto_box_seal output: 32 ephemeral pub + 32 K + 16 tag
)

// msgIDRe is the permitted msg_id shape (DEV_SPEC §7.5).
var msgIDRe = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)

// ValidMsgID reports whether s is a well-formed client msg_id.
func ValidMsgID(s string) bool { return msgIDRe.MatchString(s) }

// idRe is the shape of a server-issued id (land_…, key_…) carried in a header.
var idRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// Recipient is one entry of Header.Recipients: the land the message is
// addressed to, the public key id used to wrap the content key for it, and the
// SHA-256 of that wrapped key (DEV_SPEC §6.4).
type Recipient struct {
	LandID           string `json:"land_id"`
	KeyID            string `json:"key_id"`
	WrappedKeySHA256 []byte `json:"wrapped_key_sha256"` // 32 bytes
}

// Header is the signed, server-visible part of an envelope (DEV_SPEC §6.4,
// §7.4). Every field here is covered by the signature via SigInput, so any
// modification to any field invalidates the signature.
type Header struct {
	V                string      `json:"v"`
	BridgeID         string      `json:"bridge_id"`
	MsgID            string      `json:"msg_id"`
	SenderLandID     string      `json:"sender_land_id"`
	SenderKeyID      string      `json:"sender_key_id"`
	LandSetVersion   int64       `json:"land_set_version"`
	CreatedAt        string      `json:"created_at"` // RFC 3339, original string
	Mentions         []string    `json:"mentions"`
	ReplyTo          *string     `json:"reply_to"`
	Nonce            []byte      `json:"nonce"`             // 24 bytes
	CiphertextSHA256 []byte      `json:"ciphertext_sha256"` // 32 bytes
	Recipients       []Recipient `json:"recipients"`
}

// Envelope is the full /send payload (DEV_SPEC §6.4): the signed header, the
// signature over SigInput(header), the XChaCha20-Poly1305 ciphertext, and the
// per-recipient sealed content keys keyed by land_id.
type Envelope struct {
	Header      Header            `json:"header"`
	Signature   []byte            `json:"signature"`    // 64 bytes
	Ciphertext  []byte            `json:"ciphertext"`   // includes 16-byte tag
	WrappedKeys map[string][]byte `json:"wrapped_keys"` // land_id -> 80-byte sealed key
}

// Attachment is one item of a message Payload (DEV_SPEC §7.3). Per-attachment
// limits are enforced by the CLI before encryption; the protocol treats the
// payload as opaque plaintext.
type Attachment struct {
	Name        string `json:"name"`
	Mime        string `json:"mime"`
	Size        int64  `json:"size"`
	SHA256      []byte `json:"sha256"`
	Data        []byte `json:"data"`
	DurationSec *int   `json:"duration_sec"`
}

// Payload is the UTF-8 JSON plaintext that is encrypted into Ciphertext
// (DEV_SPEC §7.3). It is never seen by the server.
type Payload struct {
	Type        string       `json:"type"`
	Text        string       `json:"text"`
	Attachments []Attachment `json:"attachments,omitempty"`
	SentAt      string       `json:"sent_at"`
}

// EventType enumerates the inbox event kinds (DEV_SPEC §6.4). bridge_closed is
// synthesized by the CLI from a 410 response rather than enqueued.
type EventType string

const (
	EventLandJoined      EventType = "land_joined"
	EventLandLeft        EventType = "land_left"
	EventKeyChanged      EventType = "key_changed"
	EventJoinLocked      EventType = "join_locked"
	EventSettingsChanged EventType = "settings_changed"
	EventBridgeClosed    EventType = "bridge_closed"
)

// Event is a plaintext inbox event (DEV_SPEC §6.4). The land and new_key
// payloads are carried as raw JSON so the protocol package stays decoupled from
// the server's Land output object; the server and CLI shape them as needed.
type Event struct {
	Type           EventType      `json:"type"`
	Land           map[string]any `json:"land,omitempty"`
	NewKey         map[string]any `json:"new_key,omitempty"`
	LandSetVersion int64          `json:"land_set_version,omitempty"`
	Reason         string         `json:"reason,omitempty"`    // land_left / join_locked
	ClosedAt       string         `json:"closed_at,omitempty"` // bridge_closed
	Settings       map[string]any `json:"settings,omitempty"`  // settings_changed
	// Redelivered marks a land_joined notice rebuilt from the lands table because
	// the original expired while the admin was away (DEV_SPEC §11 re-delivery).
	Redelivered bool `json:"redelivered,omitempty"`
}

// Validate checks the structural invariants of a header that do not require
// server state (DEV_SPEC §6.4 field validation, §7.5 msg_id). It does not
// check the signature, nor server-relative facts such as land_set_version
// matching the current value or recipients equaling the current land set —
// those belong to the /send handler.
func (h *Header) Validate() error {
	if h.V != ProtocolVersion {
		return fmt.Errorf("header.v: got %q, want %q", h.V, ProtocolVersion)
	}
	if !idRe.MatchString(h.BridgeID) {
		return fmt.Errorf("header.bridge_id: must match %s", idRe)
	}
	if !ValidMsgID(h.MsgID) {
		return fmt.Errorf("header.msg_id: must match [A-Za-z0-9._-]{1,64}")
	}
	if !idRe.MatchString(h.SenderLandID) {
		return fmt.Errorf("header.sender_land_id: must match %s", idRe)
	}
	if !idRe.MatchString(h.SenderKeyID) {
		return fmt.Errorf("header.sender_key_id: must match %s", idRe)
	}
	if h.LandSetVersion < 1 {
		return fmt.Errorf("header.land_set_version: must be >= 1")
	}
	if _, err := time.Parse(time.RFC3339, h.CreatedAt); err != nil {
		return fmt.Errorf("header.created_at: not RFC3339: %w", err)
	}
	for i, m := range h.Mentions {
		if !idRe.MatchString(m) {
			return fmt.Errorf("header.mentions[%d]: must match %s", i, idRe)
		}
	}
	// reply_to is shown to recipients: it must be a msg_id, never free text
	// (EXFIL_DEFENSE.md §2.1 A0: a newline here forged control lines).
	if h.ReplyTo != nil && *h.ReplyTo != "" && !ValidMsgID(*h.ReplyTo) {
		return fmt.Errorf("header.reply_to: must match %s", msgIDRe)
	}
	if len(h.Nonce) != NonceSize {
		return fmt.Errorf("header.nonce: got %d bytes, want %d", len(h.Nonce), NonceSize)
	}
	if len(h.CiphertextSHA256) != SHA256Size {
		return fmt.Errorf("header.ciphertext_sha256: got %d bytes, want %d", len(h.CiphertextSHA256), SHA256Size)
	}
	if len(h.Recipients) == 0 {
		return fmt.Errorf("header.recipients: empty")
	}
	seen := make(map[string]struct{}, len(h.Recipients))
	var prev string
	for i, r := range h.Recipients {
		if r.LandID == "" {
			return fmt.Errorf("header.recipients[%d].land_id: empty", i)
		}
		if r.KeyID == "" {
			return fmt.Errorf("header.recipients[%d].key_id: empty", i)
		}
		if len(r.WrappedKeySHA256) != SHA256Size {
			return fmt.Errorf("header.recipients[%d].wrapped_key_sha256: got %d bytes, want %d", i, len(r.WrappedKeySHA256), SHA256Size)
		}
		if _, dup := seen[r.LandID]; dup {
			return fmt.Errorf("header.recipients: duplicate land_id %q", r.LandID)
		}
		seen[r.LandID] = struct{}{}
		if i > 0 && r.LandID < prev {
			return fmt.Errorf("header.recipients: not sorted by land_id (%q after %q)", r.LandID, prev)
		}
		prev = r.LandID
	}
	return nil
}

// Validate checks an envelope's structure: a valid header, a 64-byte
// signature, non-empty ciphertext, and wrapped_keys whose key set and byte
// lengths line up with the header's recipients (DEV_SPEC §6.4 steps 3–4).
func (e *Envelope) Validate() error {
	if err := e.Header.Validate(); err != nil {
		return err
	}
	if len(e.Signature) != SignatureSize {
		return fmt.Errorf("signature: got %d bytes, want %d", len(e.Signature), SignatureSize)
	}
	if len(e.Ciphertext) == 0 {
		return fmt.Errorf("ciphertext: empty")
	}
	if len(e.WrappedKeys) != len(e.Header.Recipients) {
		return fmt.Errorf("wrapped_keys: got %d entries, want %d (one per recipient)", len(e.WrappedKeys), len(e.Header.Recipients))
	}
	for _, r := range e.Header.Recipients {
		wk, ok := e.WrappedKeys[r.LandID]
		if !ok {
			return fmt.Errorf("wrapped_keys: missing entry for recipient %q", r.LandID)
		}
		if len(wk) != WrappedKeySize {
			return fmt.Errorf("wrapped_keys[%q]: got %d bytes, want %d", r.LandID, len(wk), WrappedKeySize)
		}
	}
	return nil
}
