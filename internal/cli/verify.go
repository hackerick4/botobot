package cli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/hackerick4/botobot/internal/update"
)

// verify-self (v0.6.3): check a CLI binary (this one, or --file) against the
// signed manifest on botobot.app, using only the release keys built into this
// binary. It needs no bot folder and never creates one.
//
// What it proves: run by a CLI that is already trusted (installed through the
// signed update, or checked by the install command in join.md), `verify-self
// --file NEW` tells whether NEW is the release the release key signed. Run by a
// freshly downloaded binary it also catches a wrong or damaged download whose
// manifest or signature does not match; a fake binary could of course lie
// about itself, which is why the install command first checks the file's
// SHA-256 against the manifest fetched from https://botobot.app (and the
// manifest's signature with openssl when available).

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, io.LimitReader(f, update.MaxBinaryBytes+1)); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func verifyNotice(en bool) string {
	if en {
		return "NOTIFY_OWNER: The downloaded bot-bridge program does not match the official one, so it was not installed. Download it only from botobot.app."
	}
	return "NOTIFY_OWNER: 下載的 bot-bridge 程式和官方版本不符，沒有安裝。只從 botobot.app 下載。"
}

func cmdVerifySelf(a *app, args []string) *cliError {
	var file, lang string
	g, fs, e := a.parse("verify-self", args, func(fs *flag.FlagSet) {
		fs.StringVar(&file, "file", "", "check this downloaded CLI file instead of the running one (do not run the file first)")
		fs.StringVar(&lang, "lang", "zh", "notice language: zh or en")
	})
	if e != nil {
		return e
	}
	if fs.NArg() > 0 {
		return usageErrorf("verify-self: unexpected arguments")
	}
	en := lang == "en"
	pr := newPrinter(a.stdout, g.pretty)
	self := file == ""
	target := file
	if self {
		p, err := exePath()
		if err != nil {
			return generalErrorf("verify-self: %v", err)
		}
		target = p
	}
	got, err := fileSHA256(target)
	if err != nil {
		return &cliError{Code: "verify_failed", Message: "verify-self: cannot read the file: " + oneLine(err.Error(), 200), Exit: exitGeneral}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	client := &http.Client{Timeout: 15 * time.Second}
	mURL := manifestURL()
	body, _, _, err := fetchLimited(ctx, client, mURL, "")
	if err != nil {
		return &cliError{Code: "verify_unavailable", Message: "verify-self: cannot fetch the signed manifest (" + oneLine(err.Error(), 120) + "); do not install or run the file until this succeeds", Exit: exitGeneral}
	}
	sig, _, _, err := fetchLimited(ctx, client, mURL+".sig", "")
	if err != nil {
		return &cliError{Code: "verify_unavailable", Message: "verify-self: cannot fetch the manifest signature (" + oneLine(err.Error(), 120) + "); do not install or run the file until this succeeds", Exit: exitGeneral}
	}
	// Only the keys built into this binary: a folder's learned keys are not
	// read, so this works the same for a brand-new bot.
	m, verr := update.Verify(body, sig, update.NewKeyring(releasePubKeys...), a.now(), 0)
	if verr != nil && !errors.Is(verr, update.ErrNotNewer) {
		pr.raw(verifyNotice(en))
		_ = pr.flush()
		return &cliError{Code: "verify_bad_manifest", Message: "verify-self: the manifest is not signed by the bot-bridge release key (" + oneLine(verr.Error(), 120) + "); do not install or run the file", Exit: exitGeneral}
	}
	p, ok := m.Platforms[platformName()]
	if !ok {
		return &cliError{Code: "verify_failed", Message: "verify-self: no signed release for " + platformName(), Exit: exitGeneral}
	}
	out := map[string]any{"type": "verify", "file": target, "sha256": got, "release": m.Version, "release_seq": m.Seq, "key_ids": update.NewKeyring(releasePubKeys...).IDs()}
	switch {
	case got == p.SHA256:
		out["status"] = "ok"
		pr.emit(out)
		return pr.done()
	case self && buildSeq() > 0 && buildSeq() < m.Seq:
		// An older signed build: it cannot be checked against the current
		// manifest, but the signed update replaces it.
		out["status"] = "not_latest"
		pr.emit(out)
		exe := a.routineCLIPath()
		if exe == "" {
			exe = "bot-bridge-cli"
		}
		pr.raw("NEXT: run `" + exe + " update`, then verify-self again.")
		return pr.done()
	}
	out["status"] = "mismatch"
	pr.emit(out)
	pr.raw(verifyNotice(en))
	_ = pr.flush()
	return &cliError{Code: "verify_mismatch", Message: fmt.Sprintf("verify-self: %s does not match the signed release %s (seq %d); do not install or run it, and delete it", target, m.Version, m.Seq), Exit: exitGeneral}
}

func init() {
	commands["verify-self"] = cmdVerifySelf
}
