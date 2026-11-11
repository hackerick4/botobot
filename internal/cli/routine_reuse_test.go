package cli

import (
	"strings"
	"testing"
)

// t352/t353 (corrected): a bot's "bot-bridge inbox" routine prompt is
// bridge-agnostic (plain `tick`, no --bridge) and plain tick scans every bridge
// in BB_HOME, so one enabled routine covers new bridges. BUT the CLI cannot see
// platform routines, so the reuse hint must be CONDITIONAL on the bot confirming
// an enabled, unexpired routine in its own routine list. It must never infer a
// routine exists from tick state: a manual tick or a just-deleted limited
// routine (warm02 乙's folder02 self-deleted at 17:38:17 right after native
// ticks) would be a false positive that silently drops the receive path.
func TestRoutineReuseConditional(t *testing.T) {
	url := startTestServer(t)

	// A bot with another bridge AND a very recent tick (LastRunMS ~= now) must
	// still get the CONDITIONAL confirmation wording, never an unconditional
	// "a routine is live, do not create" claim.
	home := t.TempDir()
	if _, e, c := run(t, "claim", "--new", "--server", url, "--home", home,
		"--name", "A", "--handle", "amy-bot", "--owner", "Rick", "--lang", "en"); c != exitOK {
		t.Fatalf("claim A: %d %s", c, e)
	}
	if _, e, c := run(t, "tick", "--home", home); c != exitOK { // stamps LastRunMS ~= now
		t.Fatalf("tick A: %d %s", c, e)
	}
	out, e, c := run(t, "claim", "--new", "--server", url, "--home", home,
		"--name", "B", "--handle", "amy-bot", "--owner", "Rick", "--lang", "en")
	if c != exitOK {
		t.Fatalf("claim B: %d %s", c, e)
	}
	// Must require explicit confirmation in the platform routine list.
	if !strings.Contains(out, "if you have CONFIRMED an enabled, unexpired \"bot-bridge inbox\" routine") {
		t.Fatalf("en: expected explicit-confirmation reuse wording:\n%s", out)
	}
	if !strings.Contains(out, "Otherwise create it") || !strings.Contains(out, "Do not assume a routine still exists") {
		t.Fatalf("en: expected otherwise-create + do-not-assume clauses:\n%s", out)
	}
	// A --bridge (channel-filtered) routine is NOT reusable as-is.
	if !strings.Contains(out, "A routine whose prompt contains --bridge is channel-filtered and is NOT reusable as-is") {
		t.Fatalf("en: expected the --bridge-not-reusable rule:\n%s", out)
	}
	// Must NOT claim a routine exists from tick state (the removed heuristic).
	for _, bad := range []string{"a recent tick shows", "routine also checks this new bridge", "already runs its"} {
		if strings.Contains(out, bad) {
			t.Fatalf("en: hint must not infer a live routine from tick state (found %q):\n%s", bad, out)
		}
	}

	// Same recent-tick setup in zh: conditional wording, no inferred-live claim.
	homez := t.TempDir()
	if _, e, c := run(t, "claim", "--new", "--server", url, "--home", homez,
		"--name", "A", "--handle", "amy-bot", "--owner", "Rick"); c != exitOK {
		t.Fatalf("claim Az: %d %s", c, e)
	}
	if _, e, c := run(t, "tick", "--home", homez); c != exitOK {
		t.Fatalf("tick Az: %d %s", c, e)
	}
	outz, e, c := run(t, "claim", "--new", "--server", url, "--home", homez,
		"--name", "B", "--handle", "amy-bot", "--owner", "Rick")
	if c != exitOK {
		t.Fatalf("claim Bz: %d %s", c, e)
	}
	if !strings.Contains(outz, "查看你平台的 routine 清單") || !strings.Contains(outz, "若已確認") {
		t.Fatalf("zh: expected explicit-confirmation reuse wording:\n%s", outz)
	}
	if !strings.Contains(outz, "不要因為此 Bot 以前有過 routine 就假設它還在") {
		t.Fatalf("zh: expected do-not-assume clause:\n%s", outz)
	}
	if !strings.Contains(outz, "提示詞含 --bridge 的 routine 是channel過濾的，不能直接沿用") {
		t.Fatalf("zh: expected the --bridge-not-reusable rule:\n%s", outz)
	}
	if strings.Contains(outz, "最近剛跑過 tick") {
		t.Fatalf("zh: hint must not infer a live routine from a recent tick:\n%s", outz)
	}

	// Negative (no recent tick) produces the SAME conditional wording: output
	// does not depend on tick state at all.
	home2 := t.TempDir()
	if _, e, c := run(t, "claim", "--new", "--server", url, "--home", home2,
		"--name", "A2", "--handle", "bob-bot", "--owner", "Rick", "--lang", "en"); c != exitOK {
		t.Fatalf("claim A2: %d %s", c, e)
	}
	out2, e, c := run(t, "claim", "--new", "--server", url, "--home", home2,
		"--name", "B2", "--handle", "bob-bot", "--owner", "Rick", "--lang", "en")
	if c != exitOK {
		t.Fatalf("claim B2: %d %s", c, e)
	}
	if !strings.Contains(out2, "if you have CONFIRMED an enabled, unexpired \"bot-bridge inbox\" routine") {
		t.Fatalf("en no-tick: expected the same conditional wording:\n%s", out2)
	}
}
