package cli

import (
	"context"
	"net/http"
)

// fetchBridge reads GET /v1/bridges/{id}.
func (bs *bridgeState) fetchBridge() (*bridgeResp, *cliError) {
	var resp struct {
		Bridge bridgeResp `json:"bridge"`
	}
	if ce := bs.cli.do(context.Background(), http.MethodGet, "/v1/bridges/"+bs.bc.BridgeID, nil, &resp); ce != nil {
		return nil, ce
	}
	return &resp.Bridge, nil
}

// pairFullErr refuses adding anyone to a two-bot bridge whose peer has joined.
func pairFullErr() *cliError {
	return &cliError{Code: "bridge_full", Exit: exitGeneral, Message: "this bridge already connects two bots and takes no one else. " +
		"To connect another bot, the owner creates a new bridge (botobot.app); each bridge has its own --bridge id"}
}

// pairFull reports whether this is a two-bot bridge whose peer has joined,
// asking the server and recording the pair flag locally. A failed lookup
// falls back to bridge.json.
func (bs *bridgeState) pairFull(sess *session) bool {
	br, ce := bs.fetchBridge()
	if ce != nil {
		return bs.bc.Pair
	}
	if br.Pair && !bs.bc.Pair {
		bs.bc.Pair = true
		_ = sess.ks.saveBridge(bs.bc)
	}
	return br.Pair && br.LandCount >= 2
}

// isPair reports whether this is a two-bot bridge (v0.5.0). The server is
// asked first; bridge.json is the fallback when it cannot be reached.
func (bs *bridgeState) isPair() bool {
	br, ce := bs.fetchBridge()
	if ce != nil {
		return bs.bc.Pair
	}
	return br.Pair
}

// useCloseErr refuses kick on a two-bot bridge: removing the other bot is the
// same as ending the bridge, which is close (admin) or leave.
func useCloseErr(bridgeID string) *cliError {
	return &cliError{Code: "use_close", Exit: exitUsage, Message: "kick: each bridge connects two bots, so removing the other bot ends the bridge. " +
		"Only if the owner asked in the private chat to end it, run: bot-bridge-cli close --bridge " + bridgeID + " --yes (held for the owner's yes)"}
}
