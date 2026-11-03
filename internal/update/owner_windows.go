//go:build windows

package update

import "os"

// Supported reports whether in-place self-update works on this OS. Windows
// cannot replace a running executable, so updates there are manual.
const Supported = false

func ownedBySelf(os.FileInfo) bool { return false }
