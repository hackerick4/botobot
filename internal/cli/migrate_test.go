package cli

import (
	"strings"
	"testing"
	"time"
)

func setConfig(t *testing.T, home string, f func(c *localConfig)) {
	t.Helper()
	ks, err := openKeystore(home)
	if err != nil {
		t.Fatal(err)
	}
	c, err := ks.loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	f(c)
	if err := ks.saveConfig(c); err != nil {
		t.Fatal(err)
	}
}

func getConfig(t *testing.T, home string) *localConfig {
	t.Helper()
	ks, _ := openKeystore(home)
	c, err := ks.loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// recent keeps LastTickMS close to now so the wide-gap confirm does not fire
// between prompts, and pushes the last prompt back past a cooldown.
func agoMS(d time.Duration) int64 { return time.Now().Add(-d).UnixMilli() }

// A bot created on this version is already on the hourly cadence, so a routine
// tick never prints the migration prompt and an empty tick stays exactly
// NO_NEW_MESSAGES.
func TestPollMigrateFreshBotSilent(t *testing.T) {
	p := setupPair(t)
	run(t, "tick", "--home", p.homeA) // drain the land_joined event
	if !getConfig(t, p.homeA).PollMigrated {
		t.Fatal("a freshly created bot should be marked PollMigrated")
	}
	if tk, _, _ := run(t, "tick", "--home", p.homeA); tk != "NO_NEW_MESSAGES\n" {
		t.Fatalf("fresh bot tick should be silent, got:\n%s", tk)
	}
}

// An existing 5-minute bot is prompted with the fail-safe / multi-routine
// wording, the first migrateMaxPrompts prompts migratePromptCooldown apart;
// after the cap it keeps reminding at most once a day (never gives up).
func TestPollMigratePromptFirstThreeThenDaily(t *testing.T) {
	p := setupPair(t)
	run(t, "tick", "--home", p.homeA)
	setConfig(t, p.homeA, func(c *localConfig) { c.PollMigrated = false })

	tk, _, _ := run(t, "tick", "--home", p.homeA)
	if !strings.Contains(tk, "MIGRATE_POLLING:") {
		t.Fatalf("existing bot's first tick should prompt:\n%s", tk)
	}
	for _, want := range []string{
		"先建好", "確認已啟用", "再刪掉舊的", "至少保留一個收信排程", "保留原本", // fail-safe
		"--bridge", "不要新增重複", // covers bridge-filtered routines + no duplicate
	} {
		if !strings.Contains(tk, want) {
			t.Errorf("migration prompt missing %q:\n%s", want, tk)
		}
	}
	if !strings.HasSuffix(tk, "NO_NEW_MESSAGES\n") {
		t.Errorf("migration tick should still end in NO_NEW_MESSAGES:\n%s", tk)
	}

	// Inside the cooldown: no re-prompt.
	if tk2, _, _ := run(t, "tick", "--home", p.homeA); strings.Contains(tk2, "MIGRATE_POLLING") {
		t.Fatalf("a tick inside the cooldown must not re-prompt:\n%s", tk2)
	}
	if n := getConfig(t, p.homeA).PollMigratePrompts; n != 1 {
		t.Fatalf("prompt count = %d, want 1", n)
	}

	// Past the 20-minute cooldown it prompts again, for the first three.
	bumpCooldown := func() string {
		setConfig(t, p.homeA, func(c *localConfig) {
			c.PollMigratePromptedMS = agoMS(migratePromptCooldown + time.Minute)
			c.LastTickMS = agoMS(time.Minute) // recent: no wide gap
		})
		tk, _, _ := run(t, "tick", "--home", p.homeA)
		return tk
	}
	if tk := bumpCooldown(); !strings.Contains(tk, "MIGRATE_POLLING") {
		t.Fatalf("prompt 2 expected:\n%s", tk)
	}
	if tk := bumpCooldown(); !strings.Contains(tk, "MIGRATE_POLLING") {
		t.Fatalf("prompt 3 expected:\n%s", tk)
	}
	if n := getConfig(t, p.homeA).PollMigratePrompts; n != migrateMaxPrompts {
		t.Fatalf("prompt count = %d, want %d", n, migrateMaxPrompts)
	}

	// After the cap: 20 minutes is NO longer enough (now a daily cooldown)...
	if tk := bumpCooldown(); strings.Contains(tk, "MIGRATE_POLLING") {
		t.Fatalf("after the cap, a 20-minute gap should NOT re-prompt:\n%s", tk)
	}
	// ...but a full day later it reminds again (does not give up).
	setConfig(t, p.homeA, func(c *localConfig) {
		c.PollMigratePromptedMS = agoMS(migrateDailyCooldown + time.Hour)
		c.LastTickMS = agoMS(time.Minute)
	})
	if tk, _, _ := run(t, "tick", "--home", p.homeA); !strings.Contains(tk, "MIGRATE_POLLING") {
		t.Fatalf("past the daily cooldown it should remind again:\n%s", tk)
	}
}

// Marking migrated needs TWO consecutive wide routine-run gaps: a single wide
// gap (one host pause) must not falsely confirm.
func TestPollMigrateConfirmTwoWideGaps(t *testing.T) {
	p := setupPair(t)
	run(t, "tick", "--home", p.homeA)
	setConfig(t, p.homeA, func(c *localConfig) { c.PollMigrated = false })

	if tk, _, _ := run(t, "tick", "--home", p.homeA); !strings.Contains(tk, "MIGRATE_POLLING") {
		t.Fatalf("first tick should prompt:\n%s", tk)
	}
	wide := func() { setConfig(t, p.homeA, func(c *localConfig) { c.LastTickMS = agoMS(migrateConfirmGap + time.Minute) }) }

	// One wide gap: not migrated yet.
	wide()
	run(t, "tick", "--home", p.homeA)
	if c := getConfig(t, p.homeA); c.PollMigrated {
		t.Fatal("one wide gap must NOT mark migrated")
	} else if c.PollMigrateWideGaps != 1 {
		t.Fatalf("wide-gap count = %d, want 1", c.PollMigrateWideGaps)
	}

	// A normal (short) gap resets the streak.
	setConfig(t, p.homeA, func(c *localConfig) { c.LastTickMS = agoMS(time.Minute) })
	run(t, "tick", "--home", p.homeA)
	if c := getConfig(t, p.homeA); c.PollMigrated || c.PollMigrateWideGaps != 0 {
		t.Fatalf("a short gap should reset the streak: %+v", c)
	}

	// Two consecutive wide gaps: now migrated.
	wide()
	run(t, "tick", "--home", p.homeA)
	wide()
	run(t, "tick", "--home", p.homeA)
	if !getConfig(t, p.homeA).PollMigrated {
		t.Fatal("two consecutive wide gaps should mark migrated")
	}
}

// A single per-bridge routine tick (old bridge-filtered routines) also prompts,
// and the per-bot state means the same bot is not double-counted.
func TestPollMigrateOnSingleBridgeRoutineTick(t *testing.T) {
	p := setupPair(t)
	run(t, "tick", "--home", p.homeA)
	setConfig(t, p.homeA, func(c *localConfig) { c.PollMigrated = false })

	if tk, _, _ := run(t, "tick", "--home", p.homeA, "--bridge", p.bridgeID); !strings.Contains(tk, "MIGRATE_POLLING") {
		t.Fatalf("a --bridge routine tick should prompt migration:\n%s", tk)
	}
	if n := getConfig(t, p.homeA).PollMigratePrompts; n != 1 {
		t.Fatalf("prompt count = %d, want 1", n)
	}
	// A whole-bot tick within the cooldown does not re-prompt the same bot.
	if tk, _, _ := run(t, "tick", "--home", p.homeA); strings.Contains(tk, "MIGRATE_POLLING") {
		t.Fatalf("same bot within cooldown must not re-prompt:\n%s", tk)
	}
	if n := getConfig(t, p.homeA).PollMigratePrompts; n != 1 {
		t.Fatalf("prompt count = %d, want 1", n)
	}
}

// `tick --follow` (same-turn dense polling) must neither prompt migration nor
// touch the routine-gap bookkeeping.
func TestPollMigrateFollowDoesNotPromptOrTrackGap(t *testing.T) {
	p := setupPair(t)
	run(t, "tick", "--home", p.homeA)
	setConfig(t, p.homeA, func(c *localConfig) { c.PollMigrated = false })
	// No recent activity -> follow returns at once (fast), so the test is cheap.
	setTick(t, p.homeA, p.bridgeID, func(ts *tickState) { ts.LastActivityMS = 0 })

	before := getConfig(t, p.homeA)
	tk, _, _ := run(t, "tick", "--follow", "--wait", "30", "--home", p.homeA)
	if strings.Contains(tk, "MIGRATE_POLLING") {
		t.Fatalf("follow must not prompt migration:\n%s", tk)
	}
	after := getConfig(t, p.homeA)
	if after.PollMigrated || after.PollMigratePrompts != before.PollMigratePrompts ||
		after.LastTickMS != before.LastTickMS || after.PollMigrateWideGaps != before.PollMigrateWideGaps {
		t.Fatalf("follow changed migration bookkeeping:\nbefore=%+v\nafter=%+v", before, after)
	}
}
