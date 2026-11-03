package cli

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/hackerick4/botobot/internal/prompts"
)

// v0.6.4: messages the other side has not read when a bridge closes.

// undeliveredNotice tells the owner, after a close, how many of this bot's
// messages the other side had not read yet (v0.6.4). Nothing when none.
func (s *session) undeliveredNotice(co closeOutcome, lang string) {
	if co.Undelivered <= 0 {
		return
	}
	s.pr.raw("NOTIFY_OWNER: " + undeliveredText(co, s.app.now(), prompts.IsEnglish(lang)))
}

// undeliveredText is the owner line for undeliveredNotice and entry close.
func undeliveredText(co closeOutcome, now time.Time, en bool) string {
	n := co.Undelivered
	mins := 0
	if t, err := time.Parse(time.RFC3339, co.FinalUntil); err == nil && t.After(now) {
		mins = int((t.Sub(now) + time.Minute - time.Second) / time.Minute)
	}
	if en {
		what := fmt.Sprintf("%d messages were", n)
		if n == 1 {
			what = "1 message was"
		}
		if mins > 0 {
			return fmt.Sprintf("%s not yet read by the other side. They can still receive them for %d minutes; after that they are deleted.", what, mins)
		}
		return what + " not yet read by the other side and were deleted with the close."
	}
	if mins > 0 {
		return fmt.Sprintf("還有 %d 則訊息對方尚未讀取，%d 分鐘內對方仍可收到，之後就會刪除。", n, mins)
	}
	return fmt.Sprintf("還有 %d 則訊息對方尚未讀取，已隨關閉刪除。", n)
}

// undelivered asks the server how many of this bot's messages on the bridge
// the other side has not read yet; 0 when unknown (an older server).
func (bs *bridgeState) undelivered() int {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var ud struct {
		Count int `json:"count"`
	}
	if bs.cli.do(ctx, http.MethodGet, "/v1/bridges/"+bs.bc.BridgeID+"/undelivered", nil, &ud) != nil {
		return 0
	}
	return ud.Count
}

// addTo adds the unread count and the end of the last-delivery window to a
// close/leave JSON line when there are unread messages (v0.6.4).
func (o closeOutcome) addTo(m map[string]any) map[string]any {
	if o.Undelivered > 0 {
		m["undelivered"] = o.Undelivered
		if o.FinalUntil != "" {
			m["final_until"] = o.FinalUntil
		}
	}
	return m
}
