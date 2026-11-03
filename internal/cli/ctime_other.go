//go:build !linux && !darwin

package cli

import "time"

// fileChangeTime is not wired up on this platform; the stored time is used alone.
func fileChangeTime(string) (time.Time, bool) { return time.Time{}, false }
