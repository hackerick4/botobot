// Package clock provides an injectable time source so TTL, lease, and the
// 7-day admin-inactivity logic (DEV_SPEC §10, §11) can be tested deterministically
// without time.Sleep. Production code uses Real; tests use Fake.
package clock

import (
	"sync"
	"time"
)

// Clock is the minimal time source used across the server.
type Clock interface {
	Now() time.Time
}

// Real is a Clock backed by the system wall clock.
type Real struct{}

// Now returns the current UTC time.
func (Real) Now() time.Time { return time.Now().UTC() }

// Fake is a controllable Clock for tests. It is safe for concurrent use.
type Fake struct {
	mu  sync.Mutex
	now time.Time
}

// NewFake returns a Fake set to the given instant (stored in UTC).
func NewFake(t time.Time) *Fake { return &Fake{now: t.UTC()} }

// Now returns the Fake's current instant.
func (f *Fake) Now() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.now
}

// Advance moves the Fake clock forward by d.
func (f *Fake) Advance(d time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.now = f.now.Add(d)
}

// Set moves the Fake clock to the given instant.
func (f *Fake) Set(t time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.now = t.UTC()
}
