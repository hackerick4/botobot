package cli

import (
	"strings"
	"testing"
	"time"
)

// ageTickSends shifts every recorded send timestamp back by d.
func ageTickSends(t *testing.T, home, bridgeID string, d time.Duration) {
	t.Helper()
	ks, err := openKeystore(home)
	if err != nil {
		t.Fatal(err)
	}
	ts, err := ks.loadTickState(bridgeID)
	if err != nil {
		t.Fatal(err)
	}
	for i := range ts.HourSends {
		ts.HourSends[i] -= d.Milliseconds()
	}
	if err := ks.saveTickState(bridgeID, ts); err != nil {
		t.Fatal(err)
	}
}

func TestChatLoopSessionLimit(t *testing.T) {
	p := setupPair(t)
	// A "session" is the span since the last tick. Without an intervening tick,
	// the per-session cap (3) applies: the 4th send is refused with a NOTIFY line.
	for i := 1; i <= maxSendsPerSession; i++ {
		if _, errOut, code := run(t, "send", "--home", p.homeA, "--bridge", p.bridgeID, "msg"); code != exitOK {
			t.Fatalf("send %d exit %d, %s", i, code, errOut)
		}
	}
	out, _, code := run(t, "send", "--home", p.homeA, "--bridge", p.bridgeID, "one too many")
	if code != exitOK {
		t.Fatalf("over-limit send exit %d, want 0 (NOTIFY on stdout)", code)
	}
	if !strings.Contains(out, "NOTIFY_OWNER") || !strings.Contains(out, "上限") {
		t.Fatalf("over-limit send should print a NOTIFY_OWNER limit line, got %q", out)
	}
	// A tick must NOT reset the window (P0 A6: re-ticking used to refill it).
	if _, _, code := run(t, "tick", "--home", p.homeA, "--bridge", p.bridgeID); code != exitOK {
		t.Fatalf("tick exit %d", code)
	}
	out, _, _ = run(t, "send", "--home", p.homeA, "--bridge", p.bridgeID, "after tick")
	if !strings.Contains(out, "NOTIFY_OWNER") {
		t.Fatalf("a tick must not reset the send window, got %q", out)
	}
	// Once the sends age out of the 5-minute window, sending is allowed again.
	ageTickSends(t, p.homeA, p.bridgeID, sendSessionWindow+time.Second)
	if _, errOut, code := run(t, "send", "--home", p.homeA, "--bridge", p.bridgeID, "after window"); code != exitOK {
		t.Fatalf("send after window exit %d, %s", code, errOut)
	}
}
