package ratelimit

import (
	"testing"
	"time"

	"github.com/hackerick4/botobot/internal/clock"
)

func TestBurstThenRefill(t *testing.T) {
	clk := clock.NewFake(time.Unix(0, 0))
	// 60/min, burst 10.
	l := New(60, time.Minute, 10, clk)

	// Burst of 10 succeeds immediately.
	for i := 0; i < 10; i++ {
		if ok, _ := l.Allow("land"); !ok {
			t.Fatalf("burst token %d denied", i)
		}
	}
	// 11th is denied; retry-after is at least 1s.
	ok, ra := l.Allow("land")
	if ok {
		t.Fatalf("11th token should be denied")
	}
	if ra < time.Second {
		t.Fatalf("retryAfter = %s, want >= 1s", ra)
	}

	// After 1s at 1 token/s, exactly one more is available.
	clk.Advance(time.Second)
	if ok, _ := l.Allow("land"); !ok {
		t.Fatalf("expected one token after 1s")
	}
	if ok, _ := l.Allow("land"); ok {
		t.Fatalf("expected empty bucket again")
	}
}

func TestKeysAreIndependent(t *testing.T) {
	clk := clock.NewFake(time.Unix(0, 0))
	l := New(1, time.Minute, 1, clk)
	if ok, _ := l.Allow("a"); !ok {
		t.Fatal("a should pass")
	}
	if ok, _ := l.Allow("a"); ok {
		t.Fatal("a should now be limited")
	}
	if ok, _ := l.Allow("b"); !ok {
		t.Fatal("b must be unaffected by a")
	}
}

func TestReset(t *testing.T) {
	clk := clock.NewFake(time.Unix(0, 0))
	l := New(1, time.Minute, 1, clk)
	l.Allow("k")
	if ok, _ := l.Allow("k"); ok {
		t.Fatal("k should be limited before reset")
	}
	l.Reset("k")
	if ok, _ := l.Allow("k"); !ok {
		t.Fatal("k should pass after reset")
	}
}

func TestDynamicPerCallRateAndBurst(t *testing.T) {
	clk := clock.NewFake(time.Unix(0, 0))
	d := NewDynamic(clk)

	// 30/min, burst 10 (the /send defaults). Ten pass immediately.
	for i := 0; i < 10; i++ {
		if ok, _ := d.Allow("land", 30, 10); !ok {
			t.Fatalf("burst token %d denied", i)
		}
	}
	// 11th denied with a positive retry-after.
	if ok, ra := d.Allow("land", 30, 10); ok || ra < time.Second {
		t.Fatalf("11th = ok=%v ra=%s, want denied with >=1s", ok, ra)
	}
	// At 30/min = 1 token every 2s, 2s yields exactly one token.
	clk.Advance(2 * time.Second)
	if ok, _ := d.Allow("land", 30, 10); !ok {
		t.Fatal("expected one token after 2s at 30/min")
	}
	if ok, _ := d.Allow("land", 30, 10); ok {
		t.Fatal("expected empty bucket again")
	}

	// Keys are independent, and Reset refills.
	if ok, _ := d.Allow("other", 1, 1); !ok {
		t.Fatal("independent key should pass")
	}
	d.Reset("land")
	if ok, _ := d.Allow("land", 30, 10); !ok {
		t.Fatal("land should pass after reset")
	}
}
