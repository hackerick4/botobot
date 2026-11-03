package protocol

import (
	"bytes"
	"crypto/ed25519"
	"strings"
	"testing"
)

// goldenSigInput is the exact SigInput (§7.5) for goldenSigHeader, computed
// independently (see tools/interop.py and the vector generator). Any change to
// the SigInput layout must break this test.
const goldenSigInput = "bb-e2e-v1\n" +
	"br_testbridge0000000000\n" +
	"c-20261029-0001\n" +
	"land_sender000000000000\n" +
	"pk_sender00000000000000\n" +
	"7\n" +
	"2026-10-29T07:02:00.000Z\n" +
	"\n" +
	"land_amy0000000000000000,land_bob0000000000000000\n" +
	"AAECAwQFBgcICQoLDA0ODxAREhMUFRYX\n" +
	"AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8=\n" +
	"2\n" +
	"land_amy0000000000000000:pk_amy0000000000000000:ERERERERERERERERERERERERERERERERERERERERERE=\n" +
	"land_bob0000000000000000:pk_bob0000000000000000:IiIiIiIiIiIiIiIiIiIiIiIiIiIiIiIiIiIiIiIiIiI=\n"

func goldenSigHeader() Header {
	return Header{
		V:                ProtocolVersion,
		BridgeID:         "br_testbridge0000000000",
		MsgID:            "c-20261029-0001",
		SenderLandID:     "land_sender000000000000",
		SenderKeyID:      "pk_sender00000000000000",
		LandSetVersion:   7,
		CreatedAt:        "2026-10-29T07:02:00.000Z",
		Mentions:         []string{"land_amy0000000000000000", "land_bob0000000000000000"},
		ReplyTo:          nil,
		Nonce:            mkBytes(0, 24), // 0x00..0x17
		CiphertextSHA256: mkBytes(0, 32), // 0x00..0x1f
		Recipients: []Recipient{
			{LandID: "land_amy0000000000000000", KeyID: "pk_amy0000000000000000", WrappedKeySHA256: bytes.Repeat([]byte{0x11}, 32)},
			{LandID: "land_bob0000000000000000", KeyID: "pk_bob0000000000000000", WrappedKeySHA256: bytes.Repeat([]byte{0x22}, 32)},
		},
	}
}

// mkBytes returns n bytes counting up from start (mod 256).
func mkBytes(start, n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(start + i)
	}
	return b
}

func TestSigInputGolden(t *testing.T) {
	h := goldenSigHeader()
	got := SigInput(&h)
	if string(got) != goldenSigInput {
		t.Fatalf("SigInput mismatch\n got: %q\nwant: %q", got, goldenSigInput)
	}
	// Structural guarantees: terminates with \n, no trailing BOM, no CR.
	if !bytes.HasSuffix(got, []byte{'\n'}) {
		t.Error("SigInput does not end with \\n")
	}
	if bytes.Contains(got, []byte{'\r'}) {
		t.Error("SigInput contains CR")
	}
	if bytes.HasPrefix(got, []byte{0xEF, 0xBB, 0xBF}) {
		t.Error("SigInput has a UTF-8 BOM")
	}
	// 12 fixed lines + 2 recipient lines = 14 newlines.
	if n := bytes.Count(got, []byte{'\n'}); n != 14 {
		t.Errorf("SigInput line count = %d, want 14", n)
	}
}

func TestSigInputReplyToAndEmptyMentions(t *testing.T) {
	h := goldenSigHeader()
	rt := "c-20261029-0000"
	h.ReplyTo = &rt
	h.Mentions = nil
	got := string(SigInput(&h))
	// reply_to line now carries the value; mentions line is empty.
	if !strings.Contains(got, "\n"+rt+"\n\n") {
		t.Errorf("reply_to / empty mentions not rendered as expected:\n%q", got)
	}
}

func TestSignVerifyRoundTrip(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	h := goldenSigHeader()
	sig := Sign(priv, &h)
	if len(sig) != SignatureSize {
		t.Fatalf("signature len = %d, want %d", len(sig), SignatureSize)
	}
	if !Verify(pub, &h, sig) {
		t.Fatal("Verify failed on untampered header")
	}
	// Flip one field: verification must fail (SigInput covers every field).
	h.LandSetVersion = 8
	if Verify(pub, &h, sig) {
		t.Fatal("Verify succeeded after changing land_set_version")
	}
	// Wrong key.
	other, _, _ := ed25519.GenerateKey(nil)
	h.LandSetVersion = 7
	if Verify(other, &h, sig) {
		t.Fatal("Verify succeeded under wrong public key")
	}
	// Malformed inputs.
	if Verify(pub, &h, sig[:10]) {
		t.Fatal("Verify succeeded with truncated signature")
	}
	if Verify(pub[:10], &h, sig) {
		t.Fatal("Verify succeeded with truncated public key")
	}
}

func TestKeyBinding(t *testing.T) {
	signPub, signPriv, _ := ed25519.GenerateKey(nil)
	encPriv := mkBytes(100, 32)
	encPub, err := PublicEncKey(encPriv)
	if err != nil {
		t.Fatal(err)
	}

	// BindingInput is exactly "bb-key-v1\n" || enc_pub || sign_pub.
	bi := BindingInput(encPub, signPub)
	if want := len("bb-key-v1\n") + 64; len(bi) != want {
		t.Fatalf("BindingInput len = %d, want %d", len(bi), want)
	}
	if !bytes.HasPrefix(bi, []byte("bb-key-v1\n")) {
		t.Fatal("BindingInput missing prefix")
	}
	if !bytes.Equal(bi[10:42], encPub) || !bytes.Equal(bi[42:74], signPub) {
		t.Fatal("BindingInput field order wrong")
	}

	sig, err := SignBinding(signPriv, encPub, signPub)
	if err != nil {
		t.Fatal(err)
	}
	if !VerifyBinding(encPub, signPub, sig) {
		t.Fatal("VerifyBinding failed on valid binding")
	}
	// Tamper enc_pub.
	badEnc := append([]byte(nil), encPub...)
	badEnc[0] ^= 0xff
	if VerifyBinding(badEnc, signPub, sig) {
		t.Fatal("VerifyBinding accepted tampered enc_pub")
	}
	// Tamper sign_pub (also breaks the verification key).
	badSign := append([]byte(nil), signPub...)
	badSign[0] ^= 0xff
	if VerifyBinding(encPub, badSign, sig) {
		t.Fatal("VerifyBinding accepted tampered sign_pub")
	}
	// Wrong lengths.
	if VerifyBinding(encPub[:4], signPub, sig) {
		t.Fatal("VerifyBinding accepted short enc_pub")
	}
}

func TestFingerprintGolden(t *testing.T) {
	// Deterministic all-zero keys give a reproducible, independently checkable
	// fingerprint: d = SHA-256(32 zero bytes || 32 zero bytes).
	signPub := make([]byte, 32)
	encPub := make([]byte, 32)
	fp := Fingerprint(signPub, encPub)
	const want = "25873 60152 08055 20003 97610 98233"
	if fp != want {
		t.Fatalf("Fingerprint(zeros) = %q, want %q", fp, want)
	}
	// Shape: six space-separated 5-digit groups.
	groups := strings.Split(fp, " ")
	if len(groups) != 6 {
		t.Fatalf("fingerprint has %d groups, want 6", len(groups))
	}
	for _, g := range groups {
		if len(g) != 5 {
			t.Errorf("group %q is not 5 digits", g)
		}
	}
	// Order matters: swapping sign_pub and enc_pub changes the fingerprint.
	a := mkBytes(1, 32)
	b := mkBytes(200, 32)
	if Fingerprint(a, b) == Fingerprint(b, a) {
		t.Error("Fingerprint is symmetric in its arguments; it must not be")
	}
}
