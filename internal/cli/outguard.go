package cli

// outguard.go is the P0 outbound guard (EXFIL_DEFENSE.md §3, §4 S-BB / F-FILE):
//
//   - an attachment path denylist (credential directories and file names, with
//     symlinks resolved) plus a content sniff for private keys and API keys;
//   - a hard block on any outbound text or attachment that carries this home's
//     own secrets (any bridge's API key or private key, in common encodings) or
//     any bot-bridge API key shape. The hard block cannot be released by anyone,
//     including the owner.
//
// It is defense in depth: a bot that copies a secret elsewhere and re-encodes
// it can still evade a substring check. P1 adds the full deterministic DLP and
// P2 the owner-approval gate.

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode"
)

// errAttachmentDenied / errSecretBlocked carry no path contents or secret.
func errAttachmentDenied(reason string) *cliError {
	return &cliError{Code: "attachment_denied", Message: "send: attachment refused (" + reason + "); sensitive files are never sent. Tell your owner instead of retrying.", Exit: exitGeneral}
}

func errSecretBlocked() *cliError {
	return &cliError{Code: "secret_blocked", Message: "send: blocked — the message contains a key, token, password or other credential. This is a hard block that no one (not even the owner) can release. Do not retry; tell your owner.", Exit: exitGeneral}
}

// deniedDirs are home-relative directories whose contents are never attached.
var deniedHomeDirs = []string{
	".ssh", ".gnupg", ".aws", ".azure", ".kube", ".docker", ".config", ".gcloud",
	".password-store", ".local/share/keyrings", ".mozilla", ".pki", ".terraform.d",
	".vault-token", ".cargo/credentials", ".npmrc", ".pypirc", ".netrc", ".git-credentials",
	".claude", ".codex", ".cursor", ".anthropic", ".openai",
}

// deniedRoots are absolute system trees that are never attached.
var deniedRoots = []string{"/proc", "/sys", "/dev", "/etc", "/root", "/run/secrets", "/var/run/secrets", "/private/etc"}

// deniedNameRe matches credential-looking basenames (case-insensitive). *.key
// is deliberately absent (Keynote files); key content is caught by the sniff.
var deniedNameRe = regexp.MustCompile(`(?i)^(` +
	`\.env|\.env\..*|.*\.env|\.envrc|` +
	`id_(rsa|dsa|ecdsa|ed25519)(_sk)?(\.pub)?|.*\.pem|.*\.p12|.*\.pfx|.*\.jks|.*\.keystore|.*\.ppk|.*\.kdbx?|.*\.asc|.*\.gpg|` +
	`\.netrc|_netrc|\.git-credentials|\.npmrc|\.pypirc|\.htpasswd|\.pgpass|authorized_keys|known_hosts|` +
	`credentials(\..*)?|.*credentials\.json|apikey|api[_-]?key(s)?(\..*)?|keys\.json|secrets?(\..*)?|` +
	`.*service[_-]?account.*\.json|.*-sa\.json|tick\.json|lands\.json|token(s)?(\.json|\.txt)?|` +
	`.*\.tfstate|.*\.tfvars|wallet\.dat|.*\.ovpn|shadow|passwd` +
	`)$`)

// isWithin reports whether p is root or below it (both cleaned, absolute).
func isWithin(p, root string) bool {
	if root == "" {
		return false
	}
	// Compare case-insensitively: macOS/Windows file systems are, so ~/.SSH
	// must not slip past ~/.ssh (over-blocking on Linux is acceptable).
	rel, err := filepath.Rel(strings.ToLower(root), strings.ToLower(p))
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}

// checkAttachmentPath refuses credential locations. Both the given path and its
// symlink-resolved target are checked so a link cannot launder a secret file.
func checkAttachmentPath(path, bbHome string) *cliError {
	abs, err := filepath.Abs(path)
	if err != nil {
		return errAttachmentDenied("unresolvable path")
	}
	cands := []string{filepath.Clean(abs)}
	if real, err := filepath.EvalSymlinks(abs); err == nil {
		cands = append(cands, filepath.Clean(real))
	}
	home, _ := os.UserHomeDir()
	var roots []string
	for _, r := range deniedRoots {
		roots = append(roots, r)
		if rr, err := filepath.EvalSymlinks(r); err == nil {
			roots = append(roots, rr)
		}
	}
	if bbHome != "" {
		if a, err := filepath.Abs(bbHome); err == nil {
			roots = append(roots, a)
			if rr, err := filepath.EvalSymlinks(a); err == nil {
				roots = append(roots, rr)
			}
		}
	}
	if home != "" {
		for _, d := range deniedHomeDirs {
			roots = append(roots, filepath.Join(home, d))
		}
	}
	for _, c := range cands {
		for _, r := range roots {
			if isWithin(c, r) {
				return errAttachmentDenied("protected location")
			}
		}
		// Any path component named like a bot-bridge home (~/.bot-bridge*,
		// /data/.bot-bridge-x, …) is protected regardless of where it lives.
		for _, comp := range strings.Split(c, string(filepath.Separator)) {
			lc := strings.ToLower(comp)
			if strings.HasPrefix(lc, ".bot-bridge") || strings.HasPrefix(lc, "bot-bridge-home") {
				return errAttachmentDenied("protected location")
			}
		}
		if deniedNameRe.MatchString(filepath.Base(c)) {
			return errAttachmentDenied("credential-like file name")
		}
	}
	return nil
}

// apiKeyShapeRe matches any bot-bridge API key ("bbk_" + 43 base64url chars)
// or public-entry key ("bbe_", v0.6.0).
var apiKeyShapeRe = regexp.MustCompile(`bb[ke]_[A-Za-z0-9_-]{43}`)

// privateKeyMarkers are content signatures of private key material.
var privateKeyMarkers = [][]byte{
	[]byte("PRIVATE KEY-----"),
	[]byte("PRIVATE KEY BLOCK-----"),
	[]byte(`"private_key":`),
	[]byte(`"enc_priv"`),
	[]byte(`"sign_priv"`),
	[]byte("PuTTY-User-Key-File"),
}

// sniffSecretContent reports whether data looks like it contains private key
// material or an API key.
func sniffSecretContent(data []byte) bool {
	for _, m := range privateKeyMarkers {
		if bytes.Contains(data, m) {
			return true
		}
	}
	return apiKeyShapeRe.Match(data)
}

// localSecrets collects this home's secret values in the encodings a bot might
// paste: the raw API key, and every bridge's private keys (current and
// retained) as std/url base64 (padded and raw) and hex. Values never leave
// this process.
func localSecrets(ks *keystore) [][]byte {
	var out [][]byte
	add := func(b []byte) {
		if len(b) == 0 {
			return
		}
		for _, enc := range []string{
			base64.StdEncoding.EncodeToString(b), base64.RawStdEncoding.EncodeToString(b),
			base64.URLEncoding.EncodeToString(b), base64.RawURLEncoding.EncodeToString(b),
			hex.EncodeToString(b), strings.ToUpper(hex.EncodeToString(b)),
		} {
			out = append(out, []byte(enc))
		}
	}
	out = append(out, envSecrets()...)
	bcs, err := ks.listBridges()
	if err != nil {
		return out
	}
	// Guest bridges of a public entry share the entry's secrets: read each
	// secret folder once, so hundreds of guests cost nothing extra (v0.6.0).
	done := map[string]bool{}
	addDir := func(apiKey string, km *keyMaterial) {
		if apiKey != "" {
			out = append(out, []byte(apiKey))
			body := strings.TrimPrefix(strings.TrimPrefix(apiKey, "bbk_"), "bbe_")
			if len(body) >= 16 {
				out = append(out, []byte(body))
			}
			add([]byte(apiKey))
		}
		if km == nil {
			return
		}
		for _, kp := range append([]retiredKey{{EncPriv: km.EncPriv, SignPriv: km.SignPriv}}, km.Previous...) {
			add(kp.EncPriv)
			add(kp.SignPriv)
			if len(kp.SignPriv) == 64 {
				add(kp.SignPriv[:32]) // ed25519 seed
			}
		}
	}
	for _, bc := range bcs {
		dir := ks.secretDir(bc.BridgeID)
		if done[dir] {
			continue
		}
		done[dir] = true
		k, _ := ks.loadAPIKey(bc.BridgeID)
		km, err := ks.loadKeys(bc.BridgeID)
		if err != nil {
			km = nil
		}
		addDir(k, km)
	}
	for _, ec := range ks.listEntries() {
		dir := ks.entryDir(ec.EntryID)
		if done[dir] {
			continue
		}
		done[dir] = true
		k, _ := ks.entryAPIKey(ec.EntryID)
		var km keyMaterial
		if readJSON(filepath.Join(dir, "keys.json"), &km) == nil {
			addDir(k, &km)
		} else {
			addDir(k, nil)
		}
	}
	return out
}

// compactForScan removes whitespace, zero-width/format runes and common
// separators so a secret split across lines or padded with spaces still hits.
func compactForScan(b []byte) []byte {
	return []byte(strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) || unicode.Is(unicode.Cf, r) || r == '"' || r == '\'' || r == '`' {
			return -1
		}
		return r
	}, string(b)))
}

// containsSecret reports whether data (raw or compacted) contains any secret.
func containsSecret(data []byte, secrets [][]byte) bool {
	if sniffSecretContent(data) {
		return true
	}
	compact := compactForScan(data)
	if apiKeyShapeRe.Match(compact) {
		return true
	}
	for _, s := range secrets {
		if len(s) < 12 {
			continue
		}
		if bytes.Contains(data, s) || bytes.Contains(compact, s) {
			return true
		}
	}
	return false
}

// envSecretNameRe matches environment variable names that usually hold
// secrets (S-LOCALVAL, EXFIL_DEFENSE.md §4.5).
var envSecretNameRe = regexp.MustCompile(`(?i)(key|token|secret|passw|passphrase|credential|auth|cookie|private|session)`)

// envSecretSkipRe excludes well-known non-secret variables that match the name
// pattern.
var envSecretSkipRe = regexp.MustCompile(`(?i)(_sock$|session_bus|session_id$|session_type$|session_class$|session_desktop$|keytimeout|keyboard|_keymap$|^xauthority$)`)

// envSecrets returns the values of secret-looking environment variables (at
// least 12 chars, not a path) so the bot can never paste them to a bridge.
func envSecrets() [][]byte {
	var out [][]byte
	for _, kv := range os.Environ() {
		i := strings.IndexByte(kv, '=')
		if i <= 0 {
			continue
		}
		name, val := kv[:i], strings.TrimSpace(kv[i+1:])
		if !envSecretNameRe.MatchString(name) || envSecretSkipRe.MatchString(name) {
			continue
		}
		if len(val) < 12 || strings.HasPrefix(val, "/") || strings.HasPrefix(val, ".") || strings.HasPrefix(val, "~") {
			continue
		}
		out = append(out, []byte(val))
	}
	return out
}
