package cli

import (
	"strings"
	"testing"
)

// TestMultiMemberLifecycle: one admin and four members on one bridge (three
// joining through successive rotated invites). Leaving is per member, never
// for everyone: the rest keep sending and receiving, the admin hears once,
// the leaver goes quiet. A kick removes only that member. Rejoining needs a
// fresh invite. Only the admin's close ends the bridge, and each remaining
// member is told once (Rick 2026-10-31: 有人退出不代表全退).
func TestMultiMemberLifecycle(t *testing.T) {
	url := startTestServer(t)
	_, _, out, admin := claimFresh(t, url)
	br := findLine(t, out, "claimed")["bridge_id"].(string)
	makeLegacy(t, url, br) // multi-member rules apply only to pre-v0.5.0 bridges
	invites := []string{inviteBlockOf(out)}

	type bot struct{ name, home string }
	members := []bot{{"bob", t.TempDir()}, {"carol", t.TempDir()}, {"dave", t.TempDir()}, {"erin", t.TempDir()}}
	join := func(b bot, invite string) int {
		_, e, rc := runStdin(t, invite, "join", "--home", b.home, "--invite-stdin", "--handle", b.name+"-bot", "--owner", strings.ToUpper(b.name[:1])+b.name[1:])
		if rc != exitOK {
			t.Logf("join %s: exit %d %s", b.name, rc, e)
		}
		return rc
	}
	tick := func(home string) string {
		o, e, rc := run(t, "tick", "--home", home, "--bridge", br)
		if rc != exitOK {
			t.Fatalf("tick exit %d: %s", rc, e)
		}
		return o
	}
	send := func(home, text string) int {
		_, _, rc := runStdin(t, text, "send", "--home", home, "--bridge", br, "--stdin")
		return rc
	}
	rotate := func() string {
		o, e, rc := runApproved(t, "invite-code", "--home", admin, "--bridge", br, "--rotate")
		if rc != exitOK || inviteBlockOf(o) == "" {
			t.Fatalf("rotate exit %d: %s\n%s", rc, e, o)
		}
		return inviteBlockOf(o)
	}
	mustQuiet := func(who, o string) {
		t.Helper()
		if o != "NO_NEW_MESSAGES\n" {
			t.Errorf("%s: want exact NO_NEW_MESSAGES, got:\n%s", who, o)
		}
	}
	notifies := func(o string) int { return strings.Count("\n"+o, "\nNOTIFY_OWNER: ") }
	got := func(o, text string) bool { return strings.Contains(o, "│ "+text) }

	for i, m := range members {
		inv := invites[0]
		if i > 0 {
			inv = rotate()
			invites = append(invites, inv)
		}
		if rc := join(m, inv); rc != exitOK {
			t.Fatalf("%s join exit %d", m.name, rc)
		}
	}
	// Used invites never work again. (Two tries: five failed codes from one
	// address trigger the server's anti-guessing backoff, by design.)
	for i, inv := range []string{invites[0], invites[2]} {
		if rc := join(bot{"frank", t.TempDir()}, inv); rc == exitOK {
			t.Fatalf("used invite %d joined again", i)
		}
	}
	ao := tick(admin)
	if n := strings.Count(ao, "已加入"); n != 4 {
		t.Fatalf("admin join notices = %d:\n%s", n, ao)
	}
	for _, m := range members {
		if o := tick(m.home); notifies(o) > 0 {
			t.Errorf("%s got an owner notice meant for the admin:\n%s", m.name, o)
		}
	}
	mustQuiet("admin", tick(admin))

	// Group messages reach every other party.
	if send(admin, "g1 admin") != exitOK || send(members[3].home, "g2 erin") != exitOK {
		t.Fatal("group send failed")
	}
	if o := tick(admin); !got(o, "g2 erin") || got(o, "g1 admin") {
		t.Errorf("admin inbox:\n%s", o)
	}
	for i, m := range members {
		o := tick(m.home)
		if !got(o, "g1 admin") || (i != 3) != got(o, "g2 erin") {
			t.Errorf("%s inbox:\n%s", m.name, o)
		}
	}

	// bob leaves: the bridge stays open for everyone else.
	bob, carol, dave, erin := members[0], members[1], members[2], members[3]
	if _, e, rc := runApproved(t, "leave", "--home", bob.home, "--bridge", br); rc != exitOK {
		t.Fatalf("bob leave exit %d: %s", rc, e)
	}
	if send(bob.home, "bob after leave") == exitOK {
		t.Error("bob could still send after leaving")
	}
	mustQuiet("bob after leave", tick(bob.home))
	mustQuiet("bob after leave (again)", tick(bob.home))
	if send(carol.home, "g3 carol") != exitOK || send(admin, "g4 admin") != exitOK {
		t.Fatal("send after bob left failed")
	}
	ao = tick(admin)
	if rawLine(ao, "NOTIFY_OWNER: ") != "bob-bot（Bob）已離開「研究小組」。" || strings.Count(ao, "已離開") != 1 || !got(ao, "g3 carol") {
		t.Errorf("admin after bob left:\n%s", ao)
	}
	for _, m := range []bot{carol, dave, erin} {
		o := tick(m.home)
		if notifies(o) > 0 || !got(o, "g4 admin") || (m.name != "carol") != got(o, "g3 carol") {
			t.Errorf("%s after bob left:\n%s", m.name, o)
		}
	}
	mustQuiet("admin", tick(admin))
	if st, _, _ := run(t, "status", "--home", carol.home, "--bridge", br); !strings.Contains(st, `"status":"open"`) || strings.Contains(st, "bob-bot") {
		t.Errorf("carol status after bob left:\n%s", st)
	}

	// carol leaves too.
	if _, e, rc := runApproved(t, "leave", "--home", carol.home, "--bridge", br); rc != exitOK {
		t.Fatalf("carol leave exit %d: %s", rc, e)
	}
	if send(dave.home, "g5 dave") != exitOK {
		t.Fatal("dave send failed")
	}
	if ao = tick(admin); strings.Count(ao, "已離開") != 1 || !strings.Contains(ao, "carol-bot（Carol）已離開") || !got(ao, "g5 dave") {
		t.Errorf("admin after carol left:\n%s", ao)
	}
	if o := tick(erin.home); !got(o, "g5 dave") || notifies(o) > 0 {
		t.Errorf("erin after carol left:\n%s", o)
	}

	// The admin removes dave: only dave is affected, and dave is told once.
	if _, e, rc := runApproved(t, "kick", "--home", admin, "--bridge", br, "dave-bot"); rc != exitOK {
		t.Fatalf("kick exit %d: %s", rc, e)
	}
	if send(dave.home, "dave after kick") == exitOK {
		t.Error("dave could still send after the kick")
	}
	if o := tick(dave.home); notifies(o) != 1 || !strings.Contains(o, "routine") {
		t.Errorf("dave first tick after kick:\n%s", o)
	}
	mustQuiet("dave after kick (again)", tick(dave.home))
	if send(erin.home, "g6 erin") != exitOK {
		t.Fatal("erin send after kick failed")
	}
	if ao = tick(admin); !got(ao, "g6 erin") || strings.Contains(ao, "dave-bot（Dave）已離開") {
		t.Errorf("admin after kick (no leave notice for a kick):\n%s", ao)
	}
	if lo, _, _ := run(t, "lands", "--home", admin, "--bridge", br); strings.Count(lo, `"type":"land"`) != 2 {
		t.Errorf("lands after two leaves and a kick:\n%s", lo)
	}

	// bob rejoins: the old invite fails, a fresh one works.
	if _, e, rc := run(t, "forget", "--home", bob.home, "--bridge", br, "--yes"); rc != exitOK {
		t.Fatalf("bob forget exit %d: %s", rc, e)
	}
	if rc := join(bob, invites[0]); rc == exitOK {
		t.Fatal("bob rejoined with the old invite")
	}
	if rc := join(bob, rotate()); rc != exitOK {
		t.Fatalf("bob rejoin exit %d", rc)
	}
	if ao = tick(admin); strings.Count(ao, "bob-bot（Bob）已加入") != 1 {
		t.Errorf("admin rejoin notice:\n%s", ao)
	}
	if send(bob.home, "g7 bob back") != exitOK {
		t.Fatal("bob send after rejoin failed")
	}
	if o := tick(erin.home); !got(o, "g7 bob back") {
		t.Errorf("erin did not get bob's message after rejoin:\n%s", o)
	}
	mustQuiet("erin", tick(erin.home))

	// Only the admin's close ends it for everyone; each member hears once.
	if _, e, rc := runApproved(t, "close", "--home", admin, "--bridge", br, "--yes"); rc != exitOK {
		t.Fatalf("close exit %d: %s", rc, e)
	}
	mustQuiet("admin after own close", tick(admin))
	for _, m := range []bot{bob, erin} {
		o := tick(m.home)
		if notifies(o) != 1 || !strings.Contains(o, "已由管理者關閉") || !strings.Contains(o, "「研究小組」") {
			t.Errorf("%s first tick after close:\n%s", m.name, o)
		}
		mustQuiet(m.name+" after close (again)", tick(m.home))
	}
	mustQuiet("carol (left earlier) after close", tick(carol.home))
	mustQuiet("dave (kicked earlier) after close", tick(dave.home))
	if send(erin.home, "after close") == exitOK {
		t.Error("erin could send after close")
	}
}

// TestEnglishMembershipNotices: an English bridge tells its owners about
// joins, leaves and the close in English, once each.
func TestEnglishMembershipNotices(t *testing.T) {
	url := startTestServer(t)
	admin, member := t.TempDir(), t.TempDir()
	out, e, rc := run(t, "claim", "--new", "--server", url, "--home", admin, "--name", "Team", "--handle", "amy-bot", "--owner", "Amy", "--lang", "en")
	if rc != exitOK {
		t.Fatalf("claim exit %d: %s", rc, e)
	}
	br := findLine(t, out, "claimed")["bridge_id"].(string)
	jo, e, rc := runStdin(t, inviteBlockOf(out), "join", "--home", member, "--invite-stdin", "--handle", "bob-bot", "--owner", "Bob")
	if rc != exitOK {
		t.Fatalf("join exit %d: %s", rc, e)
	}
	if n := rawLine(jo, "NOTIFY_OWNER: "); n != "Joined \"Team\"." || !strings.Contains(jo, "NOTIFY_OWNER: "+ownerCautionEN) {
		t.Errorf("member join notice = %q", n)
	}
	ao, _, _ := run(t, "tick", "--home", admin, "--bridge", br)
	if n := rawLine(ao, "NOTIFY_OWNER: "); n != `bob-bot (Bob) joined "Team". If unknown, ask to close it.` {
		t.Errorf("join notice = %q", n)
	}
	if _, e, rc := runApproved(t, "close", "--home", admin, "--bridge", br, "--yes"); rc != exitOK {
		t.Fatalf("close exit %d: %s", rc, e)
	}
	mo, _, _ := run(t, "tick", "--home", member, "--bridge", br)
	if n := rawLine(mo, "NOTIFY_OWNER: "); n != `"Team" was closed by the admin. Its data was deleted automatically.` {
		t.Errorf("close notice = %q", n)
	}
	if n := rawLine(mo, "NEXT: "); n != "delete the routine whose prompt has tick --bridge "+br+" (not pause). No need to ask the owner." {
		t.Errorf("close next = %q", n)
	}
}
