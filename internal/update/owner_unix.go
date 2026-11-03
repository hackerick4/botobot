//go:build !windows

package update

import (
	"os"
	"syscall"
)

// Supported reports whether in-place self-update works on this OS.
const Supported = true

func ownedBySelf(fi os.FileInfo) bool {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return false
	}
	uid := os.Getuid()
	return uid == 0 || int(st.Uid) == uid
}
