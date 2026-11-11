package cli

import (
	"strings"
	"testing"
)

// t378u: when seconds cannot shrink, show clear progress + one primary CTA on the
// surfaces that exist (CLI stdout the bot relays). These assert the progress
// markers and CTA lines, and that host-LLM/network waits are NEVER labelled as
// encryption (加密), and that failures carry no success CTA.

func TestClaimProgressAndInviteCTA(t *testing.T) {
	url := startTestServer(t)
	out, e, c := run(t, "claim", "--new", "--server", url, "--home", t.TempDir(),
		"--name", "X", "--handle", "amy-bot", "--owner", "Rick")
	if c != exitOK {
		t.Fatalf("claim: %d %s", c, e)
	}
	// S1 creating marker + human line go to STDERR (so stdout's first line stays
	// the JSON result). stdout must NOT carry the pre-result PROGRESS markers.
	if !strings.Contains(e, "PROGRESS: creating") || !strings.Contains(e, "正在建立通道…") {
		t.Fatalf("PROGRESS: creating must be on stderr:\nstderr=%s", e)
	}
	if strings.Contains(out, "PROGRESS: creating") {
		t.Fatalf("pre-result PROGRESS leaked to stdout:\n%s", out)
	}
	// Compat: the FIRST stdout line is the JSON result (not a PROGRESS line).
	if fl := firstLine(out); !strings.HasPrefix(fl, "{") || !strings.Contains(fl, `"type":"claimed"`) {
		t.Fatalf("first stdout line is not the claimed JSON: %q", fl)
	}
	// S2 invite_ready: INVITE_FILE still present + one CTA line to pass the invite.
	if !strings.Contains(out, "INVITE_FILE: ") {
		t.Fatalf("invite_ready path lost INVITE_FILE:\n%s", out)
	}
	if !strings.Contains(out, "PROGRESS: invite_ready") || !strings.Contains(out, "下一步：把上面的 INVITE 區塊、或 INVITE_FILE 的路徑，私下傳給對方。") {
		t.Fatalf("missing S2 invite-pass CTA:\n%s", out)
	}
	// Never label the wait as encryption.
	if strings.Contains(out, "加密中") {
		t.Fatalf("claim wait wrongly labelled 加密中:\n%s", out)
	}
}

func TestJoinProgressAndReceiveCTA(t *testing.T) {
	url := startTestServer(t)
	// creator
	cout, e, c := run(t, "claim", "--new", "--server", url, "--home", t.TempDir(),
		"--name", "Duo", "--handle", "amy-bot", "--owner", "Amy")
	if c != exitOK {
		t.Fatalf("claim: %d %s", c, e)
	}
	inv := inviteBlockOf(cout)

	// First bridge (prior==0, no entry) -> S5 "enable receiving".
	out, e, c := runStdin(t, inv, "join", "--home", t.TempDir(), "--invite-stdin", "--handle", "bob-bot", "--owner", "Bob")
	if c != exitOK {
		t.Fatalf("join: %d %s", c, e)
	}
	// PROGRESS: joining is on STDERR; stdout's first line stays the JSON result.
	if !strings.Contains(e, "PROGRESS: joining") || !strings.Contains(e, "正在加入通道…") {
		t.Fatalf("PROGRESS: joining must be on stderr:\nstderr=%s", e)
	}
	if strings.Contains(out, "PROGRESS: joining") {
		t.Fatalf("pre-result PROGRESS leaked to stdout:\n%s", out)
	}
	if fl := firstLine(out); !strings.HasPrefix(fl, "{") || !strings.Contains(fl, `"type":"joined"`) {
		t.Fatalf("first stdout line is not the joined JSON: %q", fl)
	}
	// Post-result S5 CTA stays on stdout (after the JSON; relayed to the owner).
	if !strings.Contains(out, "PROGRESS: joined_no_recv") || !strings.Contains(out, "已加入。還差一步：啟用收信") {
		t.Fatalf("first-bridge join missing the S5 enable-receiving CTA:\n%s", out)
	}
	if strings.Contains(out, "加密中") {
		t.Fatalf("join wait wrongly labelled 加密中:\n%s", out)
	}

	// Second bridge in the same home (prior>0) -> S6 conditional "if enabled".
	home2 := t.TempDir()
	c1, _, rc := run(t, "claim", "--new", "--server", url, "--home", t.TempDir(), "--name", "D1", "--handle", "amy-bot", "--owner", "Amy")
	if rc != exitOK {
		t.Fatalf("claim d1: %s", c1)
	}
	if _, e, rc := runStdin(t, inviteBlockOf(c1), "join", "--home", home2, "--invite-stdin", "--handle", "carol-bot", "--owner", "Carol"); rc != exitOK {
		t.Fatalf("join first into home2: %s", e)
	}
	c2, _, rc := run(t, "claim", "--new", "--server", url, "--home", t.TempDir(), "--name", "D2", "--handle", "dave-bot", "--owner", "Dave")
	if rc != exitOK {
		t.Fatalf("claim d2: %s", c2)
	}
	out2, e, c := runStdin(t, inviteBlockOf(c2), "join", "--home", home2, "--invite-stdin", "--handle", "carol-bot", "--owner", "Carol")
	if c != exitOK {
		t.Fatalf("join second into home2: %d %s\n%s", c, e, out2)
	}
	if !strings.Contains(out2, "PROGRESS: joined") || !strings.Contains(out2, "已加入。若收信排程已啟用，即可接收") {
		t.Fatalf("second-bridge join missing the S6 conditional CTA:\n%s", out2)
	}
	if strings.Contains(out2, "還差一步：啟用收信") {
		t.Fatalf("reuse path wrongly used the S5 create CTA:\n%s", out2)
	}
}

// Failure path: a mismatch / bad invite carries NO success CTA.
func TestJoinFailureHasNoSuccessCTA(t *testing.T) {
	// invite_unreadable (placeholder) -> error, no joined CTA.
	out, e, c := runStdin(t, inviteFieldPointer, "join", "--home", t.TempDir(), "--invite-stdin", "--owner", "Bob")
	if c == exitOK {
		t.Fatalf("placeholder join unexpectedly succeeded:\n%s%s", out, e)
	}
	for _, bad := range []string{"已加入", "joined.", `"type":"joined"`} {
		if strings.Contains(out, bad) {
			t.Fatalf("failure path printed a success line %q:\n%s", bad, out)
		}
	}
}
