package cli

import (
	"os"
	"testing"
)

// TestMain points HOME at a throwaway folder (v0.6.1), so no test ever reads
// or writes the real ~/.bot-bridge (update state, closed-entry records).
func TestMain(m *testing.M) {
	d, err := os.MkdirTemp("", "bbcli-home-")
	if err != nil {
		panic(err)
	}
	_ = os.Setenv("HOME", d)
	code := m.Run()
	_ = os.RemoveAll(d)
	os.Exit(code)
}
