package server

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/hackerick4/botobot/internal/claim"
	"github.com/hackerick4/botobot/internal/invite"
	"github.com/hackerick4/botobot/internal/prompts"
	"github.com/hackerick4/botobot/internal/protocol"
	"github.com/hackerick4/botobot/internal/store"
)

// prefixHash is HMAC(pepper, "bb-prefix\n"||prefix) hex, used as the stored key
// for the per-prefix pending-bridge cap so the raw source prefix is not retained
// in the DB (Batch 1 item 6; mirrors the §15 ip_hash approach).
func (s *Server) prefixHash(prefix string) string {
	mac := hmac.New(sha256.New, s.pepper)
	mac.Write([]byte("bb-prefix\n"))
	mac.Write([]byte(prefix))
	return hex.EncodeToString(mac.Sum(nil))
}

// joinBodyCap is the small request-body cap for POST /join (DEV_SPEC §6 /join):
// the body is at most a short name + lang, so 4 KiB is generous.
const joinBodyCap = 4 * 1024

// defaultBridgeName / defaultLang back the optional /join body fields.
const (
	defaultBridgeName = "bot-bridge room"
	defaultLang       = "zh-TW"
)

// errClaimFailed is the uniform claim failure (DEV_SPEC §6 claim): a wrong,
// expired, or unknown claim token are indistinguishable, like join_failed (§8).
var errClaimFailed = &apiErr{status: http.StatusUnauthorized, code: "claim_failed", msg: "claim token is invalid or expired"}

// handleJoinUsage implements GET /join (DEV_SPEC §6 /join): a short plain-text
// usage note showing how to call POST /join with curl. No auth, no data.
func (s *Server) handleJoinUsage(w http.ResponseWriter, r *http.Request) {
	base := strings.TrimRight(s.cfg.PublicURL, "/")
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, "bot-bridge /join — create a bridge in one call (no login).\n\n"+
		"  curl -X POST "+base+"/join\n"+
		"  curl -X POST "+base+"/join -d 'name=My Room'\n"+
		"  curl -X POST -H 'Content-Type: application/json' "+base+`/join -d '{"name":"My Room","lang":"zh-TW"}'`+"\n\n"+
		"Simpler for a bot with the CLI installed (the claim token never appears):\n"+
		"  bot-bridge-cli claim --new --name 'My Room' --display-name 'BOT NAME' --owner 'OWNER NAME' [--lang en]\n\n"+
		"The JSON response includes a creator_prompt: the bot follows it to install the\n"+
		"CLI, claim the bridge as admin, and set up a 5-minute tick routine.\n"+
		"Instructions for AI agents: "+base+"/llms.txt\n")
}

// joinCreateReq is the optional JSON body of POST /join (DEV_SPEC §6 /join).
type joinCreateReq struct {
	Name string `json:"name"`
	Lang string `json:"lang"`
}

// handleJoinCreate implements POST /join (DEV_SPEC §6 /join): unauthenticated,
// per-IP rate limited, globally capped. It creates a PENDING bridge (invite code
// generated, no lands yet) with a one-time admin claim token, and returns the
// invite link, the claim token (shown once), and the copy-paste creator
// prompts. The admin must claim first so the admin is always the first land (§11).
func (s *Server) handleJoinCreate(w http.ResponseWriter, r *http.Request) {
	ip := s.clientIP(r)
	// Rate limit per source /24 (IPv4) or /64 (IPv6) prefix, not per exact IP, so
	// a single /64 cannot spin up bridges from a fresh address each time (Batch 1
	// item 6).
	prefix := invite.SubnetKey(ip)
	prefixHash := s.prefixHash(prefix)
	if ok, ra := s.joinLimiter.Allow(prefix); !ok {
		s.writeAPIError(w, rateLimited(ra))
		return
	}

	name, lang, e := s.parseJoinCreate(w, r)
	if e != nil {
		s.writeAPIError(w, e)
		return
	}
	name = sanitizeText(name)
	if name == "" {
		name = defaultBridgeName
	}
	if n := len([]rune(name)); n < 1 || n > 80 {
		s.writeAPIError(w, badRequest("name must be 1..80 characters"))
		return
	}
	if lang == "" {
		lang = defaultLang
	}
	lang = prompts.NormLang(lang)

	ctx := r.Context()
	// Global cap on simultaneously-pending bridges (DEV_SPEC §6 /join).
	pending, err := s.store.CountPendingBridges(ctx)
	if err != nil {
		s.writeAPIError(w, storeErr(err))
		return
	}
	if pending >= s.cfg.MaxPendingBridges {
		s.writeAPIError(w, &apiErr{
			status:     http.StatusServiceUnavailable,
			code:       "pending_capacity",
			msg:        "too many unclaimed bridges awaiting their first claim; try again later",
			retryAfter: 60,
		})
		return
	}
	// Per-prefix cap on unclaimed pending bridges (Batch 1 item 6).
	perPrefix, err := s.store.CountPendingBridgesByPrefix(ctx, prefixHash)
	if err != nil {
		s.writeAPIError(w, storeErr(err))
		return
	}
	if perPrefix >= s.cfg.MaxPendingBridgesPerPrefix {
		s.writeAPIError(w, &apiErr{
			status:     http.StatusServiceUnavailable,
			code:       "pending_capacity",
			msg:        "too many unclaimed bridges from your network awaiting a claim; claim one first",
			retryAfter: 60,
		})
		return
	}

	// Generate the invite code and the one-time claim token up front so a failure
	// aborts before any row is written.
	code, err := invite.Generate()
	if err != nil {
		s.writeAPIError(w, internalErr())
		return
	}
	token, err := claim.Generate()
	if err != nil {
		s.writeAPIError(w, internalErr())
		return
	}

	b, err := s.store.CreateBridge(ctx, store.CreateBridgeParams{
		Name: name,
		// v0.5.0: every new bridge connects exactly two bots.
		MaxLands: newBridgeMaxLands(s.cfg.MaxLands),
		Pair:     true,
		// Bridges created via /join default to members NOT being able to read the
		// invite code (Batch 1 item 5); the admin issues codes explicitly.
		LandsCanInvite:  false,
		RateLimitPerMin: s.cfg.DefaultRateLimitPerMin,
		QueueQuotaBytes: s.cfg.MaxQueueQuotaBytes,
		Pending:         true,
		ClaimTokenHash:  claim.Hash(s.pepper, token),
		ClaimExpiresAt:  ptrInt64(s.clock.Now().Add(s.cfg.ClaimTTL).UnixMilli()),
		PendingPrefix:   prefixHash,
	})
	if err != nil {
		s.writeAPIError(w, storeErr(err))
		return
	}
	enc, err := invite.Encrypt(s.cfg.InviteKey, b.ID, code)
	if err != nil {
		s.writeAPIError(w, internalErr())
		return
	}
	if err := s.store.SetInviteCode(ctx, b.ID, enc, b.CreatedAt); err != nil {
		s.writeAPIError(w, storeErr(err))
		return
	}

	claimExpires := rfc3339(*b.ClaimExpiresAt)
	info := prompts.Info{
		BridgeName:      name,
		BridgeID:        b.ID,
		Lang:            lang,
		InviteLink:      s.inviteLink(b.ID),
		InviteCode:      code,
		ClaimToken:      token,
		ClaimExpiresRel: humanMinutes(s.cfg.ClaimTTL, lang),
		DLBase:          strings.TrimRight(s.cfg.PublicURL, "/") + "/dl",
	}

	// No invite_code / invite_prompt (removed in v0.4.9): the invite the owner
	// forwards is printed by claim (INVITE_MESSAGE, with the admin fingerprint
	// computed by the admin's own CLI), so the response carries only what the
	// creating bot needs. The code stays stored for claim to fetch.
	writeJSON(w, http.StatusCreated, map[string]any{
		"type":             "join_created",
		"bridge_id":        b.ID,
		"invite_link":      info.InviteLink,
		"claim_token":      token,
		"claim_expires_at": claimExpires,
		"lang":             lang,
		"creator_prompt":   prompts.Creator(info),
	})
}

// claimReq is the body of POST /v1/bridges/{id}/claim (DEV_SPEC §6 claim): the
// one-time claim token plus the admin land registration (keys generated on the
// bot's own machine — the server never sees a private key).
type claimReq struct {
	Token string    `json:"token"`
	Land  landBlock `json:"land"`
}

// handleClaim implements POST /v1/bridges/{id}/claim (DEV_SPEC §6 claim): no API
// key; the claim token in the body authenticates. On success the pending bridge
// becomes active, the claimant becomes the admin land, and an API key is returned
// exactly like create. The token is consumed; a second use yields 410.
func (s *Server) handleClaim(w http.ResponseWriter, r *http.Request) {
	bridgeID := r.PathValue("bridge_id")
	ip := s.clientIP(r)

	// Pre-verification throttle, shared with join (DEV_SPEC §8): IP/subnet backoff.
	if blk := s.throttle.Check(ip, bridgeID); blk != nil {
		s.jitter()
		s.writeAPIError(w, rateLimited(blk.RetryAfter))
		return
	}

	bridge, err := s.store.GetBridge(r.Context(), bridgeID)
	if err != nil {
		s.failClaim(w, ip, bridgeID) // unknown bridge folded into claim_failed
		return
	}
	if bridge.Status == "closed" {
		s.jitter()
		s.writeAPIError(w, closedErr(bridge))
		return
	}
	// A bridge that is not pending was already claimed (or was never a /join
	// bridge): the token, if any, is consumed. 410 is terminal and not a guess.
	if !bridge.Pending {
		s.jitter()
		s.writeAPIError(w, &apiErr{status: http.StatusGone, code: "claim_consumed", msg: "claim token already used"})
		return
	}
	// Past its claim expiry: treat as an expired token (uniform, throttled). The
	// sweeper deletes the row; a race before that still fails here.
	if bridge.ClaimExpiresAt != nil && !s.clock.Now().Before(timeFromMillis(*bridge.ClaimExpiresAt)) {
		s.failClaim(w, ip, bridgeID)
		return
	}

	var req claimReq
	if e := s.decode(w, r, &req); e != nil {
		// Malformed body is a client error, not a token guess: no throttle bump.
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

	// Verify the claim token in constant time (DEV_SPEC §6, §16).
	if !claim.Valid(req.Token) || !claim.Equal(s.pepper, req.Token, bridge.ClaimTokenHash) {
		s.failClaim(w, ip, bridgeID)
		return
	}

	// Token good: register the claimant as the admin land.
	fingerprint := protocol.Fingerprint(req.Land.Key.SignPub, req.Land.Key.EncPub)
	landID, _, err := s.store.CreateLand(r.Context(), store.CreateLandParams{
		BridgeID:    bridgeID,
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
	// Activate the bridge; the pending=1 guard makes a lost race return ErrNotFound.
	if err := s.store.ClaimBridge(r.Context(), bridgeID, landID); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			s.jitter()
			s.writeAPIError(w, &apiErr{status: http.StatusGone, code: "claim_consumed", msg: "claim token already used"})
			return
		}
		s.writeAPIError(w, storeErr(err))
		return
	}

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

// failClaim records a failed claim attempt against the shared join throttle
// (IP/subnet backoff), adds response jitter, and returns the uniform claim_failed
// (DEV_SPEC §6 claim, §8). A pending bridge has no admin to notify, so the
// persistent bridge-lock side effect is not applied here.
func (s *Server) failClaim(w http.ResponseWriter, ip, bridgeID string) {
	s.throttle.Fail(ip, bridgeID)
	s.jitter()
	s.writeAPIError(w, errClaimFailed)
}

// parseJoinCreate reads the optional POST /join body (DEV_SPEC §6 /join). It
// accepts an empty body (defaults), application/json, or form-encoded input, all
// under the small joinBodyCap. Unknown JSON fields are rejected.
func (s *Server) parseJoinCreate(w http.ResponseWriter, r *http.Request) (name, lang string, e *apiErr) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, joinBodyCap))
	if err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			return "", "", payloadTooLarge("request body too large")
		}
		return "", "", badRequest("cannot read request body")
	}
	mt := strings.TrimSpace(strings.SplitN(r.Header.Get("Content-Type"), ";", 2)[0])

	if strings.EqualFold(mt, "application/x-www-form-urlencoded") {
		vals, err := url.ParseQuery(string(body))
		if err != nil {
			return "", "", badRequest("invalid form body")
		}
		return vals.Get("name"), vals.Get("lang"), nil
	}
	if len(bytes.TrimSpace(body)) == 0 {
		return "", "", nil // empty body -> defaults
	}
	if mt != "" && !strings.EqualFold(mt, "application/json") {
		return "", "", &apiErr{status: http.StatusUnsupportedMediaType, code: "unsupported_media_type",
			msg: "Content-Type must be application/json or application/x-www-form-urlencoded"}
	}
	var req joinCreateReq
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		return "", "", badRequest("invalid JSON body")
	}
	return req.Name, req.Lang, nil
}

// internalErr is the opaque 500 used when an unexpected internal step fails
// (DEV_SPEC §6.2: never leak internal detail).
func internalErr() *apiErr {
	return &apiErr{status: http.StatusInternalServerError, code: "internal", msg: "internal error"}
}

// ptrInt64 returns a pointer to v, for the nullable claim_expires_at column.
func ptrInt64(v int64) *int64 { return &v }

// humanMinutes renders a claim TTL as a short relative phrase for the prompts
// (Batch 1 item 8: no UTC timestamps in prompts), e.g. "10 分鐘" or "1 小時".
func humanMinutes(d time.Duration, lang string) string {
	en := prompts.IsEnglish(lang)
	if d >= time.Hour && d%time.Hour == 0 {
		if en {
			return strconv.Itoa(int(d/time.Hour)) + " hour(s)"
		}
		return strconv.Itoa(int(d/time.Hour)) + " 小時"
	}
	m := int(d.Minutes())
	if m < 1 {
		m = 1
	}
	if en {
		return strconv.Itoa(m) + " minutes"
	}
	return strconv.Itoa(m) + " 分鐘"
}

// timeFromMillis converts Unix milliseconds to a UTC time.Time for clock
// comparisons against the injected clock.
func timeFromMillis(ms int64) time.Time { return time.UnixMilli(ms).UTC() }
