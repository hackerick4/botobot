package server

import (
	"net/http"
)

type ackReq struct {
	IDs []string `json:"ids"`
}

// handleAck implements POST /v1/bridges/{bridge_id}/inbox/ack (DEV_SPEC §6.4,
// §10). It hard-deletes the caller's named items, marks message deliveries
// delivered, and reclaims the quota when a message's refcount reaches zero.
// Acking an unknown or already-deleted id is not an error (idempotent): such ids
// are reported under "unknown".
func (s *Server) handleAck(w http.ResponseWriter, r *http.Request, ac *authContext) *apiErr {
	var req ackReq
	if e := s.decode(w, r, &req); e != nil {
		return e
	}
	if len(req.IDs) < 1 || len(req.IDs) > 500 {
		return badRequest("ids must contain 1..500 entries")
	}
	for _, id := range req.IDs {
		if id == "" {
			return badRequest("ids must not contain empty values")
		}
	}

	res, err := s.store.Ack(r.Context(), ac.bridge.ID, ac.land.ID, req.IDs)
	if err != nil {
		return storeErr(err)
	}
	unknown := res.Unknown
	if unknown == nil {
		unknown = []string{}
	}
	if ac.bridge.Status == "closed" {
		s.finalizeGraceIfDone(r, ac.bridge.ID)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"deleted": res.Deleted,
		"unknown": unknown,
	})
	return nil
}

// finalizeGraceIfDone ends a closed bridge's last-delivery window as soon as
// the other side has acked every kept message (v0.6.4). Best-effort: the
// window end and the sweeper are the backstop.
func (s *Server) finalizeGraceIfDone(r *http.Request, bridgeID string) {
	n, err := s.store.GracePending(r.Context(), bridgeID)
	if err != nil || n > 0 {
		return
	}
	if err := s.store.FinalizeGrace(r.Context(), bridgeID); err != nil {
		s.logger.Warn("finalize close grace failed", "bridge_id", bridgeID, "err_code", storeErr(err).code)
		return
	}
	s.keyCache.clear()
}

// handleUndelivered implements GET /v1/bridges/{bridge_id}/undelivered
// (v0.6.4): how many of the caller's own messages the other side has not
// fetched-and-acked yet. A count only, and only of the caller's messages.
func (s *Server) handleUndelivered(w http.ResponseWriter, r *http.Request, ac *authContext) *apiErr {
	n, err := s.store.UndeliveredFrom(r.Context(), ac.bridge.ID, ac.land.ID)
	if err != nil {
		return storeErr(err)
	}
	writeJSON(w, http.StatusOK, map[string]any{"count": n})
	return nil
}

// handleMessageStatus implements GET /v1/bridges/{bridge_id}/messages/{msg_id}/status
// (DEV_SPEC §6.4, §10): the per-recipient delivery status of a message the
// caller sent. Scoping the query to the caller as sender enforces "sender only";
// a message with no (surviving) status rows is 404 (also the >24h case).
func (s *Server) handleMessageStatus(w http.ResponseWriter, r *http.Request, ac *authContext) *apiErr {
	msgID := r.PathValue("msg_id")
	deliveries, err := s.store.GetDeliveries(r.Context(), ac.bridge.ID, ac.land.ID, msgID)
	if err != nil {
		return storeErr(err)
	}
	if len(deliveries) == 0 {
		return errNotFound
	}
	out := make([]map[string]any, 0, len(deliveries))
	for _, d := range deliveries {
		handle := ""
		if l, lerr := s.store.GetLand(r.Context(), d.RecipientLandID); lerr == nil {
			handle = l.Handle
		}
		out = append(out, map[string]any{
			"land_id":    d.RecipientLandID,
			"handle":     handle,
			"status":     d.Status,
			"updated_at": rfc3339(d.UpdatedAt),
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"msg_id":     msgID,
		"deliveries": out,
	})
	return nil
}
