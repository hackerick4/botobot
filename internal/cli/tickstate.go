package cli

import (
	"os"
	"sort"
	"strings"
	"time"
)

// tickState is tick.json: the per-bridge state the `tick` routine threads between
// its 5-minute invocations (Batch 1 item 1).
//
//   - ConsecutiveFailures / Notified drive the "after 12 consecutive failures,
//     print one NOTIFY_OWNER then suppress until success" behaviour.
//   - PendingAckIDs are the inbox env ids fetched last tick but not yet acked:
//     tick defers the ack to the start of the next tick (at-least-once, no extra
//     tool call on an empty wake).
//   - Seen is a bounded dedupe set ("sender\x00msg_id" or "evt\x00env_id" ->
//     first-shown ms) so redelivered items are not shown twice.
//   - ShortIDs maps the last batch's "#k" labels to msg_ids for `tick reply`.
//   - SessionSends / HourSends back the chat-loop send limits (Batch 1 item 7):
//     HourSends is a rolling one-hour window of send timestamps that backs both
//     the 5-minute and the hourly cap. SessionSends is a legacy counter kept for
//     compatibility; it no longer gates anything and tick does not reset it.
type tickState struct {
	ConsecutiveFailures int               `json:"consecutive_failures"`
	Notified            bool              `json:"notified"`
	PendingAckIDs       []string          `json:"pending_ack_ids"`
	Seen                map[string]int64  `json:"seen"`
	ShortIDs            map[string]string `json:"short_ids"`
	SessionSends        int               `json:"session_sends"`
	HourSends           []int64           `json:"hour_sends"`
	// WakeID/WakeUntilMS mark the tick "wake" in progress: a tick that shows
	// messages opens one (wakeWindow long), an empty tick ends it. A hold made
	// during a wake cannot be released in that same wake (hold.go).
	WakeID      string `json:"wake_id,omitempty"`
	WakeUntilMS int64  `json:"wake_until_ms,omitempty"`
	// TaintUntilMS: after an inbound message judged medium or above, every
	// outbound message on this bridge is held for the owner until this time.
	TaintUntilMS int64 `json:"taint_until_ms,omitempty"`
	// Ended records that this bot's membership ended (v0.4.9): "self" after its
	// own leave or close (the command already printed NEXT), otherwise the
	// reason already told to the owner once (closed by the admin, removed). An
	// ended tick prints NO_NEW_MESSAGES instead of repeating the notice.
	Ended string `json:"ended,omitempty"`
	// EndedMS is when Ended was set (v0.5.2; 0 for older states).
	EndedMS int64 `json:"ended_ms,omitempty"`
	// CleanupTriedMS throttles the server check that cleans up a bridge this
	// bot left itself but never forgot (v0.5.2).
	CleanupTriedMS int64 `json:"cleanup_tried_ms,omitempty"`
}

// tickSeen bounds the dedupe set (Batch 1 item 1: ~last 1000 ids / 48h).
const (
	tickSeenMax = 1000
	tickSeenTTL = 48 * time.Hour
)

func (ks *keystore) loadTickState(id string) (*tickState, error) {
	ts := &tickState{Seen: map[string]int64{}, ShortIDs: map[string]string{}}
	err := readJSON(ks.tickStatePath(id), ts)
	if os.IsNotExist(err) {
		return ts, nil
	}
	if err != nil {
		return nil, err
	}
	if ts.Seen == nil {
		ts.Seen = map[string]int64{}
	}
	if ts.ShortIDs == nil {
		ts.ShortIDs = map[string]string{}
	}
	return ts, nil
}

func (ks *keystore) saveTickState(id string, ts *tickState) error {
	return writeJSONAtomic(ks.tickStatePath(id), ts)
}

// markSeen records key as shown at nowMS and prunes the dedupe set to the TTL and
// size bound (Batch 1 item 1).
func (ts *tickState) markSeen(key string, nowMS int64) {
	ts.Seen[key] = nowMS
	ts.pruneSeen(nowMS)
}

// alreadySeen reports whether key was shown within the dedupe window.
func (ts *tickState) alreadySeen(key string) bool {
	_, ok := ts.Seen[key]
	return ok
}

// pruneSeen drops entries older than the TTL and, if still over the cap, the
// oldest ones.
func (ts *tickState) pruneSeen(nowMS int64) {
	ttl := tickSeenTTL.Milliseconds()
	for k, t := range ts.Seen {
		if nowMS-t > ttl {
			delete(ts.Seen, k)
		}
	}
	if len(ts.Seen) <= tickSeenMax {
		return
	}
	type kv struct {
		k string
		t int64
	}
	all := make([]kv, 0, len(ts.Seen))
	for k, t := range ts.Seen {
		all = append(all, kv{k, t})
	}
	sort.Slice(all, func(i, j int) bool { return all[i].t < all[j].t })
	for i := 0; i < len(all)-tickSeenMax; i++ {
		delete(ts.Seen, all[i].k)
	}
}

// endedClosed reports a tick state whose bridge is over for this bot: its own
// leave or close, or the server's "closed" answer already told to the owner.
// A refused key ("無法連線…") is not counted: a later fetch may clear it.
func endedClosed(ts *tickState) bool {
	return ts != nil && (ts.Ended == endedSelf || strings.HasPrefix(ts.Ended, "已"))
}

// liveCount is how many things in this folder still need the "bot-bridge
// inbox" routine (v0.6.2): bridges that are not closed for this bot, plus
// public entries. A bridge closed but not yet forgotten does not count.
func (ks *keystore) liveCount() int {
	n := ks.entryCount()
	bs, err := ks.listBridges()
	if err != nil {
		return n + 1 // unknown: keep the routine
	}
	for _, bc := range bs {
		ts, err := ks.loadTickState(bc.BridgeID)
		if err != nil || !endedClosed(ts) {
			n++
		}
	}
	return n
}

// noLiveRoutineLine is the NEXT line when only closed, not yet forgotten
// bridges are left (v0.6.2).
func noLiveRoutineLine(en bool) string {
	if en {
		return "NEXT: this bot has no bridge in use any more; delete its \"bot-bridge inbox\" routine (not pause). No need to ask the owner."
	}
	return "NEXT: 此 Bot 已無使用中的 bridge，刪掉「bot-bridge inbox」routine（不是暫停），不必再問主人。"
}
