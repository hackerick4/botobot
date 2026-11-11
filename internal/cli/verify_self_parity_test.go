package cli

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"testing"
	"time"

	"github.com/hackerick4/botobot/internal/update"
)

// verify-self has two sources for the signed manifest: fetch it over the network
// (default) or reuse an already-downloaded copy (--manifest/--sig, added in
// b3f1cfe for the cold-install round-trip saving). Both feed the SAME
// update.Verify + SHA-256 comparison, so the file path must never be weaker.
// This runs every tamper/expiry/size scenario through BOTH paths and asserts
// identical outcomes, i.e. the reuse flags add no bypass.
func TestVerifySelfFilePathParity(t *testing.T) {
	f := newUpdFixture(t, "5")
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("BB_HOME", "")
	// BB_UPDATE_URL stays pointed at the fixture server (the network path uses
	// it; the file path never calls manifestURL(), proven by TestVerifySelfManifestReuse).

	// A genuine release binary whose SHA-256 the manifests below commit to.
	bin := []byte("#!/bin/sh\necho 'bot-bridge-cli v9.9.9 seq=9'\n")
	good := filepath.Join(t.TempDir(), "good")
	if err := os.WriteFile(good, bin, 0o755); err != nil {
		t.Fatal(err)
	}
	binSHA := hex.EncodeToString(sha256Sum(bin))

	now := time.Now().UTC()
	base := func() update.Manifest {
		return update.Manifest{
			Schema: update.ManifestSchema, Channel: "stable", Version: "v9.9.9", Seq: 9,
			IssuedAt: now.Add(-time.Minute), ExpiresAt: now.Add(24 * time.Hour),
			Platforms: map[string]update.Platform{platformName(): {
				URL: f.srv.URL + "/dl/bin", SHA256: binSHA, Size: int64(len(bin))}},
		}
	}
	signPair := func(m update.Manifest, signer ed25519.PrivateKey) (body, sig []byte) {
		body, _ = json.Marshal(m)
		sf, _ := json.Marshal(update.SigFile{Schema: update.SigSchema, Sigs: []update.Sig{update.SignManifest(signer, body)}})
		return body, sf
	}

	// Build each scenario's (manifest, sig) bytes.
	validBody, validSig := signPair(base(), f.priv)

	expM := base()
	expM.IssuedAt, expM.ExpiresAt = now.Add(-48*time.Hour), now.Add(-24*time.Hour)
	expBody, expSig := signPair(expM, f.priv)

	futM := base()
	futM.IssuedAt, futM.ExpiresAt = now.Add(72*time.Hour), now.Add(96*time.Hour)
	futBody, futSig := signPair(futM, f.priv)

	oldM := base()
	oldM.Version, oldM.Seq = "v1.0.0", 1 // older but validly signed, SHA still matches
	oldBody, oldSig := signPair(oldM, f.priv)

	// flip a byte in the manifest body; the (unchanged) signature no longer matches
	tamperBody := append([]byte{}, validBody...)
	tamperBody[len(tamperBody)/2] ^= 0x20

	// corrupt the base64 signature value, keeping the JSON structure valid
	var sf update.SigFile
	_ = json.Unmarshal(validSig, &sf)
	tamperSig, _ := json.Marshal(update.SigFile{Schema: update.SigSchema,
		Sigs: []update.Sig{{KeyID: sf.Sigs[0].KeyID, Sig: "AAAA" + sf.Sigs[0].Sig[4:]}}})

	// body from one manifest (v9.9.8), signature over a DIFFERENT manifest (v9.9.9)
	mismBody, _ := signPair(func() update.Manifest { m := base(); m.Version = "v9.9.8"; return m }(), f.priv)
	mismSig := validSig

	// signed by a key the binary does not pin
	_, evil, _ := ed25519.GenerateKey(rand.Reader)
	evilBody, evilSig := signPair(base(), evil)

	oversized := make([]byte, update.MaxManifestBytes+10)
	for i := range oversized {
		oversized[i] = 'a'
	}

	statusRe := regexp.MustCompile(`"status":"(\w+)"`)
	codeRe := regexp.MustCompile(`"code":"(\w+)"`)
	// classify reduces a verify-self run to a single comparable token.
	classify := func(out, errOut string, c int) string {
		if c == exitOK {
			if m := statusRe.FindStringSubmatch(out); m != nil {
				return "ok:" + m[1]
			}
			return "ok:?"
		}
		if m := codeRe.FindStringSubmatch(errOut); m != nil {
			return "err:" + m[1]
		}
		return "err:?"
	}

	mf := filepath.Join(t.TempDir(), "m.json")
	sfp := filepath.Join(t.TempDir(), "m.sig")
	// both runs the same (manifest,sig) bytes through the file path and the
	// network path, returning the classified outcome of each.
	both := func(t *testing.T, body, sig []byte) (fileRes, netRes string) {
		if err := os.WriteFile(mf, body, 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(sfp, sig, 0o644); err != nil {
			t.Fatal(err)
		}
		f.manifest.Store(body)
		f.sig.Store(sig)
		fo, fe, fc := run(t, "verify-self", "--file", good, "--manifest", mf, "--sig", sfp)
		no, ne, nc := run(t, "verify-self", "--file", good)
		return classify(fo, fe, fc), classify(no, ne, nc)
	}

	cases := []struct {
		name       string
		body, sig  []byte
		wantPrefix string // expected classified outcome (same on both paths)
	}{
		{"valid", validBody, validSig, "ok:ok"},
		{"expired", expBody, expSig, "err:verify_bad_manifest"},
		{"future", futBody, futSig, "err:verify_bad_manifest"},
		{"older_seq_valid", oldBody, oldSig, "ok:ok"}, // verify-self does not gate on seq; anti-rollback is the update path's job (TestUpdate*)
		{"tampered_body", tamperBody, validSig, "err:verify_bad_manifest"},
		{"tampered_sig", validBody, tamperSig, "err:verify_bad_manifest"},
		{"mismatched_pair", mismBody, mismSig, "err:verify_bad_manifest"},
		{"wrong_key", evilBody, evilSig, "err:verify_bad_manifest"},
		{"oversized", oversized, validSig, "err:verify_unavailable"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fileRes, netRes := both(t, tc.body, tc.sig)
			if fileRes != netRes {
				t.Fatalf("%s: file path %q != network path %q (file path must match the network path exactly)", tc.name, fileRes, netRes)
			}
			if fileRes != tc.wantPrefix {
				t.Fatalf("%s: got %q, want %q", tc.name, fileRes, tc.wantPrefix)
			}
		})
	}
}
