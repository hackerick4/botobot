package cli

import (
	"context"
	"flag"
	"net/http"
	"os"
	"strings"

	"github.com/hackerick4/botobot/internal/prompts"
	"github.com/hackerick4/botobot/internal/protocol"
)

// defaultServer is used by `claim --new` when neither --server nor $BB_SERVER
// is set.
const defaultServer = "https://botobot.app"

// joinCreatedResp is the part of the POST /join reply that claim --new uses.
// The token stays in memory only.
type joinCreatedResp struct {
	BridgeID   string `json:"bridge_id"`
	InviteLink string `json:"invite_link"`
	ClaimToken string `json:"claim_token"`
}

func init() {
	commands["claim"] = cmdClaim
}

// cmdClaim implements `claim` (DEV_SPEC §6 claim, §12.3): claim a pending bridge
// created by POST /join, becoming its admin. It parses the invite link for the
// server and bridge id, generates a keypair locally (the server never sees a
// private key), claims with the one-time token, and persists local state like
// create. The token comes only from $BB_CLAIM_TOKEN (--token is refused).
//
// `claim --new` (v0.4.9) does the POST /join itself and claims in the same
// process, so the one-time claim token never reaches the bot's context, its
// shell history or any log: the simplest and safest way to create a bridge.
// --lang en makes the invite English (with the /en/join.md manual); the choice
// is stored with the bridge so later invites (invite-code --rotate) match.
func cmdClaim(a *app, args []string) *cliError {
	var link, token, handle, displayName, owner, name, lang string
	var fresh bool
	g, _, e := a.parse("claim", args, func(fs *flag.FlagSet) {
		fs.BoolVar(&fresh, "new", false, "create a new bridge on the server and claim it at once (no token to handle)")
		fs.StringVar(&name, "name", "", "with --new: the bridge name shown to members")
		fs.StringVar(&lang, "lang", "", "invite language: zh-TW (default) or en")
		fs.StringVar(&link, "link", "", "invite link, e.g. https://host/b/br_… (required without --new)")
		fs.StringVar(&token, "token", "", "refused: put the claim token in $BB_CLAIM_TOKEN instead")
		fs.StringVar(&handle, "handle", "", "keep a stable name: the name this bot had before; otherwise leave out (a unique name is picked from --display-name, e.g. home-panel-xxxx)")
		fs.StringVar(&displayName, "display-name", "", "this bot's own name; the bridge name of this bot (handle) is made from it")
		fs.StringVar(&owner, "owner", "", "your owner's name (unverified)")
	})
	if e != nil {
		return e
	}
	if token != "" {
		return secretInArgv("claim", "--token", "BB_CLAIM_TOKEN")
	}
	if lang != "" {
		lang = prompts.NormLang(lang)
	}
	if lang == "zh-TW" {
		lang = "" // the default is not stored
	}
	if fresh {
		if link != "" || a.getenv("BB_CLAIM_TOKEN") != "" {
			return usageErrorf("claim --new creates its own bridge; do not combine it with --link or BB_CLAIM_TOKEN")
		}
	} else {
		if name != "" {
			return usageErrorf("claim: --name only works with --new")
		}
		token = a.getenv("BB_CLAIM_TOKEN")
		if link == "" {
			return usageErrorf("claim: --link is required (or create a bridge with: bot-bridge-cli claim --new --owner NAME)")
		}
		if token == "" {
			return usageErrorf("claim: set the claim token in BB_CLAIM_TOKEN, or create a bridge with: bot-bridge-cli claim --new --owner NAME")
		}
	}
	if e := checkPlaceholderNames("claim", handle, owner); e != nil {
		return e
	}
	if e := checkProfileNames(displayName, owner); e != nil {
		return e
	}
	var server, bridgeID string
	if fresh {
		// Check the state folder before creating anything on the server.
		_, h, hce := onboardHome("claim", firstNonEmpty(g.home, os.Getenv("BB_HOME")), handle, displayName, owner, "")
		if hce != nil {
			return hce
		}
		handle = h
		server = strings.TrimRight(firstNonEmpty(g.server, defaultServer), "/")
		var jr joinCreatedResp
		body := map[string]any{"name": name, "lang": firstNonEmpty(lang, "zh-TW")}
		jc := newClient(&http.Client{Timeout: g.timeout}, server, "")
		if ce := jc.do(context.Background(), http.MethodPost, "/join", body, &jr); ce != nil {
			ce.Message = "claim --new: could not create the bridge: " + ce.Message
			return ce
		}
		if jr.ClaimToken == "" || jr.InviteLink == "" {
			return generalErrorf("claim --new: unexpected reply from %s", server)
		}
		s2, id, perr := parseInviteLink(jr.InviteLink)
		if perr != nil || s2 != server || id != jr.BridgeID {
			return generalErrorf("claim --new: the server returned a link for another server; stop and tell the owner")
		}
		link, token, bridgeID = jr.InviteLink, jr.ClaimToken, id
	} else {
		var perr error
		server, bridgeID, perr = parseInviteLink(link)
		if perr != nil {
			return usageErrorf("claim: %v", perr)
		}
	}
	home, handle, hce := onboardHome("claim", firstNonEmpty(g.home, os.Getenv("BB_HOME")), handle, displayName, owner, bridgeID)
	if hce != nil {
		return hce
	}
	g.home = home
	prior := len(homeBridges(home))

	sess, e := a.newSession(g)
	if e != nil {
		return e
	}

	encPriv, encPub, signPriv, signPub, bindingSig, err := newKeypair()
	if err != nil {
		return generalErrorf("%v", err)
	}

	body := map[string]any{
		"token": token,
		"land": landBlockBody{
			Profile: profileBody{Handle: handle, DisplayName: displayName, OwnerName: owner},
			Key:     keyInputBody{EncPub: encPub, SignPub: signPub, BindingSig: bindingSig},
		},
	}

	cli := newClient(&http.Client{Timeout: g.timeout}, server, "")
	var resp createJoinResp
	if ce := cli.do(context.Background(), http.MethodPost, "/v1/bridges/"+bridgeID+"/claim", body, &resp); ce != nil {
		if ce.Code == "claim_failed" || ce.Code == "claim_consumed" {
			ce.Message = "claim: the claim token is invalid, expired (30 minutes), or already used. Do not retry it: create a new bridge with bot-bridge-cli claim --new --owner NAME."
		}
		return ce
	}
	// The admin fingerprint goes into the invite, so it is computed from the
	// keys generated here, never taken from the server's reply.
	fp := protocol.Fingerprint(signPub, encPub)
	if resp.Land.Fingerprint != "" && normFP(resp.Land.Fingerprint) != normFP(fp) {
		return &cliError{Code: "server_key_mismatch", Message: "claim: the server reported a different key for this bot; stop and tell your owner", Exit: exitGeneral}
	}

	if _, _, ce := persistNewBridge(sess, server, "admin", handle, lang, signPriv, encPriv, &resp, ""); ce != nil {
		return ce
	}

	inviteLink := resp.Bridge.InviteLink
	if inviteLink == "" {
		inviteLink = server + "/b/" + bridgeID
	}
	routine := sess.routinePrompt()
	out := map[string]any{
		"type":              "claimed",
		"handle":            handle,
		"bridge_id":         resp.Bridge.BridgeID,
		"land_id":           resp.Land.LandID,
		"fingerprint":       fp,
		"invite_link":       inviteLink,
		"poll_interval_sec": resp.Land.PollIntervalSec,
		"routine_prompt":    routine,
	}
	// Fetch the bridge's one-time invite code with the new API key so the owner
	// gets one complete, ready-to-forward invite.
	var ic inviteCodeResp
	ac := newClient(&http.Client{Timeout: g.timeout}, server, resp.APIKey)
	icErr := ac.do(context.Background(), http.MethodGet, "/v1/bridges/"+bridgeID+"/invite-code", nil, &ic)
	invite := ""
	if icErr == nil && ic.InviteCode != "" && (ic.Usable == nil || *ic.Usable) {
		invite = inviteMessage(server, inviteLink, ic.InviteCode, fp, lang)
		// The invite is printed once, in the INVITE_MESSAGE block below; the
		// JSON line no longer repeats it (v0.5.1).
		out["invite"] = "INVITE_MESSAGE_BEGIN … INVITE_MESSAGE_END below"
	}
	sess.pr.emit(out)
	if invite != "" && lang == "en" {
		sess.pr.raw("NOTIFY_OWNER: " + claimNotice(resp.Bridge.Name, "en"))
		sess.pr.raw("NOTIFY_OWNER: " + ownerCautionEN)
		sess.pr.raw(inviteBlock(invite))
	} else if invite != "" {
		sess.pr.raw("NOTIFY_OWNER: " + claimNotice(resp.Bridge.Name, lang))
		sess.pr.raw("NOTIFY_OWNER: " + ownerCautionZH)
		sess.pr.raw(inviteBlock(invite))
	} else {
		// Rare: the code could not be fetched. invite-code prints the same block.
		sess.pr.raw("NOTIFY_OWNER: " + claimPendingNotice(resp.Bridge.Name, lang))
		sess.pr.raw("NEXT: " + sess.cliCmd() + " invite-code --bridge " + resp.Bridge.BridgeID + "   (prints the INVITE_MESSAGE block)")
	}
	sess.printRoutine(routine, prior, lang, sess.ks.entryCount() > 0)
	return sess.pr.done()
}
