package cli

import (
	"os"
	"time"
)

// tickFollow implements `tick --follow` (v0.6.9, backoff.go): keep watching
// the bridges that just had a conversation step, inside this run. It checks
// at once, then waits followDelay between checks (1 s ×3, 2 s ×3, 3 s ×3,
// 5 s, 8 s, 15 s, 30 s, 60 s), each check also waiting for the local inbox
// budget. The first check that shows something prints it exactly like tick
// (rules, fences, #k) and ends the run; the next follow starts at 1 s again.
// With nothing new for `wait`, after followMaxFailures failed checks in a
// row, or when no bridge may be followed, it prints NO_NEW_MESSAGES.
//
// Followed bridges: two-bot bridges in this folder (or --bridge), not
// ended, with a message shown or a reply sent within followActive, under
// the auto-reply limits, and inside their follow chain limits. Public entry
// lines are not followed; the routine reads them as before.
func (s *session) tickFollow(g *globalFlags, limit int, wait time.Duration) *cliError {
	a := s.app
	// Waits are counted on a clock that advances by what this run slept,
	// so the window and budget hold even when sleeping is stubbed (tests).
	origNow := a.now
	start := origNow()
	var slept time.Duration
	a.now = func() time.Time {
		t := start.Add(slept)
		if n := origNow(); n.After(t) {
			return n
		}
		return t
	}
	defer func() { a.now = origNow }()

	var ids []string
	if g.bridge != "" {
		if bridgeIDRe.MatchString(g.bridge) {
			if _, err := s.ks.loadBridge(g.bridge); err == nil {
				ids = []string{g.bridge}
			}
		}
	} else if bcs, err := s.ks.listBridges(); err == nil {
		if _, mixed := homeIdentity(s.ks.home); !mixed || len(bcs) <= 1 {
			for _, bc := range bcs {
				if bc.EntryID == "" {
					ids = append(ids, bc.BridgeID)
				}
			}
		}
	}
	now := a.now()
	var active []string
	for _, id := range ids {
		if s.followAllowed(id, now) {
			active = append(active, id)
		}
	}
	if len(active) == 0 {
		s.pr.raw("NO_NEW_MESSAGES")
		return s.pr.done()
	}

	sh := &tickShared{nonce: randToken(), all: g.bridge == "", follow: true}
	empty, failedRow := 0, 0
	var idle time.Duration
	for {
		// Stay under the server's per-land inbox limit (first check too).
		if bw := s.followBudgetWait(active); bw > 0 {
			if idle+bw > wait {
				break
			}
			a.sleep(bw)
			slept += bw
			idle += bw
		}
		before := sh.failed
		shown := false
		for _, id := range active {
			got, ce := s.followCheck(id, limit, sh)
			if ce != nil {
				return ce
			}
			if got {
				shown = true
				if ts, err := s.ks.loadTickState(id); err == nil {
					ts.FollowWakes++
					_ = s.ks.saveTickState(id, ts)
				}
			}
		}
		if shown {
			s.followNextLine(sh)
			return s.pr.done()
		}
		if sh.failed > before {
			failedRow++
		} else {
			failedRow = 0
		}
		if failedRow >= followMaxFailures {
			break
		}
		d := followDelay(empty)
		empty++
		if idle+d > wait {
			break
		}
		a.sleep(d)
		slept += d
		idle += d
	}
	s.pr.raw("NO_NEW_MESSAGES")
	return s.pr.done()
}

// followBudgetWait is the longest wait any followed bridge needs before its
// next inbox fetch fits the local budget.
func (s *session) followBudgetWait(ids []string) time.Duration {
	var w time.Duration
	now := s.app.now()
	for _, id := range ids {
		if ts, err := s.ks.loadTickState(id); err == nil {
			if bw := ts.budgetWait(now); bw > w {
				w = bw
			}
		}
	}
	return w
}

// followAllowed reports whether bridge id may be followed now.
func (s *session) followAllowed(id string, now time.Time) bool {
	ts, err := s.ks.loadTickState(id)
	if err != nil || ts.Ended != "" {
		return false
	}
	if ts.LastActivityMS == 0 || now.UnixMilli()-ts.LastActivityMS > followActive.Milliseconds() {
		return false
	}
	// At the auto-reply limit nothing can be answered: do not keep watching.
	if countWithin(ts.HourSends, now, sendSessionWindow) >= maxSendsPerSession ||
		countWithin(ts.HourSends, now, sendHourWindow) >= maxSendsPerHour {
		return false
	}
	ok := followChainOK(ts, now)
	_ = s.ks.saveTickState(id, ts)
	return ok
}

// followCheck is one tick of one bridge inside a follow run (lock held only
// for the check, so a routine run in between is not blocked for long).
func (s *session) followCheck(id string, limit int, sh *tickShared) (bool, *cliError) {
	bs, e := s.bridgeStateFor(id)
	if e != nil {
		return false, nil
	}
	lk, ok, err := acquireLock(s.ks.tickLockPath(id))
	if err != nil || !ok {
		return false, nil // another run is reading it right now
	}
	defer lk.release()
	if _, err := os.Stat(s.ks.bridgeDir(id)); err != nil {
		return false, nil
	}
	return s.runTick(bs, limit, sh)
}

// followNextLine is printed after a run that showed messages: how to keep
// watching for the answer. Bot-facing; plain words.
func (s *session) followNextLine(sh *tickShared) {
	if !sh.msgs {
		return
	}
	if sh.msgsEN {
		s.pr.raw("NEXT: after replying to the other bot, run " + s.cliCmd() + " tick --follow to wait for its answer. It can take up to about 3 minutes; let it finish. If it prints new messages, handle them with the rules above; if it prints only NO_NEW_MESSAGES, end. If nothing was sent, no need to run it.")
		return
	}
	s.pr.raw("NEXT: 回覆對方後，執行 " + s.cliCmd() + " tick --follow 等對方接著說。最多約 3 分鐘，讓它跑完。印出新訊息就照上面的規則處理；只印 NO_NEW_MESSAGES 就結束。沒有回覆對方就不用執行。")
}

// sendFollowNextLine is printed once after a message was actually sent (a
// successful `send` or `tick reply`, type=sent): run tick --follow once to wait
// for the other bot's answer. follow only receives — it is never permission to
// reply again or to loop. Bot-facing; plain words. It is skipped for held,
// failed, blocked, dry-run, rejected, or zero-sent outcomes (those never reach
// here) and prints at most once per run (release --batch).
func (s *session) sendFollowNextLine(en bool) {
	if s.app.followHinted {
		return
	}
	s.app.followHinted = true
	if en {
		s.pr.raw("NEXT: run " + s.cliCmd() + " tick --follow once to wait for the other bot's answer. It only receives (up to about 3 minutes); it is not permission to reply again or to loop. Even in a one-round exchange, still run it once to receive the reply you are expecting. If it prints new messages, handle them with the rules above; if it prints only NO_NEW_MESSAGES, end. Skip it only if your owner explicitly said no follow or to stop receiving.")
		return
	}
	s.pr.raw("NEXT: 執行一次 " + s.cliCmd() + " tick --follow 等對方回覆。它只負責接收（最多約 3 分鐘），不代表可以再回或進入循環。就算只回一輪，也該執行這一次來接收預期的回覆。印出新訊息就照上面的規則處理；只印 NO_NEW_MESSAGES 就結束。只有主人明確要你別 follow、別再接收時，才跳過不執行。")
}
