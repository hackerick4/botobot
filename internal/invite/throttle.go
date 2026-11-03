package invite

import (
	"net"
	"sync"
	"time"

	"github.com/hackerick4/botobot/internal/clock"
)

// Brute-force thresholds (DEV_SPEC §8). All windows are sliding.
const (
	// Per IP.
	ipWindowShort = 15 * time.Minute
	ipFailShort   = 5                // >= 5 failures in 15m => exponential backoff
	ipBackoffBase = 30 * time.Second // 30s, 60s, 120s, …
	ipBackoffMax  = 15 * time.Minute
	ipWindowLong  = 24 * time.Hour
	ipFailLong    = 20 // >= 20 failures in 24h => 24h ban
	ipBanLong     = 24 * time.Hour

	// Per subnet (IPv4 /24, IPv6 /64).
	subnetWindow = time.Hour
	subnetFail   = 30 // >= 30 failures in 1h => 1h ban
	subnetBan    = time.Hour

	// Per bridge.
	bridgeWindowShort = time.Hour
	bridgeFailShort   = 20 // >= 20 failures in 1h => lock join for 15m
	bridgeLockShort   = 15 * time.Minute
	bridgeWindowLong  = 24 * time.Hour
	bridgeFailLong    = 50 // >= 50 failures in 24h => persistent join_locked
)

// Reason identifies which scope blocked a join attempt.
type Reason string

const (
	ReasonIPBackoff  Reason = "ip_backoff"
	ReasonIPBanned   Reason = "ip_banned"
	ReasonSubnet     Reason = "subnet_banned"
	ReasonBridgeLock Reason = "bridge_locked"
)

// Block describes why a join attempt is refused before the code is even checked.
// RetryAfter is rounded up to whole seconds (minimum 1s) for the Retry-After
// header. All of these map to HTTP 429 rate_limited (the persistent per-bridge
// join_locked flag lives in the DB and is surfaced as 401 join_failed instead).
type Block struct {
	Reason     Reason
	RetryAfter time.Duration
}

// FailResult reports side effects a failed attempt triggered. PersistBridgeLock
// is true when the 24h/50-failure threshold was crossed, so the caller should
// set bridges.join_locked = 1 (reason brute_force) and enqueue a join_locked
// event to the admin (DEV_SPEC §8).
type FailResult struct {
	PersistBridgeLock bool
}

// Throttler is the in-memory brute-force state machine (DEV_SPEC §8). It tracks
// failed join attempts per IP, per subnet, and per bridge, and is safe for
// concurrent use. Counters live only in memory and reset on restart, which the
// spec allows (DEV_SPEC §5).
type Throttler struct {
	mu      sync.Mutex
	clk     clock.Clock
	ips     map[string]*ipState
	subnets map[string]*counter
	bridges map[string]*bridgeState
}

type counter struct {
	fails    []time.Time
	banUntil time.Time
}

type ipState struct {
	fails        []time.Time
	backoffUntil time.Time
	banUntil     time.Time
}

type bridgeState struct {
	fails     []time.Time
	lockUntil time.Time
}

// NewThrottler builds a Throttler using clk for all time decisions.
func NewThrottler(clk clock.Clock) *Throttler {
	if clk == nil {
		clk = clock.Real{}
	}
	return &Throttler{
		clk:     clk,
		ips:     make(map[string]*ipState),
		subnets: make(map[string]*counter),
		bridges: make(map[string]*bridgeState),
	}
}

// Check reports whether a join attempt from ip targeting bridgeID should be
// refused up front due to active backoff, bans, or a temporary bridge lock. It
// returns nil when the attempt may proceed to code verification.
func (t *Throttler) Check(ip, bridgeID string) *Block {
	now := t.clk.Now()
	t.mu.Lock()
	defer t.mu.Unlock()

	if s := t.ips[ip]; s != nil {
		if now.Before(s.banUntil) {
			return &Block{Reason: ReasonIPBanned, RetryAfter: until(now, s.banUntil)}
		}
		if now.Before(s.backoffUntil) {
			return &Block{Reason: ReasonIPBackoff, RetryAfter: until(now, s.backoffUntil)}
		}
	}
	if c := t.subnets[subnetKey(ip)]; c != nil && now.Before(c.banUntil) {
		return &Block{Reason: ReasonSubnet, RetryAfter: until(now, c.banUntil)}
	}
	if b := t.bridges[bridgeID]; b != nil && now.Before(b.lockUntil) {
		return &Block{Reason: ReasonBridgeLock, RetryAfter: until(now, b.lockUntil)}
	}
	return nil
}

// Fail records one failed attempt (wrong code, unknown bridge, etc.) and updates
// the backoff / ban / lock state for the IP, its subnet, and the bridge
// (DEV_SPEC §8). A successful join does NOT reset these counters.
func (t *Throttler) Fail(ip, bridgeID string) FailResult {
	now := t.clk.Now()
	t.mu.Lock()
	defer t.mu.Unlock()

	// Per IP.
	s := t.ips[ip]
	if s == nil {
		s = &ipState{}
		t.ips[ip] = s
	}
	s.fails = append(prune(s.fails, now.Add(-ipWindowLong)), now)
	if short := countSince(s.fails, now.Add(-ipWindowShort)); short >= ipFailShort {
		if bu := now.Add(backoffFor(short)); bu.After(s.backoffUntil) {
			s.backoffUntil = bu
		}
	}
	if countSince(s.fails, now.Add(-ipWindowLong)) >= ipFailLong {
		s.banUntil = now.Add(ipBanLong)
	}

	// Per subnet.
	key := subnetKey(ip)
	c := t.subnets[key]
	if c == nil {
		c = &counter{}
		t.subnets[key] = c
	}
	c.fails = append(prune(c.fails, now.Add(-subnetWindow)), now)
	if len(c.fails) >= subnetFail {
		c.banUntil = now.Add(subnetBan)
	}

	// Per bridge.
	b := t.bridges[bridgeID]
	if b == nil {
		b = &bridgeState{}
		t.bridges[bridgeID] = b
	}
	b.fails = append(prune(b.fails, now.Add(-bridgeWindowLong)), now)
	var res FailResult
	if countSince(b.fails, now.Add(-bridgeWindowShort)) >= bridgeFailShort {
		if lu := now.Add(bridgeLockShort); lu.After(b.lockUntil) {
			b.lockUntil = lu
		}
	}
	if countSince(b.fails, now.Add(-bridgeWindowLong)) >= bridgeFailLong {
		res.PersistBridgeLock = true
	}
	return res
}

// ResetBridge clears a bridge's throttling counters and temporary lock
// (DEV_SPEC §6.4: setting join_locked=false resets bridge-level throttling).
func (t *Throttler) ResetBridge(bridgeID string) {
	t.mu.Lock()
	delete(t.bridges, bridgeID)
	t.mu.Unlock()
}

// backoffFor returns the IP backoff for a short-window failure count of n
// (n >= ipFailShort): 30s doubling per extra failure, capped at 15m.
func backoffFor(n int) time.Duration {
	d := ipBackoffBase
	for i := ipFailShort; i < n; i++ {
		d *= 2
		if d >= ipBackoffMax {
			return ipBackoffMax
		}
	}
	if d > ipBackoffMax {
		return ipBackoffMax
	}
	return d
}

// SubnetKey maps an IP to its /24 (IPv4) or /64 (IPv6) network string. It is the
// exported form used by the server for per-prefix /join limits (Batch 1 item 6).
func SubnetKey(ip string) string { return subnetKey(ip) }

// subnetKey maps an IP to its /24 (IPv4) or /64 (IPv6) network string.
func subnetKey(ip string) string {
	p := net.ParseIP(ip)
	if p == nil {
		return ip
	}
	if v4 := p.To4(); v4 != nil {
		return v4.Mask(net.CIDRMask(24, 32)).String() + "/24"
	}
	return p.Mask(net.CIDRMask(64, 128)).String() + "/64"
}

// prune drops timestamps older than cutoff, reusing the backing array.
func prune(ts []time.Time, cutoff time.Time) []time.Time {
	out := ts[:0]
	for _, t := range ts {
		if t.After(cutoff) {
			out = append(out, t)
		}
	}
	return out
}

// countSince returns how many timestamps are strictly after cutoff.
func countSince(ts []time.Time, cutoff time.Time) int {
	n := 0
	for _, t := range ts {
		if t.After(cutoff) {
			n++
		}
	}
	return n
}

// until returns the duration from now to t, rounded up to whole seconds with a
// 1s floor (suitable for a Retry-After value).
func until(now, t time.Time) time.Duration {
	d := t.Sub(now)
	if d <= 0 {
		return time.Second
	}
	if r := d % time.Second; r != 0 {
		d += time.Second - r
	}
	return d
}
