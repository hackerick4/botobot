package server

import (
	"encoding/json"
	"net/http"

	"github.com/hackerick4/botobot/internal/protocol"
	"github.com/hackerick4/botobot/internal/store"
)

// recipientsExcept returns the ids of all active lands in the bridge except the
// given ones (DEV_SPEC §6.4 event fan-out).
func (s *Server) recipientsExcept(r *http.Request, bridgeID string, exclude ...string) ([]string, error) {
	lands, err := s.store.ListActiveLands(r.Context(), bridgeID)
	if err != nil {
		return nil, err
	}
	skip := make(map[string]struct{}, len(exclude))
	for _, e := range exclude {
		skip[e] = struct{}{}
	}
	out := make([]string, 0, len(lands))
	for _, l := range lands {
		if _, ok := skip[l.ID]; ok {
			continue
		}
		out = append(out, l.ID)
	}
	return out, nil
}

// enqueueEvent marshals ev and fans it out to recipients (DEV_SPEC §6.4). It is
// best-effort: an enqueue failure is logged (by error code only) and does not
// fail the triggering request, because the land-set change has already been
// committed and pollers will reconcile via GET /lands.
func (s *Server) enqueueEvent(r *http.Request, bridgeID string, recipients []string, ev protocol.Event) {
	if len(recipients) == 0 {
		return
	}
	data, err := json.Marshal(ev)
	if err != nil {
		s.logger.Warn("marshal event failed", "bridge_id", bridgeID, "event", string(ev.Type))
		return
	}
	if _, err := s.store.EnqueueEvent(r.Context(), bridgeID, recipients, data); err != nil {
		s.logger.Warn("enqueue event failed", "bridge_id", bridgeID, "event", string(ev.Type), "err_code", storeErr(err).code)
	}
}

// landEventMap renders a Land output object as a generic map for embedding in an
// event payload (DEV_SPEC §6.4 event.land).
func (s *Server) landEventMap(r *http.Request, l *store.Land) map[string]any {
	lo, e := s.buildLandOut(r, l)
	if e != nil {
		// Fall back to identity-only on a lookup error; never fail the event.
		return map[string]any{"land_id": l.ID, "handle": l.Handle, "role": l.Role}
	}
	b, _ := json.Marshal(lo)
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	return m
}
