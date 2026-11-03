package server

import (
	"errors"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/hackerick4/botobot/internal/auth"
	"github.com/hackerick4/botobot/internal/invite"
	"github.com/hackerick4/botobot/internal/protocol"
	"github.com/hackerick4/botobot/internal/store"
)

// Public entry (v0.6.0, DEV_SPEC §20). A service bot creates one reusable link;
// every guest bot that joins through it gets its own private two-bot bridge
// with the service bot. The service bot holds one entry key (bbe_…) that acts
// as the admin land on those bridges only, and polls one aggregated inbox.

// Entry limits that are not server config: hard ceilings an owner cannot
// exceed with PATCH, and the per-entry send / inbox buckets.
const (
	entryMaxOpenCeiling   = 1000
	entryJoinsHourCeiling = 600
	entrySendPerMin       = 120
	entrySendBurst        = 30
	entryInboxPerMin      = 12
	entryInboxBurst       = 10
	entryBridgeSendPerMin = 10
	entryBridgeQuotaBytes = 16 << 20
)

var (
	errEntryNotFound = &apiErr{status: http.StatusNotFound, code: "entry_not_found", msg: "this link is no longer active"}
	errEntryDisabled = &apiErr{status: http.StatusForbidden, code: "entry_disabled", msg: "this public entry is not taking new bots right now"}
	errEntryClosed   = &apiErr{status: http.StatusGone, code: "entry_closed", msg: "this public entry is closed"}
	errEntryFull     = &apiErr{status: http.StatusConflict, code: "entry_full", msg: "this public entry has reached its limit of open conversations; try again later"}
	// errEntryBridgeFixed: a public-entry bridge has no invite and fixed
	// settings; the entry's own limits govern it.
	errEntryBridgeFixed = &apiErr{status: http.StatusForbidden, code: "entry_bridge_fixed", msg: "a bridge created through a public entry has no invite and fixed settings"}
	errEntryScope       = &apiErr{status: http.StatusForbidden, code: "entry_key_scope", msg: "an entry key works only on its own entry and the bridges created through it"}
)

// entryActivity debounces entries.last_active_at writes to once a minute.
type entryActivity struct {
	mu   sync.Mutex
	last map[string]time.Time
}

func (s *Server) touchEntry(r *http.Request, entryID string) {
	now := s.clock.Now()
	s.entryAct.mu.Lock()
	last, ok := s.entryAct.last[entryID]
	due := !ok || now.Sub(last) >= time.Minute
	if due {
		s.entryAct.last[entryID] = now
	}
	s.entryAct.mu.Unlock()
	if !due {
		return
	}
	if err := s.store.TouchEntryActive(r.Context(), entryID, now.UnixMilli()); err != nil {
		s.logger.Warn("touch entry active failed", "err_code", storeErr(err).code)
	}
}

// lookupEntryKey resolves a bbe_ token to its entry. A closed entry is 410.
func (s *Server) lookupEntryKey(r *http.Request, token string) (*store.Entry, *apiErr) {
	e, err := s.store.GetEntryByKeyHash(r.Context(), auth.Hash(s.pepper, token))
	if err != nil {
		return nil, errUnauthorized
	}
	if e.Status == store.EntryClosed {
		return nil, errEntryClosed
	}
	return e, nil
}

// authenticateEntryOnBridge lets an entry key act as the service (admin) land
// of a bridge created through that entry. Any other bridge, and every route
// without a {bridge_id}, is refused.
func (s *Server) authenticateEntryOnBridge(r *http.Request, token string) (*authContext, *apiErr) {
	e, ae := s.lookupEntryKey(r, token)
	if ae != nil {
		return nil, ae
	}
	bridgeID := r.PathValue("bridge_id")
	if bridgeID == "" {
		return nil, errEntryScope
	}
	b, err := s.store.GetBridge(r.Context(), bridgeID)
	if err != nil || b.EntryID == nil || *b.EntryID != e.ID {
		return nil, errNotFound
	}
	if b.Status == "closed" {
		return nil, closedErr(b)
	}
	if b.AdminLandID == nil {
		return nil, errNotFound
	}
	land, err := s.store.GetLand(r.Context(), *b.AdminLandID)
	if err != nil {
		return nil, errNotFound
	}
	return &authContext{land: land, bridge: b, entry: e}, nil
}

// entryLinkRe is the shape of a public link id.
var entryLinkRe = regexp.MustCompile(`^pe_[0-9A-Za-z]{22}$`)

type entryHandlerFunc func(w http.ResponseWriter, r *http.Request, e *store.Entry) *apiErr

// entryAuthed wraps /v1/entries/{entry_id}/… routes: the bearer must be the
// entry key of that very entry (a land key or another entry's key is 404).
func (s *Server) entryAuthed(limit func(e *store.Entry) (bool, time.Duration), fn entryHandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token, ok := auth.ParseBearer(r.Header.Get("Authorization"))
		if !ok || !auth.IsEntryKey(token) {
			s.writeAPIError(w, errUnauthorized)
			return
		}
		e, ae := s.lookupEntryKey(r, token)
		if ae != nil {
			s.writeAPIError(w, ae)
			return
		}
		if r.PathValue("entry_id") != e.ID {
			s.writeAPIError(w, errNotFound)
			return
		}
		if limit != nil {
			if ok, ra := limit(e); !ok {
				s.writeAPIError(w, rateLimited(ra))
				return
			}
		}
		if ae := fn(w, r, e); ae != nil {
			s.writeAPIError(w, ae)
			return
		}
		s.touchEntry(r, e.ID)
	}
}

func (s *Server) entryOtherLimit(e *store.Entry) (bool, time.Duration) {
	return s.otherLimiter.Allow("entry:" + e.ID)
}

func (s *Server) entryInboxLimit(e *store.Entry) (bool, time.Duration) {
	return s.entryInboxLimiter.Allow(e.ID)
}

func (s *Server) entryAckLimit(e *store.Entry) (bool, time.Duration) {
	return s.ackLimiter.Allow("entry:" + e.ID)
}

// entryLink is the public URL of an entry.
func (s *Server) entryLink(linkID string) string {
	return strings.TrimRight(s.cfg.PublicURL, "/") + "/e/" + linkID
}

func (s *Server) entryOut(r *http.Request, e *store.Entry) (map[string]any, *apiErr) {
	open, err := s.store.CountOpenEntryBridges(r.Context(), e.ID)
	if err != nil {
		return nil, storeErr(err)
	}
	out := map[string]any{
		"entry_id":         e.ID,
		"link":             s.entryLink(e.LinkID),
		"name":             e.Name,
		"status":           e.Status,
		"handle":           e.Handle,
		"display_name":     e.DisplayName,
		"owner_name":       e.OwnerName,
		"fingerprint":      e.Fingerprint,
		"lang":             e.Lang,
		"max_open_bridges": e.MaxOpenBridges,
		"joins_per_hour":   e.JoinsPerHour,
		"open_bridges":     open,
		"created_at":       rfc3339(e.CreatedAt),
		"last_active_at":   rfc3339(e.LastActiveAt),
	}
	if e.DisabledReason != nil {
		out["disabled_reason"] = *e.DisabledReason
	}
	return out, nil
}

type entrySettingsIn struct {
	MaxOpenBridges *int `json:"max_open_bridges"`
	JoinsPerHour   *int `json:"joins_per_hour"`
}

func (in *entrySettingsIn) validate() *apiErr {
	if in.MaxOpenBridges != nil && (*in.MaxOpenBridges < 1 || *in.MaxOpenBridges > entryMaxOpenCeiling) {
		return badRequest("max_open_bridges must be 1.." + strconv.Itoa(entryMaxOpenCeiling))
	}
	if in.JoinsPerHour != nil && (*in.JoinsPerHour < 1 || *in.JoinsPerHour > entryJoinsHourCeiling) {
		return badRequest("joins_per_hour must be 1.." + strconv.Itoa(entryJoinsHourCeiling))
	}
	return nil
}

type createEntryReq struct {
	Name     string          `json:"name"`
	Lang     string          `json:"lang"`
	Land     landBlock       `json:"land"`
	Settings entrySettingsIn `json:"settings"`
}

// The entry name is shown inside guests' owner notices (「Name」…), so it
// follows the profile-name rules: letters, digits, spaces and -_.'’·&() only,
// no quotes or brackets that could close the quote and forge a notice (RT-114).
func entryName(raw string) (string, *apiErr) {
	n := sanitizeText(raw)
	if strings.TrimSpace(n) == "" {
		return "", badRequest("name is required")
	}
	n, err := protocol.NormalizeProfileName("name", raw)
	if err != nil {
		return "", badRequest(err.Error())
	}
	return n, nil
}

// handleCreateEntry implements POST /v1/entries (no auth, per-prefix limited).
func (s *Server) handleCreateEntry(w http.ResponseWriter, r *http.Request) {
	prefix := invite.SubnetKey(s.clientIP(r))
	if ok, ra := s.entryCreateLimiter.Allow(prefix); !ok {
		s.writeAPIError(w, rateLimited(ra))
		return
	}
	var req createEntryReq
	if e := s.decode(w, r, &req); e != nil {
		s.writeAPIError(w, e)
		return
	}
	name, ae := entryName(req.Name)
	if ae != nil {
		s.writeAPIError(w, ae)
		return
	}
	displayName, ownerName, ae := req.Land.Profile.normalize()
	if ae != nil {
		s.writeAPIError(w, ae)
		return
	}
	if ae := req.Land.Key.validate(); ae != nil {
		s.writeAPIError(w, ae)
		return
	}
	if ae := req.Settings.validate(); ae != nil {
		s.writeAPIError(w, ae)
		return
	}
	lang := ""
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(req.Lang)), "en") {
		lang = "en"
	}
	ph := s.prefixHash(prefix)
	if n, err := s.store.CountEntriesByPrefix(r.Context(), ph); err != nil {
		s.writeAPIError(w, storeErr(err))
		return
	} else if n >= s.cfg.MaxEntriesPerPrefix {
		s.writeAPIError(w, &apiErr{status: http.StatusTooManyRequests, code: "too_many_entries",
			msg: "too many public entries from this network; close one first", retryAfter: 3600})
		return
	}
	maxOpen, perHour := s.cfg.EntryMaxOpenBridges, s.cfg.EntryJoinsPerHour
	if req.Settings.MaxOpenBridges != nil {
		maxOpen = *req.Settings.MaxOpenBridges
	}
	if req.Settings.JoinsPerHour != nil {
		perHour = *req.Settings.JoinsPerHour
	}
	k, err := auth.GenerateEntry()
	if err != nil {
		s.writeAPIError(w, &apiErr{status: http.StatusInternalServerError, code: "internal", msg: "internal error"})
		return
	}
	e, err := s.store.CreateEntry(r.Context(), store.CreateEntryParams{
		Name:           name,
		KeyHash:        auth.Hash(s.pepper, k.Full),
		KeyPrefix:      k.Prefix,
		Handle:         req.Land.Profile.Handle,
		DisplayName:    displayName,
		OwnerName:      ownerName,
		EncPub:         req.Land.Key.EncPub,
		SignPub:        req.Land.Key.SignPub,
		BindingSig:     req.Land.Key.BindingSig,
		Fingerprint:    protocol.Fingerprint(req.Land.Key.SignPub, req.Land.Key.EncPub),
		Lang:           lang,
		MaxOpenBridges: maxOpen,
		JoinsPerHour:   perHour,
		CreatedPrefix:  ph,
	})
	if err != nil {
		s.writeAPIError(w, storeErr(err))
		return
	}
	out, ae := s.entryOut(r, e)
	if ae != nil {
		s.writeAPIError(w, ae)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"entry": out, "entry_key": k.Full})
}

func (s *Server) handleGetEntry(w http.ResponseWriter, r *http.Request, e *store.Entry) *apiErr {
	out, ae := s.entryOut(r, e)
	if ae != nil {
		return ae
	}
	writeJSON(w, http.StatusOK, map[string]any{"entry": out})
	return nil
}

type patchEntryReq struct {
	Name     *string          `json:"name"`
	Status   *string          `json:"status"`
	Settings *entrySettingsIn `json:"settings"`
}

func (s *Server) handlePatchEntry(w http.ResponseWriter, r *http.Request, e *store.Entry) *apiErr {
	var req patchEntryReq
	if ae := s.decode(w, r, &req); ae != nil {
		return ae
	}
	var p store.UpdateEntryParams
	if req.Name != nil {
		n, ae := entryName(*req.Name)
		if ae != nil {
			return ae
		}
		p.Name = &n
	}
	if req.Status != nil {
		if *req.Status != store.EntryActive && *req.Status != store.EntryDisabled {
			return badRequest("status must be active or disabled")
		}
		p.Status = req.Status
	}
	if req.Settings != nil {
		if ae := req.Settings.validate(); ae != nil {
			return ae
		}
		p.MaxOpenBridges, p.JoinsPerHour = req.Settings.MaxOpenBridges, req.Settings.JoinsPerHour
	}
	if err := s.store.UpdateEntry(r.Context(), e.ID, p); err != nil {
		if errors.Is(err, store.ErrEntryInactive) {
			return errEntryClosed
		}
		return storeErr(err)
	}
	if p.Status != nil && *p.Status == store.EntryActive {
		// Re-enabled: the inactivity clock restarts now.
		s.entryAct.mu.Lock()
		delete(s.entryAct.last, e.ID)
		s.entryAct.mu.Unlock()
	}
	ne, err := s.store.GetEntry(r.Context(), e.ID)
	if err != nil {
		return storeErr(err)
	}
	return s.handleGetEntry(w, r, ne)
}

func (s *Server) handleRotateEntryLink(w http.ResponseWriter, r *http.Request, e *store.Entry) *apiErr {
	if _, err := s.store.RotateEntryLink(r.Context(), e.ID); err != nil {
		return storeErr(err)
	}
	ne, err := s.store.GetEntry(r.Context(), e.ID)
	if err != nil {
		return storeErr(err)
	}
	return s.handleGetEntry(w, r, ne)
}

// handleDeleteEntry closes the entry for good and closes every open bridge of
// it (reason admin_closed).
func (s *Server) handleDeleteEntry(w http.ResponseWriter, r *http.Request, e *store.Entry) *apiErr {
	ids, err := s.store.CloseEntry(r.Context(), e.ID)
	if err != nil {
		return storeErr(err)
	}
	closed, undelivered := 0, 0
	var finalUntil int64
	for _, id := range ids {
		closer := ""
		if b, err := s.store.GetBridge(r.Context(), id); err == nil && b.AdminLandID != nil {
			closer = *b.AdminLandID // the service side closes every line
		}
		if co, err := s.store.CloseBridgeGrace(r.Context(), id, store.CloseAdminClosed, closer, s.cfg.CloseGrace); err == nil {
			closed++
			undelivered += co.Undelivered
			finalUntil = max(finalUntil, co.GraceUntil)
		}
	}
	s.keyCache.clear()
	// v0.6.5: lines_ended counts every line of this entry that ended in the
	// last EntryCloseLookback, including lines closed just before in the same
	// delete-all batch (bridges_closed only counts the ones closed here).
	linesEnded := closed
	since := s.clock.Now().Add(-store.EntryCloseLookback).UnixMilli()
	if cl, err := s.store.ListEntryBridgesClosedSince(r.Context(), e.ID, since, 100000); err == nil && len(cl) > linesEnded {
		linesEnded = len(cl)
	}
	out := map[string]any{"closed": true, "bridges_closed": closed, "lines_ended": linesEnded, "undelivered": undelivered}
	if finalUntil > 0 {
		out["final_until"] = rfc3339(finalUntil)
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

func (s *Server) handleListEntryBridges(w http.ResponseWriter, r *http.Request, e *store.Entry) *apiErr {
	ids, err := s.store.ListEntryBridgeIDs(r.Context(), e.ID)
	if err != nil {
		return storeErr(err)
	}
	out := make([]map[string]any, 0, len(ids))
	for _, id := range ids {
		lands, err := s.store.ListActiveLands(r.Context(), id)
		if err != nil {
			return storeErr(err)
		}
		item := map[string]any{"bridge_id": id}
		for _, l := range lands {
			if l.Role == "admin" {
				item["service_land_id"] = l.ID
				continue
			}
			lo, ae := s.buildLandOut(r, l)
			if ae != nil {
				return ae
			}
			item["guest"] = lo
		}
		// v0.6.4: how many of the service side's messages on this line the
		// guest has not read yet (a count only).
		if sl, ok := item["service_land_id"].(string); ok {
			if n, err := s.store.UndeliveredFrom(r.Context(), id, sl); err == nil {
				item["undelivered"] = n
			}
		}
		out = append(out, item)
	}
	writeJSON(w, http.StatusOK, map[string]any{"bridges": out, "open_bridges": len(out)})
	return nil
}

// handleEntryInbox implements GET /v1/entries/{entry_id}/inbox: one leased
// batch across every open bridge of the entry, each envelope tagged with its
// bridge_id. An empty wake costs one read query and no write.
func (s *Server) handleEntryInbox(w http.ResponseWriter, r *http.Request, e *store.Entry) *apiErr {
	limit := 100
	if q := r.URL.Query().Get("limit"); q != "" {
		n, err := strconv.Atoi(q)
		if err != nil || n < 1 || n > 200 {
			return badRequest("limit must be an integer 1..200")
		}
		limit = n
	}
	var since int64 = -1
	if q := r.URL.Query().Get("closed_since"); q != "" {
		n, err := strconv.ParseInt(q, 10, 64)
		if err != nil || n < 0 {
			return badRequest("closed_since must be Unix milliseconds")
		}
		since = n
	}
	var items []store.EntryInboxItem
	has, err := s.store.EntryHasInbox(r.Context(), e.ID)
	if err != nil {
		return storeErr(err)
	}
	if has {
		if items, err = s.store.FetchEntryInbox(r.Context(), e.ID, limit); err != nil {
			return storeErr(err)
		}
	}
	envelopes := make([]map[string]any, 0, len(items))
	var total int64
	truncated := false
	for i, it := range items {
		est := int64(estimateInboxSize(it.InboxItem))
		if i > 0 && total+est > s.cfg.MaxInboxResponseBytes {
			truncated = true
			break
		}
		env := inboxEnvelope(it.InboxItem)
		env["bridge_id"] = it.BridgeID
		envelopes = append(envelopes, env)
		total += est
	}
	open, err := s.store.CountOpenEntryBridges(r.Context(), e.ID)
	if err != nil {
		return storeErr(err)
	}
	now := s.clock.Now().UnixMilli()
	out := map[string]any{
		"envelopes":         envelopes,
		"has_more":          truncated || len(envelopes) == limit,
		"open_bridges":      open,
		"status":            e.Status,
		"poll_interval_sec": store.DefaultPollIntervalSec,
		"server_time":       rfc3339(now),
		"server_time_ms":    now,
	}
	if since >= 0 {
		cl, err := s.store.ListEntryBridgesClosedSince(r.Context(), e.ID, since, 200)
		if err != nil {
			return storeErr(err)
		}
		closed := make([]map[string]any, 0, len(cl))
		for _, c := range cl {
			closed = append(closed, map[string]any{"bridge_id": c.BridgeID, "closed_at_ms": c.ClosedAt, "close_reason": c.Reason})
		}
		out["closed"] = closed
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

// handleEntryAck acks rows of the aggregated inbox. Ids that are not addressed
// to this entry's service land are reported as unknown and left alone.
func (s *Server) handleEntryAck(w http.ResponseWriter, r *http.Request, e *store.Entry) *apiErr {
	var req ackReq
	if ae := s.decode(w, r, &req); ae != nil {
		return ae
	}
	if len(req.IDs) < 1 || len(req.IDs) > 500 {
		return badRequest("ids must contain 1..500 entries")
	}
	owners, err := s.store.EntryInboxOwners(r.Context(), e.ID, req.IDs)
	if err != nil {
		return storeErr(err)
	}
	known := map[string]bool{}
	deleted := 0
	for bridgeID, ids := range owners {
		b, err := s.store.GetBridge(r.Context(), bridgeID)
		if err != nil || b.AdminLandID == nil {
			continue
		}
		res, err := s.store.Ack(r.Context(), bridgeID, *b.AdminLandID, ids)
		if err != nil {
			return storeErr(err)
		}
		deleted += res.Deleted
		for _, id := range ids {
			known[id] = true
		}
		if b.Status == "closed" {
			s.finalizeGraceIfDone(r, bridgeID)
		}
	}
	unknown := []string{}
	for _, id := range req.IDs {
		if !known[id] {
			unknown = append(unknown, id)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"deleted": deleted, "unknown": unknown})
	return nil
}

type entryJoinReq struct {
	Land landBlock `json:"land"`
}

// handleEntryJoin implements POST /v1/e/{link_id}/join (no auth): a guest bot
// joins through a public entry and gets its own two-bot bridge.
func (s *Server) handleEntryJoin(w http.ResponseWriter, r *http.Request) {
	prefix := invite.SubnetKey(s.clientIP(r))
	// One network cannot open many guest bridges, across all entries.
	if ok, ra := s.entryJoinPrefixLimiter.Allow(prefix); !ok {
		s.writeAPIError(w, rateLimited(ra))
		return
	}
	e, err := s.store.GetEntryByLink(r.Context(), r.PathValue("link_id"))
	if err != nil {
		s.writeAPIError(w, errEntryNotFound)
		return
	}
	switch e.Status {
	case store.EntryClosed:
		// v0.6.1 (RT-116): a closed link answers like one never issued.
		s.writeAPIError(w, errEntryNotFound)
		return
	case store.EntryDisabled:
		s.writeAPIError(w, errEntryDisabled)
		return
	}
	var req entryJoinReq
	if ae := s.decode(w, r, &req); ae != nil {
		s.writeAPIError(w, ae)
		return
	}
	displayName, ownerName, ae := req.Land.Profile.normalize()
	if ae != nil {
		s.writeAPIError(w, ae)
		return
	}
	if ae := req.Land.Key.validate(); ae != nil {
		s.writeAPIError(w, ae)
		return
	}
	if req.Land.Profile.Handle == e.Handle {
		s.writeAPIError(w, &apiErr{status: http.StatusConflict, code: "handle_taken", msg: "handle already in use"})
		return
	}
	// One network on one entry, and the entry's own hourly budget.
	if ok, ra := s.entryJoinPerEntryPrefixLimiter.Allow(e.ID + "|" + prefix); !ok {
		s.writeAPIError(w, rateLimited(ra))
		return
	}
	burst := e.JoinsPerHour / 6
	if burst < 3 {
		burst = 3
	}
	if ok, ra := s.entryJoinLimiter.AllowRate("entry:"+e.ID, float64(e.JoinsPerHour)/3600, burst); !ok {
		s.writeAPIError(w, rateLimited(ra))
		return
	}
	k, gerr := auth.Generate()
	if gerr != nil {
		s.writeAPIError(w, &apiErr{status: http.StatusInternalServerError, code: "internal", msg: "internal error"})
		return
	}
	rate := s.cfg.DefaultRateLimitPerMin
	if rate > entryBridgeSendPerMin {
		rate = entryBridgeSendPerMin
	}
	quota := s.cfg.MaxQueueQuotaBytes
	if quota > entryBridgeQuotaBytes {
		quota = entryBridgeQuotaBytes
	}
	res, err := s.store.CreateEntryBridge(r.Context(), store.CreateEntryBridgeParams{
		EntryID:          e.ID,
		RateLimitPerMin:  rate,
		QueueQuotaBytes:  quota,
		GuestHandle:      req.Land.Profile.Handle,
		GuestDisplayName: displayName,
		GuestOwnerName:   ownerName,
		GuestEncPub:      req.Land.Key.EncPub,
		GuestSignPub:     req.Land.Key.SignPub,
		GuestBindingSig:  req.Land.Key.BindingSig,
		GuestFingerprint: protocol.Fingerprint(req.Land.Key.SignPub, req.Land.Key.EncPub),
		GuestKeyPrefix:   k.Prefix,
		GuestKeyHash:     auth.Hash(s.pepper, k.Full),
	})
	switch {
	case errors.Is(err, store.ErrEntryFull):
		s.writeAPIError(w, errEntryFull)
		return
	case errors.Is(err, store.ErrEntryInactive):
		s.writeAPIError(w, errEntryDisabled)
		return
	case err != nil:
		s.writeAPIError(w, storeErr(err))
		return
	}
	s.keyCache.clear()
	guest, err := s.store.GetLand(r.Context(), res.GuestLandID)
	if err != nil {
		s.writeAPIError(w, storeErr(err))
		return
	}
	// The service bot hears about its new guest in its aggregated inbox.
	s.enqueueEvent(r, res.BridgeID, []string{res.AdminLandID}, protocol.Event{
		Type:           protocol.EventLandJoined,
		Land:           s.landEventMap(r, guest),
		LandSetVersion: res.Version,
	})
	bridge, err := s.store.GetBridge(r.Context(), res.BridgeID)
	if err != nil {
		s.writeAPIError(w, storeErr(err))
		return
	}
	bo, be := s.buildBridgeOut(r, bridge)
	if be != nil {
		s.writeAPIError(w, be)
		return
	}
	lo, le := s.buildLandOut(r, guest)
	if le != nil {
		s.writeAPIError(w, le)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"bridge":  bo,
		"land":    lo,
		"api_key": k.Full,
		"entry": map[string]any{
			"name":         e.Name,
			"link":         s.entryLink(e.LinkID),
			"fingerprint":  e.Fingerprint,
			"display_name": e.DisplayName,
			"lang":         e.Lang,
		},
	})
}

// handleEntryPage implements GET /e/{link_id}. A live link (active, or
// paused) gets the short page telling a person to hand the link to a bot. Any
// other link (closed, rotated away, never issued, malformed) gets one and the
// same 404 page: "no longer active", no join steps. The two answers never say
// more than whether this exact link works now (v0.6.1, RT-116): a closed or
// rotated link is byte-identical to one that never existed.
func (s *Server) handleEntryPage(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	base := strings.TrimRight(s.cfg.PublicURL, "/")
	link := r.PathValue("link_id")
	live := false
	if entryLinkRe.MatchString(link) {
		if e, err := s.store.GetEntryByLink(r.Context(), link); err == nil && e.Status != store.EntryClosed {
			live = true
		}
	}
	if !live {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(entryGonePage))
		return
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("bot-bridge public entry: one service bot, and a private line for each bot that joins.\n" +
		"Hand this link to a bot. No bot can see another bot's line.\n" +
		"Steps: " + base + "/en/join.md (section: public entry). In short:\n\n" +
		"  bot-bridge-cli join --entry '" + base + "/e/" + link + "' --display-name 'BOT NAME' --owner 'OWNER NAME'\n\n" +
		"bot-bridge 公開入口：一個服務方的 Bot，每個加入的 Bot 各自一條私人線路，彼此看不到。把連結交給 Bot 即可。\n"))
}

// entryGonePage is the one answer for every link that does not work now.
const entryGonePage = "This bot-bridge link is no longer active.\n" +
	"Ask whoever shared it for a current link.\n\n" +
	"此 bot-bridge 連結已失效。請向提供連結的一方索取新的連結。\n"
