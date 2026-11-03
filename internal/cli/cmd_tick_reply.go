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
