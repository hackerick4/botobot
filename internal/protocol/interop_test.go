package protocol

import (
	"bytes"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

const (
	interopScript  = "../../tools/interop.py"
	interopVecDir  = "../../testdata/vectors"
	senderSignSeed = "a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1"
)

// requirePython skips the test unless python3 with PyNaCl is available.
func requirePython(t *testing.T) string {
	t.Helper()
	py, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 not found; skipping cross-implementation interop test")
	}
	if out, err := exec.Command(py, "-c", "import nacl.bindings").CombinedOutput(); err != nil {
		t.Skipf("PyNaCl not importable; skipping interop test: %s", out)
	}
	return py
}

// TestPythonInteropVerifyVectors runs tools/interop.py over the committed
// vectors: the Python (PyNaCl) implementation must accept every one.
func TestPythonInteropVerifyVectors(t *testing.T) {
	py := requirePython(t)
	cmd := exec.Command(py, interopScript, "verify-vectors", interopVecDir)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("python verify-vectors failed: %v\n%s", err, out)
	}
	t.Logf("python verify-vectors:\n%s", out)
}

// TestPythonInteropDecryptGoEnvelope encrypts an envelope in Go, then has
// Python decrypt it (§17.2 #11 direction: Go -> Python).
func TestPythonInteropDecryptGoEnvelope(t *testing.T) {
	py := requirePython(t)

	sender := newParty(t, "land_sender000000000000", "pk_sender00000000000000")
	amy := newParty(t, "land_amy0000000000000000", "pk_amy0000000000000000")
	bob := newParty(t, "land_bob0000000000000000", "pk_bob0000000000000000")

	plaintext := []byte(`{"type":"message","text":"Go→Python 跨實作","sent_at":"2026-10-29T07:02:00.000Z"}`)
	env, err := Encrypt(EncryptInput{
		BridgeID:       "br_testbridge0000000000",
		MsgID:          "c-interop-0001",
		SenderLandID:   sender.landID,
		SenderKeyID:    sender.keyID,
		SenderSignPriv: sender.signPriv,
		LandSetVersion: 3,
		CreatedAt:      "2026-10-29T07:02:00.000Z",
		Mentions:       []string{amy.landID},
		Plaintext:      plaintext,
		Recipients: []RecipientKey{
			{LandID: amy.landID, KeyID: amy.keyID, EncPub: amy.encPub},
			{LandID: bob.landID, KeyID: bob.keyID, EncPub: bob.encPub},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	envJSON, err := json.Marshal(env)
	if err != nil {
		t.Fatal(err)
	}

	input := map[string]any{
		"envelope":            json.RawMessage(envJSON),
		"my_land_id":          amy.landID,
		"my_key_id":           amy.keyID,
		"my_enc_priv_hex":     hex.EncodeToString(amy.encPriv),
		"sender_sign_pub_hex": hex.EncodeToString(sender.signPub),
	}
	inPath := filepath.Join(t.TempDir(), "decrypt-input.json")
	writeJSON(t, inPath, input)

	out, err := exec.Command(py, interopScript, "decrypt", inPath).CombinedOutput()
	if err != nil {
		t.Fatalf("python decrypt failed: %v\n%s", err, out)
	}
	got, err := hex.DecodeString(string(bytes.TrimSpace(out)))
	if err != nil {
		t.Fatalf("decode python output %q: %v", out, err)
	}
	if !bytes.Equal(got, plaintext) {
		t.Fatalf("python decrypted plaintext mismatch:\n got %q\nwant %q", got, plaintext)
	}
}

// TestPythonInteropEncryptForGo has Python encrypt an envelope that Go then
// decrypts (direction: Python -> Go).
func TestPythonInteropEncryptForGo(t *testing.T) {
	py := requirePython(t)

	// Sender signing key: reuse the vectors' deterministic seed so Go can
	// verify against the derived public key.
	seed, _ := hex.DecodeString(senderSignSeed)
	signPriv := ed25519.NewKeyFromSeed(seed)
	signPub := signPriv.Public().(ed25519.PublicKey)

	amy := newParty(t, "land_amy0000000000000000", "pk_amy0000000000000000")
	bob := newParty(t, "land_bob0000000000000000", "pk_bob0000000000000000")

	plaintext := []byte(`{"type":"message","text":"Python→Go 跨實作","sent_at":"2026-10-29T09:30:00.000Z"}`)
	params := map[string]any{
		"bridge_id":            "br_testbridge0000000000",
		"msg_id":               "c-interop-0002",
		"sender_land_id":       "land_sender000000000000",
		"sender_key_id":        "pk_sender00000000000000",
		"sender_sign_seed_hex": senderSignSeed,
		"land_set_version":     5,
		"created_at":           "2026-10-29T09:30:00.000Z",
		"mentions":             []string{amy.landID},
		"reply_to":             nil,
		"plaintext_hex":        hex.EncodeToString(plaintext),
		"recipients": []map[string]string{
			{"land_id": amy.landID, "key_id": amy.keyID, "enc_pub_hex": hex.EncodeToString(amy.encPub)},
			{"land_id": bob.landID, "key_id": bob.keyID, "enc_pub_hex": hex.EncodeToString(bob.encPub)},
		},
	}
	paramPath := filepath.Join(t.TempDir(), "encrypt-params.json")
	writeJSON(t, paramPath, params)

	out, err := exec.Command(py, interopScript, "encrypt", paramPath).CombinedOutput()
	if err != nil {
		t.Fatalf("python encrypt failed: %v\n%s", err, out)
	}

	var result struct {
		Envelope         Envelope `json:"envelope"`
		SenderSignPubHex string   `json:"sender_sign_pub_hex"`
	}
	if err := json.Unmarshal(out, &result); err != nil {
		t.Fatalf("parse python envelope: %v\n%s", err, out)
	}
	if result.SenderSignPubHex != hex.EncodeToString(signPub) {
		t.Fatalf("python-derived sign_pub differs from Go's")
	}

	// Go decrypts as each recipient.
	for _, me := range []party{amy, bob} {
		got, err := Decrypt(DecryptInput{
			Envelope:      &result.Envelope,
			SenderSignPub: signPub,
			MyLandID:      me.landID,
			MyEncKeys:     map[string][]byte{me.keyID: me.encPriv},
		})
		if err != nil {
			t.Fatalf("Go decrypt of Python envelope as %s: %v", me.landID, err)
		}
		if !bytes.Equal(got, plaintext) {
			t.Fatalf("plaintext mismatch for %s", me.landID)
		}
	}
}

func writeJSON(t *testing.T, path string, v any) {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}
