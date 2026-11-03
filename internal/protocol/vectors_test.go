package protocol

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"io"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"golang.org/x/crypto/nacl/box"
)

// openSeal opens a crypto_box_seal (SealAnonymous) output, recovering the
// sealed message; it is the "verify by opening" check for wrapped keys.
func openSeal(t *testing.T, sealed, pub, priv []byte) []byte {
	t.Helper()
	var p, s [32]byte
	copy(p[:], pub)
	copy(s[:], priv)
	out, ok := box.OpenAnonymous(nil, sealed, &p, &s)
	if !ok {
		t.Fatal("OpenAnonymous failed")
	}
	return out
}

// updateVectors regenerates testdata/vectors/*.json from the deterministic
// definitions below. Run:
//
//	go test ./internal/protocol -run TestVectors -update
//
// The vectors use fixed TEST-ONLY keys, a fixed content key K, a fixed nonce,
// and a deterministic randomness source for the sealed boxes, so regeneration
// is byte-reproducible. ciphertext, sig_input, signature, binding_sig and
// fingerprint are all deterministic and checked for byte-equality; sealed
// wrapped keys are verified by opening (cross-implementation safe).
var updateVectors = flag.Bool("update", false, "regenerate protocol test vectors")

// vectorDir is the repo-root testdata/vectors (DEV_SPEC §4), reached relative
// to this package directory, which is the test's working directory. The same
// canonical location is consumed by tools/interop.py.
const vectorDir = "../../testdata/vectors"

const vectorWarning = "TEST-ONLY KEYS — generated for interop vectors. DO NOT use in production."

// vectorKey is a land's key material in a vector (private keys included on
// purpose: these are throwaway test keys).
type vectorKey struct {
	LandID        string `json:"land_id"`
	KeyID         string `json:"key_id"`
	SignSeedHex   string `json:"sign_seed_hex,omitempty"` // 32B Ed25519 seed (sender only)
	SignPubHex    string `json:"sign_pub_hex"`
	EncPrivHex    string `json:"enc_priv_hex"` // 32B X25519 private
	EncPubHex     string `json:"enc_pub_hex"`
	BindingSigHex string `json:"binding_sig_hex,omitempty"`
	Fingerprint   string `json:"fingerprint"`
}

// vector is one JSON test vector file.
type vector struct {
	Name          string          `json:"name"`
	Warning       string          `json:"warning"`
	Sender        vectorKey       `json:"sender"`
	Recipients    []vectorKey     `json:"recipients"`
	KHex          string          `json:"k_hex"`
	NonceHex      string          `json:"nonce_hex"`
	AAD           string          `json:"aad"`
	PlaintextHex  string          `json:"plaintext_hex"`
	CiphertextHex string          `json:"ciphertext_hex"`
	SigInputHex   string          `json:"sig_input_hex"`
	SignatureHex  string          `json:"signature_hex"`
	Envelope      json.RawMessage `json:"envelope"` // full wire envelope (std base64 fields)
}

// seqReader yields a fixed byte sequence, giving encrypt() a deterministic K,
// nonce, and sealed-box ephemeral keys.
type seqReader struct {
	data []byte
	off  int
}

func (r *seqReader) Read(p []byte) (int, error) {
	if r.off >= len(r.data) {
		return 0, io.EOF
	}
	n := copy(p, r.data[r.off:])
	r.off += n
	return n, nil
}

// vectorSpec is a human-authored definition the generator turns into a vector.
type vectorSpec struct {
	name           string
	bridgeID       string
	msgID          string
	senderLandID   string
	senderKeyID    string
	senderSignSeed []byte
	senderEncPriv  []byte
	landSetVersion int64
	createdAt      string
	mentions       []string
	replyTo        *string
	plaintext      []byte
	k              []byte
	nonce          []byte
	recipients     []specRecipient
}

type specRecipient struct {
	landID  string
	keyID   string
	encPriv []byte
}

func fill(b byte) []byte {
	out := make([]byte, 32)
	for i := range out {
		out[i] = b
	}
	return out
}

func vectorSpecs() []vectorSpec {
	rt := "c-20261029-0000"
	return []vectorSpec{
		{
			name:           "basic-two-recipients",
			bridgeID:       "br_testbridge0000000000",
			msgID:          "c-20261029-0001",
			senderLandID:   "land_sender000000000000",
			senderKeyID:    "pk_sender00000000000000",
			senderSignSeed: fill(0xA1),
			senderEncPriv:  fill(0xA2),
			landSetVersion: 7,
			createdAt:      "2026-10-29T07:02:00.000Z",
			mentions:       []string{"land_amy0000000000000000"},
			replyTo:        nil,
			plaintext:      []byte(`{"type":"message","text":"收到，附上報價","sent_at":"2026-10-29T07:02:00.000Z"}`),
			k:              fill(0x42),
			nonce:          bytes.Repeat([]byte{0x24}, NonceSize),
			recipients: []specRecipient{
				{"land_amy0000000000000000", "pk_amy0000000000000000", fill(0xB1)},
				{"land_bob0000000000000000", "pk_bob0000000000000000", fill(0xB2)},
			},
		},
		{
			name:           "reply-and-mentions",
			bridgeID:       "br_testbridge0000000000",
			msgID:          "c-20261029-0002",
			senderLandID:   "land_sender000000000000",
			senderKeyID:    "pk_sender00000000000000",
			senderSignSeed: fill(0xA1),
			senderEncPriv:  fill(0xA2),
			landSetVersion: 9,
			createdAt:      "2026-10-29T08:15:30.500Z",
			mentions:       []string{"land_bob0000000000000000", "land_amy0000000000000000"},
			replyTo:        &rt,
			plaintext:      []byte(`{"type":"message","text":"reply body","sent_at":"2026-10-29T08:15:30.500Z"}`),
			k:              fill(0x43),
			nonce:          bytes.Repeat([]byte{0x25}, NonceSize),
			recipients: []specRecipient{
				{"land_amy0000000000000000", "pk_amy0000000000000000", fill(0xB1)},
				{"land_bob0000000000000000", "pk_bob0000000000000000", fill(0xB2)},
			},
		},
		{
			name:           "single-recipient",
			bridgeID:       "br_testbridge0000000000",
			msgID:          "c.single_01",
			senderLandID:   "land_sender000000000000",
			senderKeyID:    "pk_sender00000000000000",
			senderSignSeed: fill(0xA1),
			senderEncPriv:  fill(0xA2),
			landSetVersion: 2,
			createdAt:      "2026-10-29T09:00:00.000Z",
			mentions:       nil,
			replyTo:        nil,
			plaintext:      []byte(`{"type":"message","text":"hi","sent_at":"2026-10-29T09:00:00.000Z"}`),
			k:              fill(0x44),
			nonce:          bytes.Repeat([]byte{0x26}, NonceSize),
			recipients: []specRecipient{
				{"land_amy0000000000000000", "pk_amy0000000000000000", fill(0xB1)},
			},
		},
	}
}

// buildVector turns a spec into a fully-populated, deterministic vector.
func buildVector(t *testing.T, s vectorSpec) vector {
	t.Helper()
	signPriv := ed25519.NewKeyFromSeed(s.senderSignSeed)
	signPub := signPriv.Public().(ed25519.PublicKey)
	senderEncPub, err := PublicEncKey(s.senderEncPriv)
	if err != nil {
		t.Fatal(err)
	}
	bindingSig, err := SignBinding(signPriv, senderEncPub, signPub)
	if err != nil {
		t.Fatal(err)
	}

	// Deterministic randomness: K, nonce, then 32 ephemeral bytes per recipient
	// (consumed in sorted land_id order by encrypt).
	sorted := append([]specRecipient(nil), s.recipients...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].landID < sorted[j].landID })
	buf := append([]byte(nil), s.k...)
	buf = append(buf, s.nonce...)
	for i := range sorted {
		buf = append(buf, fill(byte(0xE0+i))...)
	}
	rnd := &seqReader{data: buf}

	in := EncryptInput{
		BridgeID:       s.bridgeID,
		MsgID:          s.msgID,
		SenderLandID:   s.senderLandID,
		SenderKeyID:    s.senderKeyID,
		SenderSignPriv: signPriv,
		LandSetVersion: s.landSetVersion,
		CreatedAt:      s.createdAt,
		Mentions:       s.mentions,
		ReplyTo:        s.replyTo,
		Plaintext:      s.plaintext,
	}
	for _, r := range s.recipients {
		pub, err := PublicEncKey(r.encPriv)
		if err != nil {
			t.Fatal(err)
		}
		in.Recipients = append(in.Recipients, RecipientKey{LandID: r.landID, KeyID: r.keyID, EncPub: pub})
	}

	env, err := encrypt(in, rnd)
	if err != nil {
		t.Fatalf("encrypt vector %s: %v", s.name, err)
	}
	envJSON, err := json.Marshal(env)
	if err != nil {
		t.Fatal(err)
	}

	recVecs := make([]vectorKey, 0, len(s.recipients))
	for _, r := range s.recipients {
		pub, _ := PublicEncKey(r.encPriv)
		recVecs = append(recVecs, vectorKey{
			LandID:      r.landID,
			KeyID:       r.keyID,
			EncPrivHex:  hex.EncodeToString(r.encPriv),
			EncPubHex:   hex.EncodeToString(pub),
			Fingerprint: "", // recipients carry no sign key in these vectors
		})
	}

	return vector{
		Name:    s.name,
		Warning: vectorWarning,
		Sender: vectorKey{
			LandID:        s.senderLandID,
			KeyID:         s.senderKeyID,
			SignSeedHex:   hex.EncodeToString(s.senderSignSeed),
			SignPubHex:    hex.EncodeToString(signPub),
			EncPrivHex:    hex.EncodeToString(s.senderEncPriv),
			EncPubHex:     hex.EncodeToString(senderEncPub),
			BindingSigHex: hex.EncodeToString(bindingSig),
			Fingerprint:   Fingerprint(signPub, senderEncPub),
		},
		Recipients:    recVecs,
		KHex:          hex.EncodeToString(s.k),
		NonceHex:      hex.EncodeToString(s.nonce),
		AAD:           string(aad(s.bridgeID, s.senderLandID, s.msgID)),
		PlaintextHex:  hex.EncodeToString(s.plaintext),
		CiphertextHex: hex.EncodeToString(env.Ciphertext),
		SigInputHex:   hex.EncodeToString(SigInput(&env.Header)),
		SignatureHex:  hex.EncodeToString(env.Signature),
		Envelope:      envJSON,
	}
}

// TestVectors verifies every committed vector; with -update it regenerates
// them first.
func TestVectors(t *testing.T) {
	if *updateVectors {
		if err := os.MkdirAll(vectorDir, 0o755); err != nil {
			t.Fatal(err)
		}
		for _, s := range vectorSpecs() {
			v := buildVector(t, s)
			out, err := json.MarshalIndent(v, "", "  ")
			if err != nil {
				t.Fatal(err)
			}
			out = append(out, '\n')
			path := filepath.Join(vectorDir, s.name+".json")
			if err := os.WriteFile(path, out, 0o644); err != nil {
				t.Fatal(err)
			}
			t.Logf("wrote %s", path)
		}
	}

	paths, err := filepath.Glob(filepath.Join(vectorDir, "*.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) == 0 {
		t.Fatal("no vectors found; run with -update")
	}
	for _, p := range paths {
		t.Run(filepath.Base(p), func(t *testing.T) { verifyVector(t, p) })
	}
}

func mustHex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatalf("bad hex: %v", err)
	}
	return b
}

func verifyVector(t *testing.T, path string) {
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var v vector
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}

	// Rebuild the sender signing key from its seed and re-derive public keys.
	signPriv := ed25519.NewKeyFromSeed(mustHex(t, v.Sender.SignSeedHex))
	signPub := signPriv.Public().(ed25519.PublicKey)
	if got := hex.EncodeToString(signPub); got != v.Sender.SignPubHex {
		t.Fatalf("sender sign_pub mismatch: %s vs %s", got, v.Sender.SignPubHex)
	}
	senderEncPub, err := PublicEncKey(mustHex(t, v.Sender.EncPrivHex))
	if err != nil {
		t.Fatal(err)
	}
	if got := hex.EncodeToString(senderEncPub); got != v.Sender.EncPubHex {
		t.Fatalf("sender enc_pub mismatch")
	}

	// Binding signature and fingerprint are deterministic: assert byte-equality.
	if !VerifyBinding(senderEncPub, signPub, mustHex(t, v.Sender.BindingSigHex)) {
		t.Fatal("binding_sig does not verify")
	}
	if got := Fingerprint(signPub, senderEncPub); got != v.Sender.Fingerprint {
		t.Fatalf("fingerprint mismatch: %q vs %q", got, v.Sender.Fingerprint)
	}

	// Parse the wire envelope.
	var env Envelope
	if err := json.Unmarshal(v.Envelope, &env); err != nil {
		t.Fatalf("envelope parse: %v", err)
	}
	if err := env.Validate(); err != nil {
		t.Fatalf("envelope invalid: %v", err)
	}

	// SigInput golden (exact bytes) and signature.
	if got := hex.EncodeToString(SigInput(&env.Header)); got != v.SigInputHex {
		t.Fatalf("sig_input mismatch:\n got %s\nwant %s", got, v.SigInputHex)
	}
	if got := hex.EncodeToString(env.Signature); got != v.SignatureHex {
		t.Fatalf("signature bytes mismatch")
	}
	if !Verify(signPub, &env.Header, env.Signature) {
		t.Fatal("signature does not verify")
	}

	// Ciphertext is deterministic given K, nonce, plaintext, AAD.
	if got := hex.EncodeToString(env.Ciphertext); got != v.CiphertextHex {
		t.Fatalf("ciphertext bytes mismatch")
	}
	ctHash := sha256.Sum256(env.Ciphertext)
	if !bytes.Equal(ctHash[:], env.Header.CiphertextSHA256) {
		t.Fatal("ciphertext_sha256 mismatch in header")
	}

	// Each recipient opens the sealed key to K and decrypts to the plaintext.
	wantK := mustHex(t, v.KHex)
	wantPlain := mustHex(t, v.PlaintextHex)
	for _, r := range v.Recipients {
		encPriv := mustHex(t, r.EncPrivHex)

		// Sealed wrapped key opens to K (verify-by-opening, not byte-compare).
		pub, _ := PublicEncKey(encPriv)
		gotK := openSeal(t, env.WrappedKeys[r.LandID], pub, encPriv)
		if !bytes.Equal(gotK, wantK) {
			t.Fatalf("recipient %s: unwrapped K mismatch", r.LandID)
		}

		plain, err := Decrypt(DecryptInput{
			Envelope:      &env,
			SenderSignPub: signPub,
			MyLandID:      r.LandID,
			MyEncKeys:     map[string][]byte{r.KeyID: encPriv},
		})
		if err != nil {
			t.Fatalf("recipient %s decrypt: %v", r.LandID, err)
		}
		if !bytes.Equal(plain, wantPlain) {
			t.Fatalf("recipient %s plaintext mismatch", r.LandID)
		}
	}
}
