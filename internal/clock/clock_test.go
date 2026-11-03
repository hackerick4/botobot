package clock

import (
	"testing"
	"time"
)

func TestFakeAdvanceAndSet(t *testing.T) {
	start := time.Date(2026, 10, 29, 7, 0, 0, 0, time.UTC)
	f := NewFake(start)
	if !f.Now().Equal(start) {
		t.Fatalf("Now = %s, want %s", f.Now(), start)
	}
	f.Advance(90 * time.Minute)
	if got := f.Now(); !got.Equal(start.Add(90 * time.Minute)) {
		t.Errorf("after advance Now = %s", got)
	}
	reset := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	f.Set(reset)
	if !f.Now().Equal(reset) {
		t.Errorf("after set Now = %s, want %s", f.Now(), reset)
	}
}

func TestRealClockUTC(t *testing.T) {
	now := Real{}.Now()
	if now.Location() != time.UTC {
		t.Errorf("Real.Now location = %s, want UTC", now.Location())
	}
}
