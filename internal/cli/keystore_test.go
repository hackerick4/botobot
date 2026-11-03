package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestKeystorePermissions verifies that the home dir and bridge dir are 0700 and
// the private files are 0600 even under a permissive umask (DEV_SPEC §12.1, §16).
func TestKeystorePermissions(t *testing.T) {
	old := syscallUmask(0)
	defer syscallUmask(old)

	home := t.TempDir()
	ks, err := openKeystore(home)
	if err != nil {
		t.Fatalf("openKeystore: %v", err)
	}
	bc := &bridgeConfig{Server: "https://x", BridgeID: "br_test", LandID: "land_1", Handle: "h", Role: "admin"}
	km := &keyMaterial{EncPriv: []byte("enc"), SignPriv: []byte("sign"), KeyID: "pk_1", CreatedAt: 1}
	if err := ks.initBridge(bc, km, "bbk_secret"); err != nil {
		t.Fatalf("initBridge: %v", err)
	}

	assertMode(t, home, 0o700)
	assertMode(t, ks.bridgeDir("br_test"), 0o700)
	assertMode(t, ks.keysPath("br_test"), 0o600)
	assertMode(t, ks.apiKeyPath("br_test"), 0o600)
	assertMode(t, ks.bridgePath("br_test"), 0o600)

	// No temp files should linger after the atomic writes (DEV_SPEC §12.1).
	entries, err := os.ReadDir(ks.bridgeDir("br_test"))
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".") && strings.Contains(e.Name(), ".tmp-") {
			t.Errorf("leftover temp file: %s", e.Name())
		}
	}
}

func assertMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	if got := fi.Mode().Perm(); got != want {
		t.Errorf("%s mode = %o, want %o", path, got, want)
	}
}

// TestWriteFileAtomicOverwrite checks that an atomic write replaces an existing
// file and still enforces 0600 (DEV_SPEC §12.1).
func TestWriteFileAtomicOverwrite(t *testing.T) {
	old := syscallUmask(0)
	defer syscallUmask(old)

	dir := t.TempDir()
	path := filepath.Join(dir, "f")
	if err := writeFileAtomic(path, []byte("one"), 0o600); err != nil {
		t.Fatalf("write 1: %v", err)
	}
	if err := writeFileAtomic(path, []byte("two"), 0o600); err != nil {
		t.Fatalf("write 2: %v", err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(b) != "two" {
		t.Errorf("content = %q, want %q", b, "two")
	}
	assertMode(t, path, 0o600)
}

// TestSeenDedupeAndPrune checks replay dedupe persistence and 24h pruning
// (DEV_SPEC §7.8 step 7).
func TestSeenDedupeAndPrune(t *testing.T) {
	ks, err := openKeystore(t.TempDir())
	if err != nil {
		t.Fatalf("openKeystore: %v", err)
	}
	t0 := time.Date(2026, 10, 29, 7, 0, 0, 0, time.UTC).UnixMilli()

	s, err := ks.loadSeen("br_x", t0)
	if err != nil {
		t.Fatalf("loadSeen: %v", err)
	}
	s.Entries[seenKey("land_a", "m1")] = t0
	if err := ks.saveSeen("br_x", s); err != nil {
		t.Fatalf("saveSeen: %v", err)
	}

	// Reload within the window: entry survives.
	s2, err := ks.loadSeen("br_x", t0+time.Hour.Milliseconds())
	if err != nil {
		t.Fatalf("loadSeen 2: %v", err)
	}
	if _, ok := s2.Entries[seenKey("land_a", "m1")]; !ok {
		t.Fatalf("entry pruned too early")
	}

	// Reload after >24h: entry pruned.
	s3, err := ks.loadSeen("br_x", t0+25*time.Hour.Milliseconds())
	if err != nil {
		t.Fatalf("loadSeen 3: %v", err)
	}
	if _, ok := s3.Entries[seenKey("land_a", "m1")]; ok {
		t.Fatalf("entry not pruned after 24h")
	}
}

// TestEncKeysRetention checks that the current key and in-window retained keys
// are offered for decryption and expired ones are dropped (DEV_SPEC §12.3 rekey).
func TestEncKeysRetention(t *testing.T) {
	now := time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC).UnixMilli()
	km := &keyMaterial{
		EncPriv: []byte("cur"), KeyID: "pk_cur", CreatedAt: now,
		Previous: []retiredKey{
			{EncPriv: []byte("recent"), KeyID: "pk_recent", RetiredAt: now - time.Hour.Milliseconds()},
			{EncPriv: []byte("old"), KeyID: "pk_old", RetiredAt: now - 25*time.Hour.Milliseconds()},
		},
	}
	m := km.encKeysByID(now)
	if _, ok := m["pk_cur"]; !ok {
		t.Errorf("current key missing")
	}
	if _, ok := m["pk_recent"]; !ok {
		t.Errorf("recent retained key missing")
	}
	if _, ok := m["pk_old"]; ok {
		t.Errorf("expired retained key should be excluded")
	}

	km.pruneRetained(now)
	if len(km.Previous) != 1 || km.Previous[0].KeyID != "pk_recent" {
		t.Errorf("pruneRetained = %+v, want only pk_recent", km.Previous)
	}
}

// TestResolveHomePrecedence checks --home > $BB_HOME > default (DEV_SPEC §12.2).
func TestResolveHomePrecedence(t *testing.T) {
	h, err := resolveHome("/flag/home", "/env/home")
	if err != nil || h != "/flag/home" {
		t.Errorf("flag precedence: got %q, %v", h, err)
	}
	h, err = resolveHome("", "/env/home")
	if err != nil || h != "/env/home" {
		t.Errorf("env precedence: got %q, %v", h, err)
	}
}
