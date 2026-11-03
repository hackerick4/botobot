package update

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func newKey(t *testing.T) (ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return pub, priv
}

func b64(b []byte) string { return base64.StdEncoding.EncodeToString(b) }

func mkManifest(t *testing.T, seq uint64, now time.Time, mut func(*Manifest)) []byte {
	t.Helper()
	m := Manifest{Schema: ManifestSchema, Channel: "stable", Version: "v1.2.3", Seq: seq,
		IssuedAt: now.Add(-time.Hour), ExpiresAt: now.Add(30 * 24 * time.Hour),
		Platforms: map[string]Platform{"linux-amd64": {URL: "https://botobot.app/dl/x", SHA256: strings.Repeat("a", 64), Size: 10}}}
	if mut != nil {
		mut(&m)
	}
	b, _ := json.Marshal(m)
	return b
}

func sigFile(privs ...ed25519.PrivateKey) func([]byte) []byte {
	return func(body []byte) []byte {
		sf := SigFile{Schema: SigSchema}
		for _, p := range privs {
			sf.Sigs = append(sf.Sigs, SignManifest(p, body))
		}
		b, _ := json.Marshal(sf)
		return b
	}
}

func TestVerify(t *testing.T) {
	now := time.Now()
	pub, priv := newKey(t)
	evilPub, evilPriv := newKey(t)
	_ = evilPub
	kr := NewKeyring(b64(pub))
	good := mkManifest(t, 5, now, nil)
	sign := sigFile(priv)

	if m, err := Verify(good, sign(good), kr, now, 4); err != nil || m.Seq != 5 {
		t.Fatalf("good manifest: %v", err)
	}
	cases := []struct {
		name string
		body []byte
		sig  []byte
		min  uint64
		want error
	}{
		{"attacker key (MITM / compromised server)", good, sigFile(evilPriv)(good), 0, ErrBadSignature},
		{"no signature", good, []byte(`{"schema":"bb-cli-manifest-sig-v1","sigs":[]}`), 0, ErrBadSignature},
		{"tampered body", append(append([]byte{}, good[:len(good)-1]...), ' ', '}'), sign(good), 0, ErrBadSignature},
		{"replayed old manifest", good, sign(good), 5, ErrNotNewer},
		{"downgrade", good, sign(good), 9, ErrNotNewer},
		{"expired", mkManifest(t, 6, now, func(m *Manifest) { m.ExpiresAt = now.Add(-time.Minute) }), nil, 0, ErrExpired},
		{"future", mkManifest(t, 6, now, func(m *Manifest) { m.IssuedAt = now.Add(48 * time.Hour); m.ExpiresAt = now.Add(72 * time.Hour) }), nil, 0, ErrFuture},
		{"plain http url", mkManifest(t, 6, now, func(m *Manifest) {
			m.Platforms["linux-amd64"] = Platform{URL: "http://evil.example/x", SHA256: strings.Repeat("a", 64), Size: 10}
		}), nil, 0, ErrBadManifest},
		{"bad sha", mkManifest(t, 6, now, func(m *Manifest) {
			m.Platforms["linux-amd64"] = Platform{URL: "https://botobot.app/dl/x", SHA256: "zz", Size: 10}
		}), nil, 0, ErrBadManifest},
		{"next key id mismatch", mkManifest(t, 6, now, func(m *Manifest) { m.NextKeys = []PubKey{{KeyID: "0000000000000000", Pub: b64(pub)}} }), nil, 0, ErrBadManifest},
		{"unknown field", []byte(strings.Replace(string(good), `"schema"`, `"evil":1,"schema"`, 1)), nil, 0, ErrBadManifest},
	}
	for _, c := range cases {
		sig := c.sig
		if sig == nil {
			sig = sign(c.body)
		}
		if _, err := Verify(c.body, sig, kr, now, c.min); !errors.Is(err, c.want) {
			t.Errorf("%s: err = %v, want %v", c.name, err, c.want)
		}
	}
}

func TestKeyRotation(t *testing.T) {
	now := time.Now()
	oldPub, oldPriv := newKey(t)
	newPub, newPriv := newKey(t)
	kr := NewKeyring(b64(oldPub))
	// Manifest signed by the next key is rejected before it is announced.
	m2 := mkManifest(t, 2, now, nil)
	if _, err := Verify(m2, sigFile(newPriv)(m2), kr, now, 0); !errors.Is(err, ErrBadSignature) {
		t.Fatalf("unannounced key accepted: %v", err)
	}
	// Old key announces the new key.
	m1 := mkManifest(t, 1, now, func(m *Manifest) { m.NextKeys = []PubKey{{KeyID: KeyID(newPub), Pub: b64(newPub)}} })
	got, err := Verify(m1, sigFile(oldPriv)(m1), kr, now, 0)
	if err != nil || len(got.NextKeys) != 1 {
		t.Fatalf("announce: %v", err)
	}
	kr[got.NextKeys[0].KeyID], _ = ParsePub(got.NextKeys[0].Pub)
	if _, err := Verify(m2, sigFile(newPriv)(m2), kr, now, 1); err != nil {
		t.Fatalf("rotated key rejected: %v", err)
	}
}

// --- installer ---

const goodScript = "#!/bin/sh\necho ok-new\n"

func setupTarget(t *testing.T) (dir, target string) {
	t.Helper()
	dir = t.TempDir()
	_ = os.Chmod(dir, 0o755)
	target = filepath.Join(dir, "bot-bridge-cli")
	if err := os.WriteFile(target, []byte("#!/bin/sh\necho old\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return dir, target
}

func plat(url string, body []byte) Platform {
	h := sha256.Sum256(body)
	return Platform{URL: url, SHA256: hex.EncodeToString(h[:]), Size: int64(len(body))}
}

func serve(t *testing.T, h http.HandlerFunc) *httptest.Server {
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv
}

func okSelfTest(context.Context, string) error { return nil }

func assertOld(t *testing.T, dir, target string) {
	t.Helper()
	b, _ := os.ReadFile(target)
	if string(b) != "#!/bin/sh\necho old\n" {
		t.Fatalf("target changed: %q", b)
	}
	ents, _ := os.ReadDir(dir)
	for _, e := range ents {
		if strings.HasPrefix(e.Name(), ".bb-cli-") || strings.HasSuffix(e.Name(), ".update-lock") {
			t.Fatalf("leftover temp file %s", e.Name())
		}
	}
}

func TestInstallOK(t *testing.T) {
	dir, target := setupTarget(t)
	body := []byte(goodScript)
	srv := serve(t, func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(body) })
	in := &Installer{Client: srv.Client(), Target: target, SelfTest: okSelfTest}
	if err := in.Install(context.Background(), plat(srv.URL+"/x", body)); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(target); string(b) != goodScript {
		t.Fatalf("not replaced: %q", b)
	}
	if b, _ := os.ReadFile(target + ".bak"); string(b) != "#!/bin/sh\necho old\n" {
		t.Fatalf("backup wrong: %q", b)
	}
	_ = dir
}

func TestInstallRejectsBadDownloads(t *testing.T) {
	body := []byte(goodScript)
	cases := map[string]http.HandlerFunc{
		"different bytes (compromised server)": func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("#!/bin/sh\necho evil\n")) },
		"partial download": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Length", "100")
			_, _ = w.Write(body[:5])
		},
		"oversized": func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write(append(append([]byte{}, body...), make([]byte, 4096)...))
		},
		"404": func(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) },
	}
	for name, h := range cases {
		t.Run(name, func(t *testing.T) {
			dir, target := setupTarget(t)
			srv := serve(t, h)
			in := &Installer{Client: srv.Client(), Target: target, SelfTest: okSelfTest}
			if err := in.Install(context.Background(), plat(srv.URL+"/x", body)); err == nil {
				t.Fatal("bad download installed")
			}
			assertOld(t, dir, target)
		})
	}
}

func TestInstallSelfTestRollback(t *testing.T) {
	body := []byte(goodScript)
	// Candidate fails before install: nothing changes.
	dir, target := setupTarget(t)
	srv := serve(t, func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(body) })
	in := &Installer{Client: srv.Client(), Target: target, SelfTest: func(context.Context, string) error { return errors.New("boom") }}
	if err := in.Install(context.Background(), plat(srv.URL+"/x", body)); !errors.Is(err, ErrSelfTest) {
		t.Fatalf("err = %v", err)
	}
	assertOld(t, dir, target)
	// Candidate passes but the installed binary fails: roll back from .bak.
	dir, target = setupTarget(t)
	calls := 0
	in = &Installer{Client: srv.Client(), Target: target, SelfTest: func(_ context.Context, p string) error {
		calls++
		if p == target {
			return errors.New("installed binary broken")
		}
		return nil
	}}
	if err := in.Install(context.Background(), plat(srv.URL+"/x", body)); !errors.Is(err, ErrSelfTest) || calls != 2 {
		t.Fatalf("err = %v calls=%d", err, calls)
	}
	if b, _ := os.ReadFile(target); string(b) != "#!/bin/sh\necho old\n" {
		t.Fatalf("not rolled back: %q", b)
	}
	_ = dir
}

func TestInstallTargetSafety(t *testing.T) {
	body := []byte(goodScript)
	srv := serve(t, func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(body) })

	// Symlinked install path: the real file is replaced, the link stays a link
	// and keeps pointing at it.
	dir, target := setupTarget(t)
	link := filepath.Join(t.TempDir(), "bb")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	in := &Installer{Client: srv.Client(), Target: link, SelfTest: okSelfTest}
	if err := in.Install(context.Background(), plat(srv.URL+"/x", body)); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Lstat(link); fi.Mode()&os.ModeSymlink == 0 {
		t.Fatal("symlink was replaced instead of its target")
	}
	if b, _ := os.ReadFile(target); string(b) != goodScript {
		t.Fatalf("real target not updated: %q", b)
	}
	_ = dir

	// World-writable directory: another user could race the rename. Refuse.
	dir, target = setupTarget(t)
	_ = os.Chmod(dir, 0o777)
	in = &Installer{Client: srv.Client(), Target: target, SelfTest: okSelfTest}
	if err := in.Install(context.Background(), plat(srv.URL+"/x", body)); !errors.Is(err, ErrUnsafeTarget) {
		t.Fatalf("world-writable dir accepted: %v", err)
	}

	// Pre-planted lock (or symlink at the lock path) blocks instead of being followed.
	dir, target = setupTarget(t)
	victim := filepath.Join(t.TempDir(), "victim")
	_ = os.WriteFile(victim, []byte("keep"), 0o600)
	_ = os.Symlink(victim, target+".update-lock")
	in = &Installer{Client: srv.Client(), Target: target, SelfTest: okSelfTest}
	if err := in.Install(context.Background(), plat(srv.URL+"/x", body)); !errors.Is(err, ErrBusy) {
		t.Fatalf("planted lock: %v", err)
	}
	if b, _ := os.ReadFile(victim); string(b) != "keep" {
		t.Fatal("lock symlink was followed")
	}
	_ = dir
}
