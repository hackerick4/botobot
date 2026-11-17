package cli

import (
	"flag"
	"strings"
)

// cmdTickReply implements `tick reply <k> [--stdin|TEXT]` (Batch 1 items 1, 2):
// reply to the message that the last tick labelled #k. The short id -> msg_id
// mapping is read from tick.json; the reply is sent with reply_to set, going
// through the same TOFU gate and chat-loop limits as `send`.
func cmdTickReply(a *app, args []string) *cliError {
	// The documented form puts the message number first (`tick reply 1 --bridge
	// … --stdin`), but Go's flag package stops at the first non-flag token. Pull a
	// leading positional off as the number before parsing the flags.
	var k string
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		k = strings.TrimPrefix(args[0], "#")
		args = args[1:]
	}
	var stdin bool
	var mentions stringSlice
	var files stringSlice
	g, fs, e := a.parse("tick reply", args, func(fs *flag.FlagSet) {
		fs.BoolVar(&stdin, "stdin", false, "read the reply body from stdin (preferred; use a quoted heredoc)")
		fs.Var(&mentions, "mention", "handle to mention (repeatable)")
		fs.Var(&files, "file", "attachment file path (repeatable)")
	})
	if e != nil {
		return e
	}
	rest := fs.Args()
	if k == "" {
		if len(rest) == 0 {
			return usageErrorf("tick reply: a message number is required, e.g. `tick reply 1 --bridge <id> --stdin`")
		}
		k = strings.TrimPrefix(rest[0], "#")
		rest = rest[1:]
	}

	var text string
	if stdin {
		if len(rest) > 0 {
			return usageErrorf("tick reply: pass the body via --stdin OR as TEXT, not both")
		}
		var ce *cliError
		if text, ce = readStdinBody(a); ce != nil {
			return ce
		}
	} else {
		text = strings.Join(rest, " ")
	}
	if text == "" && len(files) == 0 {
		return usageErrorf("tick reply: provide a reply (via --stdin or TEXT) and/or at least one --file")
	}

	sess, e := a.newSession(g)
	if e != nil {
		return e
	}
	bs, e := sess.resolveBridgeStrict()
	if e != nil {
		return e
	}
	ts, err := sess.ks.loadTickState(bs.bc.BridgeID)
	if err != nil {
		return generalErrorf("read tick state: %v", err)
	}
	msgID, ok := ts.ShortIDs[k]
	if !ok {
		return usageErrorf("tick reply: unknown message number %q; run tick again to get current numbers", k)
	}

	mentionHandles := map[string]struct{}{}
	for _, h := range mentions {
		if h = strings.TrimSpace(h); h != "" {
			mentionHandles[h] = struct{}{}
		}
	}

	return runSend(a, g, sendRequest{
		text:           text,
		files:          files,
		mentionHandles: mentionHandles,
		replyTo:        msgID,
	})
}

// cmdTickAutoReply implements `tick auto-reply <yes|no> --bridge br_…` (t434u):
// record the owner's per-bridge preference for whether the bot may keep
// replying to ordinary messages on its own (reply-then-report) or must ask
// first. The preference is stored in this bridge's tick.json and surfaced to
// the bot on later ticks as an AUTO_REPLY_PREF line. It never changes the
// "always ask first" actions in tickRules item 5.
func cmdTickAutoReply(a *app, args []string) *cliError {
	// The preference is a leading positional (yes|no); pull it before flags, as
	// `tick reply` does with its message number.
	var pref string
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		pref = args[0]
		args = args[1:]
	}
	g, fs, e := a.parse("tick auto-reply", args, func(fs *flag.FlagSet) {})
	if e != nil {
		return e
	}
	rest := fs.Args()
	if pref == "" && len(rest) > 0 {
		pref = rest[0]
		rest = rest[1:]
	}
	pref = strings.ToLower(strings.TrimSpace(pref))
	if pref != "yes" && pref != "no" {
		return usageErrorf("tick auto-reply: say yes or no, e.g. `tick auto-reply yes --bridge <id>`")
	}
	if len(rest) > 0 {
		return usageErrorf("tick auto-reply: unexpected extra argument %q", rest[0])
	}

	sess, e := a.newSession(g)
	if e != nil {
		return e
	}
	bs, e := sess.resolveBridgeStrict()
	if e != nil {
		return e
	}
	ts, err := sess.ks.loadTickState(bs.bc.BridgeID)
	if err != nil {
		return generalErrorf("read tick state: %v", err)
	}
	ts.AutoReplyPref = pref
	if err := sess.ks.saveTickState(bs.bc.BridgeID, ts); err != nil {
		return generalErrorf("save tick state: %v", err)
	}
	sess.pr.emit(map[string]any{
		"type":            "auto_reply_pref",
		"bridge_id":       bs.bc.BridgeID,
		"auto_reply_pref": pref,
	})
	sess.pr.raw("AUTO_REPLY_PREF_SET: " + pref)
	return sess.pr.done()
}
