package cli

import (
	"os"
	"testing"
)

// seedBridge writes a minimal keystore entry for a bridge id so resolveBridge*
// can find it. It does not touch the network.
func seedBridge(t *testing.T, ks *keystore, id string) {
	t.Helper()
	bc := &bridgeConfig{Server: "https://example.test", BridgeID: id, LandID: "land_" + id, Handle: "h", Role: "member"}
	km := &keyMaterial{EncPriv: []byte("x"), SignPriv: []byte("y"), KeyID: "pk_" + id}
	if err := ks.initBridge(bc, km, "bbk_"+id); err != nil {
		t.Fatalf("initBridge(%s): %v", id, err)
	}
}

func TestResolveBridgeStrictRefusesImplicitDefault(t *testing.T) {
	ks, err := openKeystore(t.TempDir())
	if err != nil {
		t.Fatalf("openKeystore: %v", err)
	}
	seedBridge(t, ks, "br_aaaaaaaaaaaaaaaaaaaaaa")
	seedBridge(t, ks, "br_bbbbbbbbbbbbbbbbbbbbbb")
	// Record a default so the non-strict path would have one to fall back on.
	_ = ks.saveConfig(&localConfig{DefaultBridge: "br_aaaaaaaaaaaaaaaaaaaaaa"})

	s := &session{ks: ks, g: &globalFlags{}}

	// Strict: with >1 bridge and no --bridge, refuse even though a default exists.
	if _, e := s.resolveBridgeStrict(); e == nil || e.Exit != exitUsage {
		t.Fatalf("resolveBridgeStrict with 2 bridges: want usage error, got %v", e)
	}
	// Since v0.4.8 resolveBridge is strict too: a stored default is never used
	// when the folder holds more than one bridge.
	if _, e := s.resolveBridge(); e == nil || e.Code != "bridge_required" {
		t.Fatalf("resolveBridge with 2 bridges: want bridge_required, got %v", e)
	}
	// Strict with an explicit --bridge is fine.
	s.g.bridge = "br_bbbbbbbbbbbbbbbbbbbbbb"
	if bs, e := s.resolveBridgeStrict(); e != nil || bs.bc.BridgeID != "br_bbbbbbbbbbbbbbbbbbbbbb" {
		t.Fatalf("resolveBridgeStrict with --bridge: %v / %v", e, bs)
	}
}

func TestResolveBridgeStrictSoleBridgeOK(t *testing.T) {
	ks, err := openKeystore(t.TempDir())
	if err != nil {
		t.Fatalf("openKeystore: %v", err)
	}
	seedBridge(t, ks, "br_only000000000000000000")
	s := &session{ks: ks, g: &globalFlags{}}
	bs, e := s.resolveBridgeStrict()
	if e != nil || bs.bc.BridgeID != "br_only000000000000000000" {
		t.Fatalf("sole bridge should resolve without --bridge: %v", e)
	}
}

func TestEnforcePrivatePermsTightensAndWarns(t *testing.T) {
	ks, err := openKeystore(t.TempDir())
	if err != nil {
		t.Fatalf("openKeystore: %v", err)
	}
	seedBridge(t, ks, "br_perm00000000000000000x")
	// Loosen keys.json, then load it: the loader must tighten and warn.
	path := ks.keysPath("br_perm00000000000000000x")
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	var warned bool
	ks.warn = func(string, ...any) { warned = true }
	if _, err := ks.loadKeys("br_perm00000000000000000x"); err != nil {
		t.Fatalf("loadKeys: %v", err)
	}
	if !warned {
		t.Fatalf("expected a warning for loose keys.json permissions")
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("keys.json mode = %#o, want 0600", got)
	}
}
