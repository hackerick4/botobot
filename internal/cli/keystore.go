package cli

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/hackerick4/botobot/internal/protocol"
)

// Local-state file layout under BB_HOME (DEV_SPEC §12.1):
//
//	BB_HOME/                      (0700)
//	  config.json                 {"default_bridge":"br_…"}
//	  <bridge_id>/
//	    bridge.json               {"server","bridge_id","land_id","handle","role"}
//	    keys.json      (0600)     {"enc_priv","sign_priv","key_id","created_at","previous":[…]}
//	    apikey         (0600)     bbk_…
//	    lands.json                /lands cache (incl land_set_version)
//	    seen.json                 processed (sender,msg_id) within 24h
//	    files/<msg_id>/<name>     decrypted attachments (0600)
//
// Private files are written O_CREAT|O_EXCL at 0600 to a temp name then renamed
// into place (atomic replace); directories are 0700.
const (
	dirPerm      os.FileMode = 0o700
	privFilePerm os.FileMode = 0o600
)

// seenTTL is how long a processed (sender, msg_id) pair is remembered for replay
// suppression (DEV_SPEC §7.8 step 7, §10: at-least-once dedupe window = 24h).
const seenTTL = 24 * time.Hour

// retainWindow is how long a superseded private key is kept so in-flight
// messages encrypted to it can still be decrypted (DEV_SPEC §6.4 PUT
// public-key, §12.3 rekey: 24h).
const retainWindow = 24 * time.Hour

// bridgeConfig is the per-bridge bridge.json (DEV_SPEC §12.1). It holds no
// secret material; the API key and private keys live in their own 0600 files.
type bridgeConfig struct {
	Server   string `json:"server"`
	BridgeID string `json:"bridge_id"`
	LandID   string `json:"land_id"`
	Handle   string `json:"handle"`
	Role     string `json:"role"`
	// Name and JoinedAt are recorded since v0.4.5 (shown by `bridges`).
	Name     string `json:"name,omitempty"`
	JoinedAt int64  `json:"joined_at_ms,omitempty"`
	// Lang is the admin's invite language ("en"; empty = zh-TW), since v0.4.9.
	Lang string `json:"lang,omitempty"`
	// Pair marks a two-bot bridge (v0.5.0): the peer leaving, or the admin
	// removing the peer, closes it for both.
	Pair bool `json:"pair,omitempty"`
	// EntryID marks a guest bridge of this bot's public entry (v0.6.0): its
	// keys and API key live once in entries/<entry_id>/, not in the bridge
	// folder. Peer is the guest's label, for headers and notices.
	EntryID string `json:"entry_id,omitempty"`
	KeyID   string `json:"key_id,omitempty"`
	Peer    string `json:"peer,omitempty"`
	// EntryLink is set on the guest side: the public link this bridge came
	// from, so the same link is not joined twice from one bot.
	EntryLink string `json:"entry_link,omitempty"`
}

// retiredKey is a superseded keypair kept for the retain window (DEV_SPEC §12.3).
type retiredKey struct {
	EncPriv   []byte `json:"enc_priv"`
	SignPriv  []byte `json:"sign_priv"`
	KeyID     string `json:"key_id"`
	CreatedAt int64  `json:"created_at"`
	RetiredAt int64  `json:"retired_at"`
}

// keyMaterial is keys.json: the land's current X25519 (enc) and Ed25519 (sign)
// private keys plus superseded keys retained for decryption of in-flight
// messages (DEV_SPEC §12.1, §12.3). Byte fields are base64 in JSON.
type keyMaterial struct {
	EncPriv   []byte       `json:"enc_priv"`
	SignPriv  []byte       `json:"sign_priv"`
	KeyID     string       `json:"key_id"`
	CreatedAt int64        `json:"created_at"`
	Previous  []retiredKey `json:"previous,omitempty"`
}

// encKeysByID returns key_id -> X25519 private key for the current key and every
// retained previous key still within the retain window (DEV_SPEC §7.8 step 3,
// §12.3). nowMS prunes expired retained keys from the result.
func (k *keyMaterial) encKeysByID(nowMS int64) map[string][]byte {
	out := make(map[string][]byte, 1+len(k.Previous))
	if k.KeyID != "" {
		out[k.KeyID] = k.EncPriv
	}
	for _, p := range k.Previous {
		if nowMS-p.RetiredAt <= retainWindow.Milliseconds() {
			out[p.KeyID] = p.EncPriv
		}
	}
	return out
}

// pruneRetained drops retained keys older than the retain window (DEV_SPEC §12.3).
func (k *keyMaterial) pruneRetained(nowMS int64) {
	kept := k.Previous[:0]
	for _, p := range k.Previous {
		if nowMS-p.RetiredAt <= retainWindow.Milliseconds() {
			kept = append(kept, p)
		}
	}
	k.Previous = kept
}

// newKeypair generates a fresh X25519 + Ed25519 keypair and the key binding
// signature (DEV_SPEC §7.2, §7.6), all from crypto/rand.
func newKeypair() (encPriv, encPub, signPriv, signPub, bindingSig []byte, err error) {
	encPriv = make([]byte, protocol.KeySize)
	if _, err = rand.Read(encPriv); err != nil {
		return nil, nil, nil, nil, nil, fmt.Errorf("generate enc key: %w", err)
	}
	encPub, err = protocol.PublicEncKey(encPriv)
	if err != nil {
		return nil, nil, nil, nil, nil, err
	}
	sp, sk, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, nil, nil, nil, nil, fmt.Errorf("generate sign key: %w", err)
	}
	signPub = sp
	signPriv = sk
	bindingSig, err = protocol.SignBinding(sk, encPub, sp)
	if err != nil {
		return nil, nil, nil, nil, nil, err
	}
	return encPriv, encPub, signPriv, signPub, bindingSig, nil
}

// keystore is the on-disk local state rooted at home (DEV_SPEC §12.1).
type keystore struct {
	home string
	// warn reports a non-fatal condition (e.g. a secret file found with loose
	// permissions that was tightened) to stderr. Defaults to a no-op; the session
	// wires it to the app's stderr. It must never carry secret material (§15).
	warn func(format string, args ...any)
	// entryOf caches bridge id -> entry id ("" for an ordinary bridge).
	entryOf map[string]string
}

// resolveHome picks BB_HOME: the --home flag, then $BB_HOME, then
// $HOME/.bot-bridge (DEV_SPEC §12.1, §12.2).
func resolveHome(flagHome, envHome string) (string, error) {
	switch {
	case flagHome != "":
		return flagHome, nil
	case envHome != "":
		return envHome, nil
	default:
		h, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("cannot determine home directory: %w", err)
		}
		return filepath.Join(h, ".bot-bridge"), nil
	}
}

// openKeystore returns a keystore for home. An existing home is forced to
// 0700; a missing one is not created here (v0.4.9): every write goes through
// writeFileAtomic/ensureDir, so a read-only command such as `bridges` run after
// forget removed the folder does not leave an empty folder behind.
func openKeystore(home string) (*keystore, error) {
	if fi, err := os.Stat(home); err == nil {
		if !fi.IsDir() {
			return nil, fmt.Errorf("state folder %s is not a folder", home)
		}
		if err := os.Chmod(home, dirPerm); err != nil {
			return nil, fmt.Errorf("chmod dir %s: %w", home, err)
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	return &keystore{home: home, warn: func(string, ...any) {}}, nil
}

// enforcePrivatePerms makes sure a secret file is not readable or writable by
// group/other (DEV_SPEC §12.1, §16, Batch 1 item 3). If it is, the file is
// auto-chmod'd to 0600 and a warning is emitted (we fix-and-warn rather than
// refuse so an unattended routine is not broken by a stray umask). A missing
// file is not an error here; the caller's own read reports that.
func (ks *keystore) enforcePrivatePerms(path string) {
	info, err := os.Stat(path)
	if err != nil {
		return
	}
	if mode := info.Mode().Perm(); mode&0o077 != 0 {
		if err := os.Chmod(path, privFilePerm); err != nil {
			ks.warn("%s has insecure permissions %#o and could not be tightened: %v", filepath.Base(path), mode, err)
			return
		}
		ks.warn("%s had insecure permissions %#o; tightened to 0600", filepath.Base(path), mode)
	}
}

func (ks *keystore) bridgeDir(bridgeID string) string { return filepath.Join(ks.home, bridgeID) }

// configPath / per-bridge file paths.
func (ks *keystore) configPath() string { return filepath.Join(ks.home, "config.json") }
func (ks *keystore) bridgePath(id string) string {
	return filepath.Join(ks.bridgeDir(id), "bridge.json")
}
func (ks *keystore) keysPath(id string) string   { return filepath.Join(ks.secretDir(id), "keys.json") }
func (ks *keystore) apiKeyPath(id string) string { return filepath.Join(ks.secretDir(id), "apikey") }

// secretDir is where a bridge's keys.json and apikey live: the bridge folder,
// or for a public-entry guest bridge the entry folder (one copy, v0.6.0).
func (ks *keystore) secretDir(id string) string {
	if e := ks.entryOfBridge(id); e != "" {
		return ks.entryDir(e)
	}
	return ks.bridgeDir(id)
}

// forgetEntryOf drops the cached entry id of a bridge whose bridge.json was
// just written or removed.
func (ks *keystore) forgetEntryOf(id string) { delete(ks.entryOf, id) }

func (ks *keystore) entryOfBridge(id string) string {
	if e, ok := ks.entryOf[id]; ok {
		return e
	}
	var bc bridgeConfig
	if err := readJSON(ks.bridgePath(id), &bc); err != nil {
		return "" // not cached: the bridge may be written later
	}
	e := ""
	if entryIDRe.MatchString(bc.EntryID) {
		e = bc.EntryID
	}
	if ks.entryOf == nil {
		ks.entryOf = map[string]string{}
	}
	ks.entryOf[id] = e
	return e
}
func (ks *keystore) landsPath(id string) string { return filepath.Join(ks.bridgeDir(id), "lands.json") }
func (ks *keystore) seenPath(id string) string  { return filepath.Join(ks.bridgeDir(id), "seen.json") }
func (ks *keystore) tickStatePath(id string) string {
	return filepath.Join(ks.bridgeDir(id), "tick.json")
}
func (ks *keystore) tickLockPath(id string) string {
	return filepath.Join(ks.bridgeDir(id), "tick.lock")
}
func (ks *keystore) filesDir(id, msgID string) string {
	return filepath.Join(ks.bridgeDir(id), "files", msgID)
}

// inboxTextPath is where the full (untruncated) decrypted text of a message is
// written so the truncated display line can point at it (Batch 1 cheap fixes).
func (ks *keystore) inboxTextPath(id, msgID string) string {
	return filepath.Join(ks.bridgeDir(id), "inbox", sanitizeFilename(msgID)+".txt")
}

// localConfig is config.json (DEV_SPEC §12.1).
type localConfig struct {
	DefaultBridge string `json:"default_bridge"`
	// Handle is the bot this home belongs to (v0.4.5); one bot per home.
	Handle string `json:"handle,omitempty"`
	// Cleaned lists bridges whose local data was deleted while other bridges
	// remain (v0.5.3), so a later tick can say so plainly instead of staying
	// silent. Only the id, name and time are kept, for cleanedKeep.
	Cleaned []cleanedBridge `json:"cleaned,omitempty"`
}

// cleanedBridge is one entry of localConfig.Cleaned.
type cleanedBridge struct {
	BridgeID string `json:"bridge_id"`
	Name     string `json:"name,omitempty"`
	Lang     string `json:"lang,omitempty"`
	AtMS     int64  `json:"at_ms"`
}

// cleanedKeep is how long a cleaned bridge is still mentioned by tick.
const cleanedKeep = 24 * 60 * 60 * 1000

// addCleaned records that bc's local data was deleted at nowMS.
func (ks *keystore) addCleaned(bc *bridgeConfig, _ string, nowMS int64) {
	c, err := ks.loadConfig()
	if err != nil {
		return
	}
	keep := c.Cleaned[:0]
	for _, x := range c.Cleaned {
		if x.BridgeID != bc.BridgeID && nowMS-x.AtMS < cleanedKeep {
			keep = append(keep, x)
		}
	}
	c.Cleaned = append(keep, cleanedBridge{BridgeID: bc.BridgeID, Name: bc.Name, Lang: bc.Lang, AtMS: nowMS})
	_ = ks.saveConfig(c)
}

// recentCleaned returns the cleaned bridges still within cleanedKeep.
func (ks *keystore) recentCleaned(nowMS int64) []cleanedBridge {
	c, err := ks.loadConfig()
	if err != nil {
		return nil
	}
	var out []cleanedBridge
	for _, x := range c.Cleaned {
		if nowMS-x.AtMS < cleanedKeep {
			out = append(out, x)
		}
	}
	return out
}

func (ks *keystore) loadConfig() (*localConfig, error) {
	var c localConfig
	err := readJSON(ks.configPath(), &c)
	if os.IsNotExist(err) {
		return &localConfig{}, nil
	}
	if err != nil {
		return nil, err
	}
	return &c, nil
}

func (ks *keystore) saveConfig(c *localConfig) error {
	return writeJSONAtomic(ks.configPath(), c)
}

// initBridge creates the per-bridge directory (0700) and writes bridge.json,
// keys.json (0600), and apikey (0600) atomically (DEV_SPEC §12.1).
func (ks *keystore) initBridge(bc *bridgeConfig, km *keyMaterial, apiKey string) error {
	if err := ensureDir(ks.bridgeDir(bc.BridgeID)); err != nil {
		return err
	}
	if err := writeJSONAtomic(ks.bridgePath(bc.BridgeID), bc); err != nil {
		return err
	}
	if err := writeJSONAtomic(ks.keysPath(bc.BridgeID), km); err != nil {
		return err
	}
	if err := writeFileAtomic(ks.apiKeyPath(bc.BridgeID), []byte(apiKey), privFilePerm); err != nil {
		return err
	}
	return nil
}

// saveBridge rewrites bridge.json for an existing bridge.
func (ks *keystore) saveBridge(bc *bridgeConfig) error {
	return writeJSONAtomic(ks.bridgePath(bc.BridgeID), bc)
}

func (ks *keystore) loadBridge(id string) (*bridgeConfig, error) {
	var bc bridgeConfig
	if err := readJSON(ks.bridgePath(id), &bc); err != nil {
		return nil, err
	}
	return &bc, nil
}

func (ks *keystore) loadKeys(id string) (*keyMaterial, error) {
	ks.enforcePrivatePerms(ks.keysPath(id))
	var km keyMaterial
	if err := readJSON(ks.keysPath(id), &km); err != nil {
		return nil, err
	}
	if ks.entryOfBridge(id) != "" {
		// One entry key pair, a server key id per guest bridge (v0.6.0).
		bc, err := ks.loadBridge(id)
		if err != nil {
			return nil, err
		}
		if bc.KeyID == "" {
			return nil, fmt.Errorf("entry bridge %s has no key id", id)
		}
		km.KeyID = bc.KeyID
		km.Previous = nil
	}
	return &km, nil
}

func (ks *keystore) saveKeys(id string, km *keyMaterial) error {
	if ks.entryOfBridge(id) != "" {
		return fmt.Errorf("the keys of a public-entry bridge belong to the entry")
	}
	return writeJSONAtomic(ks.keysPath(id), km)
}

func (ks *keystore) loadAPIKey(id string) (string, error) {
	ks.enforcePrivatePerms(ks.apiKeyPath(id))
	b, err := os.ReadFile(ks.apiKeyPath(id))
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(b)), nil
}

func (ks *keystore) saveAPIKey(id, apiKey string) error {
	return writeFileAtomic(ks.apiKeyPath(id), []byte(apiKey), privFilePerm)
}

// landsCache is lands.json: the /lands response with its land_set_version
// (DEV_SPEC §12.1). It is written verbatim from the server's response shape.
type landsCache struct {
	LandSetVersion int64        `json:"land_set_version"`
	Lands          []cachedLand `json:"lands"`
}

// cachedLand mirrors the server's Land output (DEV_SPEC §6.3) for the fields the
// CLI needs to encrypt to, verify, and display.
type cachedLand struct {
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

// selfName is this land's own display name in the cache, or "".
func (lc *landsCache) selfName(landID string) string {
	if lc == nil {
		return ""
	}
	for _, l := range lc.Lands {
		if l.LandID == landID {
			return l.DisplayName
		}
	}
	return ""
}

func (ks *keystore) loadLands(id string) (*landsCache, error) {
	var lc landsCache
	err := readJSON(ks.landsPath(id), &lc)
	if os.IsNotExist(err) {
		return &landsCache{}, nil
	}
	if err != nil {
		return nil, err
	}
	return &lc, nil
}

func (ks *keystore) saveLands(id string, lc *landsCache) error {
	return writeJSONAtomic(ks.landsPath(id), lc)
}

// seenStore is seen.json: processed (sender_land_id, msg_id) pairs mapped to the
// time they were processed, for 24h replay suppression (DEV_SPEC §7.8 step 7).
type seenStore struct {
	Entries map[string]int64 `json:"entries"`
}

func seenKey(senderLandID, msgID string) string { return senderLandID + "\x00" + msgID }

func (ks *keystore) loadSeen(id string, nowMS int64) (*seenStore, error) {
	s := &seenStore{Entries: map[string]int64{}}
	err := readJSON(ks.seenPath(id), s)
	if os.IsNotExist(err) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	if s.Entries == nil {
		s.Entries = map[string]int64{}
	}
	// Prune entries older than the 24h window (DEV_SPEC §7.8 step 7).
	for k, ts := range s.Entries {
		if nowMS-ts > seenTTL.Milliseconds() {
			delete(s.Entries, k)
		}
	}
	return s, nil
}

func (ks *keystore) saveSeen(id string, s *seenStore) error {
	return writeJSONAtomic(ks.seenPath(id), s)
}

// listBridges returns the bridge ids with a readable bridge.json under home
// (DEV_SPEC §12.3 bridges).
func (ks *keystore) listBridges() ([]*bridgeConfig, error) {
	entries, err := os.ReadDir(ks.home)
	if os.IsNotExist(err) {
		return nil, nil // no state folder yet (or removed by forget): no bridges
	}
	if err != nil {
		return nil, err
	}
	var out []*bridgeConfig
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		bc, err := ks.loadBridge(e.Name())
		if err != nil {
			continue // not a bridge dir (or unreadable); skip
		}
		out = append(out, bc)
	}
	sortBridges(out)
	return out, nil
}

// sortBridges orders bridges oldest first by when this bot created or joined
// them (v0.5.4), so tick output, `bridges` and the /delete menu read in the
// same order. Bridges from before v0.4.5 have no time and come first; ties go
// by bridge id.
func sortBridges(bcs []*bridgeConfig) {
	sort.SliceStable(bcs, func(i, j int) bool {
		if bcs[i].JoinedAt != bcs[j].JoinedAt {
			return bcs[i].JoinedAt < bcs[j].JoinedAt
		}
		return bcs[i].BridgeID < bcs[j].BridgeID
	})
}

// ---- low-level file helpers ----

// ensureDir creates dir (and parents) and forces its mode to 0700 (DEV_SPEC
// §12.1). Chmod defeats a permissive umask on an already-existing directory.
func ensureDir(dir string) error {
	if err := os.MkdirAll(dir, dirPerm); err != nil {
		return fmt.Errorf("create dir %s: %w", dir, err)
	}
	if err := os.Chmod(dir, dirPerm); err != nil {
		return fmt.Errorf("chmod dir %s: %w", dir, err)
	}
	return nil
}

// writeFileAtomic writes data to a fresh temp file (O_CREAT|O_EXCL, then Chmod
// to perm to defeat umask), fsyncs it, and renames it over path (DEV_SPEC
// §12.1 atomic replace). The temp file is removed on any failure.
func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	if err := ensureDir(dir); err != nil {
		return err
	}
	tmp := filepath.Join(dir, "."+filepath.Base(path)+".tmp-"+randToken())
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, perm)
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	// Enforce the exact mode regardless of umask.
	if err := f.Chmod(perm); err != nil {
		f.Close()
		os.Remove(tmp)
		return fmt.Errorf("chmod temp file: %w", err)
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(tmp)
		return fmt.Errorf("write temp file: %w", err)
	}
	if err := f.Sync(); err != nil {
		f.Close()
		os.Remove(tmp)
		return fmt.Errorf("fsync temp file: %w", err)
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("close temp file: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("rename temp file: %w", err)
	}
	return nil
}

// writeJSONAtomic marshals v and writes it atomically at 0600 (DEV_SPEC §12.1).
// All local-state files use 0600; only keys.json and apikey hold secrets, but a
// tight mode on the rest is harmless and simpler.
func writeJSONAtomic(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal %s: %w", filepath.Base(path), err)
	}
	return writeFileAtomic(path, b, privFilePerm)
}

// readJSON reads and unmarshals a JSON file. os.IsNotExist on the returned error
// is meaningful to callers that treat absence as empty.
func readJSON(path string, v any) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(b, v); err != nil {
		return fmt.Errorf("parse %s: %w", filepath.Base(path), err)
	}
	return nil
}

// absOr returns the absolute form of path, or path unchanged if it cannot be
// resolved. Used for the paths reported in output.
func absOr(path string) string {
	if abs, err := filepath.Abs(path); err == nil {
		return abs
	}
	return path
}

// randToken returns a short random hex string for temp-file names (crypto/rand).
func randToken() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		// Extremely unlikely; fall back to a fixed token (still O_EXCL-guarded).
		return "fallback"
	}
	return hex.EncodeToString(b[:])
}
