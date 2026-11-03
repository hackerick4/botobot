package server

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/hackerick4/botobot/internal/protocol"
	"github.com/hackerick4/botobot/internal/store"
)

// (POST /inbox/ack and GET /messages/{id}/status live in handlers_ack.go.)

// inboxItemOverhead is a fixed per-item allowance (JSON keys, timestamps, id,
// base64 padding) added when estimating a response's size against the cap.
const inboxItemOverhead = 512

// handleInbox implements GET /v1/bridges/{bridge_id}/inbox (DEV_SPEC §6.4, §10).
// It leases up to limit available, unexpired items for the caller (setting
// lease_until = now + lease and incrementing fetch_count via the store), returns
// each with the per-recipient wrapped key, and caps the total response size,
// truncating with has_more=true (always returning at least one item).
func (s *Server) handleInbox(w http.ResponseWriter, r *http.Request, ac *authContext) *apiErr {
	limit := 50
	if q := r.URL.Query().Get("limit"); q != "" {
		n, err := strconv.Atoi(q)
		if err != nil || n < 1 || n > 200 {
			return badRequest("limit must be an integer 1..200")
		}
		limit = n
	}

	// Before leasing, the admin rebuilds any land_joined notices that expired
	// while it was away so they are delivered in this same response (DEV_SPEC §11).
	closing := ac.bridge.Status == "closed" // v0.6.4 last-delivery window
	if !closing {
		s.redeliverAdminJoins(r, ac)
	}

	items, err := s.store.FetchInbox(r.Context(), ac.bridge.ID, ac.land.ID, limit)
	if err != nil {
		return storeErr(err)
	}

	envelopes := make([]map[string]any, 0, len(items))
	var total int64
	truncated := false
	for i, it := range items {
		// Size cap (DEV_SPEC §6.4): stop before exceeding the limit, but always
		// return at least one item. Truncated items stay leased and reappear
		// after the lease expires (at-least-once, DEV_SPEC §10).
		est := int64(estimateInboxSize(it))
		if i > 0 && total+est > s.cfg.MaxInboxResponseBytes {
			truncated = true
			break
		}
		envelopes = append(envelopes, inboxEnvelope(it))
		total += est
	}

	// More remain if we truncated for size or filled the whole limit window.
	hasMore := truncated || len(envelopes) == limit

	out := map[string]any{
		"envelopes":         envelopes,
		"has_more":          hasMore,
		"poll_interval_sec": ac.land.PollIntervalSec,
		"server_time":       rfc3339(s.clock.Now().UnixMilli()),
	}
	if closing {
		// The bridge is already closed: these are its last messages, readable
		// until final_until (or until acked). The body of closedErr tells the
		// CLI how it closed, so it can print one closed notice after them.
		c := closedErr(ac.bridge).extra
		if ac.bridge.GraceUntil != nil {
			c["final_until"] = rfc3339(*ac.bridge.GraceUntil)
		}
		out["closing"] = c
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

// redeliverAdminJoins rebuilds land_joined notices the admin missed while away
// (DEV_SPEC §11). It is a no-op unless the caller is the bridge admin. For each
// active member land that joined after the re-delivery watermark and whose
// land_joined notice is no longer sitting unexpired in the admin inbox, it
// enqueues a fresh notice (redelivered=true, metadata only) to the admin. The
// watermark advances when the admin acks these, so an acked notice is never
// rebuilt again; the still-present check prevents duplicating an unexpired one.
// It is best-effort: failures are logged by code only and never fail the poll.
func (s *Server) redeliverAdminJoins(r *http.Request, ac *authContext) {
	if ac.bridge.AdminLandID == nil || *ac.bridge.AdminLandID != ac.land.ID {
		return
	}
	candidates, err := s.store.LandsJoinedAfter(r.Context(), ac.bridge.ID, ac.bridge.AdminJoinsAckedThrough)
	if err != nil {
		s.logger.Warn("redeliver joins: list lands failed", "bridge_id", ac.bridge.ID, "err_code", storeErr(err).code)
		return
	}
	if len(candidates) == 0 {
		return
	}
	now := s.clock.Now().UnixMilli()
	blobs, err := s.store.UnexpiredEventJSON(r.Context(), ac.bridge.ID, ac.land.ID, now)
	if err != nil {
		s.logger.Warn("redeliver joins: list inbox events failed", "bridge_id", ac.bridge.ID, "err_code", storeErr(err).code)
		return
	}
	present := make(map[string]struct{}, len(blobs))
	for _, b := range blobs {
		var ev protocol.Event
		if err := json.Unmarshal(b, &ev); err != nil || ev.Type != protocol.EventLandJoined {
			continue
		}
		if id, _ := ev.Land["land_id"].(string); id != "" {
			present[id] = struct{}{}
		}
	}
	for _, l := range candidates {
		if _, ok := present[l.ID]; ok {
			continue // original (or an earlier rebuild) is still queued; no dup
		}
		s.enqueueEvent(r, ac.bridge.ID, []string{ac.land.ID}, protocol.Event{
			Type:           protocol.EventLandJoined,
			Land:           s.landEventMap(r, l),
			LandSetVersion: ac.bridge.LandSetVersion,
			Redelivered:    true,
		})
	}
}

// estimateInboxSize approximates an item's on-the-wire JSON size, accounting for
// base64 expansion (~4/3) of the binary fields, plus a fixed overhead.
func estimateInboxSize(it store.InboxItem) int {
	raw := len(it.HeaderJSON) + len(it.Signature) + len(it.Ciphertext) + len(it.WrappedKey) + len(it.EventJSON)
	return raw*4/3 + inboxItemOverhead
}

// inboxEnvelope renders one leased inbox item as its API object (DEV_SPEC §6.4
// GET /inbox). For messages the signed header is embedded verbatim and the
// caller's own wrapped key is attached; for events the plaintext event is
// embedded.
func inboxEnvelope(it store.InboxItem) map[string]any {
	env := map[string]any{
		"id":          it.ID,
		"kind":        it.Kind,
		"enqueued_at": rfc3339(it.EnqueuedAt),
		"expires_at":  rfc3339(it.ExpiresAt),
		"fetch_count": it.FetchCount,
	}
	if it.Kind == "message" {
		env["header"] = json.RawMessage(it.HeaderJSON)
		env["signature"] = it.Signature
		env["ciphertext"] = it.Ciphertext
		env["wrapped_key"] = it.WrappedKey
	} else {
		env["event"] = json.RawMessage(it.EventJSON)
	}
	return env
}
