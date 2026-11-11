package cli

import (
	"strings"
	"testing"
)

// join --first-tick folds the first receive check into the join command, so the
// joiner confirms the channel in one bot command instead of two (join, then a
// separate tick). Default join (no flag) must not run a tick.
func TestJoinFirstTick(t *testing.T) {
	url := startTestServer(t)

	invite := func(name, handle string) string {
		out, e, rc := run(t, "claim", "--new", "--server", url, "--home", t.TempDir(),
			"--name", name, "--handle", handle, "--owner", "Amy", "--lang", "en")
		if rc != exitOK {
			t.Fatalf("claim --new %s: %d %s", name, rc, e)
		}
		return inviteBlockOf(out)
	}

	// hasTickLine reports whether out has a standalone NO_NEW_MESSAGES line (the
	// tick result), not the substring that also appears inside the routine prompt.
	hasTickLine := func(out string) bool {
		for _, ln := range strings.Split(out, "\n") {
			if strings.TrimSpace(ln) == "NO_NEW_MESSAGES" {
				return true
			}
		}
		return false
	}

	// Default join does NOT run a receive check.
	plain, e, rc := runStdin(t, invite("Duo1", "amy1-bot"), "join",
		"--home", t.TempDir(), "--invite-stdin", "--handle", "bob1-bot", "--owner", "Bob")
	if rc != exitOK {
		t.Fatalf("plain join: %d %s", rc, e)
	}
	if hasTickLine(plain) {
		t.Fatalf("plain join unexpectedly ran a tick:\n%s", plain)
	}

	// --first-tick joins, then runs exactly one receive check in the same call.
	fout, e, rc := runStdin(t, invite("Duo2", "amy2-bot"), "join",
		"--home", t.TempDir(), "--invite-stdin", "--first-tick", "--handle", "bob2-bot", "--owner", "Bob")
	if rc != exitOK {
		t.Fatalf("first-tick join: %d %s", rc, e)
	}
	ji := strings.Index(fout, `"type":"joined"`)
	if ji < 0 {
		t.Fatalf("first-tick join missing joined line:\n%s", fout)
	}
	if !hasTickLine(fout) {
		t.Fatalf("first-tick join did not run a receive check:\n%s", fout)
	}
	if ji > strings.LastIndex(fout, "NO_NEW_MESSAGES") {
		t.Fatalf("receive check printed before the join result:\n%s", fout)
	}
	// The admin fingerprint was still verified by the join itself.
	if !strings.Contains(fout, `"admin_fingerprint_check":"match"`) {
		t.Fatalf("first-tick join lost the fingerprint check:\n%s", fout)
	}
}
