package cli

import (
	"context"
	"net/http"
)

// refreshLands fetches GET /lands and overwrites the local cache (DEV_SPEC §12.1,
// §7.8 step 1). It is the CLI's responsibility (not the protocol package) to keep
// the land set and public keys current (DEV_SPEC §19 M1 note).
func (bs *bridgeState) refreshLands(ctx context.Context, ks *keystore) (*landsCache, *cliError) {
	var resp landsResp
	if ce := bs.cli.do(ctx, http.MethodGet, "/v1/bridges/"+bs.bc.BridgeID+"/lands", nil, &resp); ce != nil {
		return nil, ce
	}
	lc := &landsCache{LandSetVersion: resp.LandSetVersion, Lands: resp.Lands}
	if err := ks.saveLands(bs.bc.BridgeID, lc); err != nil {
		return nil, generalErrorf("save lands cache: %v", err)
	}
	return lc, nil
}

// findByHandle returns the land with the given handle, or nil.
func (lc *landsCache) findByHandle(handle string) *cachedLand {
	for i := range lc.Lands {
		if lc.Lands[i].Handle == handle {
			return &lc.Lands[i]
		}
	}
	return nil
}

// findByID returns the land with the given id, or nil.
func (lc *landsCache) findByID(id string) *cachedLand {
	for i := range lc.Lands {
		if lc.Lands[i].LandID == id {
			return &lc.Lands[i]
		}
	}
	return nil
}
