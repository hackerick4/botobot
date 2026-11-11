package cli

import (
	"strings"
	"testing"
	"time"
)

// follow-after-send: a bot that sends a cold first message (or any reply that
// actually goes out, type=sent) now gets the same NEXT tick --follow hint a
// reply after a shown message gets, so it waits once for the answer. The hint
// is never printed for held, failed, or other no-send outcomes, and follow is
// only receiving — not permission to reply again or loop.

// sendFollowZH / sendFollowEN are stable fragments of the hint so the test does
// not pin the whole sentence.
const (
	sendFollowZH = "tick --follow 等對方回覆。它只負責接收"
	sendFollowEN = "tick --follow once to wait for the other bot's answer. It only receives"
)

// nextFollowLines returns the NEXT lines in out that point at tick --follow.
func nextFollowLines(out string) []string {
	var got []string
	for _, ln := range strings.Split(out, "\n") {
		if strings.HasPrefix(ln, "NEXT:") && strings.Contains(ln, "tick --follow") {
			got = append(got, ln)
		}
	}
	return got
}

// (1) The hint is printed after a successful cold send and after a tick reply.
func TestFollowAfterSendPrintsHint(t *testing.T) {
	p := setupPair(t)

	// A cold first send from the admin: nothing was shown first, yet it sent,
	// so the follow hint is printed.
	out, e, c := runStdin(t, "你好，想跟你對接一下。", "send", "--home", p.homeA, "--bridge", p.bridgeID, "--stdin")
	if c != exitOK || !strings.Contains(out, `"type":"sent"`) {
		t.Fatalf("cold send failed: exit %d out=%s err=%s", c, out, e)
	}
	lines := nextFollowLines(out)
	if len(lines) != 1 || !strings.Contains(lines[0], sendFollowZH) {
		t.Fatalf("cold send did not print exactly one follow hint:\n%s", out)
	}
	// It must say follow is receive-only and still wanted even for one round;
	// it must not tell the bot to skip follow just because this is one round or
	// because no more sends are allowed (follow never sends).
	if !strings.Contains(out, "不代表可以再回或進入循環") || !strings.Contains(out, "NO_NEW_MESSAGES") {
		t.Fatalf("follow hint missing the receive-only wording:\n%s", out)
	}
	if !strings.Contains(out, "就算只回一輪") || !strings.Contains(out, "別 follow") {
		t.Fatalf("follow hint missing the one-round / skip-only-on-explicit-no-follow wording:\n%s", out)
	}
	if strings.Contains(out, "別再送") {
		t.Fatalf("follow hint wrongly ties skipping follow to a no-more-sends limit:\n%s", out)
	}

	// A tick reply that actually sends also prints the hint.
	if _, e, c := runStdin(t, "在嗎？", "send", "--home", p.homeB, "--bridge", p.bridgeID, "--stdin"); c != exitOK {
		t.Fatalf("peer send failed: %s", e)
	}
	if tk, _, _ := run(t, "tick", "--home", p.homeA, "--bridge", p.bridgeID); !strings.Contains(tk, "在嗎？") {
		t.Fatalf("tick did not show the peer message:\n%s", tk)
	}
	rout, e, c := runStdin(t, "在的，說吧。", "tick", "reply", "1", "--home", p.homeA, "--bridge", p.bridgeID, "--stdin")
	if c != exitOK || !strings.Contains(rout, `"type":"sent"`) {
		t.Fatalf("tick reply failed: exit %d out=%s err=%s", c, rout, e)
	}
	if lines := nextFollowLines(rout); len(lines) != 1 || !strings.Contains(lines[0], sendFollowZH) {
		t.Fatalf("tick reply did not print exactly one follow hint:\n%s", rout)
	}
}

// (2) The hint is NOT printed for a held send or a failed send.
func TestFollowAfterSendSkipsNonSends(t *testing.T) {
	p := setupPair(t)

	// Held (DLP medium: owner PII) — no message went out, so no follow hint.
	held, _, c := runStdin(t, "主人的電話是 0912-345-678，地址 台北市大安區復興南路一段100號",
		"send", "--home", p.homeA, "--bridge", p.bridgeID, "--stdin")
	if c != exitOK || !strings.Contains(held, `"type":"held"`) {
		t.Fatalf("PII message not held:\n%s", held)
	}
	if n := nextFollowLines(held); len(n) != 0 {
		t.Fatalf("held send printed a follow hint:\n%s", held)
	}

	// Failed: a bridge the other bot has not joined — trySend errors before any
	// send, so there is no hint (and nothing on stdout).
	url := startTestServer(t)
	solo := t.TempDir()
	cout, cerr, cc := run(t, "create", "--home", solo, "--server", url,
		"--name", "Solo", "--handle", "solo-bot", "--owner", "Rick")
	if cc != exitOK {
		t.Fatalf("create solo bridge: %s", cerr)
	}
	soloBridge, _ := findLine(t, cout, "created")["bridge_id"].(string)
	fout, _, fc := runStdin(t, "有人嗎？", "send", "--home", solo, "--bridge", soloBridge, "--stdin")
	if fc == exitOK {
		t.Fatalf("send with no joined peer unexpectedly succeeded:\n%s", fout)
	}
	if n := nextFollowLines(fout); len(n) != 0 {
		t.Fatalf("failed send printed a follow hint:\n%s", fout)
	}
}

// (3) Simulated time-to-detect (NOT a real measurement): once tick --follow is
// running, the backoff ladder tickFollow uses would notice a message that
// becomes available at T+5s / T+20s / T+90s within the idle window. We advance
// a fake clock by the same followDelay() ladder with no real sleeping, mirroring
// tickFollow's loop (one immediate check, then followDelay(empty) between checks).
func TestFollowAfterSendSimulatedDetectTime(t *testing.T) {
	detect := func(avail time.Duration) (time.Duration, bool) {
		var now time.Duration // fake clock, measured from follow start
		for empty := 0; ; empty++ {
			if now >= avail { // this check sees the message
				return now, true
			}
			d := followDelay(empty)
			if now+d > followDefaultWait { // would give up with NO_NEW_MESSAGES
				return now, false
			}
			now += d
		}
	}
	for _, tc := range []struct {
		avail, maxDetect time.Duration
	}{
		{5 * time.Second, 10 * time.Second},
		{20 * time.Second, 30 * time.Second},
		{90 * time.Second, followDefaultWait},
	} {
		got, ok := detect(tc.avail)
		if !ok || got < tc.avail || got > tc.maxDetect {
			t.Fatalf("message available at T+%v: detected at fake-clock T+%v ok=%v (want within [%v, %v])",
				tc.avail, got, ok, tc.avail, tc.maxDetect)
		}
		t.Logf("SIMULATED GOAL (not a real measurement): message available at T+%v detected at fake-clock T+%v", tc.avail, got)
	}
}
