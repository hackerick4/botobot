package invite

import (
	"bytes"
	"testing"
	"time"

	"github.com/hackerick4/botobot/internal/clock"
)

func TestIsWeak(t *testing.T) {
	weak := []string{"111111", "000000", "123456", "234567", "987654", "876543", "123123", "456456", "12345", "1234567"}
	for _, c := range weak {
		if !IsWeak(c) {
			t.Errorf("IsWeak(%q) = false, want true", c)
		}
	}
	strong := []string{"482913", "731055", "123124", "135791", "900001", "123122"}
	for _, c := range strong {
		if IsWeak(c) {
			t.Errorf("IsWeak(%q) = true, want false", c)
		}
	}
}

func TestGenerateExcludesWeak(t *testing.T) {
	for i := 0; i < 2000; i++ {
		c, err := Generate()
		if err != nil {
			t.Fatalf("Generate: %v", err)
		}
		if len(c) != CodeLen {
			t.Fatalf("code %q len != %d", c, CodeLen)
		}
		if IsWeak(c) {
			t.Fatalf("Generate returned weak code %q", c)
		}
	}
}

func TestEncryptDecryptRoundTrip(t *testing.T) {
	key := bytes.Repeat([]byte{0x11}, 32)
	enc, err := Encrypt(key, "br_abc", "482913")
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	// Ciphertext must not contain the plaintext code.
	if bytes.Contains(enc, []byte("482913")) {
		t.Fatalf("ciphertext leaks the code")
	}
	got, err := Decrypt(key, "br_abc", enc)
	if err != nil {
		t.Fatalf("Decrypt: %v", err)
	}
	if got != "482913" {
		t.Fatalf("Decrypt = %q, want 482913", got)
	}
	// Wrong bridge id (AAD) fails.
	if _, err := Decrypt(key, "br_other", enc); err == nil {
		t.Fatalf("Decrypt with wrong bridge id should fail")
	}
	// Wrong key fails.
	if _, err := Decrypt(bytes.Repeat([]byte{0x22}, 32), "br_abc", enc); err == nil {
		t.Fatalf("Decrypt with wrong key should fail")
	}
	// Nonce randomisation => two encryptions differ.
	enc2, _ := Encrypt(key, "br_abc", "482913")
	if bytes.Equal(enc, enc2) {
		t.Fatalf("two encryptions should differ (random nonce)")
	}
}

func TestEqualConstantTime(t *testing.T) {
	if !Equal("482913", "482913") {
		t.Fatal("equal codes should match")
	}
	if Equal("482913", "482914") {
		t.Fatal("different codes should not match")
	}
}

func TestThrottleIPBackoffAfterFive(t *testing.T) {
	clk := clock.NewFake(time.Unix(1000, 0))
	th := NewThrottler(clk)
	const ip, bridge = "203.0.113.5", "br_x"

	// First five attempts are allowed by Check (we record failures via Fail).
	for i := 0; i < 5; i++ {
		if b := th.Check(ip, bridge); b != nil {
			t.Fatalf("attempt %d blocked early: %+v", i, b)
		}
		th.Fail(ip, bridge)
	}
	// After five failures the IP is in backoff (30s).
	b := th.Check(ip, bridge)
	if b == nil || b.Reason != ReasonIPBackoff {
		t.Fatalf("expected IP backoff, got %+v", b)
	}
	if b.RetryAfter != 30*time.Second {
		t.Fatalf("backoff = %s, want 30s", b.RetryAfter)
	}
	// After the backoff elapses it is allowed again.
	clk.Advance(30 * time.Second)
	if b := th.Check(ip, bridge); b != nil {
		t.Fatalf("still blocked after backoff: %+v", b)
	}
}

func TestThrottleBridgeTempLockAfterTwenty(t *testing.T) {
	clk := clock.NewFake(time.Unix(1000, 0))
	th := NewThrottler(clk)
	const bridge = "br_y"

	// Twenty failures spread across distinct IPs in the same /24 (a distributed
	// guess) trip the bridge-level 1h/20 lock without any single IP backing off
	// and before the subnet 1h/30 ban.
	for i := 0; i < 20; i++ {
		ip := net20(i)
		if b := th.Check(ip, bridge); b != nil {
			t.Fatalf("attempt %d blocked early: %+v", i, b)
		}
		th.Fail(ip, bridge)
	}
	b := th.Check(net20(99), bridge)
	if b == nil || b.Reason != ReasonBridgeLock {
		t.Fatalf("expected bridge lock, got %+v", b)
	}
	if b.RetryAfter != bridgeLockShort {
		t.Fatalf("lock retry = %s, want %s", b.RetryAfter, bridgeLockShort)
	}
	// Admin unlock resets the bridge counters.
	th.ResetBridge(bridge)
	if b := th.Check(net20(99), bridge); b != nil {
		t.Fatalf("still locked after ResetBridge: %+v", b)
	}
}

func TestThrottlePersistentBridgeLockAfterFifty(t *testing.T) {
	clk := clock.NewFake(time.Unix(1000, 0))
	th := NewThrottler(clk)
	const bridge = "br_z"
	var persisted bool
	for i := 0; i < 50; i++ {
		res := th.Fail(net20(i%250), bridge)
		if res.PersistBridgeLock {
			persisted = true
		}
	}
	if !persisted {
		t.Fatalf("expected PersistBridgeLock after 50 failures")
	}
}

// net20 returns distinct IPs within 198.51.0.0/24-ish spread so that a batch of
// them shares subnets sparsely; good enough to exercise per-bridge counting.
func net20(i int) string {
	return "198.51.100." + itoa(i%250+1)
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var buf [4]byte
	n := len(buf)
	for i > 0 {
		n--
		buf[n] = byte('0' + i%10)
		i /= 10
	}
	return string(buf[n:])
}
