//go:build unix

package cli

import "syscall"

// syscallUmask sets the process umask and returns the previous value, so the
// permission tests can force an exact mode regardless of the environment's
// umask (used only by tests).
func syscallUmask(mask int) int { return syscall.Umask(mask) }
