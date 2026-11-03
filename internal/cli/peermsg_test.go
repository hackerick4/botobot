package cli

import (
	"testing"
	"time"
)

func TestIsStaleSentAt(t *testing.T) {
	now := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	fmtT := func(d time.Duration) string { return now.Add(d).Format(sentAtLayout) }
	cases := []struct {
		name  string
		in    string
		stale bool
	}{
		{"empty", "", false},
		{"garbage", "not-a-time", false},
		{"now", fmtT(0), false},
		{"1h ago", fmtT(-time.Hour), false},
		{"23h ago", fmtT(-23 * time.Hour), false},
		{"just over 24h+skew", fmtT(-(24*time.Hour + 6*time.Minute)), true},
		{"2min future", fmtT(2 * time.Minute), false},
		{"10min future", fmtT(10 * time.Minute), true},
	}
	for _, c := range cases {
		if got := isStaleSentAt(c.in, now); got != c.stale {
			t.Errorf("%s: isStaleSentAt(%q) = %v, want %v", c.name, c.in, got, c.stale)
		}
	}
}
