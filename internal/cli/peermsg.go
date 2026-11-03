package cli

import (
	"time"
)

// Staleness bounds for a message's self-reported sent_at (Batch 1 cheap fixes).
// A message claiming to be older than 24h + skew, or more than skew in the
// future, is accepted off the queue (acked) but not displayed: with a 24h inbox
// TTL a much older timestamp is either a clock problem or a replay, and content
// the bot might act on should not carry a misleading time.
const (
	staleMaxAge  = 24*time.Hour + 5*time.Minute
	staleSkew    = 5 * time.Minute
	sentAtLayout = "2006-01-02T15:04:05.000Z07:00"
)

// isStaleSentAt reports whether sentAt is implausibly old or in the future
// relative to now. An unparseable or empty timestamp is treated as not stale
// (we prefer to show the message rather than silently swallow it on a format
// quirk); the caller still shows sent_at verbatim.
func isStaleSentAt(sentAt string, now time.Time) bool {
	if sentAt == "" {
		return false
	}
	t, err := time.Parse(sentAtLayout, sentAt)
	if err != nil {
		if t, err = time.Parse(time.RFC3339, sentAt); err != nil {
			return false
		}
	}
	t = t.UTC()
	if now.Sub(t) > staleMaxAge {
		return true
	}
	if t.Sub(now) > staleSkew {
		return true
	}
	return false
}

// writeFullText writes the full, untruncated decrypted text of a message to
// inbox/<msgid>.txt (0600) so a truncated display line can point at it. It
// returns the absolute path, or "" with no error when text is empty.
func writeFullText(ks *keystore, bridgeID, msgID, text string) (string, *cliError) {
	if text == "" {
		return "", nil
	}
	path := ks.inboxTextPath(bridgeID, msgID)
	if err := writeFileAtomic(path, []byte(text), privFilePerm); err != nil {
		return "", generalErrorf("write message text: %v", err)
	}
	return absOr(path), nil
}
