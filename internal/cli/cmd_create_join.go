package cli

import (
	"context"
	"flag"
	"net/http"
	"net/url"
	"os"
	"strings"

	"github.com/hackerick4/botobot/internal/prompts"
)

func init() {
	commands["create"] = cmdCreate
	commands["join"] = cmdJoin
}

// cmdCreate implements `create` (DEV_SPEC §12.3): generate a keypair, create a
// bridge, persist local state, and print the created record.
func cmdCreate(a *app, args []string) *cliError {
	var (
		name, description, handle, displayName, owner string
		noMemberInvite                                bool
	)
	g, _, e := a.parse("create", args, func(fs *flag.FlagSet) {
		fs.StringVar(&name, "name", "", "bridge name (required)")
		fs.StringVar(&description, "description", "", "bridge description")
		fs.StringVar(&handle, "handle", "", "keep a stable name: the name this bot had before; otherwise leave out (a unique name is picked from --display-name, e.g. home-panel-xxxx)")
		fs.StringVar(&displayName, "display-name", "", "this bot's own name; the bridge name of this bot (handle) is made from it")
		fs.StringVar(&owner, "owner", "", "your owner's name (unverified)")
		fs.BoolVar(&noMemberInvite, "no-member-invite", false, "members may not read the invite code")
	})
	if e != nil {
		return e
	}
	if g.server == "" {
		return usageErrorf("create: --server is required (or set $BB_SERVER)")
	}
	if name == "" {
		return usageErrorf("create: --name is required")
	}
	if e := checkPlaceholderNames("create", handle, owner); e != nil {
		return e
	}

	if e := checkProfileNames(displayName, owner); e != nil {
		return e
	}
	home, handle, hce := onboardHome("create", firstNonEmpty(g.home, os.Getenv("BB_HOME")), handle, displayName, owner, "")
	if hce != nil {
		return hce
	}
	g.home = home

	sess, e := a.newSession(g)
	if e != nil {
		return e
	}

	encPriv, encPub, signPriv, signPub, bindingSig, err := newKeypair()
	if err != nil {
		return generalErrorf("%v", err)
	}

	landsCanInvite := !noMemberInvite
	body := map[string]any{
		"name":        name,
		"description": description,
		"land": landBlockBody{
			Profile: profileBody{Handle: handle, DisplayName: displayName, OwnerName: owner},
			Key:     keyInputBody{EncPub: encPub, SignPub: signPub, BindingSig: bindingSig},
		},
		"settings": map[string]any{"lands_can_invite": landsCanInvite},
	}

	cli := newClient(&http.Client{Timeout: g.timeout}, g.server, "")
	var resp createJoinResp
	if ce := cli.do(context.Background(), http.MethodPost, "/v1/bridges", body, &resp); ce != nil {
		return ce
	}

	if _, _, ce := persistNewBridge(sess, g.server, "admin", handle, "", signPriv, encPriv, &resp, ""); ce != nil {
		return ce
	}

	sess.pr.emit(map[string]any{
		"type":        "created",
		"handle":      handle,
		"bridge_id":   resp.Bridge.BridgeID,
		"invite_link": resp.Bridge.InviteLink,
		"invite_code": resp.InviteCode,
		"land_id":     resp.Land.LandID,
		"fingerprint": resp.Land.Fingerprint,
	})
	return sess.pr.done()
}

// cmdJoin implements `join` (DEV_SPEC §12.3): parse the invite link for the
// server and bridge id, generate a keypair, join with the invite code, and
// persist local state. The code comes only from $BB_CODE (DEV_SPEC §16; --code is refused).
func cmdJoin(a *app, args []string) *cliError {
	var link, code, handle, displayName, owner, adminFP, entryLink string
	var inviteStdin bool
	var inviteFile string
	g, _, e := a.parse("join", args, func(fs *flag.FlagSet) {
		fs.BoolVar(&inviteStdin, "invite-stdin", false, "read the whole forwarded invite (link, code, admin fingerprint) from stdin")
		fs.StringVar(&inviteFile, "invite-file", "", "read the whole forwarded invite from this file instead of stdin")
		fs.StringVar(&adminFP, "admin-fp", "", "admin fingerprint from the invite (管理者指紋), checked after joining (or set $BB_ADMIN_FP)")
		fs.StringVar(&link, "link", "", "invite link, e.g. https://host/b/br_… (required)")
		fs.StringVar(&code, "code", "", "refused: put the invite code in $BB_CODE instead")
		fs.StringVar(&handle, "handle", "", "keep a stable name: the name this bot had before; otherwise leave out (a unique name is picked from --display-name, e.g. home-panel-xxxx)")
		fs.StringVar(&displayName, "display-name", "", "this bot's own name; the bridge name of this bot (handle) is made from it")
		fs.StringVar(&owner, "owner", "", "your owner's name (unverified)")
		fs.StringVar(&entryLink, "entry", "", "a public entry link, e.g. https://host/e/pe_… (no invite code needed)")
	})
	if e != nil {
		return e
	}
	if entryLink != "" {
		if code != "" || link != "" || inviteStdin || inviteFile != "" {
			return usageErrorf("join: --entry takes only the public link; do not also pass --link, --code or an invite")
		}
		return cmdJoinEntry(a, g, entryLink, handle, displayName, owner, adminFP)
	}
	if code != "" {
		return secretInArgv("join", "--code", "BB_CODE")
	}
	if inviteStdin && inviteFile != "" {
		return usageErrorf("join: pass the invite either on stdin (--invite-stdin) or as a file (--invite-file), not both")
	}
	joinLang := ""
	if inviteStdin || inviteFile != "" {
		if link != "" || adminFP != "" {
			return usageErrorf("join: --invite-stdin / --invite-file read the link and admin fingerprint from the invite; do not also pass --link or --admin-fp")
		}
		text, ie := a.readInviteText(inviteFile)
		if ie != nil {
			return ie
		}
		l, c, fp, perr := parseInvite(text)
		if perr != nil {
			return &cliError{Code: "invite_unreadable", Message: "join: " + perr.Error() + ". Ask the owner to forward the invite again, unchanged.", Exit: exitUsage}
		}
		link, code, adminFP = l, c, fp
		// An English invite means an English-speaking owner: keep the
		// owner notices on this bridge in English too (v0.4.9).
		if strings.Contains(strings.ToLower(text), "invite code") && !strings.Contains(text, "邀請碼") {
			joinLang = "en"
		}
	} else {
		code = a.getenv("BB_CODE")
		if adminFP == "" {
			adminFP = os.Getenv("BB_ADMIN_FP")
		}
	}
	if link == "" {
		return usageErrorf("join: --link is required (or pass the whole invite with --invite-stdin)")
	}
	if code == "" {
		return usageErrorf("join: pass the whole invite with --invite-file or --invite-stdin, or set the invite code in BB_CODE, e.g. BB_CODE='123456' bot-bridge-cli join --link LINK --handle H")
	}
	if e := checkPlaceholderNames("join", handle, owner); e != nil {
		return e
	}
	server, bridgeID, perr := parseInviteLink(link)
	if perr != nil {
		return usageErrorf("join: %v", perr)
	}

	if e := checkProfileNames(displayName, owner); e != nil {
		return e
	}
	home, handle, hce := onboardHome("join", firstNonEmpty(g.home, os.Getenv("BB_HOME")), handle, displayName, owner, bridgeID)
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
		"code": code,
		"land": landBlockBody{
			Profile: profileBody{Handle: handle, DisplayName: displayName, OwnerName: owner},
			Key:     keyInputBody{EncPub: encPub, SignPub: signPub, BindingSig: bindingSig},
		},
	}

	cli := newClient(&http.Client{Timeout: g.timeout}, server, "")
	var resp createJoinResp
	if ce := cli.do(context.Background(), http.MethodPost, "/v1/bridges/"+bridgeID+"/join", body, &resp); ce != nil {
		if ce.Code == "bridge_full" {
			// The owner hears it in plain words (v0.5.1); the error line follows.
			if prompts.IsEnglish(joinLang) {
				sess.pr.raw("NOTIFY_OWNER: This invite was already used. Ask the other person to create a new bridge and send a new invite.")
			} else {
				sess.pr.raw("NOTIFY_OWNER: 此邀請已用過。請對方另建一個 bridge，再傳新的邀請。")
			}
			_ = sess.pr.done()
		}
		return ce
	}

	gotAdmin, check, ce := persistNewBridge(sess, server, "member", handle, joinLang, signPriv, encPriv, &resp, adminFP)
	if ce != nil {
		return ce
	}

	routine := sess.routinePrompt()
	out := map[string]any{
		"type":                    "joined",
		"handle":                  handle,
		"bridge_id":               resp.Bridge.BridgeID,
		"land_id":                 resp.Land.LandID,
		"fingerprint":             resp.Land.Fingerprint,
		"poll_interval_sec":       resp.Land.PollIntervalSec,
		"admin_fingerprint":       gotAdmin,
		"admin_fingerprint_check": check,
	}
	if check != fpMismatch {
		out["routine_prompt"] = routine
	}
	sess.pr.emit(out)
	sess.pr.raw(joinOwnerNotice(resp.Bridge.BridgeID, resp.Bridge.Name, gotAdmin, check, joinLang))
	if check == fpMismatch {
		_ = sess.pr.done()
		return &cliError{Code: "admin_fingerprint_mismatch", Message: "join: the admin fingerprint does not match the invite; stop, do not create the routine, and tell your owner", Exit: exitGeneral}
	}
	sess.printRoutine(routine, prior, joinLang, sess.ks.entryCount() > 0)
	return sess.pr.done()
}

// persistNewBridge writes the per-bridge keystore files for a freshly created or
// joined bridge and records it as the default (DEV_SPEC §12.1).
func persistNewBridge(sess *session, server, role, handle, lang string, signPriv, encPriv []byte, resp *createJoinResp, expectAdminFP string) (adminFP, fpCheck string, ce *cliError) {
	nowMS := sess.app.now().UnixMilli()
	km := &keyMaterial{
		EncPriv:   encPriv,
		SignPriv:  signPriv,
		KeyID:     resp.Land.KeyID,
		CreatedAt: nowMS,
	}
	bc := &bridgeConfig{
		Server:   strings.TrimRight(server, "/"),
		BridgeID: resp.Bridge.BridgeID,
		LandID:   resp.Land.LandID,
		Handle:   handle,
		Role:     role,
		Name:     resp.Bridge.Name,
		JoinedAt: nowMS,
		Lang:     lang,
		Pair:     resp.Bridge.Pair,
	}
	sess.ks.saveIdentity(handle)
	if err := sess.ks.initBridge(bc, km, resp.APIKey); err != nil {
		return "", "", generalErrorf("save bridge state: %v", err)
	}
	// Record as the default bridge so single-bridge use needs no --bridge.
	cfg, err := sess.ks.loadConfig()
	if err != nil {
		return "", "", generalErrorf("read config: %v", err)
	}
	if cfg.DefaultBridge == "" {
		cfg.DefaultBridge = resp.Bridge.BridgeID
		if err := sess.ks.saveConfig(cfg); err != nil {
			return "", "", generalErrorf("save config: %v", err)
		}
	}

	// Pin the roster present at join/claim as trusted (TOFU). A fetch failure
	// here is non-fatal: the next send/tick pins the roster on first use.
	hc := &http.Client{Timeout: sess.g.timeout}
	pbs := &bridgeState{bc: bc, cli: newClient(hc, bc.Server, resp.APIKey)}
	fpCheck = fpUnavailable
	if lc, e := pbs.refreshLands(context.Background(), sess.ks); e == nil {
		if role != "admin" {
			adminFP, fpCheck = checkAdminFP(lc, bc.LandID, expectAdminFP)
		}
		// On an admin fingerprint mismatch nothing is pinned: every land stays
		// untrusted, so nothing can be sent until the owner sorts it out.
		if pins, err := sess.ks.loadPins(bc.BridgeID); err == nil {
			if fpCheck == fpMismatch {
				pins.Locked = "admin_fingerprint_mismatch"
			} else {
				pins.pinInitial(lc, bc.LandID, nowMS)
			}
			_ = sess.ks.savePins(bc.BridgeID, pins)
		}
	} else if role != "admin" && strings.TrimSpace(expectAdminFP) != "" {
		// Could not compare now: compare against the first admin key seen.
		if pins, err := sess.ks.loadPins(bc.BridgeID); err == nil {
			pins.ExpectAdminFP = expectAdminFP
			_ = sess.ks.savePins(bc.BridgeID, pins)
		}
	}
	return adminFP, fpCheck, nil
}

// parseInviteLink extracts the server origin and bridge id from an invite link
// of the form <server>/b/<bridge_id> (DEV_SPEC §6.4 page, §12.3 join). Any path
// prefix before /b/ is preserved as part of the server base.
func parseInviteLink(link string) (server, bridgeID string, err error) {
	u, perr := url.Parse(strings.TrimSpace(link))
	if perr != nil {
		return "", "", &parseLinkError{"invalid link URL"}
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", "", &parseLinkError{"link must be an http(s) URL"}
	}
	if u.Host == "" {
		return "", "", &parseLinkError{"link has no host"}
	}
	idx := strings.Index(u.Path, "/b/")
	if idx < 0 {
		return "", "", &parseLinkError{"link must contain /b/<bridge_id>"}
	}
	bridgeID = strings.Trim(u.Path[idx+len("/b/"):], "/")
	if bridgeID == "" || !strings.HasPrefix(bridgeID, "br_") || strings.Contains(bridgeID, "/") {
		return "", "", &parseLinkError{"link does not contain a valid bridge id"}
	}
	server = u.Scheme + "://" + u.Host + u.Path[:idx]
	return server, bridgeID, nil
}

type parseLinkError struct{ msg string }

func (e *parseLinkError) Error() string { return e.msg }
