package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/hackerick4/botobot/internal/protocol"
	"github.com/hackerick4/botobot/internal/store"
)

// maxBodyBytes is the request body cap for every non-/send endpoint (DEV_SPEC
// §6.1: 64 KiB). /send has its own larger cap and is M4.
const maxBodyBytes = 64 * 1024

// apiErr is an HTTP error mapped to a DEV_SPEC §6.2 code. extra carries the
// optional body fields some errors add (e.g. bridge_closed's closed_at).
type apiErr struct {
	status     int
	code       string
	msg        string
	retryAfter int // seconds; sets retry_after_sec + Retry-After when > 0
	extra      map[string]any
}

// Common errors (DEV_SPEC §6.2). Messages are human-readable and leak nothing.
var (
	errUnauthorized = &apiErr{status: http.StatusUnauthorized, code: "unauthorized", msg: "missing or invalid API key"}
	errNotFound     = &apiErr{status: http.StatusNotFound, code: "not_found", msg: "resource not found"}
	errForbidden    = &apiErr{status: http.StatusForbidden, code: "forbidden", msg: "operation requires admin role"}
	errJoinFailed   = &apiErr{status: http.StatusUnauthorized, code: "join_failed", msg: "invite code is invalid or join is not permitted"}
)

func badRequest(msg string) *apiErr {
	return &apiErr{status: http.StatusBadRequest, code: "bad_request", msg: msg}
}

func badSignature(msg string) *apiErr {
	return &apiErr{status: http.StatusBadRequest, code: "bad_signature", msg: msg}
}

func payloadTooLarge(msg string) *apiErr {
	return &apiErr{status: http.StatusRequestEntityTooLarge, code: "payload_too_large", msg: msg}
}

// recipientsMismatch is 409 recipients_mismatch (DEV_SPEC §6.2, §6.4 step 2):
// the recipient set or land_set_version does not match the bridge's current
// state. The body carries the current land_set_version and the expected
// recipient set so the client can refresh and retry.
func recipientsMismatch(version int64, recipients []map[string]any) *apiErr {
	return &apiErr{
		status: http.StatusConflict,
		code:   "recipients_mismatch",
		msg:    "recipient set or land_set_version does not match current lands",
		extra: map[string]any{
			"land_set_version": version,
			"recipients":       recipients,
		},
	}
}

func rateLimited(retryAfter time.Duration) *apiErr {
	sec := int((retryAfter + time.Second - 1) / time.Second)
	if sec < 1 {
		sec = 1
	}
	return &apiErr{status: http.StatusTooManyRequests, code: "rate_limited", msg: "rate limited", retryAfter: sec}
}

// storeErr maps a store error to the right HTTP error (DEV_SPEC §6.2).
func storeErr(err error) *apiErr {
	switch {
	case errors.Is(err, store.ErrNotFound):
		return errNotFound
	case errors.Is(err, store.ErrBackpressure):
		return &apiErr{status: http.StatusServiceUnavailable, code: "backpressure", msg: "server busy, retry", retryAfter: 1}
	case errors.Is(err, store.ErrQuotaExceeded), errors.Is(err, store.ErrInboxFull):
		return &apiErr{status: http.StatusInsufficientStorage, code: "queue_quota_exceeded", msg: "bridge queue quota exceeded"}
	case errors.Is(err, store.ErrDuplicateMsgID):
		return &apiErr{status: http.StatusConflict, code: "duplicate_msg_id", msg: "msg_id already used with different content"}
	default:
		return &apiErr{status: http.StatusInternalServerError, code: "internal", msg: "internal error"}
	}
}

// writeAPIError emits the DEV_SPEC §6.1 error envelope. Retry-After is set for
// throttle/backpressure errors.
func (s *Server) writeAPIError(w http.ResponseWriter, e *apiErr) {
	obj := map[string]any{"code": e.code, "message": e.msg}
	if e.retryAfter > 0 {
		obj["retry_after_sec"] = e.retryAfter
		w.Header().Set("Retry-After", strconv.Itoa(e.retryAfter))
	}
	for k, v := range e.extra {
		obj[k] = v
	}
	writeJSON(w, e.status, map[string]any{"error": obj})
}

// decode reads the JSON request body into dst with DisallowUnknownFields, a
// Content-Type check (415), and the 64 KiB cap (413) (DEV_SPEC §6.1).
func (s *Server) decode(w http.ResponseWriter, r *http.Request, dst any) *apiErr {
	return s.decodeLimited(w, r, dst, maxBodyBytes)
}

// decodeLimited is decode with an explicit body-size cap. /send uses a larger
// cap (MaxEnvelopeBytes + 1 MiB per DEV_SPEC §6.1); everything else uses 64 KiB.
func (s *Server) decodeLimited(w http.ResponseWriter, r *http.Request, dst any, limit int64) *apiErr {
	if ct := r.Header.Get("Content-Type"); ct != "" {
		mediaType := strings.TrimSpace(strings.SplitN(ct, ";", 2)[0])
		if !strings.EqualFold(mediaType, "application/json") {
			return &apiErr{status: http.StatusUnsupportedMediaType, code: "unsupported_media_type", msg: "Content-Type must be application/json"}
		}
	}
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			return &apiErr{status: http.StatusRequestEntityTooLarge, code: "payload_too_large", msg: "request body too large"}
		}
		return badRequest("invalid JSON body")
	}
	if dec.More() {
		return badRequest("unexpected trailing data after JSON body")
	}
	return nil
}

// ---- Shared input objects (DEV_SPEC §6.3) ----

// landKeyInput is a land's public key material. encoding/json decodes the base64
// (standard, with padding per §6.1) fields into bytes.
type landKeyInput struct {
	EncPub     []byte `json:"enc_pub"`
	SignPub    []byte `json:"sign_pub"`
	BindingSig []byte `json:"binding_sig"`
}

// validate checks the key lengths and the Ed25519 key binding (DEV_SPEC §7.6);
// a binding failure is 400 bad_signature.
func (k *landKeyInput) validate() *apiErr {
	if len(k.EncPub) != protocol.KeySize {
		return badRequest("enc_pub must be 32 bytes")
	}
	if len(k.SignPub) != protocol.KeySize {
		return badRequest("sign_pub must be 32 bytes")
	}
	if len(k.BindingSig) != protocol.SignatureSize {
		return badRequest("binding_sig must be 64 bytes")
	}
	if !protocol.VerifyBinding(k.EncPub, k.SignPub, k.BindingSig) {
		return badSignature("key binding signature is invalid")
	}
	return nil
}

// landProfileInput is a land's mutable profile (DEV_SPEC §6.3).
type landProfileInput struct {
	Handle      string `json:"handle"`
	DisplayName string `json:"display_name"`
	OwnerName   string `json:"owner_name"`
}

var handleRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{1,31}$`)

// normalize validates the handle and sanitizes/limits the free-text fields,
// returning the cleaned values (DEV_SPEC §6.3).
func (p *landProfileInput) normalize() (displayName, ownerName string, e *apiErr) {
	if !handleRe.MatchString(p.Handle) {
		return "", "", badRequest("handle must match ^[a-z0-9][a-z0-9_-]{1,31}$")
	}
	dn, err := protocol.NormalizeProfileName("display_name", p.DisplayName)
	if err != nil {
		return "", "", badRequest(err.Error())
	}
	on, err := protocol.NormalizeProfileName("owner_name", p.OwnerName)
	if err != nil {
		return "", "", badRequest(err.Error())
	}
	return dn, on, nil
}

// sanitizeText drops control characters (C0, DEL, C1), Unicode format/bidi
// characters and line/paragraph separators (DEV_SPEC §6.3; EXFIL_DEFENSE P0).
func sanitizeText(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) || r == 0x2028 || r == 0x2029 {
			return -1
		}
		return r
	}, s)
}

// ---- Output objects (DEV_SPEC §6.3) ----

type landOut struct {
	LandID          string `json:"land_id"`
	Role            string `json:"role"`
	Handle          string `json:"handle"`
	DisplayName     string `json:"display_name"`
	OwnerName       string `json:"owner_name"`
	EncPub          []byte `json:"enc_pub"`
	SignPub         []byte `json:"sign_pub"`
	BindingSig      []byte `json:"binding_sig"`
	Fingerprint     string `json:"fingerprint"`
	KeyID           string `json:"key_id"`
	PollIntervalSec int    `json:"poll_interval_sec"`
	JoinedAt        string `json:"joined_at"`
}

type bridgeSettingsOut struct {
	LandsCanInvite  bool  `json:"lands_can_invite"`
	JoinLocked      bool  `json:"join_locked"`
	RateLimitPerMin int   `json:"rate_limit_per_min"`
	QueueQuotaBytes int64 `json:"queue_quota_bytes"`
}

type bridgeOut struct {
	BridgeID       string            `json:"bridge_id"`
	Name           string            `json:"name"`
	Description    string            `json:"description"`
	Status         string            `json:"status"`
	AdminLandID    string            `json:"admin_land_id"`
	MaxLands       int               `json:"max_lands"`
	Pair           bool              `json:"pair"`
	LandCount      int               `json:"land_count"`
	LandSetVersion int64             `json:"land_set_version"`
	Settings       bridgeSettingsOut `json:"settings"`
	QueueBytes     int64             `json:"queue_bytes"`
	CreatedAt      string            `json:"created_at"`
	InviteLink     string            `json:"invite_link"`
	// PublicEntry marks a bridge created through a public entry (v0.6.0).
	PublicEntry bool `json:"public_entry,omitempty"`
}

// rfc3339 renders a Unix-millis timestamp as an RFC 3339 UTC string with
// millisecond precision (DEV_SPEC §6.1, e.g. 2026-10-29T07:02:00.123Z).
func rfc3339(ms int64) string {
	return time.UnixMilli(ms).UTC().Format("2006-01-02T15:04:05.000Z07:00")
}

// inviteLink builds the public invite URL for a bridge (DEV_SPEC §6.3).
// inviteLinkFor is the bridge's invite link, empty for a public-entry bridge
// (it has no invite; guests come in through the entry link).
func (s *Server) inviteLinkFor(b *store.Bridge) string {
	if b.EntryID != nil {
		return ""
	}
	return s.inviteLink(b.ID)
}

func (s *Server) inviteLink(bridgeID string) string {
	return strings.TrimRight(s.cfg.PublicURL, "/") + "/b/" + bridgeID
}

// buildLandOut assembles a Land output object, resolving the land's active
// public key (DEV_SPEC §6.3).
func (s *Server) buildLandOut(r *http.Request, l *store.Land) (*landOut, *apiErr) {
	out := &landOut{
		LandID:          l.ID,
		Role:            l.Role,
		Handle:          l.Handle,
		DisplayName:     l.DisplayName,
		OwnerName:       l.OwnerName,
		PollIntervalSec: l.PollIntervalSec,
		JoinedAt:        rfc3339(l.JoinedAt),
	}
	if l.ActiveKeyID != nil {
		pk, err := s.store.GetPublicKey(r.Context(), *l.ActiveKeyID)
		if err != nil {
			return nil, storeErr(err)
		}
		out.EncPub = pk.EncPub
		out.SignPub = pk.SignPub
		out.BindingSig = pk.BindingSig
		out.Fingerprint = pk.Fingerprint
		out.KeyID = pk.ID
	}
	return out, nil
}

// buildBridgeOut assembles a Bridge output object (DEV_SPEC §6.3).
func (s *Server) buildBridgeOut(r *http.Request, b *store.Bridge) (*bridgeOut, *apiErr) {
	count, err := s.store.CountActiveLands(r.Context(), b.ID)
	if err != nil {
		return nil, storeErr(err)
	}
	admin := ""
	if b.AdminLandID != nil {
		admin = *b.AdminLandID
	}
	return &bridgeOut{
		BridgeID:       b.ID,
		Name:           b.Name,
		Description:    b.Description,
		Status:         b.Status,
		AdminLandID:    admin,
		MaxLands:       b.MaxLands,
		Pair:           b.Pair,
		LandCount:      count,
		LandSetVersion: b.LandSetVersion,
		Settings: bridgeSettingsOut{
			LandsCanInvite:  b.LandsCanInvite,
			JoinLocked:      b.JoinLocked,
			RateLimitPerMin: b.RateLimitPerMin,
			QueueQuotaBytes: b.QueueQuotaBytes,
		},
		QueueBytes:  b.QueueBytes,
		CreatedAt:   rfc3339(b.CreatedAt),
		InviteLink:  s.inviteLinkFor(b),
		PublicEntry: b.EntryID != nil,
	}, nil
}
