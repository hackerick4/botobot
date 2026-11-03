package server

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"net/http"
	"sort"

	"github.com/hackerick4/botobot/internal/protocol"
	"github.com/hackerick4/botobot/internal/store"
)

// handleSend implements POST /v1/bridges/{bridge_id}/send (DEV_SPEC §6.4, §7).
// It runs the server-side envelope validations in the order §6.4 specifies,
// then stores the message once and fans a per-recipient wrapped key out to each
// recipient's inbox (idempotent on a matching resend). The content stays end-to-
// end encrypted; the server never sees plaintext.
func (s *Server) handleSend(w http.ResponseWriter, r *http.Request, ac *authContext) *apiErr {
	// Body cap for /send (DEV_SPEC §6.1 + §19). The envelope size limit (§9)
	// counts raw bytes, but the JSON body carries the ciphertext and wrapped
	// keys as standard base64 (≈4/3 expansion); over the cap is 413.
	var env protocol.Envelope
	if e := s.decodeLimited(w, r, &env, sendBodyCap(s.cfg.MaxEnvelopeBytes)); e != nil {
		return e
	}

	// Structural checks: header field shapes, 64-byte signature, non-empty
	// ciphertext, and a wrapped key of 80 bytes per recipient with the key set
	// equal to the recipient set (DEV_SPEC §6.4 steps 3–4 structure, §7).
	if err := env.Validate(); err != nil {
		return badRequest(err.Error())
	}
	h := &env.Header

	// Step 1: the header must name this caller's bridge, land, and active key.
	if h.BridgeID != ac.bridge.ID {
		return badRequest("header.bridge_id does not match the request path")
	}
	if h.SenderLandID != ac.land.ID {
		return badRequest("header.sender_land_id does not match the authenticated land")
	}
	if ac.land.ActiveKeyID == nil || h.SenderKeyID != *ac.land.ActiveKeyID {
		return badRequest("header.sender_key_id is not the land's active key")
	}

	// Step 2: land_set_version and the recipient set must match the bridge's
	// current lands (minus the sender), each with its active key id.
	lands, err := s.store.ListActiveLands(r.Context(), ac.bridge.ID)
	if err != nil {
		return storeErr(err)
	}
	expected := make(map[string]string, len(lands)) // land_id -> active key_id
	for _, l := range lands {
		if l.ID == ac.land.ID || l.ActiveKeyID == nil {
			continue
		}
		expected[l.ID] = *l.ActiveKeyID
	}
	if len(expected) == 0 {
		// Only the sender is present: there is nobody to deliver to (DEV_SPEC §6.4).
		return badRequest("the other bot has not joined this bridge yet")
	}
	if h.LandSetVersion != ac.bridge.LandSetVersion {
		return recipientsMismatch(ac.bridge.LandSetVersion, expectedRecipients(expected))
	}
	if !recipientsEqual(h.Recipients, expected) {
		return recipientsMismatch(ac.bridge.LandSetVersion, expectedRecipients(expected))
	}

	// Step 3: each wrapped key must hash to the value pinned in the header.
	for _, rc := range h.Recipients {
		wk := env.WrappedKeys[rc.LandID]
		sum := sha256.Sum256(wk)
		if subtle.ConstantTimeCompare(sum[:], rc.WrappedKeySHA256) != 1 {
			return badRequest("wrapped_key_sha256 does not match the wrapped key")
		}
	}

	// Step 4: the ciphertext must hash to header.ciphertext_sha256 (the nonce
	// length is already checked by Validate).
	ctSum := sha256.Sum256(env.Ciphertext)
	if subtle.ConstantTimeCompare(ctSum[:], h.CiphertextSHA256) != 1 {
		return badRequest("ciphertext_sha256 does not match the ciphertext")
	}

	// Step 5: verify the signature with the sender's registered signing key.
	pk, err := s.store.GetPublicKey(r.Context(), *ac.land.ActiveKeyID)
	if err != nil {
		return storeErr(err)
	}
	if !protocol.Verify(pk.SignPub, h, env.Signature) {
		return badSignature("envelope signature verification failed")
	}

	// Step 6: envelope size (header + ciphertext + wrapped keys) must be within
	// the limit (DEV_SPEC §9). This size is also what the bridge quota charges.
	headerJSON, err := json.Marshal(h)
	if err != nil {
		return &apiErr{status: http.StatusInternalServerError, code: "internal", msg: "internal error"}
	}
	var wrappedBytes int64
	for _, wk := range env.WrappedKeys {
		wrappedBytes += int64(len(wk))
	}
	size := int64(len(headerJSON)) + int64(len(env.Ciphertext)) + wrappedBytes
	if size > s.cfg.MaxEnvelopeBytes {
		return payloadTooLarge("envelope exceeds the maximum size")
	}

	// Step 7: store once and fan out (idempotent on a matching resend; a
	// conflicting resend under the same msg_id is 409 duplicate_msg_id).
	recips := make([]store.RecipientWrap, 0, len(h.Recipients))
	for _, rc := range h.Recipients {
		recips = append(recips, store.RecipientWrap{LandID: rc.LandID, WrappedKey: env.WrappedKeys[rc.LandID]})
	}
	res, err := s.store.EnqueueMessage(r.Context(), store.EnqueueMessageParams{
		BridgeID:         ac.bridge.ID,
		SenderLandID:     ac.land.ID,
		MsgID:            h.MsgID,
		HeaderJSON:       headerJSON,
		Signature:        env.Signature,
		Ciphertext:       env.Ciphertext,
		CiphertextSHA256: h.CiphertextSHA256,
		SizeBytes:        size,
		Recipients:       recips,
	})
	if err != nil {
		return storeErr(err)
	}

	deliveries := make([]map[string]any, 0, len(res.Deliveries))
	for _, d := range res.Deliveries {
		deliveries = append(deliveries, map[string]any{"land_id": d.RecipientLandID, "status": d.Status})
	}
	writeJSON(w, http.StatusAccepted, map[string]any{
		"msg_id":     res.MsgID,
		"expires_at": rfc3339(res.ExpiresAt),
		"deliveries": deliveries,
	})
	return nil
}

// sendBodyCap is the HTTP request-body limit for POST /send. The envelope size
// limit (DEV_SPEC §9) is on raw bytes (header + ciphertext + wrapped keys), but
// the JSON body transports the ciphertext and wrapped keys as standard base64
// (4/3 expansion). The cap is therefore 4/3·MaxEnvelopeBytes plus 1 MiB of
// headroom for JSON structure and the header; anything larger is rejected with
// 413 before decoding (DEV_SPEC §6.1 said MaxEnvelopeBytes+1 MiB — see §19,
// that literal value would reject a legitimately max-sized base64 envelope).
func sendBodyCap(maxEnvelope int64) int64 {
	return maxEnvelope + maxEnvelope/3 + 1<<20
}

// recipientsEqual reports whether the header's recipient set exactly equals the
// expected land_id -> active key_id map: same land ids and matching key ids
// (DEV_SPEC §6.4 step 2).
func recipientsEqual(got []protocol.Recipient, expected map[string]string) bool {
	if len(got) != len(expected) {
		return false
	}
	for _, rc := range got {
		keyID, ok := expected[rc.LandID]
		if !ok || keyID != rc.KeyID {
			return false
		}
	}
	return true
}

// expectedRecipients renders the expected recipient set for a recipients_mismatch
// body (DEV_SPEC §6.4 step 2: "current info"), sorted for stable output.
func expectedRecipients(expected map[string]string) []map[string]any {
	ids := make([]string, 0, len(expected))
	for id := range expected {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	out := make([]map[string]any, 0, len(ids))
	for _, id := range ids {
		out = append(out, map[string]any{"land_id": id, "key_id": expected[id]})
	}
	return out
}
