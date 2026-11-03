//go:build linux

package cli

import (
	"os"
	"syscall"
	"time"
)

// fileChangeTime is the inode change time of path (ctime). Unlike the
// modification time it cannot be set back with touch or utimes: any edit of
// the file moves it to now (v0.5.3, RT-80).
func fileChangeTime(path string) (time.Time, bool) {
	fi, err := os.Stat(path)
	if err != nil {
		return time.Time{}, false
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return time.Time{}, false
	}
	return time.Unix(int64(st.Ctim.Sec), int64(st.Ctim.Nsec)), true
}
