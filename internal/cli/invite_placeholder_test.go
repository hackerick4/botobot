package cli

import (
	"strings"
	"testing"
)

// warm06 root cause: the claim JSON "invite" field held a placeholder POINTER
// (looking like "INVITE_MESSAGE_BEGIN … INVITE_MESSAGE_END below"); a bot saved
// that 44-byte string and fed it to join, which failed invite_unreadable. The
// real invite (with the one-time code) is only in the INVITE_MESSAGE block on
// stdout and is never in JSON. The fix: make the field obviously a pointer, flag
// it with invite_block_follows, and give join an actionable error for it.
func TestInviteFieldIsPointerNotInvite(t *testing.T) {
	url := startTestServer(t)
	out, e, c := run(t, "claim", "--new", "--server", url, "--home", t.TempDir(),
		"--name", "X", "--handle", "amy-bot", "--owner", "Rick")
	if c != exitOK {
		t.Fatalf("claim: %d %s", c, e)
	}
	m := findLine(t, out, "claimed")
	// "invite" is the pointer, not an invite; a machine reader cannot mistake it.
	if m["invite"] != inviteFieldPointer {
		t.Fatalf("invite field = %v, want the pointer %q", m["invite"], inviteFieldPointer)
	}
	if bf, _ := m["invite_block_follows"].(bool); !bf {
		t.Fatalf("expected invite_block_follows:true, got %v", m["invite_block_follows"])
	}
	// The pointer must NOT look like a usable invite (no link / code / markers).
	ptr, _ := m["invite"].(string)
	for _, bad := range []string{"/b/br_", "INVITE_MESSAGE_BEGIN", "邀請碼"} {
		if strings.Contains(ptr, bad) {
			t.Fatalf("invite pointer contains %q (looks like an invite): %q", bad, ptr)
		}
	}
	// The REAL invite block is still printed on stdout (the owner relays it).
	block := inviteBlockOf(out)
	if !strings.Contains(block, "/b/br_") || !strings.Contains(block, "邀請碼") {
		t.Fatalf("real INVITE_MESSAGE block missing from stdout:\n%s", out)
	}
	// Secret never in the JSON line.
	if strings.Contains(firstLine(out), "邀請碼") {
		t.Fatalf("JSON line leaked the invite code:\n%s", firstLine(out))
	}
}

// join given the JSON "invite" pointer (new or pre-fix form) fails with an
// actionable message BEFORE any server call (no bridge is created).
func TestJoinRejectsInvitePlaceholder(t *testing.T) {
	for _, ph := range []string{
		inviteFieldPointer,
		"INVITE_MESSAGE_BEGIN … INVITE_MESSAGE_END below", // pre-fix placeholder
	} {
		home := t.TempDir()
		out, e, c := runStdin(t, ph, "join", "--home", home, "--invite-stdin", "--owner", "Bob")
		if c != exitUsage {
			t.Fatalf("placeholder %q: exit %d (want %d) out=%s err=%s", ph, c, exitUsage, out, e)
		}
		if !strings.Contains(e, "invite_unreadable") {
			t.Fatalf("placeholder %q: want invite_unreadable code, got: %s", ph, e)
		}
		for _, want := range []string{"claim JSON", "INVITE_MESSAGE block", "INVITE_MESSAGE_BEGIN"} {
			if !strings.Contains(e, want) {
				t.Fatalf("placeholder %q: error missing %q: %s", ph, want, e)
			}
		}
	}
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
