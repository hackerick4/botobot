package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"

	"github.com/hackerick4/botobot/internal/protocol"
)

// Trust-on-first-use (TOFU) key pinning for a bridge (Batch 1 item 4).
//
// Pure TOFU, no prompts (v0.4.5). The first key seen for a member is pinned and
// trusted silently: at join/claim for the roster then present, and later for
// any member who joins. Nobody is asked to compare fingerprints. Only a CHANGE
// of an already-pinned key (or a key whose binding signature does not verify)
// marks that land untrusted. While any other land is untrusted, sending is
// paused (the server requires every other land as a recipient, so we cannot
// send to a trusted subset) and the owner is told in one sentence; the owner
// can re-trust with `trust` (held for one confirmation). Messages from an
// untrusted key are still shown but flagged.
//
// Residual risk: the first key is trusted blindly, so a server that swaps a
// newcomer's key before we ever see it is not caught. It cannot swap a pinned
// key later unnoticed, and the join --admin-fp check (done by the CLI itself)
// still protects the admin key a member starts from. When that check fails the
// store is Locked: nothing is auto-trusted on that bridge.

// pinEntry is the pinned state for one land. Fingerprint/KeyID is what we trust;
// Pending* records a newly-observed key that differs and awaits owner approval.
type pinEntry struct {
	Fingerprint        string `json:"fingerprint"`
	KeyID              string `json:"key_id"`
	Trusted            bool   `json:"trusted"`
	FirstSeenMS        int64  `json:"first_seen_ms"`
	PendingFingerprint string `json:"pending_fingerprint,omitempty"`
	PendingKeyID       string `json:"pending_key_id,omitempty"`
}

// pinStore is pins.json: land_id -> pinEntry.
type pinStore struct {
	Entries map[string]pinEntry `json:"entries"`
	// Locked is set when join's admin fingerprint check failed. While set, no
	// key is trusted automatically on this bridge.
	Locked string `json:"locked,omitempty"`
	// ExpectAdminFP is the invite's admin fingerprint when join could not fetch
	// the roster; the first admin key seen later is checked against it.
	ExpectAdminFP string `json:"expect_admin_fp,omitempty"`
}

// trustKind classifies why a land is currently untrusted.
type trustKind string

const (
	trustNewMember  trustKind = "new_member"
	trustKeyChanged trustKind = "key_changed"
	trustBindingBad trustKind = "binding_invalid"
)

// trustAlert describes one untrusted land for owner notification.
type trustAlert struct {
	LandID      string
	Handle      string
	DisplayName string
	Kind        trustKind
	Fingerprint string // current observed §7.7 fingerprint (30 digits)
	ShortCode   string // short human-verifiable code (4 groups of 4 hex)
	Fresh       bool   // first time this exact condition was observed (notify once)
}

func (ks *keystore) pinsPath(id string) string { return filepath.Join(ks.bridgeDir(id), "pins.json") }

func (ks *keystore) loadPins(id string) (*pinStore, error) {
	ps := &pinStore{Entries: map[string]pinEntry{}}
	err := readJSON(ks.pinsPath(id), ps)
	if os.IsNotExist(err) {
		return ps, nil
	}
	if err != nil {
		return nil, err
	}
	if ps.Entries == nil {
		ps.Entries = map[string]pinEntry{}
	}
	return ps, nil
}

func (ks *keystore) savePins(id string, ps *pinStore) error {
	return writeJSONAtomic(ks.pinsPath(id), ps)
}

// computeFingerprint recomputes a land's §7.7 fingerprint from its public keys so
// pinning never trusts the server's claimed fingerprint value.
func computeFingerprint(l *cachedLand) string {
	return protocol.Fingerprint(l.SignPub, l.EncPub)
}

// shortCode is a short, human-verifiable code derived from the same key material
// as the fingerprint: the first 8 bytes of SHA-256(sign_pub||enc_pub) as four
// 4-hex groups (Batch 1 item 4 "4 groups of 4 hex").
func shortCode(l *cachedLand) string {
	h := sha256.New()
	h.Write(l.SignPub)
	h.Write(l.EncPub)
	sum := h.Sum(nil)
	hexStr := hex.EncodeToString(sum[:8])
	return hexStr[0:4] + " " + hexStr[4:8] + " " + hexStr[8:12] + " " + hexStr[12:16]
}

// bindingOK reports whether a land's key binding signature verifies (§7.6).
func bindingOK(l *cachedLand) bool {
	return len(l.EncPub) == protocol.KeySize && len(l.SignPub) == protocol.KeySize &&
		len(l.BindingSig) == protocol.SignatureSize &&
		protocol.VerifyBinding(l.EncPub, l.SignPub, l.BindingSig)
}

// pinInitial trusts every land currently in lc except self, used right after
// join/claim (TOFU). Lands without an active key or with an invalid binding are
// skipped (they will surface as alerts on the next reconcile).
func (ps *pinStore) pinInitial(lc *landsCache, selfID string, nowMS int64) {
	for i := range lc.Lands {
		l := &lc.Lands[i]
		if l.LandID == selfID || l.KeyID == "" || !bindingOK(l) {
			continue
		}
		ps.Entries[l.LandID] = pinEntry{
			Fingerprint: computeFingerprint(l),
			KeyID:       l.KeyID,
			Trusted:     true,
			FirstSeenMS: nowMS,
		}
	}
}

// reconcile compares the current land set against the pins, recording new members
// and key changes and returning an alert per untrusted land. It mutates ps
// (adding pending state); the caller persists it. changed reports whether ps was
// modified.
func (ps *pinStore) reconcile(lc *landsCache, selfID string, nowMS int64) (alerts []trustAlert, changed bool) {
	// Deferred admin check: join could not compare the invite's admin
	// fingerprint yet. Compare as soon as the admin key is visible; until then
	// trust nobody new (no prompt, just wait for a later tick).
	waitingAdmin := false
	if ps.Locked == "" && ps.ExpectAdminFP != "" {
		var admin *cachedLand
		for i := range lc.Lands {
			if l := &lc.Lands[i]; l.Role == "admin" && l.LandID != selfID && l.KeyID != "" && bindingOK(l) {
				admin = l
			}
		}
		if admin != nil {
			if normFP(ps.ExpectAdminFP) != normFP(computeFingerprint(admin)) {
				ps.Locked = "admin_fingerprint_mismatch"
			}
			ps.ExpectAdminFP = ""
			changed = true
		} else {
			waitingAdmin = true
		}
	}
	for i := range lc.Lands {
		l := &lc.Lands[i]
		if l.LandID == selfID || l.KeyID == "" {
			continue
		}
		fp := computeFingerprint(l)
		mkAlert := func(kind trustKind, fresh bool) trustAlert {
			return trustAlert{LandID: l.LandID, Handle: l.Handle, DisplayName: sanitizeDisplayName(l.DisplayName),
				Kind: kind, Fingerprint: fp, ShortCode: shortCode(l), Fresh: fresh}
		}
		e, ok := ps.Entries[l.LandID]
		firstSeen := nowMS
		if ok && e.FirstSeenMS != 0 {
			firstSeen = e.FirstSeenMS
		}
		if !bindingOK(l) {
			// Keep an existing trusted pin (so a later valid key is still compared
			// against it); otherwise record an untrusted entry. The alert fires
			// once per distinct bad key rather than every tick.
			if ok && e.Trusted {
				fresh := e.PendingFingerprint != fp
				if fresh {
					e.PendingFingerprint, e.PendingKeyID = fp, l.KeyID
					ps.Entries[l.LandID] = e
					changed = true
				}
				alerts = append(alerts, mkAlert(trustBindingBad, fresh))
				continue
			}
			fresh := !ok || e.Fingerprint != fp
			if fresh {
				ps.Entries[l.LandID] = pinEntry{Fingerprint: fp, KeyID: l.KeyID, Trusted: false, FirstSeenMS: firstSeen}
				changed = true
			}
			alerts = append(alerts, mkAlert(trustBindingBad, fresh))
			continue
		}
		switch {
		case !ok && waitingAdmin:
			// Not pinned yet: decided at a later tick, after the admin check.
		case !ok && ps.Locked == "":
			// First time we see this member: trust on first use, silently.
			ps.Entries[l.LandID] = pinEntry{Fingerprint: fp, KeyID: l.KeyID, Trusted: true, FirstSeenMS: nowMS}
			changed = true
		case !ok:
			// Locked bridge (admin fingerprint mismatch at join): never auto-trust.
			ps.Entries[l.LandID] = pinEntry{Fingerprint: fp, KeyID: l.KeyID, Trusted: false, FirstSeenMS: nowMS}
			changed = true
			alerts = append(alerts, mkAlert(trustNewMember, true))
		case e.Trusted && e.Fingerprint == fp:
			// Consistent with the pin; clear any stale pending record.
			if e.PendingFingerprint != "" {
				e.PendingFingerprint, e.PendingKeyID = "", ""
				ps.Entries[l.LandID] = e
				changed = true
			}
		case e.Trusted && e.Fingerprint != fp:
			// A known member's key changed; keep the old pin, flag the new one.
			fresh := e.PendingFingerprint != fp
			if fresh {
				e.PendingFingerprint, e.PendingKeyID = fp, l.KeyID
				ps.Entries[l.LandID] = e
				changed = true
			}
			alerts = append(alerts, mkAlert(trustKeyChanged, fresh))
		case ps.Locked == "" && e.Fingerprint == fp:
			// Known but never trusted, same key as first recorded: a new member
			// left pending by a CLI older than v0.4.5. Trust it on first use.
			ps.Entries[l.LandID] = pinEntry{Fingerprint: fp, KeyID: l.KeyID, Trusted: true, FirstSeenMS: firstSeen}
			changed = true
		case ps.Locked == "":
			// A different key than the one first recorded: never auto-accept a
			// changed key; the owner decides once.
			fresh := e.PendingFingerprint != fp
			if fresh {
				e.PendingFingerprint, e.PendingKeyID = fp, l.KeyID
				ps.Entries[l.LandID] = e
				changed = true
			}
			alerts = append(alerts, mkAlert(trustKeyChanged, fresh))
		default:
			// Locked bridge: stays untrusted until the owner re-trusts.
			fresh := e.Fingerprint != fp
			if fresh {
				e.Fingerprint, e.KeyID = fp, l.KeyID
				ps.Entries[l.LandID] = e
				changed = true
			}
			alerts = append(alerts, mkAlert(trustNewMember, fresh))
		}
	}
	return alerts, changed
}

// isTrusted reports whether a land's current key may be encrypted to / acted on:
// a trusted pin whose fingerprint matches the current key and whose binding
// verifies.
func (ps *pinStore) isTrusted(l *cachedLand) bool {
	if l.KeyID == "" || !bindingOK(l) {
		return false
	}
	e, ok := ps.Entries[l.LandID]
	return ok && e.Trusted && e.Fingerprint == computeFingerprint(l)
}

// trust pins the land's current key as trusted (owner approval), clearing any
// pending state. Returns false if the land has no usable key.
func (ps *pinStore) trust(l *cachedLand, nowMS int64) bool {
	if l.KeyID == "" || !bindingOK(l) {
		return false
	}
	first := nowMS
	if e, ok := ps.Entries[l.LandID]; ok && e.FirstSeenMS != 0 {
		first = e.FirstSeenMS
	}
	ps.Entries[l.LandID] = pinEntry{
		Fingerprint: computeFingerprint(l),
		KeyID:       l.KeyID,
		Trusted:     true,
		FirstSeenMS: first,
	}
	return true
}
