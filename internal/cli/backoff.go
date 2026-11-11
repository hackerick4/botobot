package cli

import (
	"math"
	"time"
)

// v0.6.9 (Rick, 2026-11-04): polling that feels closer to a person.
//
// Two parts:
//
//  1. Follow (tick --follow): after a conversation step, the bot keeps
//     watching inside the same run. It checks every 1 s; after 3 empty checks
//     it moves one level up, gently (followLevels), and any new message sends
//     it back to 1 s. It stops after an idle window (default 3 min, hard cap
//     5 min, far below a 13-min tool limit) and after a bounded number of
//     wakes per conversation (loop protection, RT-174..).
//  2. Idle stretching (the 5-minute routine): a bridge with no activity for a
//     while is not checked on every routine run. The target gap between real
//     server checks grows ln-like from 5 min (idle <= 15 min) to 10 min (idle
//     >= 75 min); a fractional credit spreads the checks over the 5-minute
//     runs, and a hard rule never lets more than 10 min pass without a check.
//
// Every real inbox fetch, routine or follow, also goes through a small local
// token bucket that stays under the server's per-land GET /inbox limit
// (6/min, burst 10; DEV_SPEC §9), so following never draws 429s.

// followLevels are the follow intervals, followPerLevel empty checks each.
var followLevels = []time.Duration{
	1 * time.Second, 2 * time.Second, 3 * time.Second, 5 * time.Second,
	8 * time.Second, 15 * time.Second, 30 * time.Second, 60 * time.Second,
}

const followPerLevel = 3

// followDelay is the wait before the next check after `empty` consecutive
// empty checks (0 right after start or after a new message).
func followDelay(empty int) time.Duration {
	if empty < 0 {
		empty = 0
	}
	lvl := empty / followPerLevel
	if lvl >= len(followLevels) {
		lvl = len(followLevels) - 1
	}
	return followLevels[lvl]
}

// Follow limits.
const (
	followDefaultWait = 180 * time.Second // idle window per follow run
	followMinWait     = 30 * time.Second
	followMaxWait     = 300 * time.Second // hard cap per run (tool limit is 13 min)
	// followActive: only bridges with activity this recent are followed.
	followActive = 15 * time.Minute
	// A conversation chain: follow runs with no more than followChainGap
	// between them. Within one chain at most followChainWakes follow runs may
	// return new messages and the chain may last at most followChainMax;
	// after that, follow returns at once and the routine takes over
	// (RT-174: a peer cannot keep this bot in 1-second mode forever).
	followChainGap   = 10 * time.Minute
	followChainWakes = 12
	followChainMax   = 30 * time.Minute
	// followMaxFailures: consecutive failed checks that end a follow run.
	followMaxFailures = 3
)

// clampFollowWait bounds a requested idle window.
func clampFollowWait(d time.Duration) time.Duration {
	if d <= 0 {
		return followDefaultWait
	}
	if d < followMinWait {
		return followMinWait
	}
	if d > followMaxWait {
		return followMaxWait
	}
	return d
}

// Idle stretching.
const (
	idleBaseGap  = 5 * time.Minute  // routine cadence (platform minimum)
	idleMaxGap   = 10 * time.Minute // never longer between real checks
	idleFastFor  = 15 * time.Minute // idle up to this: check every run
	idleFullAt   = 75 * time.Minute // idle from this: 10-minute gap
	idleSlack    = 30 * time.Second // routine runs drift a little
	idleCreditOK = 0.95             // credit needed to check (drift tolerance)
)

// idleGap is the target time between real server checks for a bridge idle
// for idle: 5 min up to 15 min idle, then 5 + 5·ln(1+x)/ln(5) min with
// x = (idle-15m)/15m, reaching 10 min at 75 min idle. Gentle and ln-like:
// 30 min idle → ~7.2 min, 45 min → ~8.4 min, 60 min → ~9.3 min.
func idleGap(idle time.Duration) time.Duration {
	if idle <= idleFastFor {
		return idleBaseGap
	}
	if idle >= idleFullAt {
		return idleMaxGap
	}
	x := float64(idle-idleFastFor) / float64(idleFastFor)
	f := math.Log1p(x) / math.Log(float64(idleFullAt-idleFastFor)/float64(idleFastFor)+1)
	g := idleBaseGap + time.Duration(f*float64(idleMaxGap-idleBaseGap))
	if g > idleMaxGap {
		g = idleMaxGap
	}
	return g
}

// idleDecision says whether this routine run should really check the server
// for a bridge, updating the run bookkeeping in ts. It never skips when the
// bridge is new or active, when acks are pending, or when idleMaxGap (less
// slack) has passed since the last real check.
func idleDecision(ts *tickState, now time.Time) bool {
	nowMS := now.UnixMilli()
	lastRun := ts.LastRunMS
	ts.LastRunMS = nowMS
	if ts.LastPollMS == 0 || len(ts.PendingAckIDs) > 0 || lastRun == 0 || nowMS < ts.LastPollMS || nowMS < lastRun {
		ts.IdleCredit = 0
		return true
	}
	act := ts.LastActivityMS
	if act == 0 {
		act = ts.LastPollMS // never any activity: count idle from the first check
	}
	idle := time.Duration(nowMS-act) * time.Millisecond
	if idle <= idleFastFor {
		ts.IdleCredit = 0
		return true
	}
	sincePoll := time.Duration(nowMS-ts.LastPollMS) * time.Millisecond
	if sincePoll >= idleMaxGap-idleSlack {
		ts.IdleCredit = 0
		return true
	}
	step := time.Duration(nowMS-lastRun) * time.Millisecond
	ts.IdleCredit += float64(step) / float64(idleGap(idle))
	if ts.IdleCredit >= idleCreditOK {
		ts.IdleCredit -= 1
		if ts.IdleCredit < 0 {
			ts.IdleCredit = 0
		}
		return true
	}
	return false
}

// markActivity records a conversation step (a message shown, a reply sent):
// the routine goes back to checking on every run.
func (ts *tickState) markActivity(now time.Time) {
	ts.LastActivityMS = now.UnixMilli()
	ts.IdleCredit = 0
}

// Local inbox budget: a token bucket kept under the server's per-land GET
// /inbox limit (6/min, burst 10), with one token of margin for a manual or
// routine check.
const (
	inboxBudgetPerMin = 6
	inboxBudgetBurst  = 9
	// tickBudgetMaxWait caps how long a normal run waits for a token
	// (one token refills in 10 s at 6/min).
	tickBudgetMaxWait = 12 * time.Second
)

// budgetRefill brings the bucket up to now and returns the tokens.
func (ts *tickState) budgetRefill(now time.Time) float64 {
	nowMS := now.UnixMilli()
	if ts.BudgetAtMS == 0 {
		ts.BudgetTokens, ts.BudgetAtMS = inboxBudgetBurst, nowMS
		return ts.BudgetTokens
	}
	if nowMS > ts.BudgetAtMS {
		ts.BudgetTokens += float64(nowMS-ts.BudgetAtMS) / float64(time.Minute.Milliseconds()) * inboxBudgetPerMin
		if ts.BudgetTokens > inboxBudgetBurst {
			ts.BudgetTokens = inboxBudgetBurst
		}
		ts.BudgetAtMS = nowMS
	}
	return ts.BudgetTokens
}

// budgetWait is how long to wait until one token is available (0 = now).
func (ts *tickState) budgetWait(now time.Time) time.Duration {
	tok := ts.budgetRefill(now)
	if tok >= 1 {
		return 0
	}
	need := (1 - tok) / inboxBudgetPerMin * float64(time.Minute)
	return time.Duration(math.Ceil(need/float64(time.Millisecond))) * time.Millisecond
}

// budgetTake spends one token (callers wait with budgetWait first; a
// routine run that finds the bucket empty still checks, the bucket going
// negative, so follow waits longer afterwards).
func (ts *tickState) budgetTake(now time.Time) {
	ts.budgetRefill(now)
	ts.BudgetTokens--
}

// followChainOK reports whether another follow run is allowed on this
// bridge, starting a new chain when the last one is old.
func followChainOK(ts *tickState, now time.Time) bool {
	nowMS := now.UnixMilli()
	if ts.FollowChainStartMS == 0 || nowMS-ts.FollowLastMS > followChainGap.Milliseconds() || nowMS < ts.FollowChainStartMS {
		ts.FollowChainStartMS, ts.FollowWakes = nowMS, 0
	}
	ts.FollowLastMS = nowMS
	if ts.FollowWakes >= followChainWakes {
		return false
	}
	return nowMS-ts.FollowChainStartMS < followChainMax.Milliseconds()
}
