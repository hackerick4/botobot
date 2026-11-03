package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/hackerick4/botobot/internal/prompts"
	"github.com/hackerick4/botobot/internal/update"
)

const (
	defaultManifestURL = "https://botobot.app/dl/manifest.json"
	updateInterval     = 24 * time.Hour
)

// exePath is the binary that self-update replaces; tests override it.
var exePath = os.Executable

// updateState is <home>/update.json.
type updateState struct {
	LastCheckMS int64            `json:"last_check_ms,omitempty"`
	ETag        string           `json:"etag,omitempty"`
	SeqSeen     uint64           `json:"seq_seen,omitempty"`
	TrustedKeys []update.PubKey  `json:"trusted_keys,omitempty"`
	LastApplied *updateAppliedAt `json:"last_applied,omitempty"`
}

type updateAppliedAt struct {
	From string `json:"from"`
	To   string `json:"to"`
	Seq  uint64 `json:"seq"`
	AtMS int64  `json:"at_ms"`
}

func (ks *keystore) updateStatePath() string { return filepath.Join(ks.home, "update.json") }

func (ks *keystore) loadUpdateState() *updateState {
	st := &updateState{}
	if b, err := os.ReadFile(ks.updateStatePath()); err == nil {
		_ = json.Unmarshal(b, st)
	}
	return st
}

func (ks *keystore) saveUpdateState(st *updateState) error {
	b, err := json.Marshal(st)
	if err != nil {
		return err
	}
	// Never create a state folder just for this bookkeeping (a removed bot's
	// folder must stay removed).
	if _, err := os.Stat(ks.home); err != nil {
		return nil
	}
	return writeFileAtomic(ks.updateStatePath(), b, privFilePerm)
}

func buildSeq() uint64 {
	n, _ := strconv.ParseUint(releaseSeq, 10, 64)
	return n
}

func versionLine(version string) string {
	return fmt.Sprintf("bot-bridge-cli %s seq=%d", version, buildSeq())
}

func manifestURL() string {
	if u := os.Getenv("BB_UPDATE_URL"); u != "" {
		return u
	}
	return defaultManifestURL
}

func platformName() string { return runtime.GOOS + "-" + runtime.GOARCH }

// keyring is the pinned keys plus keys announced by earlier verified manifests.
func (st *updateState) keyring() update.Keyring {
	kr := update.NewKeyring(releasePubKeys...)
	for _, k := range st.TrustedKeys {
		if pub, err := update.ParsePub(k.Pub); err == nil && update.KeyID(pub) == k.KeyID {
			kr[k.KeyID] = pub
		}
	}
	return kr
}

func (st *updateState) learnKeys(m *update.Manifest) {
	have := map[string]bool{}
	for _, k := range st.TrustedKeys {
		have[k.KeyID] = true
	}
	for _, k := range m.NextKeys {
		if !have[k.KeyID] {
			st.TrustedKeys = append(st.TrustedKeys, k)
		}
	}
}

type updateResult struct {
	Status  string // up_to_date | not_modified | available | updated
	Current string
	Latest  string
	Seq     uint64
}

func fetchLimited(ctx context.Context, c *http.Client, url, etag string) (body []byte, newETag string, notModified bool, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, "", false, err
	}
	if etag != "" {
		req.Header.Set("If-None-Match", etag)
	}
	req.Header.Set("Cache-Control", "no-cache")
	resp, err := c.Do(req)
	if err != nil {
		return nil, "", false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotModified {
		return nil, etag, true, nil
	}
	if resp.StatusCode != http.StatusOK {
		return nil, "", false, fmt.Errorf("http %d", resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, update.MaxManifestBytes+1))
	if err != nil {
		return nil, "", false, err
	}
	if len(b) > update.MaxManifestBytes {
		return nil, "", false, errors.New("manifest too large")
	}
	return b, resp.Header.Get("ETag"), false, nil
}

// checkAndApply fetches and verifies the manifest and, when install is true and
// a newer signed release exists, installs it. useETag lets the daily automatic
// check skip the work when the manifest has not changed.
func (a *app) checkAndApply(ctx context.Context, ks *keystore, install, useETag bool) (*updateResult, error) {
	st := ks.loadUpdateState()
	res := &updateResult{Current: a.version}
	client := &http.Client{Timeout: 15 * time.Second}

	etag := ""
	if useETag {
		etag = st.ETag
	}
	mURL := manifestURL()
	body, newETag, notMod, err := fetchLimited(ctx, client, mURL, etag)
	if useETag {
		// Only the automatic check (tick) uses the daily slot; a manual
		// `update --check` must not postpone the next automatic install.
		st.LastCheckMS = a.now().UnixMilli()
	}
	if err != nil {
		_ = ks.saveUpdateState(st)
		return nil, fmt.Errorf("fetch manifest: %w", err)
	}
	if notMod {
		_ = ks.saveUpdateState(st)
		res.Status = "not_modified"
		return res, nil
	}
	sig, _, _, err := fetchLimited(ctx, client, mURL+".sig", "")
	if err != nil {
		_ = ks.saveUpdateState(st)
		return nil, fmt.Errorf("fetch signature: %w", err)
	}
	minSeq := buildSeq()
	if st.SeqSeen > minSeq {
		minSeq = st.SeqSeen
	}
	m, verr := update.Verify(body, sig, st.keyring(), a.now(), minSeq)
	if m != nil && (verr == nil || errors.Is(verr, update.ErrNotNewer)) {
		st.learnKeys(m)
		res.Latest, res.Seq = m.Version, m.Seq
	}
	if errors.Is(verr, update.ErrNotNewer) {
		st.ETag = newETag
		_ = ks.saveUpdateState(st)
		res.Status = "up_to_date"
		return res, nil
	}
	if verr != nil {
		_ = ks.saveUpdateState(st)
		return nil, verr
	}
	p, ok := m.Platforms[platformName()]
	if !ok {
		st.ETag = newETag
		_ = ks.saveUpdateState(st)
		res.Status = "up_to_date"
		return res, nil
	}
	if !install {
		_ = ks.saveUpdateState(st)
		res.Status = "available"
		return res, nil
	}
	if !update.Supported {
		_ = ks.saveUpdateState(st)
		return nil, errors.New("self-update is not supported on this OS; download the new version manually")
	}
	target, err := exePath()
	if err != nil {
		_ = ks.saveUpdateState(st)
		return nil, err
	}
	want := fmt.Sprintf("bot-bridge-cli %s seq=%d", m.Version, m.Seq)
	in := &update.Installer{
		Client:   &http.Client{Timeout: 3 * time.Minute},
		Target:   target,
		SelfTest: func(ctx context.Context, path string) error { return selfTest(ctx, path, want) },
	}
	if err := in.Install(ctx, p); err != nil {
		_ = ks.saveUpdateState(st)
		return nil, err
	}
	st.SeqSeen = m.Seq
	st.ETag = newETag
	st.LastApplied = &updateAppliedAt{From: a.version, To: m.Version, Seq: m.Seq, AtMS: a.now().UnixMilli()}
	if err := ks.saveUpdateState(st); err != nil {
		return nil, err
	}
	res.Status = "updated"
	return res, nil
}

// selfTest runs `<path> --version` and requires the exact signed version line.
func selfTest(ctx context.Context, path, want string) error {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, "--version")
	cmd.Env = append(os.Environ(), "BB_NO_AUTO_UPDATE=1")
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return err
	}
	if got := strings.TrimSpace(out.String()); got != want {
		return fmt.Errorf("version output %q, want %q", oneLine(got, 80), want)
	}
	return nil
}

// autoUpdateDue reports whether the daily automatic check should run now.
// Development builds (releaseSeq 0) and BB_NO_AUTO_UPDATE=1 never check.
func (a *app) autoUpdateDue(ks *keystore) bool {
	if buildSeq() == 0 || !update.Supported || os.Getenv("BB_NO_AUTO_UPDATE") != "" {
		return false
	}
	st := ks.loadUpdateState()
	return a.now().UnixMilli()-st.LastCheckMS >= updateInterval.Milliseconds()
}

// autoUpdate runs the daily check from tick. It is silent unless an update was
// applied, in which case it returns the one-line owner notice.
func (a *app) autoUpdate(ks *keystore) string {
	if !a.autoUpdateDue(ks) {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	res, err := a.checkAndApply(ctx, ks, true, true)
	if err != nil || res.Status != "updated" {
		return ""
	}
	return fmt.Sprintf("NOTIFY_OWNER: bot-bridge 已自動更新到 %s。", oneLine(res.Latest, 40))
}

// cmdUpdate implements `update [--check]`.
func cmdUpdate(a *app, args []string) *cliError {
	var checkOnly bool
	g, _, e := a.parse("update", args, func(fs *flag.FlagSet) {
		fs.BoolVar(&checkOnly, "check", false, "only report whether a signed update is available")
	})
	if e != nil {
		return e
	}
	sess, e := a.updateSession(g)
	if e != nil {
		return e
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	res, err := a.checkAndApply(ctx, sess.ks, !checkOnly, false)
	if err != nil {
		return &cliError{Code: "update_failed", Message: "update: " + oneLine(err.Error(), 300), Exit: exitGeneral}
	}
	out := map[string]any{"type": "update", "status": res.Status, "current": res.Current, "current_seq": buildSeq()}
	if res.Latest != "" {
		out["latest"], out["latest_seq"] = res.Latest, res.Seq
	}
	sess.pr.emit(out)
	if ln := updateNotice(res, allEnglish(homeBridges(sess.ks.home))); ln != "" {
		sess.pr.raw(ln)
	}
	return sess.pr.done()
}

// updateNotice is the short owner line next to update's JSON (v0.6.5).
func updateNotice(res *updateResult, en bool) string {
	switch res.Status {
	case "updated":
		return "NOTIFY_OWNER: " + pick(en, "bot-bridge was updated to "+res.Latest+" (the official version).",
			"bot-bridge 已更新到 "+res.Latest+"（官方版本）。")
	case "up_to_date", "not_modified":
		v := res.Current
		if res.Latest != "" {
			v = res.Latest // the newest signed release seen; a dev build's own string can be stale
		}
		return "NOTIFY_OWNER: " + pick(en, "bot-bridge is already the latest version ("+v+"). No update needed.",
			"bot-bridge 已是最新版本（"+v+"），不需要更新。")
	case "available":
		return "NOTIFY_OWNER: " + pick(en, "A new bot-bridge version ("+res.Latest+") is available.",
			"bot-bridge 有新版本（"+res.Latest+"）可以更新。")
	}
	return ""
}

// allEnglish reports whether there is at least one bridge and all are English.
func allEnglish(bcs []*bridgeConfig) bool {
	for _, bc := range bcs {
		if !prompts.IsEnglish(bc.Lang) {
			return false
		}
	}
	return len(bcs) > 0
}

// updateSession is newSession for `update` (v0.6.1): the binary is shared by
// every bot on this computer, so update needs no bot folder. It uses this
// bot's folder when BB_HOME/--home names one that exists, and the shared
// ~/.bot-bridge otherwise (also when no bridge or folder is left), and never
// creates or picks another bot's folder.
func (a *app) updateSession(g *globalFlags) (*session, *cliError) {
	home := firstNonEmpty(g.home, os.Getenv("BB_HOME"))
	if home != "" {
		if fi, err := os.Stat(home); err != nil || !fi.IsDir() {
			home = ""
		}
	}
	if home == "" {
		root, err := defaultRoot()
		if err != nil {
			return nil, generalErrorf("%v", err)
		}
		home = root
	}
	gg := *g
	gg.home = home
	return a.newSession(&gg)
}

func init() {
	commands["update"] = cmdUpdate
}
