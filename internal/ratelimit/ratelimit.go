// Package ratelimit is a small per-key token-bucket limiter (DEV_SPEC §9). It is
// used for the per-land "other authenticated request" limit and the per-IP
// bridge-creation limit in M3; M4 adds the send / inbox / ack buckets.
//
// Buckets are kept in memory (DEV_SPEC §5: "memory structures, reset on restart
// is acceptable"). Time comes from an injected clock.Clock so limits can be
// tested deterministically without time.Sleep (DEV_SPEC §8, CLAUDE.md rule 8).
package ratelimit

import (
	"sync"
	"time"

	"github.com/hackerick4/botobot/internal/clock"
)

// Limiter hands out tokens at ratePerSec with a maximum burst, independently per
// key (e.g. land id or client-IP hash). It is safe for concurrent use.
type Limiter struct {
	mu         sync.Mutex
	clk        clock.Clock
	ratePerSec float64
	burst      float64
	buckets    map[string]*bucket
}

type bucket struct {
	tokens float64
	last   time.Time
}

// New builds a limiter allowing perWindow events per window with the given burst
// capacity. window is typically time.Minute (per-minute limits) or time.Hour
// (bridge creation). A non-positive burst falls back to perWindow.
func New(perWindow int, window time.Duration, burst int, clk clock.Clock) *Limiter {
	if clk == nil {
		clk = clock.Real{}
	}
	if perWindow < 1 {
		perWindow = 1
	}
	if window <= 0 {
		window = time.Minute
	}
	if burst < 1 {
		burst = perWindow
	}
	return &Limiter{
		clk:        clk,
		ratePerSec: float64(perWindow) / window.Seconds(),
		burst:      float64(burst),
		buckets:    make(map[string]*bucket),
	}
}

// Allow attempts to consume one token for key. It returns ok=true when a token
// was available; otherwise ok=false and retryAfter is how long until the next
// token accrues (rounded up to whole seconds, minimum 1s, for Retry-After).
func (l *Limiter) Allow(key string) (ok bool, retryAfter time.Duration) {
	now := l.clk.Now()
	l.mu.Lock()
	defer l.mu.Unlock()

	b := l.buckets[key]
	if b == nil {
		b = &bucket{tokens: l.burst, last: now}
		l.buckets[key] = b
	} else {
		elapsed := now.Sub(b.last).Seconds()
		if elapsed > 0 {
			b.tokens += elapsed * l.ratePerSec
			if b.tokens > l.burst {
				b.tokens = l.burst
			}
			b.last = now
		}
	}

	if b.tokens >= 1 {
		b.tokens -= 1
		return true, 0
	}
	need := (1 - b.tokens) / l.ratePerSec // seconds until one token
	d := time.Duration(need * float64(time.Second))
	if d < time.Second {
		d = time.Second
	}
	return false, d
}

// Reset forgets a key's bucket (next Allow starts at full burst). Used when an
// administrator action should clear accumulated throttling.
func (l *Limiter) Reset(key string) {
	l.mu.Lock()
	delete(l.buckets, key)
	l.mu.Unlock()
}

// Dynamic is a per-key token-bucket limiter whose rate and burst are supplied on
// each call rather than fixed at construction. It backs the /send limit, which
// is the bridge's rate_limit_per_min — a per-bridge setting that the admin can
// change via PATCH (DEV_SPEC §9: 30 msg/min/land default, burst 10; §6.4 PATCH
// rate_limit_per_min 1..600). Buckets live in memory; time comes from an
// injected clock.Clock (DEV_SPEC §5, §8).
type Dynamic struct {
	mu      sync.Mutex
	clk     clock.Clock
	buckets map[string]*bucket
}

// NewDynamic builds a Dynamic limiter using clk (defaulting to the wall clock).
func NewDynamic(clk clock.Clock) *Dynamic {
	if clk == nil {
		clk = clock.Real{}
	}
	return &Dynamic{clk: clk, buckets: make(map[string]*bucket)}
}

// Allow consumes one token for key using a bucket that fills at perMin tokens
// per minute and holds at most burst tokens. A new key starts full (burst
// tokens). It returns ok=false with the wait until the next token otherwise.
func (l *Dynamic) Allow(key string, perMin, burst int) (ok bool, retryAfter time.Duration) {
	if perMin < 1 {
		perMin = 1
	}
	if burst < 1 {
		burst = perMin
	}
	return l.AllowRate(key, float64(perMin)/60.0, burst)
}

// AllowRate is Allow with the refill rate given per second, for budgets that
// are set per hour (v0.6.0 public-entry joins). ratePerSec must be > 0.
func (l *Dynamic) AllowRate(key string, ratePerSec float64, burst int) (ok bool, retryAfter time.Duration) {
	if ratePerSec <= 0 {
		ratePerSec = 1.0 / 3600
	}
	if burst < 1 {
		burst = 1
	}
	cap := float64(burst)

	now := l.clk.Now()
	l.mu.Lock()
	defer l.mu.Unlock()

	b := l.buckets[key]
	if b == nil {
		b = &bucket{tokens: cap, last: now}
		l.buckets[key] = b
	} else {
		if elapsed := now.Sub(b.last).Seconds(); elapsed > 0 {
			b.tokens += elapsed * ratePerSec
			if b.tokens > cap {
				b.tokens = cap
			}
			b.last = now
		}
	}

	if b.tokens >= 1 {
		b.tokens -= 1
		return true, 0
	}
	need := (1 - b.tokens) / ratePerSec
	d := time.Duration(need * float64(time.Second))
	if d < time.Second {
		d = time.Second
	}
	return false, d
}

// Reset forgets a key's bucket (next Allow starts full).
func (l *Dynamic) Reset(key string) {
	l.mu.Lock()
	delete(l.buckets, key)
	l.mu.Unlock()
}
