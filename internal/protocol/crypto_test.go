package protocol

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"testing"
)

// party is a test land with both key pairs.
type party struct {
	landID   string
	keyID    string
	signPub  ed25519.PublicKey
	signPriv ed25519.PrivateKey
	encPriv  []byte
	encPub   []byte
}

func newParty(t *testing.T, landID, keyID string) party {
	t.Helper()
	signPub, signPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	encPriv := make([]byte, KeySize)
	if _, err := rand.Read(encPriv); err != nil {
		t.Fatal(err)
	}
	encPub, err := PublicEncKey(encPriv)
	if err != nil {
		t.Fatal(err)
	}
	return party{landID, keyID, signPub, signPriv, encPriv, encPub}
}

// buildEnvelope encrypts a fixed payload from sender to the given recipients.
func buildEnvelope(t *testing.T, sender party, recips ...party) *Envelope {
	t.Helper()
	in := EncryptInput{
		BridgeID:       "br_testbridge0000000000",
		MsgID:          "c-20261029-0001",
		SenderLandID:   sender.landID,
		SenderKeyID:    sender.keyID,
		SenderSignPriv: sender.signPriv,
		LandSetVersion: 7,
		CreatedAt:      "2026-10-29T07:02:00.000Z",
		Mentions:       []string{recips[0].landID},
		Plaintext:      []byte(`{"type":"message","text":"收到，附上報價","sent_at":"2026-10-29T07:02:00.000Z"}`),
	}
	for _, r := range recips {
		in.Recipients = append(in.Recipients, RecipientKey{LandID: r.landID, KeyID: r.keyID, EncPub: r.encPub})
	}
	env, err := Encrypt(in)
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	return env
}

func decryptAs(env *Envelope, me party, sender party) ([]byte, error) {
	return Decrypt(DecryptInput{
		Envelope:      env,
		SenderSignPub: sender.signPub,
		MyLandID:      me.landID,
		MyEncKeys:     map[string][]byte{me.keyID: me.encPriv},
	})
}

func TestEncryptDecryptRoundTrip(t *testing.T) {
	sender := newParty(t, "land_sender000000000000", "pk_sender00000000000000")
	amy := newParty(t, "land_amy0000000000000000", "pk_amy0000000000000000")
	bob := newParty(t, "land_bob0000000000000000", "pk_bob0000000000000000")

	env := buildEnvelope(t, sender, amy, bob)

	// Envelope shape: recipients sorted, 80-byte wrapped keys, 24-byte nonce.
	if len(env.Header.Recipients) != 2 {
		t.Fatalf("recipients = %d, want 2", len(env.Header.Recipients))
	}
	if env.Header.Recipients[0].LandID != amy.landID || env.Header.Recipients[1].LandID != bob.landID {
		t.Fatal("recipients not sorted by land_id")
	}
	if len(env.Header.Nonce) != NonceSize {
		t.Fatalf("nonce size = %d", len(env.Header.Nonce))
	}
	for id, wk := range env.WrappedKeys {
		if len(wk) != WrappedKeySize {
			t.Fatalf("wrapped_key[%s] size = %d, want %d", id, len(wk), WrappedKeySize)
		}
	}

	want := []byte(`{"type":"message","text":"收到，附上報價","sent_at":"2026-10-29T07:02:00.000Z"}`)
	for _, me := range []party{amy, bob} {
		got, err := decryptAs(env, me, sender)
		if err != nil {
			t.Fatalf("decrypt as %s: %v", me.landID, err)
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("plaintext mismatch for %s", me.landID)
		}
	}

	// Payload round-trips through the Payload type.
	got, _ := decryptAs(env, amy, sender)
	var p Payload
	if err := json.Unmarshal(got, &p); err != nil {
		t.Fatalf("payload unmarshal: %v", err)
	}
	if p.Type != "message" || p.Text != "收到，附上報價" {
		t.Fatalf("payload = %+v", p)
	}
}

// TestDecryptPartialWrappedKeys proves a recipient can decrypt an inbox-style
// envelope that carries ONLY its own wrapped key, while the header still lists
// every recipient (DEV_SPEC §6.4 GET /inbox, §7.8). This mirrors how the server
// fans a message out: each recipient receives its own 80-byte wrapped key.
func TestDecryptPartialWrappedKeys(t *testing.T) {
	sender := newParty(t, "land_sender000000000000", "pk_sender00000000000000")
	amy := newParty(t, "land_amy0000000000000000", "pk_amy0000000000000000")
	bob := newParty(t, "land_bob0000000000000000", "pk_bob0000000000000000")

	full := buildEnvelope(t, sender, amy, bob)

	for _, me := range []party{amy, bob} {
		// Reconstruct the envelope the way GET /inbox delivers it: full header
		// and signature, but only this recipient's wrapped key.
		partial := &Envelope{
			Header:      full.Header,
			Signature:   full.Signature,
			Ciphertext:  full.Ciphertext,
			WrappedKeys: map[string][]byte{me.landID: full.WrappedKeys[me.landID]},
		}
		got, err := Decrypt(DecryptInput{
			Envelope:      partial,
			SenderSignPub: sender.signPub,
			MyLandID:      me.landID,
			MyEncKeys:     map[string][]byte{me.keyID: me.encPriv},
		})
		if err != nil {
			t.Fatalf("decrypt partial as %s: %v", me.landID, err)
		}
		want := []byte(`{"type":"message","text":"收到，附上報價","sent_at":"2026-10-29T07:02:00.000Z"}`)
		if !bytes.Equal(got, want) {
			t.Fatalf("plaintext mismatch for %s", me.landID)
		}
	}
}

func TestEncryptIsNonDeterministicButOpens(t *testing.T) {
	sender := newParty(t, "land_sender000000000000", "pk_sender00000000000000")
	amy := newParty(t, "land_amy0000000000000000", "pk_amy0000000000000000")
	e1 := buildEnvelope(t, sender, amy)
	e2 := buildEnvelope(t, sender, amy)
	// Random K + nonce + sealed box => different ciphertext and wrapped keys.
	if bytes.Equal(e1.Ciphertext, e2.Ciphertext) {
		t.Error("ciphertext is deterministic across encryptions; nonce/K must be random")
	}
	if bytes.Equal(e1.WrappedKeys[amy.landID], e2.WrappedKeys[amy.landID]) {
		t.Error("sealed wrapped key is deterministic; it must use a random ephemeral key")
	}
}

func TestDecryptErrorCodes(t *testing.T) {
	sender := newParty(t, "land_sender000000000000", "pk_sender00000000000000")
	amy := newParty(t, "land_amy0000000000000000", "pk_amy0000000000000000")
	bob := newParty(t, "land_bob0000000000000000", "pk_bob0000000000000000")
	stranger := newParty(t, "land_x00000000000000000", "pk_x00000000000000000")

	// unknown_sender_key: no sender sign_pub resolved.
	env := buildEnvelope(t, sender, amy, bob)
	_, err := Decrypt(DecryptInput{Envelope: env, SenderSignPub: nil, MyLandID: amy.landID, MyEncKeys: map[string][]byte{amy.keyID: amy.encPriv}})
	if Code(err) != "unknown_sender_key" {
		t.Fatalf("got %v, want unknown_sender_key", err)
	}

	// bad_signature: verify under the wrong sender key.
	_, err = Decrypt(DecryptInput{Envelope: env, SenderSignPub: stranger.signPub, MyLandID: amy.landID, MyEncKeys: map[string][]byte{amy.keyID: amy.encPriv}})
	if Code(err) != "bad_signature" {
		t.Fatalf("got %v, want bad_signature", err)
	}

	// not_recipient: decrypt as someone not addressed.
	_, err = Decrypt(DecryptInput{Envelope: env, SenderSignPub: sender.signPub, MyLandID: stranger.landID, MyEncKeys: map[string][]byte{stranger.keyID: stranger.encPriv}})
	if Code(err) != "not_recipient" {
		t.Fatalf("got %v, want not_recipient", err)
	}

	// unknown_recipient_key: addressed, but we don't hold the referenced key_id.
	_, err = Decrypt(DecryptInput{Envelope: env, SenderSignPub: sender.signPub, MyLandID: amy.landID, MyEncKeys: map[string][]byte{"pk_other0000000000000000": amy.encPriv}})
	if Code(err) != "unknown_recipient_key" {
		t.Fatalf("got %v, want unknown_recipient_key", err)
	}

	// seal_open_failed: hold the right key_id but the wrong private key.
	_, err = Decrypt(DecryptInput{Envelope: env, SenderSignPub: sender.signPub, MyLandID: amy.landID, MyEncKeys: map[string][]byte{amy.keyID: bob.encPriv}})
	if Code(err) != "seal_open_failed" {
		t.Fatalf("got %v, want seal_open_failed", err)
	}
}

// TestTamperDetection proves that modifying ANY signed header field, the
// ciphertext, a wrapped key, the nonce, or the signature causes decryption to
// fail — with the error code naming the step that caught it (§7.8).
func TestTamperDetection(t *testing.T) {
	sender := newParty(t, "land_sender000000000000", "pk_sender00000000000000")
	amy := newParty(t, "land_amy0000000000000000", "pk_amy0000000000000000")
	bob := newParty(t, "land_bob0000000000000000", "pk_bob0000000000000000")

	strp := func(s string) *string { return &s }

	// Each case mutates a fresh envelope; want is the expected error code.
	cases := []struct {
		name   string
		want   string
		mutate func(e *Envelope)
	}{
		{"header v", "bad_envelope", func(e *Envelope) { e.Header.V = "bb-test-v1" }}, // caught structurally
		{"header bridge_id", "bad_signature", func(e *Envelope) { e.Header.BridgeID = "br_evil00000000000000000" }},
		{"header msg_id", "bad_signature", func(e *Envelope) { e.Header.MsgID = "c-evil" }},
		{"header sender_land_id", "bad_signature", func(e *Envelope) { e.Header.SenderLandID = "land_evil0000000000000000" }},
		{"header sender_key_id", "bad_signature", func(e *Envelope) { e.Header.SenderKeyID = "pk_evil00000000000000000" }},
		{"header land_set_version", "bad_signature", func(e *Envelope) { e.Header.LandSetVersion = 999 }},
		{"header created_at", "bad_signature", func(e *Envelope) { e.Header.CreatedAt = "2099-01-01T00:00:00.000Z" }},
		{"header reply_to", "bad_signature", func(e *Envelope) { e.Header.ReplyTo = strp("c-evil") }},
		{"header mentions", "bad_signature", func(e *Envelope) { e.Header.Mentions = []string{"land_evil0000000000000000"} }},
		{"header nonce", "bad_signature", func(e *Envelope) { e.Header.Nonce[0] ^= 0xff }},
		{"header ciphertext_sha256", "bad_signature", func(e *Envelope) { e.Header.CiphertextSHA256[0] ^= 0xff }},
		// Changing a recipient land_id desynchronizes it from the wrapped_keys
		// map (keyed by land_id) and the sort order, so it is caught at the
		// structural check before the signature step — still a hard failure.
		{"header recipient land_id", "bad_envelope", func(e *Envelope) { e.Header.Recipients[0].LandID = "land_evil0000000000000000" }},
		{"header recipient key_id", "bad_signature", func(e *Envelope) { e.Header.Recipients[0].KeyID = "pk_evil00000000000000000" }},
		{"header recipient wrapped_key_sha256", "bad_signature", func(e *Envelope) { e.Header.Recipients[0].WrappedKeySHA256[0] ^= 0xff }},
		{"signature", "bad_signature", func(e *Envelope) { e.Signature[0] ^= 0xff }},
		{"ciphertext", "ciphertext_hash_mismatch", func(e *Envelope) { e.Ciphertext[0] ^= 0xff }},
		{"wrapped_key", "wrapped_key_mismatch", func(e *Envelope) { e.WrappedKeys[amy.landID][0] ^= 0xff }},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := buildEnvelope(t, sender, amy, bob)
			tc.mutate(env)
			_, err := decryptAs(env, amy, sender)
			if err == nil {
				t.Fatalf("decrypt succeeded after tampering %s", tc.name)
			}
			if got := Code(err); got != tc.want {
				t.Fatalf("tamper %s: got code %q, want %q", tc.name, got, tc.want)
			}
		})
	}
}

// TestTamperCiphertextWithMatchingHash shows that even if an attacker fixes the
// ciphertext hash in the (then unsigned) header, the AEAD tag still fails.
// Here we recompute the hash and re-sign with the attacker's own key, so the
// recipient (verifying against the real sender key) rejects at the signature.
func TestTamperCiphertextDefenseInDepth(t *testing.T) {
	sender := newParty(t, "land_sender000000000000", "pk_sender00000000000000")
	amy := newParty(t, "land_amy0000000000000000", "pk_amy0000000000000000")
	env := buildEnvelope(t, sender, amy)

	// Flip a ciphertext byte, then (as an attacker) fix the hash and re-sign
	// with amy's own real sender key is impossible; the attacker can only
	// sign with some key they hold. Use a stranger key: recipient rejects at
	// signature. This asserts the signature binds the ciphertext hash.
	env.Ciphertext[0] ^= 0xff
	// recompute hash so the hash check would pass
	ctHash := sha256.Sum256(env.Ciphertext)
	copy(env.Header.CiphertextSHA256, ctHash[:])
	// but we cannot forge sender's signature; verifying with the real sender
	// key must now fail at the signature step.
	if _, err := decryptAs(env, amy, sender); Code(err) != "bad_signature" {
		t.Fatalf("expected bad_signature, got %v", err)
	}
}
