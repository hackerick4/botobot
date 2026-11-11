package server

import (
	"errors"
	"net/http"

	"github.com/hackerick4/botobot/internal/auth"
	"github.com/hackerick4/botobot/internal/invite"
	"github.com/hackerick4/botobot/internal/protocol"
	"github.com/hackerick4/botobot/internal/store"
)

// landBlock is the {profile, key} object used by create and join (DEV_SPEC §6.4).
type landBlock struct {
	Profile landProfileInput `json:"profile"`
	Key     landKeyInput     `json:"key"`
}

type createBridgeReq struct {
	Name        string    `json:"name"`
	Description string    `json:"description"`
	Land        landBlock `json:"land"`
	Settings    *struct {
		LandsCanInvite  *bool `json:"lands_can_invite"`
		RateLimitPerMin *int  `json:"rate_limit_per_min"`
	} `json:"settings"`
}

// handleCreateBridge implements POST /v1/bridges (no auth; 5/hour/IP)
// (DEV_SPEC §6.4). The creator land becomes the admin.
func (s *Server) handleCreateBridge(w http.ResponseWriter, r *http.Request) {
	// The unauthenticated direct-create endpoint is disabled by default; the
	// supported path is POST /join + claim (Batch 1 item 6).
	if !s.cfg.AllowLegacyCreate {
		s.writeAPIError(w, &apiErr{status: http.StatusNotFound, code: "not_found",
			msg: "direct bridge creation is disabled; use POST /join"})
		return
	}
	ip := s.clientIP(r)
	if ok, ra := s.createLimiter.Allow(ip); !ok {
		s.writeAPIError(w, rateLimited(ra))
		return
	}

	var req createBridgeReq
	if e := s.decode(w, r, &req); e != nil {
		s.writeAPIError(w, e)
		return
	}
	name := sanitizeText(req.Name)
	if n := len([]rune(name)); n < 1 || n > 80 {
		s.writeAPIError(w, badRequest("name must be 1..80 characters"))
		return
	}
	description := sanitizeText(req.Description)
	if len([]rune(description)) > 500 {
		s.writeAPIError(w, badRequest("description must be at most 500 characters"))
		return
	}
	displayName, ownerName, e := req.Land.Profile.normalize()
	if e != nil {
		s.writeAPIError(w, e)
		return
	}
	if e := req.Land.Key.validate(); e != nil {
		s.writeAPIError(w, e)
		return
	}

	// v0.5.0: a two-bot bridge has no members who could invite anyone.
	landsCanInvite := false
	rateLimit := s.cfg.DefaultRateLimitPerMin
	if req.Settings != nil {
		if req.Settings.LandsCanInvite != nil {
			landsCanInvite = *req.Settings.LandsCanInvite
		}
		if req.Settings.RateLimitPerMin != nil {
			rateLimit = *req.Settings.RateLimitPerMin
			if rateLimit < 1 || rateLimit > 600 {
				s.writeAPIError(w, badRequest("rate_limit_per_min must be 1..600"))
				return
			}
		}
	}

	ctx := r.Context()
	b, err := s.store.CreateBridge(ctx, store.CreateBridgeParams{
		Name:            name,
		Description:     description,
		MaxLands:        newBridgeMaxLands(s.cfg.MaxLands),
		Pair:            true,
		LandsCanInvite:  landsCanInvite,
		RateLimitPerMin: rateLimit,
		QueueQuotaBytes: s.cfg.MaxQueueQuotaBytes,
	})
	if err != nil {
		s.writeAPIError(w, storeErr(err))
		return
	}

	// Generate and store the invite code (encrypted, bound to the bridge id).
	code, err := invite.Generate()
	if err != nil {
		s.writeAPIError(w, storeErr(err))
		return
	}
	enc, err := invite.Encrypt(s.cfg.InviteKey, b.ID, code)
	if err != nil {
		s.writeAPIError(w, &apiErr{status: http.StatusInternalServerError, code: "internal", msg: "internal error"})
		return
	}
	if err := s.store.SetInviteCode(ctx, b.ID, enc, b.CreatedAt); err != nil {
		s.writeAPIError(w, storeErr(err))
		return
	}

	fingerprint := protocol.Fingerprint(req.Land.Key.SignPub, req.Land.Key.EncPub)
	landID, _, err := s.store.CreateLand(ctx, store.CreateLandParams{
		BridgeID:    b.ID,
		Role:        "admin",
		Handle:      req.Land.Profile.Handle,
		DisplayName: displayName,
		OwnerName:   ownerName,
		EncPub:      req.Land.Key.EncPub,
		SignPub:     req.Land.Key.SignPub,
		BindingSig:  req.Land.Key.BindingSig,
		Fingerprint: fingerprint,
	})
	if err != nil {
		s.writeAPIError(w, storeErr(err))
		return
	}
	if err := s.store.SetAdminLandID(ctx, b.ID, landID); err != nil {
		s.writeAPIError(w, storeErr(err))
		return
	}

	apiKey, _, bodyErr := s.issueAPIKey(r, landID)
	if bodyErr != nil {
		s.writeAPIError(w, bodyErr)
		return
	}

	land, err := s.store.GetLand(ctx, landID)
	if err != nil {
		s.writeAPIError(w, storeErr(err))
		return
	}
	bridge, err := s.store.GetBridge(ctx, b.ID)
	if err != nil {
		s.writeAPIError(w, storeErr(err))
		return
	}
	bo, be := s.buildBridgeOut(r, bridge)
	if be != nil {
		s.writeAPIError(w, be)
		return
	}
	lo, le := s.buildLandOut(r, land)
	if le != nil {
		s.writeAPIError(w, le)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"bridge":      bo,
		"land":        lo,
		"api_key":     apiKey,
		"invite_code": code,
	})
}

// issueAPIKey generates and stores a new API key for a land, returning the
// plaintext key (shown once). It clears the key cache so the new key is live.
func (s *Server) issueAPIKey(r *http.Request, landID string) (full, keyID string, e *apiErr) {
	k, err := auth.Generate()
	if err != nil {
		return "", "", &apiErr{status: http.StatusInternalServerError, code: "internal", msg: "internal error"}
	}
	id, err := s.store.InsertAPIKey(r.Context(), store.InsertAPIKeyParams{
		LandID:  landID,
		Prefix:  k.Prefix,
		KeyHash: auth.Hash(s.pepper, k.Full),
	})
	if err != nil {
		return "", "", storeErr(err)
	}
	s.keyCache.clear()
	return k.Full, id, nil
}

// handleGetBridge implements GET /v1/bridges/{bridge_id} (DEV_SPEC §6.4).
func (s *Server) handleGetBridge(w http.ResponseWriter, r *http.Request, ac *authContext) *apiErr {
	bo, e := s.buildBridgeOut(r, ac.bridge)
	if e != nil {
		return e
	}
	writeJSON(w, http.StatusOK, map[string]any{"bridge": bo})
	return nil
}

type patchBridgeReq struct {
	Name        *string `json:"name"`
	Description *string `json:"description"`
	Settings    *struct {
		LandsCanInvite  *bool  `json:"lands_can_invite"`
		JoinLocked      *bool  `json:"join_locked"`
		RateLimitPerMin *int   `json:"rate_limit_per_min"`
		QueueQuotaBytes *int64 `json:"queue_quota_bytes"`
	} `json:"settings"`
}

// handlePatchBridge implements PATCH /v1/bridges/{bridge_id} (admin)
// (DEV_SPEC §6.4). Setting join_locked=false also resets bridge-level throttle.
func (s *Server) handlePatchBridge(w http.ResponseWriter, r *http.Request, ac *authContext) *apiErr {
	if ac.bridge.EntryID != nil {
		return errEntryBridgeFixed
	}
	var req patchBridgeReq
	if e := s.decode(w, r, &req); e != nil {
		return e
	}
	var up store.UpdateBridgeParams
	changed := false
	if req.Name != nil {
		n := sanitizeText(*req.Name)
		if c := len([]rune(n)); c < 1 || c > 80 {
			return badRequest("name must be 1..80 characters")
		}
		up.Name = &n
		changed = true
	}
	if req.Description != nil {
		d := sanitizeText(*req.Description)
		if len([]rune(d)) > 500 {
			return badRequest("description must be at most 500 characters")
		}
		up.Description = &d
		changed = true
	}
	var joinLocked *bool
	if req.Settings != nil {
		if req.Settings.LandsCanInvite != nil {
			// A two-bot bridge has nobody else to invite (v0.5.0).
			if ac.bridge.Pair && *req.Settings.LandsCanInvite {
				return badRequest("lands_can_invite does not apply: each bridge connects two bots")
			}
			up.LandsCanInvite = req.Settings.LandsCanInvite
			changed = true
		}
		if req.Settings.RateLimitPerMin != nil {
			if *req.Settings.RateLimitPerMin < 1 || *req.Settings.RateLimitPerMin > 600 {
				return badRequest("rate_limit_per_min must be 1..600")
			}
			up.RateLimitPerMin = req.Settings.RateLimitPerMin
			changed = true
		}
		if req.Settings.QueueQuotaBytes != nil {
			if *req.Settings.QueueQuotaBytes < 1024*1024 || *req.Settings.QueueQuotaBytes > s.cfg.MaxQueueQuotaBytes {
				return badRequest("queue_quota_bytes out of range")
			}
			up.QueueQuotaBytes = req.Settings.QueueQuotaBytes
			changed = true
		}
		joinLocked = req.Settings.JoinLocked
	}

	if err := s.store.UpdateBridge(r.Context(), ac.bridge.ID, up); err != nil {
		return storeErr(err)
	}
	if joinLocked != nil {
		changed = true
		if *joinLocked {
			if err := s.store.SetJoinLocked(r.Context(), ac.bridge.ID, true, "admin"); err != nil {
				return storeErr(err)
			}
		} else {
			if err := s.store.SetJoinLocked(r.Context(), ac.bridge.ID, false, ""); err != nil {
				return storeErr(err)
			}
			s.throttle.ResetBridge(ac.bridge.ID) // DEV_SPEC §6.4
		}
	}

	bridge, err := s.store.GetBridge(r.Context(), ac.bridge.ID)
	if err != nil {
		return storeErr(err)
	}
	if changed {
		recips, rerr := s.recipientsExcept(r, bridge.ID, ac.land.ID)
		if rerr == nil {
			s.enqueueEvent(r, bridge.ID, recips, protocol.Event{
				Type: protocol.EventSettingsChanged,
				Settings: map[string]any{
					"lands_can_invite":   bridge.LandsCanInvite,
					"join_locked":        bridge.JoinLocked,
					"rate_limit_per_min": bridge.RateLimitPerMin,
					"queue_quota_bytes":  bridge.QueueQuotaBytes,
				},
			})
		}
	}
	bo, e := s.buildBridgeOut(r, bridge)
	if e != nil {
		return e
	}
	writeJSON(w, http.StatusOK, map[string]any{"bridge": bo})
	return nil
}

// handleDeleteBridge implements DELETE /v1/bridges/{bridge_id} (admin): immediate
// close with reason admin_closed (DEV_SPEC §6.4, §11).
func (s *Server) handleDeleteBridge(w http.ResponseWriter, r *http.Request, ac *authContext) *apiErr {
	co, err := s.store.CloseBridgeGrace(r.Context(), ac.bridge.ID, store.CloseAdminClosed, ac.land.ID, s.cfg.CloseGrace)
	if err != nil {
		return storeErr(err)
	}
	s.keyCache.clear()
	s.throttle.ResetBridge(ac.bridge.ID)
	out := map[string]any{"closed": true}
	addCloseOutcome(out, co)
	writeJSON(w, http.StatusOK, out)
	return nil
}

// addCloseOutcome reports (v0.6.4) how many of the closer's messages the other
// side had not read, and until when it can still fetch them.
func addCloseOutcome(out map[string]any, co store.CloseOutcome) {
	out["undelivered"] = co.Undelivered
	if co.GraceUntil > 0 {
		out["final_until"] = rfc3339(co.GraceUntil)
	}
}

// handleGetInviteCode implements GET /v1/bridges/{bridge_id}/invite-code (admin,
// or member when lands_can_invite) (DEV_SPEC §6.4).
func (s *Server) handleGetInviteCode(w http.ResponseWriter, r *http.Request, ac *authContext) *apiErr {
	if ac.bridge.EntryID != nil {
		return errEntryBridgeFixed
	}
	if ac.land.Role != "admin" && !ac.bridge.LandsCanInvite {
		return errForbidden
	}
	if len(ac.bridge.InviteCodeEnc) == 0 {
		return errNotFound
	}
	code, err := invite.Decrypt(s.cfg.InviteKey, ac.bridge.ID, ac.bridge.InviteCodeEnc)
	if err != nil {
		return &apiErr{status: http.StatusInternalServerError, code: "internal", msg: "internal error"}
	}
	rotated := ""
	if ac.bridge.InviteCodeRotatedAt != nil {
		rotated = rfc3339(*ac.bridge.InviteCodeRotatedAt)
	}
	// usable tells the admin's CLI whether this one-time code can still be
	// forwarded (unused and not past its 24h expiry), so it never hands the
	// owner a dead invite. It reveals nothing a member of the bridge cannot
	// already see in the roster.
	usable := !ac.bridge.InviteCodeUsed &&
		(ac.bridge.InviteCodeExpiresAt == nil || s.clock.Now().Before(timeFromMillis(*ac.bridge.InviteCodeExpiresAt)))
	writeJSON(w, http.StatusOK, map[string]any{
		"invite_code": code,
		"rotated_at":  rotated,
		"invite_link": s.inviteLink(ac.bridge.ID),
		"usable":      usable,
	})
	return nil
}

// handleRotateInviteCode implements POST /v1/bridges/{bridge_id}/invite-code/rotate
// (admin). The old code is invalidated immediately (DEV_SPEC §6.4).
func (s *Server) handleRotateInviteCode(w http.ResponseWriter, r *http.Request, ac *authContext) *apiErr {
	if ac.bridge.EntryID != nil {
		return errEntryBridgeFixed
	}
	// A two-bot bridge whose peer has joined takes no one else (v0.5.0), so a
	// fresh code would be useless: refuse before minting one.
	if ac.bridge.Pair {
		count, err := s.store.CountActiveLands(r.Context(), ac.bridge.ID)
		if err != nil {
			return storeErr(err)
		}
		if count >= pairMaxLands {
			return errBridgeFull
		}
	}
	code, err := invite.Generate()
	if err != nil {
		return &apiErr{status: http.StatusInternalServerError, code: "internal", msg: "internal error"}
	}
	enc, err := invite.Encrypt(s.cfg.InviteKey, ac.bridge.ID, code)
	if err != nil {
		return &apiErr{status: http.StatusInternalServerError, code: "internal", msg: "internal error"}
	}
	now := s.clock.Now().UnixMilli()
	if err := s.store.SetInviteCode(r.Context(), ac.bridge.ID, enc, now); err != nil {
		return storeErr(err)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"invite_code": code,
		"rotated_at":  rfc3339(now),
		"invite_link": s.inviteLink(ac.bridge.ID),
		"usable":      true,
	})
	return nil
}

// pairMaxLands is the size of every bridge created since v0.5.0: the admin
// and one peer.
const pairMaxLands = 2

// newBridgeMaxLands is the max_lands stored on a new bridge: two, or the
// configured ceiling when an operator set it lower.
func newBridgeMaxLands(cfgMax int) int {
	if cfgMax > 0 && cfgMax < pairMaxLands {
		return cfgMax
	}
	return pairMaxLands
}

// errBridgeFull refuses a third bot (or a rotate after the peer joined) on a
// two-bot bridge.
var errBridgeFull = &apiErr{status: http.StatusConflict, code: "bridge_full",
	msg: "this bridge already connects two bots; each bridge takes exactly two. Ask the owner for a new bridge"}

type joinReq struct {
	Code string    `json:"code"`
	Land landBlock `json:"land"`
}

// handleJoin implements POST /v1/bridges/{bridge_id}/join (no auth + invite
// code) (DEV_SPEC §6.4, §8). All failure paths return 401 join_failed (or 429
// when throttled) with response jitter; bridge_full is reported only when the
// code is correct.
func (s *Server) handleJoin(w http.ResponseWriter, r *http.Request) {
	bridgeID := r.PathValue("bridge_id")
	ip := s.clientIP(r)

	// Pre-verification throttle (IP backoff/ban, subnet ban, bridge temp lock).
	if blk := s.throttle.Check(ip, bridgeID); blk != nil {
		s.jitter()
		s.writeAPIError(w, rateLimited(blk.RetryAfter))
		return
	}

	// An unknown or closed bridge is folded into join_failed so the caller
	// (who holds no key) cannot tell whether the id ever existed (v0.6.6: a
	// closed bridge used to answer 410 here).
	bridge, err := s.store.GetBridge(r.Context(), bridgeID)
	if err != nil || bridge.Status == "closed" {
		s.failJoin(w, r, ip, bridgeID)
		return
	}
	// A bridge created via POST /join is PENDING until its admin claims it, so
	// the admin is always the first land (DEV_SPEC §6 /join, §11). Reject member
	// joins until then with a clear code (the caller already holds the bridge id).
	if bridge.Pending {
		s.writeAPIError(w, &apiErr{status: http.StatusConflict, code: "bridge_pending",
			msg: "bridge is awaiting its admin claim; try again after the admin has claimed it"})
		return
	}

	var req joinReq
	if e := s.decode(w, r, &req); e != nil {
		// Malformed body is a client error, not a code guess: no throttle bump.
		s.jitter()
		s.writeAPIError(w, e)
		return
	}
	displayName, ownerName, e := req.Land.Profile.normalize()
	if e != nil {
		s.jitter()
		s.writeAPIError(w, e)
		return
	}
	if e := req.Land.Key.validate(); e != nil {
		s.jitter()
		s.writeAPIError(w, e)
		return
	}

	// Persistent join lock (admin or brute_force) is indistinguishable from a
	// wrong code (DEV_SPEC §8).
	if bridge.JoinLocked {
		s.failJoin(w, r, ip, bridgeID)
		return
	}

	// Verify the invite code in constant time.
	if len(bridge.InviteCodeEnc) == 0 {
		s.failJoin(w, r, ip, bridgeID)
		return
	}
	stored, derr := invite.Decrypt(s.cfg.InviteKey, bridgeID, bridge.InviteCodeEnc)
	if derr != nil || !invite.Equal(req.Code, stored) {
		s.failJoin(w, r, ip, bridgeID)
		return
	}
	// A one-time code already consumed, or one past its 24h expiry, fails exactly
	// like a wrong code so neither condition leaks (Batch 1 item 5).
	if bridge.InviteCodeUsed {
		// A two-bot bridge consumes its code when the peer joins (v0.5.0). The
		// caller holds the correct code, so saying the bridge is full leaks
		// nothing new and tells a third bot why it cannot join. No throttle
		// bump: this is not a guess.
		if bridge.Pair {
			if n, cerr := s.store.CountActiveLands(r.Context(), bridgeID); cerr == nil && n >= pairMaxLands {
				s.jitter()
				s.writeAPIError(w, errBridgeFull)
				return
			}
		}
		s.failJoin(w, r, ip, bridgeID)
		return
	}
	if bridge.InviteCodeExpiresAt != nil && !s.clock.Now().Before(timeFromMillis(*bridge.InviteCodeExpiresAt)) {
		s.failJoin(w, r, ip, bridgeID)
		return
	}

	// Code correct: capacity and handle checks may now reveal their real codes.
	count, err := s.store.CountActiveLands(r.Context(), bridgeID)
	if err != nil {
		s.writeAPIError(w, storeErr(err))
		return
	}
	if count >= bridge.MaxLands || (bridge.Pair && count >= pairMaxLands) {
		s.jitter()
		if bridge.Pair {
			s.writeAPIError(w, errBridgeFull)
		} else {
			s.writeAPIError(w, &apiErr{status: http.StatusConflict, code: "bridge_full", msg: "bridge is full"})
		}
		return
	}
	taken, err := s.store.HandleTaken(r.Context(), bridgeID, req.Land.Profile.Handle)
	if err != nil {
		s.writeAPIError(w, storeErr(err))
		return
	}
	if taken {
		s.jitter()
		s.writeAPIError(w, &apiErr{status: http.StatusConflict, code: "handle_taken", msg: "handle already in use"})
		return
	}

	fingerprint := protocol.Fingerprint(req.Land.Key.SignPub, req.Land.Key.EncPub)
	landID, _, err := s.store.CreateLand(r.Context(), store.CreateLandParams{
		BridgeID:      bridgeID,
		Role:          "member",
		Handle:        req.Land.Profile.Handle,
		DisplayName:   displayName,
		OwnerName:     ownerName,
		EncPub:        req.Land.Key.EncPub,
		SignPub:       req.Land.Key.SignPub,
		BindingSig:    req.Land.Key.BindingSig,
		Fingerprint:   fingerprint,
		ConsumeInvite: true,
	})
	if errors.Is(err, store.ErrInviteConsumed) {
		// Lost a race for the one-time code: indistinguishable from a wrong code.
		s.failJoin(w, r, ip, bridgeID)
		return
	}
	if err != nil {
		s.writeAPIError(w, storeErr(err))
		return
	}
	version, err := s.store.BumpLandSetVersion(r.Context(), bridgeID)
	if err != nil {
		s.writeAPIError(w, storeErr(err))
		return
	}
	// The one-time invite code was consumed atomically inside CreateLand above.

	apiKey, _, bodyErr := s.issueAPIKey(r, landID)
	if bodyErr != nil {
		s.writeAPIError(w, bodyErr)
		return
	}

	land, err := s.store.GetLand(r.Context(), landID)
	if err != nil {
		s.writeAPIError(w, storeErr(err))
		return
	}
	// Notify every existing land (admin included) that the land set changed.
	if recips, rerr := s.recipientsExcept(r, bridgeID, landID); rerr == nil {
		s.enqueueEvent(r, bridgeID, recips, protocol.Event{
			Type:           protocol.EventLandJoined,
			Land:           s.landEventMap(r, land),
			LandSetVersion: version,
		})
	}

	bridge, err = s.store.GetBridge(r.Context(), bridgeID)
	if err != nil {
		s.writeAPIError(w, storeErr(err))
		return
	}
	bo, be := s.buildBridgeOut(r, bridge)
	if be != nil {
		s.writeAPIError(w, be)
		return
	}
	lo, le := s.buildLandOut(r, land)
	if le != nil {
		s.writeAPIError(w, le)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"bridge":  bo,
		"land":    lo,
		"api_key": apiKey,
	})
}

// failJoin records a failed join attempt, applies the brute-force side effects
// (persistent lock + join_locked event), adds response jitter, and returns the
// uniform 401 join_failed (DEV_SPEC §8).
func (s *Server) failJoin(w http.ResponseWriter, r *http.Request, ip, bridgeID string) {
	res := s.throttle.Fail(ip, bridgeID)
	if res.PersistBridgeLock {
		ctx := r.Context()
		if err := s.store.SetJoinLocked(ctx, bridgeID, true, "brute_force"); err == nil {
			s.throttle.ResetBridge(bridgeID) // temp counters give way to the persistent flag
			if b, err := s.store.GetBridge(ctx, bridgeID); err == nil && b.AdminLandID != nil {
				s.enqueueEvent(r, bridgeID, []string{*b.AdminLandID}, protocol.Event{
					Type:   protocol.EventJoinLocked,
					Reason: "brute_force",
				})
			}
		}
	}
	s.jitter()
	s.writeAPIError(w, errJoinFailed)
}

// handleBridgePage implements GET /b/{bridge_id} (DEV_SPEC §6.4): a minimal,
// info-free page that tells a human to run the CLI. It never reveals whether the
// bridge exists.
func (s *Server) handleBridgePage(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("This is a bot-bridge link. Give the whole invite message to your bot; it follows /en/join.md (or /join.md) on this site.\n" +
		"Each bridge connects two bots. The invite works once and expires when the other bot joins.\n" +
		"In short, pass the whole invite on stdin in a single-quoted heredoc (never in flags), with an end marker of BB_END_ plus 16 fresh random letters and digits:\n\n" +
		"  bot-bridge-cli join --invite-stdin --display-name 'BOT NAME' --owner 'OWNER NAME' <<'BB_END_NONCE'\n" +
		"  (the whole invite)\n" +
		"  BB_END_NONCE\n"))
}
