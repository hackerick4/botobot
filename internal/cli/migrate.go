package cli

import "time"

// Hourly-cadence migration (t435u). Existing bots run a 5-minute "bot-bridge
// inbox" routine (a plain tick, or an older `tick --bridge br_…` one); the new
// default is every 60 minutes. Existing bots never re-read the handbook, so the
// switch is driven from what `tick` prints on a routine run (whole-bot or a
// single --bridge one, but never a same-turn `tick --follow`). The prompt is
// per-bot (home config), bounded, fail-safe and self-clearing:
//   - the first migrateMaxPrompts prompts are migratePromptCooldown apart; after
//     that at most once a day, but it never gives up until the bot is migrated;
//   - it marks migrated only after migrateConfirmRuns consecutive routine-run
//     gaps of at least migrateConfirmGap (so one host pause does not falsely
//     confirm, and a 5-minute routine that keeps ticking is never marked done);
//   - fresh bots are marked migrated at routine creation, so they never see it;
//   - the CLI never changes the schedule itself — the bot keeps its working
//     5-minute routine until it has confirmed a 60-minute one.
const (
	migrateMaxPrompts     = 3
	migratePromptCooldown = 20 * time.Minute
	migrateDailyCooldown  = 24 * time.Hour
	migrateConfirmGap     = 45 * time.Minute
	migrateConfirmRuns    = 2
)

// emitPollMigrate prints the migration prompt on this per-bot routine run when
// due, updating the bounded bookkeeping in the home config. It must not be
// called for a `tick --follow` run (so same-turn dense polling neither prompts
// nor disturbs the routine-gap bookkeeping).
func (s *session) emitPollMigrate(en bool, now time.Time) {
	c, err := s.ks.loadConfig()
	if err != nil || c == nil || c.PollMigrated {
		return // migrated bots write nothing here (empty ticks stay clean)
	}
	nowMS := now.UnixMilli()
	last := c.LastTickMS
	c.LastTickMS = nowMS
	save := func() { _ = s.ks.saveConfig(c) }
	// Count consecutive wide gaps between routine runs; two in a row (after at
	// least one prompt) means the hourly cadence really took hold.
	if last > 0 && nowMS-last >= migrateConfirmGap.Milliseconds() {
		c.PollMigrateWideGaps++
	} else {
		c.PollMigrateWideGaps = 0
	}
	if c.PollMigratePrompts > 0 && c.PollMigrateWideGaps >= migrateConfirmRuns {
		c.PollMigrated = true
		save()
		return
	}
	// The first few prompts are 20 min apart; after that, at most once a day, but
	// never give up (the bot must end up migrated).
	cooldown := migratePromptCooldown.Milliseconds()
	if c.PollMigratePrompts >= migrateMaxPrompts {
		cooldown = migrateDailyCooldown.Milliseconds()
	}
	if c.PollMigratePromptedMS != 0 && nowMS-c.PollMigratePromptedMS < cooldown {
		save()
		return
	}
	c.PollMigratePromptedMS = nowMS
	c.PollMigratePrompts++
	save()
	for _, line := range pollMigrateLines(en) {
		s.pr.raw(line)
	}
}

func pollMigrateLines(en bool) []string {
	if en {
		return []string{
			"MIGRATE_POLLING: this bot's inbox routine still runs every 5 minutes; switch it to every 60 minutes to save tokens. A reply right after a message is still caught in the same turn by tick --follow.",
			"NEXT: change every routine of this bot whose prompt contains a bot-bridge tick (including an older one with --bridge) to run every 60 minutes (every_minutes 60, poll_interval_sec 3600), keeping the rest of the prompt. Prefer editing the existing routine's frequency (one approval tap at most). If your platform cannot edit it, first create the 60-minute routine and confirm it is enabled, THEN delete the old one; never leave zero receiving routines. If this bot has more than one receiving routine, keep only the ones you need afterwards and do not add duplicates. If editing fails or the owner does not approve, keep the existing routine; it still works.",
		}
	}
	return []string{
		"MIGRATE_POLLING: 這個 Bot 的收信 routine 還是每 5 分鐘跑一次；改成每 60 分鐘可省 token。剛收到訊息後的回覆仍由同一回合的 tick --follow 接著收。",
		"NEXT: 把這個 Bot 所有提示詞含 bot-bridge tick 的 routine（包含舊式帶 --bridge 的）都改成每 60 分鐘執行一次（every_minutes 60、poll_interval_sec 3600），提示詞其餘不變。優先直接改現有排程的頻率（最多按一次核准）。平台不能改就先建好 60 分鐘的新排程、確認已啟用，再刪掉舊的；任何時候都至少保留一個收信排程。同一個 Bot 有多個收信 routine 時，改完只保留必要的，不要新增重複。改失敗或主人不核准，就保留原本的排程，一切照常。",
	}
}
