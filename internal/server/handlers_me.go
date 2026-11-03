package server

import (
	"net/http"

	"github.com/hackerick4/botobot/internal/protocol"
	"github.com/hackerick4/botobot/internal/store"
)

// handleGetMe implements GET /v1/me (DEV_SPEC §6.4): the caller's land, bridge,
// and the metadata of the API key being used.
func (s *Server) handleGetMe(w http.ResponseWriter, r *http.Request, ac *authContext) *apiErr {
	lo, e := s.buildLandOut(r, ac.land)
	if e != nil {
		return e
	}
	bo, e := s.buildBridgeOut(r, ac.bridge)
	if e != nil {
		return e
	}
	k, err := s.store.GetAPIKey(r.Context(), ac.keyID)
	if err != nil {
		return storeErr(err)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"land":   lo,
		"bridge": bo,
		"api_key": map[string]any{
			"key_id":     k.ID,
			"prefix":     k.Prefix,
			"created_at": rfc3339(k.CreatedAt),
		},
	})
	return nil
}

type patchMeReq struct {
	DisplayName     *string `json:"display_name"`
	OwnerName       *string `json:"owner_name"`
	PollIntervalSec *int    `json:"poll_interval_sec"`
}

// handlePatchMe implements PATCH /v1/me (DEV_SPEC §6.4).
func (s *Server) handlePatchMe(w http.ResponseWriter, r *http.Request, ac *authContext) *apiErr {
	var req patchMeReq
	if e := s.decode(w, r, &req); e != nil {
		return e
	}
	var up store.UpdateLandProfileParams
	if req.DisplayName != nil {
		dn, err := protocol.NormalizeProfileName("display_name", *req.DisplayName)
		if err != nil {
			return badRequest(err.Error())
		}
		up.DisplayName = &dn
	}
	if req.OwnerName != nil {
		on, err := protocol.NormalizeProfileName("owner_name", *req.OwnerName)
		if err != nil {
			return badRequest(err.Error())
		}
		up.OwnerName = &on
	}
	if req.PollIntervalSec != nil {
		if *req.PollIntervalSec < 60 || *req.PollIntervalSec > 86400 {
			return badRequest("poll_interval_sec must be 60..86400")
		}
		up.PollIntervalSec = req.PollIntervalSec
	}
	if err := s.store.UpdateLandProfile(r.Context(), ac.land.ID, up); err != nil {
		return storeErr(err)
	}
	land, err := s.store.GetLand(r.Context(), ac.land.ID)
	if err != nil {
		return storeErr(err)
	}
	lo, e := s.buildLandOut(r, land)
	if e != nil {
		return e
	}
	writeJSON(w, http.StatusOK, map[string]any{"land": lo})
	return nil
}

// handlePutPublicKey implements PUT /v1/me/public-key (rekey) (DEV_SPEC §6.4):
// verify the key binding, supersede the old key, bump land_set_version, and
// notify the other lands with a key_changed event.
func (s *Server) handlePutPublicKey(w http.ResponseWriter, r *http.Request, ac *authContext) *apiErr {
	var req landKeyInput
	if e := s.decode(w, r, &req); e != nil {
		return e
	}
	if e := req.validate(); e != nil {
		return e
	}

	fingerprint := protocol.Fingerprint(req.SignPub, req.EncPub)
	keyID, err := s.store.InsertPublicKey(r.Context(), store.InsertPublicKeyParams{
		LandID:      ac.land.ID,
		EncPub:      req.EncPub,
		SignPub:     req.SignPub,
		BindingSig:  req.BindingSig,
		Fingerprint: fingerprint,
	})
	if err != nil {
		return storeErr(err)
	}
	if ac.land.ActiveKeyID != nil {
		if err := s.store.SupersedeKey(r.Context(), *ac.land.ActiveKeyID); err != nil {
			return storeErr(err)
		}
	}
	if err := s.store.SetLandActiveKey(r.Context(), ac.land.ID, keyID); err != nil {
		return storeErr(err)
	}
	version, err := s.store.BumpLandSetVersion(r.Context(), ac.bridge.ID)
	if err != nil {
		return storeErr(err)
	}

	land, err := s.store.GetLand(r.Context(), ac.land.ID)
	if err != nil {
		return storeErr(err)
	}
	if recips, rerr := s.recipientsExcept(r, ac.bridge.ID, ac.land.ID); rerr == nil {
		s.enqueueEvent(r, ac.bridge.ID, recips, protocol.Event{
			Type:           protocol.EventKeyChanged,
			Land:           s.landEventMap(r, land),
			NewKey:         s.landEventMap(r, land),
			LandSetVersion: version,
		})
	}
	lo, e := s.buildLandOut(r, land)
	if e != nil {
		return e
	}
	writeJSON(w, http.StatusOK, map[string]any{"land": lo})
	return nil
}

type apiKeysReq struct {
	RevokeOthers bool `json:"revoke_others"`
}

// handleCreateAPIKey implements POST /v1/me/api-keys (DEV_SPEC §6.4): issue a new
// key, optionally revoking the land's other keys (rotation).
func (s *Server) handleCreateAPIKey(w http.ResponseWriter, r *http.Request, ac *authContext) *apiErr {
	var req apiKeysReq
	if e := s.decode(w, r, &req); e != nil {
		return e
	}
	full, keyID, e := s.issueAPIKey(r, ac.land.ID)
	if e != nil {
		return e
	}
	if req.RevokeOthers {
		if _, err := s.store.RevokeLandAPIKeys(r.Context(), ac.land.ID, keyID); err != nil {
			return storeErr(err)
		}
		s.keyCache.clear()
	}
	writeJSON(w, http.StatusCreated, map[string]any{"key_id": keyID, "api_key": full})
	return nil
}

// handleDeleteAPIKey implements DELETE /v1/me/api-keys/{key_id} (DEV_SPEC §6.4).
// If the admin revokes its last live key the bridge closes (admin_keys_revoked).
func (s *Server) handleDeleteAPIKey(w http.ResponseWriter, r *http.Request, ac *authContext) *apiErr {
	keyID := r.PathValue("key_id")
	k, err := s.store.GetAPIKey(r.Context(), keyID)
	if err != nil {
		return storeErr(err)
	}
	if k.LandID != ac.land.ID {
		return errNotFound
	}
	if err := s.store.RevokeAPIKey(r.Context(), keyID); err != nil {
		return storeErr(err)
	}
	s.keyCache.clear()

	if ac.land.Role == "admin" {
		n, err := s.store.CountActiveAPIKeys(r.Context(), ac.land.ID)
		if err != nil {
			return storeErr(err)
		}
		if n == 0 {
			if err := s.store.CloseBridge(r.Context(), ac.bridge.ID, store.CloseAdminKeysRevoked); err != nil {
				return storeErr(err)
			}
			s.keyCache.clear()
			s.throttle.ResetBridge(ac.bridge.ID)
			writeJSON(w, http.StatusOK, map[string]any{"revoked": keyID, "bridge_closed": true})
			return nil
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"revoked": keyID, "bridge_closed": false})
	return nil
}

// handlePing implements POST /v1/me/ping (DEV_SPEC §6.4): updates admin activity
// (via the authed wrapper) and returns the server time.
func (s *Server) handlePing(w http.ResponseWriter, r *http.Request, ac *authContext) *apiErr {
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":          true,
		"server_time": rfc3339(s.clock.Now().UnixMilli()),
	})
	return nil
}
