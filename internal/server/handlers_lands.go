package server

import (
	"net/http"

	"github.com/hackerick4/botobot/internal/protocol"
	"github.com/hackerick4/botobot/internal/store"
)

// handleListLands implements GET /v1/bridges/{bridge_id}/lands (DEV_SPEC §6.4):
// the current (not-left) land set with the bridge's land_set_version.
func (s *Server) handleListLands(w http.ResponseWriter, r *http.Request, ac *authContext) *apiErr {
	lands, err := s.store.ListActiveLands(r.Context(), ac.bridge.ID)
	if err != nil {
		return storeErr(err)
	}
	out := make([]*landOut, 0, len(lands))
	for _, l := range lands {
		lo, e := s.buildLandOut(r, l)
		if e != nil {
			return e
		}
		out = append(out, lo)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"land_set_version": ac.bridge.LandSetVersion,
		"lands":            out,
	})
	return nil
}

// handleDeleteLand implements DELETE /v1/bridges/{bridge_id}/lands/{land_id}
// (admin kicks, or the land itself leaves) (DEV_SPEC §6.4, §11). Removing the
// admin closes the bridge.
func (s *Server) handleDeleteLand(w http.ResponseWriter, r *http.Request, ac *authContext) *apiErr {
	targetID := r.PathValue("land_id")

	// Authorization: admin may remove anyone; a member only itself.
	self := targetID == ac.land.ID
	if ac.land.Role != "admin" && !self {
		return errForbidden
	}

	target, err := s.store.GetLand(r.Context(), targetID)
	if err != nil {
		return storeErr(err) // ErrNotFound -> 404
	}
	if target.BridgeID != ac.bridge.ID || target.LeftAt != nil {
		return errNotFound
	}

	// Removing the admin (only possible as self-leave) closes the bridge.
	if target.Role == "admin" {
		co, err := s.store.CloseBridgeGrace(r.Context(), ac.bridge.ID, store.CloseAdminLeft, ac.land.ID, s.cfg.CloseGrace)
		if err != nil {
			return storeErr(err)
		}
		s.keyCache.clear()
		s.throttle.ResetBridge(ac.bridge.ID)
		out := map[string]any{"removed": targetID, "bridge_closed": true}
		addCloseOutcome(out, co)
		writeJSON(w, http.StatusOK, out)
		return nil
	}

	reason := "left"
	if !self {
		reason = "kicked"
	}

	// A two-bot bridge (v0.5.0) ends for both sides when the peer leaves or is
	// removed: there is no one left to talk to.
	if ac.bridge.Pair {
		closeReason := store.CloseMemberLeft
		if !self {
			closeReason = store.CloseMemberRemoved
		}
		co, err := s.store.CloseBridgeGrace(r.Context(), ac.bridge.ID, closeReason, ac.land.ID, s.cfg.CloseGrace)
		if err != nil {
			return storeErr(err)
		}
		s.keyCache.clear()
		s.throttle.ResetBridge(ac.bridge.ID)
		out := map[string]any{"removed": targetID, "bridge_closed": true}
		addCloseOutcome(out, co)
		writeJSON(w, http.StatusOK, out)
		return nil
	}
	// Capture the leaver's identity for the event before tearing down state.
	leaverMap := s.landEventMap(r, target)

	if err := s.store.MarkLandLeft(r.Context(), targetID, reason); err != nil {
		return storeErr(err)
	}
	if _, err := s.store.RevokeLandAPIKeys(r.Context(), targetID, ""); err != nil {
		return storeErr(err)
	}
	if _, err := s.store.DropLandInbox(r.Context(), ac.bridge.ID, targetID); err != nil {
		return storeErr(err)
	}
	version, err := s.store.BumpLandSetVersion(r.Context(), ac.bridge.ID)
	if err != nil {
		return storeErr(err)
	}
	s.keyCache.clear() // the removed land's keys are now invalid

	if recips, rerr := s.recipientsExcept(r, ac.bridge.ID, targetID); rerr == nil {
		s.enqueueEvent(r, ac.bridge.ID, recips, protocol.Event{
			Type:           protocol.EventLandLeft,
			Land:           leaverMap,
			Reason:         reason,
			LandSetVersion: version,
		})
	}

	writeJSON(w, http.StatusOK, map[string]any{"removed": targetID, "bridge_closed": false})
	return nil
}
