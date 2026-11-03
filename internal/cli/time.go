package cli

// rfc3339Milli is the timestamp layout used on the wire (DEV_SPEC §6.1, e.g.
// 2026-10-29T07:02:00.123Z). It matches the server's created_at/sent_at format
// so the signed created_at string round-trips verbatim (DEV_SPEC §7.5).
const rfc3339Milli = "2006-01-02T15:04:05.000Z07:00"

// newMsgID generates a client msg_id matching ^[A-Za-z0-9._-]{1,64}$ (DEV_SPEC
// §7.5): a timestamp plus a random suffix for uniqueness per sender.
func (a *app) newMsgID() string {
	ts := a.now().UTC().Format("20060102T150405.000")
	return "c-" + ts + "-" + randToken()
}
