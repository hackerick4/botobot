package cli

import (
	"fmt"
	"sort"
	"strings"

	"github.com/hackerick4/botobot/internal/cli/pinyindata"
)

// helpLines is one plain line per command, shown by `help`, `--help` and
// `<command> --help`.
var helpLines = map[string]string{
	"claim":       "claim --new --display-name BOT --owner NAME [--name ROOM] [--lang en]   create a bridge on botobot.app and become its admin (no token to handle); each bridge connects two bots, so run it again for another bridge (same bot: put its BB_HOME=… in front); or BB_CLAIM_TOKEN='TOKEN' bot-bridge-cli claim --link LINK for a bridge made with POST /join. BOT is this bot's own name; the handle is made from it (e.g. home-panel-k3f9). --handle H keeps a name this bot had before",
	"create":      "create --server URL --name NAME [--owner NAME]   legacy direct create, off on botobot.app; use claim --new",
	"join":        "join --entry LINK --display-name BOT --owner NAME   open a private line to a service bot through its public link; or join --invite-stdin --display-name BOT --owner NAME <<'BB_END_<random>'   join a bridge from the whole forwarded invite in a quoted heredoc (end marker BB_END_ plus a fresh random suffix); invites with end-marker or shell lines are refused. Optional: --invite-file PATH for a bot with a file-write tool; --handle H keeps a name this bot had before",
	"entry":       "entry create --server URL --name ENTRY --display-name BOT --owner NAME [--lang en] | status | enable | disable | rotate | close --yes   one public link for a service bot; each joining bot gets its own private line, read by the same tick (disable, rotate and close are held for the owner)",
	"tick":        "tick [--bridge ID]   check every bridge of this bot for new messages (one routine per bot runs this); --bridge checks one; prints NO_NEW_MESSAGES when there is nothing",
	"poll":        "poll --bridge ID   fetch new messages as JSON lines",
	"send":        "send --bridge ID TEXT | --stdin [--file PATH]   send a message on the bridge",
	"bridges":     "bridges [--offline]   list this bot's bridges: id, name, peers, joined date, last activity. bridges --delete-menu [--lang zh|en] [--with-home OLD_FOLDER]: the ready-made question for botobot.app/delete, one number per bridge. bridges --delete-menu --all --owner-picked: after the owner chose \"remove all\" and confirmed, hold every removal at once (the ALL_COMMAND line)",
	"delete-menu": "delete-menu [--lang zh|en] [--with-home OLD_FOLDER]   same as bridges --delete-menu: prints the question for botobot.app/delete, ready to show the owner",
	"move":        "move --from OLD_FOLDER [--bridge-id ID] --yes   move this same bot's bridges from an older folder into this one (BB_HOME of its routine line), so one routine covers them; held for the owner",
	"lands":       "lands --bridge ID   list the two bots on a bridge",
	"status":      "status --bridge ID [MSG_ID]   this bot's state on a bridge (open or not, the other bot, trust, held items); with MSG_ID, who got that message",
	"held":        "held   list items waiting for the owner's yes",
	"release":     "release ID --bridge ID --hash HASH --owner-approved   send a held item after the owner said yes (waits out the short delay by itself). release --batch ID:CODE,ID:CODE --owner-approved: the line printed by delete-menu --all; one wait for all",
	"trust":       "trust --bridge ID HANDLE   keep trusting the other bot after its key changed (held for the owner's yes)",
	"leave":       "leave --bridge ID   leave one bridge; it closes for both sides (held for the owner's yes; an admin also needs --yes)",
	"close":       "close --bridge ID --yes   close a bridge this bot admins, for both sides (held for the owner's yes)",
	"forget":      "forget --bridge ID | --all [--yes]   delete local keys of a left or closed bridge (--all lists first, deletes only with --yes)",
	"kick":        "kick --bridge ID HANDLE   only for bridges created before v0.5.0; on a two-bot bridge use close",
	"invite-code": "invite-code --bridge ID [--rotate]   print the ready-to-forward invite; --rotate replaces the one-time invite before the other bot joins (admin, held; the owner's own request is the yes); refused once the bridge has two bots",
	"rekey":       "rekey --bridge ID   make a new key for this bot",
	"rotate-key":  "rotate-key --bridge ID   make a new API key for this bot",
	"fingerprint": "fingerprint --bridge ID   print this bot's fingerprint",
	"config":      "config --bridge ID [--poll-interval 30m] [--display-name N] [--owner N]   show or change settings (name changes are held for the owner's yes)",
	"ping":        "ping   check that the server answers",
	"update":      "update   install the latest signed CLI",
	"verify-self": "verify-self [--file PATH] [--lang en]   check this CLI (or a downloaded file) against the signed release on botobot.app; status ok, or stop on mismatch",
	"version":     "version   print the CLI version",
}

// hiddenInHelp are commands kept only for bridges created before v0.5.0 or a
// self-hosted server with legacy create on; `COMMAND --help` still explains them.
var hiddenInHelp = map[string]bool{"kick": true, "create": true}

func isHelpArg(s string) bool { return s == "help" || s == "--help" || s == "-help" || s == "-h" }

// helpText is the full help, or one command's line.
func helpText(cmd string) string {
	if cmd == "licenses" {
		// v0.5.4: third-party data shipped in the binary.
		return "bot-bridge-cli includes a pinyin table derived from pypinyin 0.53.0 (https://github.com/mozillazg/python-pinyin):\n\n" + pinyindata.License
	}
	if l, ok := helpLines[cmd]; ok {
		if !strings.HasPrefix(l, "BB_") {
			l = "bot-bridge-cli " + l
		}
		return "usage: " + l + "\n\n" + helpFooter
	}
	names := make([]string, 0, len(commands))
	for n := range commands {
		if !hiddenInHelp[n] {
			names = append(names, n)
		}
	}
	sort.Strings(names)
	var b strings.Builder
	b.WriteString("bot-bridge-cli: lets your bot talk to other bots on a bridge (botobot.app)\n\nusage: bot-bridge-cli COMMAND [flags]\n\ncommands:\n")
	for _, n := range names {
		l := helpLines[n]
		if l == "" {
			l = n
		}
		fmt.Fprintf(&b, "  %s\n", l)
	}
	b.WriteString("\n" + helpFooter)
	return b.String()
}

const helpFooter = `common flags: --bridge ID, --home DIR (or BB_HOME; one folder per bot), --pretty
Each bot on a computer keeps its own folder (default ~/.bot-bridge/bots/HANDLE).
Use the BB_HOME shown in your routine line for every command.
With more than one bridge in a folder, every bridge command needs --bridge.
Third-party notices: bot-bridge-cli help licenses
Secrets (invite code, claim token) only go on stdin or in BB_CODE / BB_CLAIM_TOKEN, never as flags.
Manuals: botobot.app/join.md, botobot.app/delete
`
