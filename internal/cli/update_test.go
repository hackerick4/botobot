package cli

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hackerick4/botobot/internal/update"
)

type updFixture struct {
	srv      *httptest.Server
	priv     ed25519.PrivateKey
	manifest atomic.Value // []byte
	sig      atomic.Value // []byte
	bin      atomic.Value // []byte
	hits     atomic.Int32
	target   string
}

func newUpdFixture(t *testing.T, curSeq string) *updFixture {
	t.Helper()
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	f := &updFixture{priv: priv}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/dl/manifest.json":
			f.hits.Add(1)
			b := f.manifest.Load().([]byte)
			etag := `"` + hex.EncodeToString(sha256Sum(b)[:8]) + `"`
			if r.Header.Get("If-None-Match") == etag {
				w.WriteHeader(http.StatusNotModified)
				return
			}
			w.Header().Set("ETag", etag)
			_, _ = w.Write(b)
		case "/dl/manifest.json.sig":
			_, _ = w.Write(f.sig.Load().([]byte))
		case "/dl/bin":
			_, _ = w.Write(f.bin.Load().([]byte))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(f.srv.Close)

	dir := t.TempDir()
	_ = os.Chmod(dir, 0o755)
	f.target = filepath.Join(dir, "bot-bridge-cli")
	_ = os.WriteFile(f.target, []byte("#!/bin/sh\necho old\n"), 0o755)

	oldKeys, oldSeq, oldExe := releasePubKeys, releaseSeq, exePath
	releasePubKeys = []string{base64.StdEncoding.EncodeToString(pub)}
	releaseSeq = curSeq
	exePath = func() (string, error) { return f.target, nil }
	t.Cleanup(func() { releasePubKeys, releaseSeq, exePath = oldKeys, oldSeq, oldExe })
	t.Setenv("BB_UPDATE_URL", f.srv.URL+"/dl/manifest.json")
	t.Setenv("BB_NO_AUTO_UPDATE", "")
	return f
}

func sha256Sum(b []byte) []byte { h := sha256.Sum256(b); return h[:] }

// publish signs a manifest for a fake binary that self-tests as version/seq.
func (f *updFixture) publish(t *testing.T, version string, seq uint64, signer ed25519.PrivateKey) {
	t.Helper()
	bin := []byte("#!/bin/sh\necho 'bot-bridge-cli " + version + " seq=" + itoa(seq) + "'\n")
	f.bin.Store(bin)
	now := time.Now().UTC()
	m := update.Manifest{Schema: update.ManifestSchema, Channel: "stable", Version: version, Seq: seq,
		IssuedAt: now.Add(-time.Minute), ExpiresAt: now.Add(24 * time.Hour),
		Platforms: map[string]update.Platform{platformName(): {URL: f.srv.URL + "/dl/bin", SHA256: hex.EncodeToString(sha256Sum(bin)), Size: int64(len(bin))}}}
	b, _ := json.Marshal(m)
	sf, _ := json.Marshal(update.SigFile{Schema: update.SigSchema, Sigs: []update.Sig{update.SignManifest(signer, b)}})
	f.manifest.Store(b)
	f.sig.Store(sf)
}

func itoa(n uint64) string    { return strings.TrimSpace(strings.Replace(jsonNum(n), "\"", "", -1)) }
func jsonNum(n uint64) string { b, _ := json.Marshal(n); return string(b) }

func (f *updFixture) installed(t *testing.T) string {
	b, _ := os.ReadFile(f.target)
	return string(b)
}

func TestTickAutoUpdate(t *testing.T) {
	p := setupPair(t)
	drainTicks(t, p)
	f := newUpdFixture(t, "1")
	f.publish(t, "v9.0.0", 2, f.priv)

	// A manual check first must not use up the daily automatic slot.
	if out, e, c := run(t, "update", "--check", "--home", p.homeA); c != exitOK || !strings.Contains(out, `"available"`) {
		t.Fatalf("update --check: exit %d %s %s", c, out, e)
	}
	out, e, c := run(t, "tick", "--home", p.homeA, "--bridge", p.bridgeID)
	if c != exitOK || !strings.Contains(out, "NOTIFY_OWNER: bot-bridge 已自動更新到") || !strings.Contains(out, "v9.0.0") {
		t.Fatalf("update not applied/announced: %s %s", out, e)
	}
	if !strings.Contains(f.installed(t), "v9.0.0 seq=2") {
		t.Fatalf("binary not replaced: %s", f.installed(t))
	}
	// Within 24h: no network, byte-identical empty output.
	before := f.hits.Load()
	out, _, _ = run(t, "tick", "--home", p.homeA, "--bridge", p.bridgeID)
	if out != "NO_NEW_MESSAGES\n" || f.hits.Load() != before {
		t.Fatalf("second tick = %q hits %d->%d", out, before, f.hits.Load())
	}
}

func TestTickNoUpdateIsByteIdentical(t *testing.T) {
	p := setupPair(t)
	drainTicks(t, p)
	f := newUpdFixture(t, "3")
	f.publish(t, "v9.0.0", 3, f.priv) // same seq as running build
	out, _, _ := run(t, "tick", "--home", p.homeA, "--bridge", p.bridgeID)
	if out != "NO_NEW_MESSAGES\n" {
		t.Fatalf("empty tick with no update = %q", out)
	}
	if f.hits.Load() != 1 {
		t.Fatalf("manifest fetched %d times, want 1", f.hits.Load())
	}
}

func TestUpdateRejectsUnsignedAndReplay(t *testing.T) {
	p := setupPair(t)
	drainTicks(t, p)
	f := newUpdFixture(t, "1")
	_, evil, _ := ed25519.GenerateKey(rand.Reader)

	// MITM / compromised server: manifest signed by another key.
	f.publish(t, "v6.6.6", 9, evil)
	_, e, c := run(t, "update", "--home", p.homeA)
	if c == exitOK || !strings.Contains(e, "update_failed") || !strings.Contains(f.installed(t), "echo old") {
		t.Fatalf("unsigned update accepted: exit %d %s", c, e)
	}
	// And the automatic path stays silent and changes nothing.
	if out, _, _ := run(t, "tick", "--home", p.homeA, "--bridge", p.bridgeID); out != "NO_NEW_MESSAGES\n" || !strings.Contains(f.installed(t), "echo old") {
		t.Fatalf("tick with evil manifest: %q", out)
	}

	// Genuine update to seq 3.
	f.publish(t, "v3.0.0", 3, f.priv)
	if o, e, c := run(t, "update", "--home", p.homeA); c != exitOK || !strings.Contains(o, `"status":"updated"`) {
		t.Fatalf("genuine update: %s %s", o, e)
	}
	// Replay of an older genuine manifest (seq 2): ignored, no downgrade.
	f.publish(t, "v2.0.0", 2, f.priv)
	o, e, c := run(t, "update", "--home", p.homeA)
	if c != exitOK || !strings.Contains(o, `"status":"up_to_date"`) || !strings.Contains(f.installed(t), "v3.0.0") {
		t.Fatalf("replayed manifest: %s %s / %s", o, e, f.installed(t))
	}
	// Binary swapped on the server after signing: refused.
	f.publish(t, "v4.0.0", 4, f.priv)
	f.bin.Store([]byte("#!/bin/sh\necho 'bot-bridge-cli v4.0.0 seq=4'; evil\n"))
	if _, e, c := run(t, "update", "--home", p.homeA); c == exitOK || !strings.Contains(f.installed(t), "v3.0.0") {
		t.Fatalf("swapped binary installed: %s", e)
	}
}

func TestUpdateSelfTestMismatchRollsBack(t *testing.T) {
	p := setupPair(t)
	drainTicks(t, p)
	f := newUpdFixture(t, "1")
	f.publish(t, "v5.0.0", 5, f.priv)
	// Signed binary that reports the wrong version: fails the self-test.
	bin := []byte("#!/bin/sh\necho 'bot-bridge-cli v0.0.1 seq=1'\n")
	m := update.Manifest{}
	_ = json.Unmarshal(f.manifest.Load().([]byte), &m)
	m.Platforms[platformName()] = update.Platform{URL: f.srv.URL + "/dl/bin", SHA256: hex.EncodeToString(sha256Sum(bin)), Size: int64(len(bin))}
	b, _ := json.Marshal(m)
	sf, _ := json.Marshal(update.SigFile{Schema: update.SigSchema, Sigs: []update.Sig{update.SignManifest(f.priv, b)}})
	f.manifest.Store(b)
	f.sig.Store(sf)
	f.bin.Store(bin)
	if _, e, c := run(t, "update", "--home", p.homeA); c == exitOK || !strings.Contains(e, "self-test") || !strings.Contains(f.installed(t), "echo old") {
		t.Fatalf("bad binary kept: exit %d %s / %s", c, e, f.installed(t))
	}
}

func TestVersionFlag(t *testing.T) {
	old := releaseSeq
	releaseSeq = "7"
	defer func() { releaseSeq = old }()
	out, _, c := run(t, "--version")
	if c != exitOK || out != "bot-bridge-cli test seq=7\n" {
		t.Fatalf("--version = %q", out)
	}
	out, _, _ = run(t, "version")
	if !strings.Contains(out, `"seq":7`) || !strings.Contains(out, `"update_keys":["117a95e2967bf881"]`) {
		t.Fatalf("version = %s", out)
	}
}

// Development builds (seq 0) never check automatically.
func TestDevBuildNoAutoCheck(t *testing.T) {
	p := setupPair(t)
	drainTicks(t, p)
	f := newUpdFixture(t, "0")
	f.publish(t, "v9.0.0", 2, f.priv)
	run(t, "tick", "--home", p.homeA, "--bridge", p.bridgeID)
	if f.hits.Load() != 0 {
		t.Fatal("dev build checked for updates")
	}
}

// drainTicks consumes setup notices/events (dev build: no update check).
func drainTicks(t *testing.T, p pair) {
	t.Helper()
	for i := 0; i < 2; i++ {
		run(t, "tick", "--home", p.homeA, "--bridge", p.bridgeID)
	}
}
