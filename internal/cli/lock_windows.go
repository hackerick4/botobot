//go:build windows

package cli

// fileLock is a no-op on Windows (Batch 1 item 1). syscall.Flock is not
// available there; the per-bridge lock is a best-effort guard against concurrent
// ticks, and the scheduled routine is single-instance per bot in practice. This
// keeps the cross-platform build matrix green.
type fileLock struct{}

// acquireLock always succeeds on Windows.
func acquireLock(string) (*fileLock, bool, error) { return &fileLock{}, true, nil }

// release is a no-op on Windows.
func (l *fileLock) release() {}
