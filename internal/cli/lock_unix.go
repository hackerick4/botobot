//go:build !windows

package cli

import (
	"os"
	"syscall"
)

// fileLock is a held advisory lock on a lock file (Batch 1 item 1). On Unix it
// is a non-blocking flock; closing the file releases it.
type fileLock struct{ f *os.File }

// acquireLock takes a non-blocking exclusive flock on path. ok=false means the
// lock is already held by another process (another tick is running): the caller
// should treat that as "nothing to do" and exit quietly. A real I/O error is
// returned as err.
func acquireLock(path string) (lk *fileLock, ok bool, err error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, privFilePerm)
	if err != nil {
		return nil, false, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		if err == syscall.EWOULDBLOCK {
			return nil, false, nil
		}
		return nil, false, err
	}
	return &fileLock{f: f}, true, nil
}

// release drops the lock (via close, which unlocks the flock).
func (l *fileLock) release() {
	if l == nil || l.f == nil {
		return
	}
	_ = syscall.Flock(int(l.f.Fd()), syscall.LOCK_UN)
	_ = l.f.Close()
}
