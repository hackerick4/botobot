package cli

import (
	"crypto/ed25519"
	"crypto/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hackerick4/botobot/internal/prompts"
	"github.com/hackerick4/botobot/internal/update"
)

// v0.6.3 item 2: verify-self checks a CLI file against the signed manifest
// with the built-in key only.
func TestV063VerifySelf(t *testing.T) {
	f := newUpdFixture(t, "5")
	f.publish(t, "v9.9.9", 9, f.priv)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("BB_HOME", "")

	// The genuine release file.
	good := filepath.Join(t.TempDir(), "good")
	if err := os.WriteFile(good, f.bin.Load().([]byte), 0o755); err != nil {
		t.Fatal(err)
	}
	out, e, c := run(t, "verify-self", "--file", good)
	if c != exitOK || !strings.Contains(out, `"status":"ok"`) || !strings.Contains(out, `"release":"v9.9.9"`) {
		t.Fatalf("good file: %d %s %s", c, out, e)
	}

	// A fake binary (lookalike site, link over a bridge): mismatch, notice, exit != 0.
	fake := filepath.Join(t.TempDir(), "fake")
	_ = os.WriteFile(fake, []byte("#!/bin/sh\necho 'bot-bridge-cli v9.9.9 seq=9'\n# evil\n"), 0o755)
	out, e, c = run(t, "verify-self", "--file", fake)
	if c == exitOK || !strings.Contains(e, "verify_mismatch") || !strings.Contains(out, "NOTIFY_OWNER: 下載的 bot-bridge 程式和官方版本不符，沒有安裝。只從 botobot.app 下載。") {
		t.Fatalf("fake file: %d %s %s", c, out, e)
	}
	out, _, _ = run(t, "verify-self", "--file", fake, "--lang", "en")
	if !strings.Contains(out, "NOTIFY_OWNER: The downloaded bot-bridge program does not match the official one, so it was not installed. Download it only from botobot.app.") {
		t.Fatalf("en notice: %s", out)
	}

	// Running copy older than the release: not_latest + update NEXT, exit 0.
	out, e, c = run(t, "verify-self")
	if c != exitOK || !strings.Contains(out, `"status":"not_latest"`) || !strings.Contains(out, " update`, then verify-self again.") {
		t.Fatalf("self older: %d %s %s", c, out, e)
	}
	// Same seq but different bytes: mismatch.
	releaseSeq = "9"
	if out, e, c = run(t, "verify-self"); c == exitOK || !strings.Contains(e, "verify_mismatch") {
		t.Fatalf("self same seq: %d %s %s", c, out, e)
	}

	// Tampered manifest (sha of the fake swapped in, old signature kept).
	m := f.manifest.Load().([]byte)
	fb, _ := os.ReadFile(fake)
	f.manifest.Store([]byte(strings.Replace(string(m), hexSHA(f.bin.Load().([]byte)), hexSHA(fb), 1)))
	out, e, c = run(t, "verify-self", "--file", fake)
	if c == exitOK || !strings.Contains(e, "verify_bad_manifest") || !strings.Contains(out, "NOTIFY_OWNER:") {
		t.Fatalf("tampered manifest: %d %s %s", c, out, e)
	}

	// Manifest signed by another key (fake site with its own key).
	_, other, _ := ed25519.GenerateKey(rand.Reader)
	f.publish(t, "v9.9.9", 9, other)
	if _, e, c = run(t, "verify-self", "--file", fake); c == exitOK || !strings.Contains(e, "verify_bad_manifest") {
		t.Fatalf("other key: %d %s", c, e)
	}

	// Unreachable manifest: stop, never ok.
	t.Setenv("BB_UPDATE_URL", "http://127.0.0.1:1/dl/manifest.json")
	if _, e, c = run(t, "verify-self", "--file", good); c == exitOK || !strings.Contains(e, "verify_unavailable") {
		t.Fatalf("offline: %d %s", c, e)
	}

	// No bot folder is ever created.
	if _, err := os.Stat(filepath.Join(home, ".bot-bridge")); err == nil {
		t.Fatal("verify-self created ~/.bot-bridge")
	}
	if _, _, c = run(t, "verify-self", "extra"); c == exitOK {
		t.Fatal("extra args accepted")
	}
}

func hexSHA(b []byte) string {
	const h = "0123456789abcdef"
	s := sha256Sum(b)
	out := make([]byte, 0, 64)
	for _, x := range s {
		out = append(out, h[x>>4], h[x&15])
	}
	return string(out)
}

// The key published in the manuals (prompts.ReleasePubKey) is the pinned one.
func TestV063PublishedKeyIsPinned(t *testing.T) {
	kr := update.NewKeyring(releasePubKeys...)
	if _, ok := kr[prompts.ReleaseKeyID]; !ok || len(releasePubKeys) == 0 || releasePubKeys[0] != prompts.ReleasePubKey {
		t.Fatalf("published key %s/%s is not the pinned release key %v", prompts.ReleaseKeyID, prompts.ReleasePubKey, kr.IDs())
	}
}
