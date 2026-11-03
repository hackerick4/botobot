package cli

import "time"

// Chat-loop send limits (Batch 1 item 7, hardened in EXFIL_DEFENSE.md P0). Both
// are rolling windows over the persisted send timestamps (tick.json HourSends):
// at most maxSendsPerSession within sendSessionWindow and maxSendsPerHour within
// an hour. The session window used to reset on every `tick`, so a bot could
// tick again to refill it; it is now purely time-based and tick never resets it.
const (
	maxSendsPerSession = 3
	sendSessionWindow  = 5 * time.Minute
	maxSendsPerHour    = 20
	sendHourWindow     = time.Hour
)

// sendLimitNotify is printed (to stdout, as the routine expects) when a send is
// refused for exceeding the auto-reply limits (Batch 1 item 7).
const sendLimitNotify = "NOTIFY_OWNER: 已達自動回覆上限（每 5 分鐘 3 則、每小時 20 則），已暫停，待主人確認後繼續。"

// checkSendLimit reports whether another send is allowed right now without
// recording it (Batch 1 item 7). A refusal is a cliError with code "send_limit"
// so the caller can surface it as a NOTIFY_OWNER line.
func (s *session) checkSendLimit(bridgeID string) *cliError {
	ts, err := s.ks.loadTickState(bridgeID)
	if err != nil {
		return generalErrorf("read tick state: %v", err)
	}
	now := s.app.now()
	if countWithin(ts.HourSends, now, sendSessionWindow) >= maxSendsPerSession {
		return &cliError{Code: "send_limit", Message: "per-session auto-reply limit reached", Exit: exitGeneral}
	}
	if countWithin(ts.HourSends, now, sendHourWindow) >= maxSendsPerHour {
		return &cliError{Code: "send_limit", Message: "hourly auto-reply limit reached", Exit: exitGeneral}
	}
	return nil
}

// recordSend commits one send against the session and hourly counters (Batch 1
// item 7). Best-effort: a persistence error is returned but callers treat it as
// non-fatal (the message was already delivered).
func (s *session) recordSend(bridgeID string) *cliError {
	ts, err := s.ks.loadTickState(bridgeID)
	if err != nil {
		return generalErrorf("read tick state: %v", err)
	}
	now := s.app.now()
	ts.SessionSends++
	ts.HourSends = append(pruneOlder(ts.HourSends, now, sendHourWindow), now.UnixMilli())
	if err := s.ks.saveTickState(bridgeID, ts); err != nil {
		return generalErrorf("save tick state: %v", err)
	}
	return nil
}

// countWithin returns how many timestamps (Unix ms) fall within window before now.
func countWithin(ms []int64, now time.Time, window time.Duration) int {
	cutoff := now.Add(-window).UnixMilli()
	n := 0
	for _, t := range ms {
		if t > cutoff {
			n++
		}
	}
	return n
}

// pruneOlder drops timestamps (Unix ms) older than window before now.
func pruneOlder(ms []int64, now time.Time, window time.Duration) []int64 {
	cutoff := now.Add(-window).UnixMilli()
	out := ms[:0]
	for _, t := range ms {
		if t > cutoff {
			out = append(out, t)
		}
	}
	return out
}
