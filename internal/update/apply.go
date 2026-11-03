package update

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

// Installer replaces the running binary with a verified download.
type Installer struct {
	Client *http.Client
	// Target is the binary to replace (normally os.Executable()).
	Target string
	// SelfTest runs the candidate binary and returns an error if it does not
	// work (normally: exec `<path> --version` and compare the output).
	SelfTest func(ctx context.Context, path string) error
}

// Errors from Install.
var (
	ErrUnsafeTarget = errors.New("update target is not safe to replace")
	ErrDownload     = errors.New("download failed")
	ErrHash         = errors.New("downloaded binary does not match the signed sha256/size")
	ErrSelfTest     = errors.New("new binary failed its self-test; rolled back")
	ErrBusy         = errors.New("another update is in progress")
)

// ResolveTarget resolves symlinks once and checks that the result is a
// regular file in a directory no other user can write to. All later file
// operations use this resolved path, so swapping a symlink afterwards cannot
// redirect the write.
func ResolveTarget(path string) (string, error) {
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrUnsafeTarget, err)
	}
	real, err = filepath.Abs(real)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrUnsafeTarget, err)
	}
	fi, err := os.Lstat(real)
	if err != nil || !fi.Mode().IsRegular() {
		return "", fmt.Errorf("%w: not a regular file", ErrUnsafeTarget)
	}
	dir := filepath.Dir(real)
	di, err := os.Lstat(dir)
	if err != nil || !di.IsDir() {
		return "", fmt.Errorf("%w: bad directory", ErrUnsafeTarget)
	}
	if di.Mode().Perm()&0o022 != 0 {
		return "", fmt.Errorf("%w: directory %s is writable by other users", ErrUnsafeTarget, dir)
	}
	if !ownedBySelf(di) {
		return "", fmt.Errorf("%w: directory %s belongs to another user", ErrUnsafeTarget, dir)
	}
	if !ownedBySelf(fi) {
		return "", fmt.Errorf("%w: binary belongs to another user", ErrUnsafeTarget)
	}
	return real, nil
}

// Install downloads p, verifies size and sha256, swaps it in atomically with a
// backup at <target>.bak, runs the self-test and rolls back on failure.
func (in *Installer) Install(ctx context.Context, p Platform) error {
	target, err := ResolveTarget(in.Target)
	if err != nil {
		return err
	}
	dir := filepath.Dir(target)

	// One updater at a time per binary. O_EXCL so a pre-planted symlink or file
	// is never followed; a stale lock (older than 15 minutes) is replaced.
	lockPath := target + ".update-lock"
	if fi, err := os.Lstat(lockPath); err == nil && time.Since(fi.ModTime()) > 15*time.Minute {
		_ = os.Remove(lockPath)
	}
	lf, err := os.OpenFile(lockPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return ErrBusy
	}
	_ = lf.Close()
	defer os.Remove(lockPath)

	// 1. Download into a fresh temp file in the same directory (same
	// filesystem, so rename is atomic). CreateTemp uses O_EXCL with a random
	// name, so nothing can be pre-planted there.
	tmp, err := os.CreateTemp(dir, ".bb-cli-new-*")
	if err != nil {
		return fmt.Errorf("%w: %v", ErrUnsafeTarget, err)
	}
	tmpName := tmp.Name()
	keepTmp := false
	defer func() {
		if !keepTmp {
			_ = os.Remove(tmpName)
		}
	}()
	if err := download(ctx, in.Client, p, tmp); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o755); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	// Re-hash the file on disk (not the stream) right before use.
	if err := checkFile(tmpName, p); err != nil {
		return err
	}

	// 2. Self-test the candidate BEFORE it replaces anything.
	if in.SelfTest != nil {
		if err := in.SelfTest(ctx, tmpName); err != nil {
			return fmt.Errorf("%w: %v", ErrSelfTest, err)
		}
	}

	// 3. Backup: copy current -> temp -> rename to .bak (atomic).
	bak := target + ".bak"
	if err := copyAtomic(target, bak, dir); err != nil {
		return fmt.Errorf("backup: %w", err)
	}

	// 4. Swap: rename replaces the directory entry; it never follows a symlink
	// at the destination.
	if err := os.Rename(tmpName, target); err != nil {
		return fmt.Errorf("replace: %w", err)
	}
	keepTmp = true
	syncDir(dir)

	// 5. Self-test the installed binary; roll back from .bak on failure.
	if in.SelfTest != nil {
		if err := in.SelfTest(ctx, target); err != nil {
			if rerr := copyAtomic(bak, target, dir); rerr != nil {
				return fmt.Errorf("%w (rollback also failed: %v)", ErrSelfTest, rerr)
			}
			return fmt.Errorf("%w: %v", ErrSelfTest, err)
		}
	}
	return nil
}

func download(ctx context.Context, c *http.Client, p Platform, w io.Writer) error {
	if err := checkURL(p.URL); err != nil {
		return fmt.Errorf("%w: %v", ErrDownload, err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.URL, nil)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrDownload, err)
	}
	resp, err := c.Do(req)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrDownload, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%w: http %d", ErrDownload, resp.StatusCode)
	}
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(w, h), io.LimitReader(resp.Body, p.Size+1))
	if err != nil {
		return fmt.Errorf("%w: %v", ErrDownload, err)
	}
	if n != p.Size || hex.EncodeToString(h.Sum(nil)) != p.SHA256 {
		return ErrHash
	}
	if f, ok := w.(*os.File); ok {
		if err := f.Sync(); err != nil {
			return err
		}
	}
	return nil
}

func checkFile(path string, p Platform) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, io.LimitReader(f, p.Size+1))
	if err != nil || n != p.Size || hex.EncodeToString(h.Sum(nil)) != p.SHA256 {
		return ErrHash
	}
	return nil
}

// copyAtomic copies src to dst through a temp file in dir and a rename.
func copyAtomic(src, dst, dir string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp, err := os.CreateTemp(dir, ".bb-cli-copy-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	if _, err := io.Copy(tmp, in); err != nil {
		_ = tmp.Close()
		_ = os.Remove(name)
		return err
	}
	if err := tmp.Chmod(0o755); err != nil {
		_ = tmp.Close()
		_ = os.Remove(name)
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		_ = os.Remove(name)
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(name)
		return err
	}
	if err := os.Rename(name, dst); err != nil {
		_ = os.Remove(name)
		return err
	}
	syncDir(dir)
	return nil
}

func syncDir(dir string) {
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
}
