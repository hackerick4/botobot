package cli

import (
	"context"
)

func init() { commands["trust"] = cmdTrust }

// cmdTrust implements `trust <handle|land_id>` (Batch 1 item 4): the owner has
// confirmed a member's fingerprint out of band, so pin that member's current key
// as trusted. This is the only way a new member or a changed key becomes
// sendable again. Routine/onboarding prompts tell the bot to ask the owner
// first; the CLI itself performs no verification beyond recomputing the pin.
func cmdTrust(a *app, args []string) *cliError {
	g, fs, e := a.parse("trust", args, nil)
	if e != nil {
		return e
	}
	target := fs.Arg(0)
	if target == "" {
		return usageErrorf("trust: a member handle or land_id is required")
	}
	sess, e := a.newSession(g)
	if e != nil {
		return e
	}
	bs, e := sess.resolveBridge()
	if e != nil {
		return e
	}
	lc, e := bs.refreshLands(context.Background(), sess.ks)
	if e != nil {
		return e
	}
	land := lc.findByHandle(target)
	if land == nil {
		land = lc.findByID(target)
	}
	if land == nil {
		return usageErrorf("trust: no land with handle or id %q", target)
	}
	if land.LandID == bs.bc.LandID {
		return usageErrorf("trust: cannot trust your own land")
	}
	pins, err := sess.ks.loadPins(bs.bc.BridgeID)
	if err != nil {
		return generalErrorf("read pins: %v", err)
	}
	if !pins.trust(land, a.now().UnixMilli()) {
		return generalErrorf("trust: %q has no usable key (missing active key or invalid binding)", target)
	}
	if err := sess.ks.savePins(bs.bc.BridgeID, pins); err != nil {
		return generalErrorf("save pins: %v", err)
	}
	sess.pr.emit(map[string]any{
		"type":        "trusted",
		"land_id":     land.LandID,
		"handle":      land.Handle,
		"fingerprint": computeFingerprint(land),
	})
	return sess.pr.done()
}
