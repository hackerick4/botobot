package cli

import (
	"strings"
	"testing"
)

// TestHoldLeadComesFirst: for owner-picked removals and invites the exception
// is the first line, before NOTIFY_OWNER; plain message holds have no lead.
func TestHoldLeadComesFirst(t *testing.T) {
	for _, argv0 := range []string{"leave", "close", "invite-code"} {
		it := &heldItem{ID: "h1", Kind: holdKindAdmin, Argv: []string{argv0}, Hash: strings.Repeat("a", 64), Reasons: []string{"x"}, Bridge: "br_x"}
		lines := holdInstructions(it, "bot-bridge-cli")
		if !strings.HasPrefix(lines[0], "給助理，先看：") {
			t.Fatalf("%s: first line %q", argv0, lines[0])
		}
		if !strings.HasPrefix(lines[1], "NOTIFY_OWNER: ") {
			t.Fatalf("%s: second line %q", argv0, lines[1])
		}
		if !strings.Contains(lines[0], "release") {
			t.Fatalf("%s: lead lacks release: %q", argv0, lines[0])
		}
	}
	it := &heldItem{ID: "h2", Kind: holdKindMsg, Hash: strings.Repeat("b", 64), Reasons: []string{"x"}, Bridge: "br_x"}
	if lines := holdInstructions(it, "bot-bridge-cli"); !strings.HasPrefix(lines[0], "NOTIFY_OWNER: ") {
		t.Fatalf("message hold first line %q", lines[0])
	}
}
