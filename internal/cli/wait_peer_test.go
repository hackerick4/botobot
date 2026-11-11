package cli

import (
	"strings"
	"testing"
)

// S3 wait_peer (Rick t378u): on an empty tick, an admin of an open two-bot
// bridge with no peer yet prints one PROGRESS: wait_peer status. It must never
// fire once a peer is present, for a member, or for a closed bridge.
func TestWaitPeerOnTick(t *testing.T) {
	url := startTestServer(t)
	admin := t.TempDir()
	out, e, c := run(t, "claim", "--new", "--server", url, "--home", admin,
		"--name", "W", "--handle", "amy-bot", "--owner", "Rick")
	if c != exitOK {
		t.Fatalf("claim: %d %s", c, e)
	}
	inv := inviteBlockOf(out)
	br, _ := findLine(t, out, "claimed")["bridge_id"].(string)

	// (1) no-peer admin tick -> wait_peer present (exactly one marker line).
	tk, e, c := run(t, "tick", "--home", admin, "--now")
	if c != exitOK {
		t.Fatalf("tick: %d %s", c, e)
	}
	if !strings.Contains(tk, "PROGRESS: wait_peer") || !strings.Contains(tk, "通道已建立，等待對方加入。") {
		t.Fatalf("no-peer admin tick missing wait_peer:\n%s", tk)
	}
	if n := strings.Count(tk, "PROGRESS: wait_peer"); n != 1 {
		t.Fatalf("wait_peer printed %d times, want 1:\n%s", n, tk)
	}

	// (3) member role -> absent.
	member := t.TempDir()
	if _, e, c := runStdin(t, inv, "join", "--home", member, "--invite-stdin", "--handle", "bob-bot", "--owner", "Bob"); c != exitOK {
		t.Fatalf("join: %d %s", c, e)
	}
	if mtk, _, _ := run(t, "tick", "--home", member, "--now"); strings.Contains(mtk, "PROGRESS: wait_peer") {
		t.Fatalf("member tick wrongly printed wait_peer:\n%s", mtk)
	}

	// (2) after the peer joined -> absent, both on the join-event tick and the
	// following empty tick (the roster cache now holds the peer).
	if a1, _, _ := run(t, "tick", "--home", admin, "--now"); strings.Contains(a1, "PROGRESS: wait_peer") {
		t.Fatalf("admin join-event tick wrongly printed wait_peer:\n%s", a1)
	}
	if a2, _, _ := run(t, "tick", "--home", admin, "--now"); strings.Contains(a2, "PROGRESS: wait_peer") {
		t.Fatalf("admin tick after peer joined wrongly printed wait_peer:\n%s", a2)
	}

	// (4) closed -> absent.
	co, e, c := run(t, "close", "--home", admin, "--bridge", br, "--yes", "--owner-picked")
	if c != exitOK {
		t.Fatalf("close: %d %s\n%s", c, e, co)
	}
	if _, e, c := approveHeld(t, admin, co); c != exitOK {
		t.Fatalf("release close: %d %s", c, e)
	}
	if ctk, _, _ := run(t, "tick", "--home", admin, "--now"); strings.Contains(ctk, "PROGRESS: wait_peer") {
		t.Fatalf("closed-bridge tick wrongly printed wait_peer:\n%s", ctk)
	}
}

// warm08 (corrected): one bot ticking several bridges in one run must label
// which channel a wait_peer belongs to. With one bridge still waiting and
// another already joined, the wait_peer line names the waiting channel and the
// joined one prints no wait_peer — so an orphan's "waiting" is never misread as
// the channel that just joined.
func TestWaitPeerLabelsChannelMultiBridge(t *testing.T) {
	url := startTestServer(t)
	admin := t.TempDir()

	// Bridge 1 "Warm06": claimed, peer never joins (the orphan).
	o1, e, c := run(t, "claim", "--new", "--server", url, "--home", admin,
		"--name", "Warm06", "--handle", "amy-bot", "--owner", "Rick")
	if c != exitOK {
		t.Fatalf("claim1: %d %s", c, e)
	}
	br1, _ := findLine(t, o1, "claimed")["bridge_id"].(string)

	// Bridge 2 "Warm07": same bot (same home), peer joins.
	o2, e, c := run(t, "claim", "--new", "--server", url, "--home", admin,
		"--name", "Warm07", "--owner", "Rick")
	if c != exitOK {
		t.Fatalf("claim2: %d %s", c, e)
	}
	inv2 := inviteBlockOf(o2)
	member := t.TempDir()
	if _, e, c := runStdin(t, inv2, "join", "--home", member, "--invite-stdin", "--handle", "bob-bot", "--owner", "Bob"); c != exitOK {
		t.Fatalf("join: %d %s", c, e)
	}
	run(t, "tick", "--home", admin, "--now") // drain Warm07's land_joined event

	// One tick over all bridges: Warm06 waits (labeled), Warm07 does not.
	tk, _, _ := run(t, "tick", "--home", admin, "--now")
	if n := strings.Count(tk, "PROGRESS: wait_peer"); n != 1 {
		t.Fatalf("wait_peer printed %d times, want 1 (only the orphan):\n%s", n, tk)
	}
	if !strings.Contains(tk, "PROGRESS: wait_peer "+br1) {
		t.Fatalf("wait_peer marker missing the orphan bridge id %s:\n%s", br1, tk)
	}
	if !strings.Contains(tk, "Warm06：通道已建立，等待對方加入。") {
		t.Fatalf("wait_peer line not labeled with the waiting channel:\n%s", tk)
	}
	if strings.Contains(tk, "Warm07：通道已建立") {
		t.Fatalf("the already-joined channel wrongly printed wait_peer:\n%s", tk)
	}
}
