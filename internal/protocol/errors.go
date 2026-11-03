package protocol

import "errors"

// CodeError is a protocol verification failure carrying a stable, machine
// readable code. Decrypt returns these so callers (the CLI) can surface the
// exact §7.8 step that failed as verified:false / error:"<code>" without
// leaking secret material in the message.
type CodeError struct {
	ErrCode string
	Msg     string
}

func (e *CodeError) Error() string {
	if e.Msg == "" {
		return e.ErrCode
	}
	return e.ErrCode + ": " + e.Msg
}

// Decrypt verification errors, one per §7.8 failure mode. The codes are
// deliberately distinct so tamper tests can assert which check caught a
// modification.
var (
	// ErrUnknownSenderKey: no public key is known for (sender_land_id,
	// sender_key_id) — the caller could not resolve the sender's sign_pub
	// (§7.8 step 1).
	ErrUnknownSenderKey = &CodeError{ErrCode: "unknown_sender_key", Msg: "sender public key not found"}
	// ErrBadSignature: the Ed25519 signature over SigInput did not verify
	// (§7.8 step 2). Any tampered header field lands here.
	ErrBadSignature = &CodeError{ErrCode: "bad_signature", Msg: "signature verification failed"}
	// ErrNotRecipient: this land is not in header.recipients (§7.8 step 3).
	ErrNotRecipient = &CodeError{ErrCode: "not_recipient", Msg: "not a recipient of this message"}
	// ErrWrappedKeyMismatch: SHA-256(wrapped_key) != the recipient's
	// wrapped_key_sha256 (§7.8 step 3). Catches a tampered wrapped key.
	ErrWrappedKeyMismatch = &CodeError{ErrCode: "wrapped_key_mismatch", Msg: "wrapped_key hash mismatch"}
	// ErrUnknownRecipientKey: the recipient key_id is neither the current nor
	// a retained (<=24h) key of this land (§7.8 step 3).
	ErrUnknownRecipientKey = &CodeError{ErrCode: "unknown_recipient_key", Msg: "recipient key_id not held"}
	// ErrCiphertextHashMismatch: SHA-256(ciphertext) != header.ciphertext_sha256
	// (§7.8 step 4). Catches a tampered ciphertext.
	ErrCiphertextHashMismatch = &CodeError{ErrCode: "ciphertext_hash_mismatch", Msg: "ciphertext hash mismatch"}
	// ErrSealOpenFailed: crypto_box_seal_open of the wrapped key failed
	// (§7.8 step 5).
	ErrSealOpenFailed = &CodeError{ErrCode: "seal_open_failed", Msg: "could not open sealed content key"}
	// ErrDecryptFailed: XChaCha20-Poly1305 Open failed — wrong key, nonce, or
	// AAD, or a tampered tag (§7.8 step 5).
	ErrDecryptFailed = &CodeError{ErrCode: "decrypt_failed", Msg: "content decryption failed"}
	// ErrBadEnvelope: the envelope is structurally malformed (lengths, missing
	// wrapped keys) before any crypto step runs.
	ErrBadEnvelope = &CodeError{ErrCode: "bad_envelope", Msg: "malformed envelope"}
)

// Code returns the protocol error code of err, or "" if err is not a
// *CodeError.
func Code(err error) string {
	var ce *CodeError
	if errors.As(err, &ce) {
		return ce.ErrCode
	}
	return ""
}
