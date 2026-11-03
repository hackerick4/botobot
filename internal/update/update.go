// Package update implements the signed CLI self-update channel
// (docs/CLI_UPDATE.md).
//
// Trust model: the release signing key lives only on the release box. The
// server (botobot.app) only hosts files. A client accepts an update only if:
//
//   - manifest.json carries a valid Ed25519 signature (manifest.json.sig) by a
//     key the client trusts (pinned at build time, or announced as next_keys in
//     an earlier manifest that was itself signed by a trusted key);
//   - its sequence number is greater than the running binary's and greater
//     than any sequence this home has already accepted (no downgrade/replay);
//   - it has not expired and was not issued in the future;
//   - the downloaded binary matches the signed size and sha256.
//
// A compromised server or a network attacker can therefore only withhold
// updates (or replay an old manifest, which is ignored); it cannot make a client
// run a binary that the release key did not sign.
package update

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"
)

const (
	// ManifestSchema identifies the manifest format.
	ManifestSchema = "bb-cli-manifest-v1"
	// SigSchema identifies the detached signature file format.
	SigSchema = "bb-cli-manifest-sig-v1"
	// sigDomain is prepended to the manifest bytes before signing so a
	// signature can never be reused for another purpose.
	sigDomain = "bb-cli-manifest-v1\n"
	// MaxManifestBytes bounds the manifest and signature downloads.
	MaxManifestBytes = 64 << 10
	// MaxBinaryBytes bounds a binary download.
	MaxBinaryBytes = 128 << 20
	// maxFutureSkew tolerates small clock differences on issued_at.
	maxFutureSkew = 24 * time.Hour
)

// Manifest is the signed release description.
type Manifest struct {
	Schema    string              `json:"schema"`
	Channel   string              `json:"channel"`
	Version   string              `json:"version"`
	Seq       uint64              `json:"seq"`
	IssuedAt  time.Time           `json:"issued_at"`
	ExpiresAt time.Time           `json:"expires_at"`
	Platforms map[string]Platform `json:"platforms"`
	// NextKeys announces release keys that future manifests may be signed
	// with (key rotation). They are covered by this manifest's signature, so
	// they are trusted only as far as the key that signed it.
	NextKeys []PubKey `json:"next_keys,omitempty"`
}

// Platform is one downloadable binary.
type Platform struct {
	URL    string `json:"url"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

// PubKey is a release public key.
type PubKey struct {
	KeyID string `json:"key_id"`
	Pub   string `json:"pub"` // base64 std, 32 bytes
}

// SigFile is manifest.json.sig.
type SigFile struct {
	Schema string `json:"schema"`
	Sigs   []Sig  `json:"sigs"`
}

// Sig is one detached signature.
type Sig struct {
	KeyID string `json:"key_id"`
	Sig   string `json:"sig"` // base64 std, 64 bytes
}

var (
	versionRe = regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+(?:-[0-9A-Za-z.-]{1,40})?$`)
	hexRe     = regexp.MustCompile(`^[0-9a-f]{64}$`)
	platRe    = regexp.MustCompile(`^[a-z0-9]+-[a-z0-9]+$`)
)

// KeyID is the first 8 bytes of sha256(pub), hex.
func KeyID(pub ed25519.PublicKey) string {
	h := sha256.Sum256(pub)
	return hex.EncodeToString(h[:8])
}

// ParsePub decodes a base64 public key.
func ParsePub(b64 string) (ed25519.PublicKey, error) {
	b, err := base64.StdEncoding.DecodeString(strings.TrimSpace(b64))
	if err != nil || len(b) != ed25519.PublicKeySize {
		return nil, errors.New("bad public key")
	}
	return ed25519.PublicKey(b), nil
}

// Keyring maps key id to public key.
type Keyring map[string]ed25519.PublicKey

// NewKeyring builds a keyring from base64 keys; invalid entries are skipped.
func NewKeyring(b64s ...string) Keyring {
	kr := Keyring{}
	for _, s := range b64s {
		if s == "" {
			continue
		}
		if pub, err := ParsePub(s); err == nil {
			kr[KeyID(pub)] = pub
		}
	}
	return kr
}

// SignManifest signs manifest bytes (release tooling only).
func SignManifest(priv ed25519.PrivateKey, manifest []byte) Sig {
	pub := priv.Public().(ed25519.PublicKey)
	sig := ed25519.Sign(priv, append([]byte(sigDomain), manifest...))
	return Sig{KeyID: KeyID(pub), Sig: base64.StdEncoding.EncodeToString(sig)}
}

// Errors returned by Verify. ErrNotNewer is the normal "nothing to do" case.
var (
	ErrBadSignature = errors.New("manifest signature not valid for any trusted release key")
	ErrBadManifest  = errors.New("manifest malformed")
	ErrExpired      = errors.New("manifest expired")
	ErrFuture       = errors.New("manifest issued in the future")
	ErrNotNewer     = errors.New("no newer release")
)

// Verify checks the detached signature over the exact manifest bytes, then
// parses and validates the manifest. minSeq is the highest sequence the client
// already has; the manifest must be strictly greater.
func Verify(manifest, sigFile []byte, keys Keyring, now time.Time, minSeq uint64) (*Manifest, error) {
	if len(manifest) == 0 || len(manifest) > MaxManifestBytes || len(sigFile) > MaxManifestBytes {
		return nil, ErrBadManifest
	}
	var sf SigFile
	if err := json.Unmarshal(sigFile, &sf); err != nil || sf.Schema != SigSchema {
		return nil, ErrBadSignature
	}
	msg := append([]byte(sigDomain), manifest...)
	ok := false
	for _, s := range sf.Sigs {
		pub, known := keys[s.KeyID]
		if !known {
			continue
		}
		sig, err := base64.StdEncoding.DecodeString(s.Sig)
		if err != nil || len(sig) != ed25519.SignatureSize {
			continue
		}
		if ed25519.Verify(pub, msg, sig) {
			ok = true
			break
		}
	}
	if !ok {
		return nil, ErrBadSignature
	}
	dec := json.NewDecoder(bytes.NewReader(manifest))
	dec.DisallowUnknownFields()
	var m Manifest
	if err := dec.Decode(&m); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrBadManifest, err)
	}
	if err := m.validate(); err != nil {
		return nil, err
	}
	if now.After(m.ExpiresAt) {
		return nil, ErrExpired
	}
	if m.IssuedAt.After(now.Add(maxFutureSkew)) {
		return nil, ErrFuture
	}
	if m.Seq <= minSeq {
		return &m, ErrNotNewer
	}
	return &m, nil
}

func (m *Manifest) validate() error {
	bad := func(why string) error { return fmt.Errorf("%w: %s", ErrBadManifest, why) }
	if m.Schema != ManifestSchema {
		return bad("schema")
	}
	if !versionRe.MatchString(m.Version) {
		return bad("version")
	}
	if m.Seq == 0 {
		return bad("seq")
	}
	if m.IssuedAt.IsZero() || m.ExpiresAt.IsZero() || !m.ExpiresAt.After(m.IssuedAt) {
		return bad("dates")
	}
	if len(m.Platforms) == 0 {
		return bad("platforms")
	}
	for name, p := range m.Platforms {
		if !platRe.MatchString(name) || !hexRe.MatchString(p.SHA256) || p.Size <= 0 || p.Size > MaxBinaryBytes {
			return bad("platform " + name)
		}
		if err := checkURL(p.URL); err != nil {
			return bad(err.Error())
		}
	}
	for _, k := range m.NextKeys {
		pub, err := ParsePub(k.Pub)
		if err != nil || KeyID(pub) != k.KeyID {
			return bad("next_keys")
		}
	}
	return nil
}

// checkURL requires https, or http only for loopback test servers.
func checkURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil {
		return errors.New("url")
	}
	switch u.Scheme {
	case "https":
		return nil
	case "http":
		h := u.Hostname()
		if h == "127.0.0.1" || h == "localhost" || h == "::1" {
			return nil
		}
	}
	return errors.New("url must be https")
}

// IDs lists the key ids in the keyring, sorted.
func (kr Keyring) IDs() []string {
	ids := make([]string, 0, len(kr))
	for id := range kr {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}
