package protocol

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"fmt"
	"io"
	"sort"

	"golang.org/x/crypto/chacha20poly1305"
	"golang.org/x/crypto/nacl/box"
)

// aad builds the XChaCha20-Poly1305 associated data (DEV_SPEC §7.4 step 2):
// "bb-e2e-v1|" + bridge_id + "|" + sender_land_id + "|" + msg_id.
func aad(bridgeID, senderLandID, msgID string) []byte {
	return []byte(ProtocolVersion + "|" + bridgeID + "|" + senderLandID + "|" + msgID)
}

// RecipientKey is a destination land and the X25519 public key to seal the
// content key for (input to Encrypt).
type RecipientKey struct {
	LandID string
	KeyID  string
	EncPub []byte // 32-byte X25519 public key
}

// EncryptInput carries everything Encrypt needs to build a signed envelope
// (DEV_SPEC §7.4). The sender's signing private key signs the header; the
// recipients' X25519 public keys wrap the random content key.
type EncryptInput struct {
	BridgeID       string
	MsgID          string
	SenderLandID   string
	SenderKeyID    string
	SenderSignPriv ed25519.PrivateKey
	LandSetVersion int64
	CreatedAt      string // RFC 3339 string, used verbatim
	Mentions       []string
	ReplyTo        *string
	Plaintext      []byte
	Recipients     []RecipientKey
}

// Encrypt performs the full sender flow (DEV_SPEC §7.4) using crypto/rand for
// the content key K, the nonce, and each sealed box. It returns a complete,
// signed, validated envelope.
func Encrypt(in EncryptInput) (*Envelope, error) {
	return encrypt(in, rand.Reader)
}

// encrypt is Encrypt with an injectable randomness source. Tests and the vector
// generator pass a deterministic reader to fix K and the nonce; the sealed
// boxes still consume randomness from the same reader.
func encrypt(in EncryptInput, rnd io.Reader) (*Envelope, error) {
	if len(in.SenderSignPriv) != ed25519.PrivateKeySize {
		return nil, fmt.Errorf("encrypt: sender sign_priv must be %d bytes", ed25519.PrivateKeySize)
	}
	if len(in.Recipients) == 0 {
		return nil, fmt.Errorf("encrypt: no recipients")
	}
	if !ValidMsgID(in.MsgID) {
		return nil, fmt.Errorf("encrypt: invalid msg_id")
	}

	// 1. Random content key K and nonce.
	K := make([]byte, KeySize)
	if _, err := io.ReadFull(rnd, K); err != nil {
		return nil, fmt.Errorf("encrypt: read K: %w", err)
	}
	nonce := make([]byte, NonceSize)
	if _, err := io.ReadFull(rnd, nonce); err != nil {
		return nil, fmt.Errorf("encrypt: read nonce: %w", err)
	}

	// 2. Seal the plaintext with XChaCha20-Poly1305 and the AAD.
	aead, err := chacha20poly1305.NewX(K)
	if err != nil {
		return nil, fmt.Errorf("encrypt: new xchacha: %w", err)
	}
	ad := aad(in.BridgeID, in.SenderLandID, in.MsgID)
	ciphertext := aead.Seal(nil, nonce, in.Plaintext, ad)
	ctHash := sha256.Sum256(ciphertext)

	// 3. Wrap K for each recipient with crypto_box_seal; sort recipients by
	//    land_id so the header, SigInput, and wrapped_keys all agree.
	recips := append([]RecipientKey(nil), in.Recipients...)
	sort.Slice(recips, func(i, j int) bool { return recips[i].LandID < recips[j].LandID })

	headerRecips := make([]Recipient, 0, len(recips))
	wrappedKeys := make(map[string][]byte, len(recips))
	seen := make(map[string]struct{}, len(recips))
	for _, r := range recips {
		if len(r.EncPub) != KeySize {
			return nil, fmt.Errorf("encrypt: recipient %q enc_pub must be %d bytes", r.LandID, KeySize)
		}
		if _, dup := seen[r.LandID]; dup {
			return nil, fmt.Errorf("encrypt: duplicate recipient land_id %q", r.LandID)
		}
		seen[r.LandID] = struct{}{}

		var encPub [KeySize]byte
		copy(encPub[:], r.EncPub)
		wrapped, err := box.SealAnonymous(nil, K, &encPub, rnd)
		if err != nil {
			return nil, fmt.Errorf("encrypt: seal for %q: %w", r.LandID, err)
		}
		wkHash := sha256.Sum256(wrapped)
		wrappedKeys[r.LandID] = wrapped
		headerRecips = append(headerRecips, Recipient{
			LandID:           r.LandID,
			KeyID:            r.KeyID,
			WrappedKeySHA256: wkHash[:],
		})
	}

	// 4. Assemble the header.
	h := Header{
		V:                ProtocolVersion,
		BridgeID:         in.BridgeID,
		MsgID:            in.MsgID,
		SenderLandID:     in.SenderLandID,
		SenderKeyID:      in.SenderKeyID,
		LandSetVersion:   in.LandSetVersion,
		CreatedAt:        in.CreatedAt,
		Mentions:         in.Mentions,
		ReplyTo:          in.ReplyTo,
		Nonce:            nonce,
		CiphertextSHA256: ctHash[:],
		Recipients:       headerRecips,
	}

	// 5. Sign.
	env := &Envelope{
		Header:      h,
		Signature:   Sign(in.SenderSignPriv, &h),
		Ciphertext:  ciphertext,
		WrappedKeys: wrappedKeys,
	}
	if err := env.Validate(); err != nil {
		return nil, fmt.Errorf("encrypt: produced invalid envelope: %w", err)
	}
	return env, nil
}

// DecryptInput carries everything Decrypt needs to run the recipient
// verification steps (DEV_SPEC §7.8). SenderSignPub is the sign_pub resolved
// for (sender_land_id, sender_key_id) from the caller's /lands cache; a nil or
// empty value means the key could not be resolved (unknown_sender_key).
// MyEncKeys maps each key_id this land currently holds or has retained for the
// last 24h to its 32-byte X25519 private key.
type DecryptInput struct {
	Envelope      *Envelope
	SenderSignPub []byte
	MyLandID      string
	MyEncKeys     map[string][]byte
}

// Decrypt runs every §7.8 verification step in order and returns the plaintext.
// On any failure it returns a *CodeError whose code names the failing step, so
// the caller can emit verified:false and still ack the item. It never returns
// partial plaintext.
func Decrypt(in DecryptInput) ([]byte, error) {
	env := in.Envelope
	if env == nil {
		return nil, ErrBadEnvelope
	}
	// Validate the signed header and the envelope-level fields, but NOT that a
	// wrapped key is present for every recipient: an inbox-delivered envelope
	// carries only this recipient's own wrapped key (DEV_SPEC §6.4 GET /inbox),
	// while the header still lists every recipient (needed to verify the
	// signature, §7.5). Step 3 below checks this recipient's own wrapped key.
	if err := env.Header.Validate(); err != nil {
		return nil, &CodeError{ErrCode: ErrBadEnvelope.ErrCode, Msg: err.Error()}
	}
	if len(env.Signature) != SignatureSize {
		return nil, &CodeError{ErrCode: ErrBadEnvelope.ErrCode, Msg: "signature: wrong length"}
	}
	if len(env.Ciphertext) == 0 {
		return nil, &CodeError{ErrCode: ErrBadEnvelope.ErrCode, Msg: "ciphertext: empty"}
	}

	// Step 1: resolve the sender's signing key.
	if len(in.SenderSignPub) != ed25519.PublicKeySize {
		return nil, ErrUnknownSenderKey
	}

	// Step 2: verify the signature over SigInput.
	if !Verify(in.SenderSignPub, &env.Header, env.Signature) {
		return nil, ErrBadSignature
	}

	// Step 3: locate our recipient entry; check the wrapped-key hash and that
	// we hold the referenced key_id.
	var me *Recipient
	for i := range env.Header.Recipients {
		if env.Header.Recipients[i].LandID == in.MyLandID {
			me = &env.Header.Recipients[i]
			break
		}
	}
	if me == nil {
		return nil, ErrNotRecipient
	}
	wrapped, ok := env.WrappedKeys[in.MyLandID]
	if !ok {
		// We are listed as a recipient but the envelope carries no wrapped key
		// for us (a malformed inbox item).
		return nil, ErrNotRecipient
	}
	if len(wrapped) != WrappedKeySize {
		return nil, ErrWrappedKeyMismatch
	}
	wkHash := sha256.Sum256(wrapped)
	if subtle.ConstantTimeCompare(wkHash[:], me.WrappedKeySHA256) != 1 {
		return nil, ErrWrappedKeyMismatch
	}
	encPriv, ok := in.MyEncKeys[me.KeyID]
	if !ok {
		return nil, ErrUnknownRecipientKey
	}
	if len(encPriv) != KeySize {
		return nil, &CodeError{ErrCode: ErrUnknownRecipientKey.ErrCode, Msg: "held private key has wrong length"}
	}

	// Step 4: verify the ciphertext hash.
	ctHash := sha256.Sum256(env.Ciphertext)
	if subtle.ConstantTimeCompare(ctHash[:], env.Header.CiphertextSHA256) != 1 {
		return nil, ErrCiphertextHashMismatch
	}

	// Step 5: open the sealed content key, then decrypt the content.
	pubBytes, err := PublicEncKey(encPriv)
	if err != nil {
		return nil, &CodeError{ErrCode: ErrUnknownRecipientKey.ErrCode, Msg: err.Error()}
	}
	var priv, pub [KeySize]byte
	copy(priv[:], encPriv)
	copy(pub[:], pubBytes)
	K, ok := box.OpenAnonymous(nil, wrapped, &pub, &priv)
	if !ok {
		return nil, ErrSealOpenFailed
	}
	aead, err := chacha20poly1305.NewX(K)
	if err != nil {
		return nil, &CodeError{ErrCode: ErrDecryptFailed.ErrCode, Msg: "new xchacha: " + err.Error()}
	}
	ad := aad(env.Header.BridgeID, env.Header.SenderLandID, env.Header.MsgID)
	plaintext, err := aead.Open(nil, env.Header.Nonce, env.Ciphertext, ad)
	if err != nil {
		return nil, ErrDecryptFailed
	}
	return plaintext, nil
}
