package cli

import (
	"strings"
	"testing"
)

// t434u: the per-bridge auto-reply preference. `tick auto-reply --bridge … yes|no`
// stores it in tick.json; a message tick surfaces it as one AUTO_REPLY_PREF line
// so the bot knows whether to reply-then-report (yes), ask first (no), or
// reply-then-ask once (unset).
func TestTickAutoReplyPref(t *testing.T) {
	url := startTestServer(t)
	admin := t.TempDir()
	co, e, c := run(t, "claim", "--new", "--server", url, "--home", admin,
		"--name", "Feed", "--handle", "amy-bot", "--owner", "Rick")
	if c != exitOK {
		t.Fatalf("claim: %d %s", c, e)
	}
	abr, _ := findLine(t, co, "claimed")["bridge_id"].(string)
	inv := inviteBlockOf(co)

	member := t.TempDir()
	jo, e, c := runStdin(t, inv, "join", "--home", member, "--invite-stdin", "--handle", "bob-bot", "--owner", "Bob")
	if c != exitOK {
		t.Fatalf("join: %d %s", c, e)
	}
	mbr, _ := findLine(t, jo, "joined")["bridge_id"].(string)
	run(t, "tick", "--home", admin, "--now") // admin learns of the join

	send := func(text string) {
		t.Helper()
		if _, e, c := runStdin(t, text, "send", "--home", member, "--bridge", mbr, "--stdin"); c != exitOK {
			t.Fatalf("send %q: %d %s", text, c, e)
		}
	}

	// Unset by default: exactly one AUTO_REPLY_PREF line, value unset.
	send("請看這週報表：A 123 B 456。")
	tk, e, c := run(t, "tick", "--home", admin, "--now")
	if c != exitOK {
		t.Fatalf("admin tick: %d %s", c, e)
	}
	if !strings.Contains(tk, "AUTO_REPLY_PREF: unset") {
		t.Fatalf("expected AUTO_REPLY_PREF: unset on a message tick:\n%s", tk)
	}
	if n := strings.Count(tk, "AUTO_REPLY_PREF:"); n != 1 {
		t.Fatalf("AUTO_REPLY_PREF printed %d times, want 1:\n%s", n, tk)
	}

	// Owner says yes; the preference is recorded and surfaced on the next message.
	so, e, c := run(t, "tick", "auto-reply", "yes", "--home", admin, "--bridge", abr)
	if c != exitOK {
		t.Fatalf("auto-reply yes: %d %s", c, e)
	}
	if !strings.Contains(so, "AUTO_REPLY_PREF_SET: yes") {
		t.Fatalf("auto-reply set output = %q", so)
	}
	send("更新：C 789。")
	if tk2, _, _ := run(t, "tick", "--home", admin, "--now"); !strings.Contains(tk2, "AUTO_REPLY_PREF: yes") {
		t.Fatalf("expected AUTO_REPLY_PREF: yes after setting:\n%s", tk2)
	}

	// Switch to no; it persists.
	if _, _, c := run(t, "tick", "auto-reply", "no", "--home", admin, "--bridge", abr); c != exitOK {
		t.Fatalf("auto-reply no failed")
	}
	send("再一則。")
	if tk3, _, _ := run(t, "tick", "--home", admin, "--now"); !strings.Contains(tk3, "AUTO_REPLY_PREF: no") {
		t.Fatalf("expected AUTO_REPLY_PREF: no:\n%s", tk3)
	}

	// A bad value is a usage error.
	if _, _, c := run(t, "tick", "auto-reply", "maybe", "--home", admin, "--bridge", abr); c == exitOK {
		t.Fatalf("auto-reply with a bad value should fail")
	}
}
