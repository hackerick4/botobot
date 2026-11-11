package cli

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// After a successful claim, the full INVITE_MESSAGE block is also written to a
// 0600 file in a secure dir; JSON carries the path (invite_file), never the
// secret; and join --invite-file reads it (same parse as stdin, placeholder
// rejected). This closes the warm06 footgun (a bot can hand the joiner a path
// instead of re-extracting the block by hand).
func TestClaimWritesInviteFile(t *testing.T) {
	url := startTestServer(t)
	home := t.TempDir()
	out, e, c := run(t, "claim", "--new", "--server", url, "--home", home,
		"--name", "X", "--handle", "amy-bot", "--owner", "Rick")
	if c != exitOK {
		t.Fatalf("claim: %d %s", c, e)
	}
	m := findLine(t, out, "claimed")
	path, _ := m["invite_file"].(string)
	if path == "" {
		t.Fatalf("claim JSON has no invite_file:\n%s", out)
	}
	// JSON must carry the path, never the secret invite code.
	if strings.Contains(firstLine(out), "邀請碼") || strings.Contains(firstLine(out), path+"邀請碼") {
		t.Fatalf("JSON line leaked the invite code:\n%s", firstLine(out))
	}
	// stdout has an INVITE_FILE: line pointing at the same path.
	if !strings.Contains(out, "INVITE_FILE: "+path) {
		t.Fatalf("missing INVITE_FILE line for %s:\n%s", path, out)
	}
	// File is 0600.
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat invite file: %v", err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Fatalf("invite file perm = %o, want 600", perm)
	}
	// Default (no BB_INVITE_DIR, no sibling): inside the bridge's own 0700 dir,
	// so it is moved/cleaned up with the bridge.
	bid, _ := m["bridge_id"].(string)
	if filepath.Dir(path) != filepath.Join(home, bid) || filepath.Base(path) != "invite-full.txt" {
		t.Fatalf("unexpected default invite path %q (want %s/invite-full.txt)", path, filepath.Join(home, bid))
	}
	// File contents equal the stdout INVITE_MESSAGE block.
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read invite file: %v", err)
	}
	block := inviteBlockOf(out) // the body between the markers, as other tests use
	fileBody := strings.TrimSpace(string(data))
	if !strings.Contains(fileBody, "INVITE_MESSAGE_BEGIN") || !strings.Contains(fileBody, "INVITE_MESSAGE_END") ||
		!strings.Contains(fileBody, "/b/br_") || !strings.Contains(fileBody, block) {
		t.Fatalf("invite file does not equal the stdout block.\nfile:\n%s\nblock:\n%s", fileBody, block)
	}

	// join --invite-file reads the file and succeeds (fresh joiner home).
	jout, e, c := run(t, "join", "--home", t.TempDir(), "--invite-file", path, "--handle", "bob-bot", "--owner", "Bob")
	if c != exitOK || !strings.Contains(jout, `"type":"joined"`) {
		t.Fatalf("join --invite-file: %d %s\n%s", c, e, jout)
	}
}

// BB_INVITE_DIR override is honored; a placeholder file is rejected by
// join --invite-file with the actionable error.
func TestClaimInviteFileOverrideAndPlaceholderFile(t *testing.T) {
	url := startTestServer(t)
	secure := t.TempDir()
	t.Setenv("BB_INVITE_DIR", secure)
	out, e, c := run(t, "claim", "--new", "--server", url, "--home", t.TempDir(),
		"--name", "Y", "--handle", "amy-bot", "--owner", "Rick")
	if c != exitOK {
		t.Fatalf("claim: %d %s", c, e)
	}
	path, _ := findLine(t, out, "claimed")["invite_file"].(string)
	if filepath.Dir(path) != secure {
		t.Fatalf("invite_file %q not under BB_INVITE_DIR %q", path, secure)
	}

	// A file holding the JSON placeholder is rejected by join --invite-file.
	ph := filepath.Join(t.TempDir(), "bad.txt")
	if err := os.WriteFile(ph, []byte(inviteFieldPointer+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, e, c = run(t, "join", "--home", t.TempDir(), "--invite-file", ph, "--owner", "Bob")
	if c != exitUsage || !strings.Contains(e, "invite_unreadable") || !strings.Contains(e, "INVITE_MESSAGE block") {
		t.Fatalf("placeholder file not rejected actionably: %d %s", c, e)
	}
}

func TestClaimInviteFileRefusesSymlinkTarget(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, "br_test")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(dir, "invite-full.txt")
	elsewhere := filepath.Join(t.TempDir(), "elsewhere")
	if err := os.WriteFile(elsewhere, []byte("nope"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(elsewhere, target); err != nil {
		t.Fatal(err)
	}
	_, err := writeInviteFile(home, "br_test", "INVITE_MESSAGE_BEGIN\nx\nINVITE_MESSAGE_END")
	if err == nil || !errors.Is(err, errSymlinkInviteTarget) {
		t.Fatalf("want errSymlinkInviteTarget, got %v", err)
	}
}
