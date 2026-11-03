// Package config parses and validates the bot-bridge server configuration from
// environment variables (DEV_SPEC §14). Secrets are read from an env var or the
// companion "<NAME>_FILE" path (Docker secrets). The server must refuse to start
// when a required secret is missing or any value is out of range.
package config

import (
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Hard protocol limits that configuration may not exceed (DEV_SPEC §1.1 G4,
// §9, §14). These are enforced at startup regardless of the supplied values.
const (
	// MaxInboxTTL is the hard cap on how long ciphertext may be queued.
	MaxInboxTTL = 24 * time.Hour
	// MaxLandsCeiling is the ceiling on lands per bridge. Since v0.5.0 new
	// bridges always take two; BB_MAX_LANDS only bounds bridges made before.
	MaxLandsCeiling = 32
)

// Config holds every server setting. Secret material is stored decoded as raw
// bytes; the original base64 strings are never retained.
type Config struct {
	ListenAddr  string
	PublicURL   string
	DataDir     string
	MetricsAddr string

	// TrustProxyHeader, when non-empty, names the header to read the client IP
	// from (e.g. "X-Forwarded-For" or "CF-Connecting-IP"). Empty => TCP addr.
	TrustProxyHeader string

	// TrustedProxies is the allowlist of proxy source networks whose forwarding
	// header is honoured (DEV_SPEC §8, §14). Loopback is always trusted (Caddy
	// runs on the same host, §18); this adds any non-loopback proxy networks.
	// Empty (the default) => loopback only.
	TrustedProxies []*net.IPNet

	// Secrets (decoded). APIKeyPepper is the HMAC pepper for API keys (>=32B).
	// InviteKey is the AEAD key for invite-code encryption (exactly 32B).
	APIKeyPepper []byte
	InviteKey    []byte

	MaxLands              int
	MaxEnvelopeBytes      int64
	MaxQueueQuotaBytes    int64
	MaxInboxItemsPerLand  int
	MaxInboxResponseBytes int64

	InboxTTL time.Duration
	Lease    time.Duration
	// CloseGrace (v0.6.4) is how long, after a bridge is closed, the other
	// side may still fetch the messages it had not read yet (0 = none).
	CloseGrace      time.Duration
	AdminInactivity time.Duration
	SweepInterval   time.Duration

	// Public /join flow (DEV_SPEC §6 /join). ClaimTTL is how long an unclaimed
	// pending bridge (and its one-time admin claim token) lives; it must be
	// <=24h. JoinCreatePerHour is the per-prefix rate limit on POST /join (per
	// /24 IPv4 or /64 IPv6). MaxPendingBridges is the global cap on
	// simultaneously-pending bridges; MaxPendingBridgesPerPrefix caps them per
	// source prefix (Batch 1 item 6).
	ClaimTTL                   time.Duration
	JoinCreatePerHour          int
	MaxPendingBridges          int
	MaxPendingBridgesPerPrefix int

	// Public entry (v0.6.0, DEV_SPEC §20). EntryCreatePerHour limits POST
	// /v1/entries per source prefix and MaxEntriesPerPrefix caps open entries
	// per prefix. EntryMaxOpenBridges / EntryJoinsPerHour are the defaults for
	// a new entry (its owner may change them within hard ceilings). Guest
	// joins are limited per prefix across all entries and per prefix per entry.
	EntryCreatePerHour                int
	MaxEntriesPerPrefix               int
	EntryMaxOpenBridges               int
	EntryJoinsPerHour                 int
	EntryJoinPerPrefixPerHour         int
	EntryJoinPerPrefixPerEntryPerHour int

	// AllowLegacyCreate enables the unauthenticated direct-create endpoint
	// POST /v1/bridges. Disabled by default: the supported onboarding path is the
	// public /join + claim flow (Batch 1 item 6).
	AllowLegacyCreate bool

	WriterBatchMax int
	WriterFlush    time.Duration
	WriterQueueMax int

	// Rate limits (DEV_SPEC §9). DefaultRateLimitPerMin is the per-bridge send
	// default; the others are per-land server limits.
	DefaultRateLimitPerMin       int
	RateInboxPerMin              int
	RateAckPerMin                int
	RateOtherPerMin              int
	RateCreateBridgePerHourPerIP int

	LogLevel  string
	LogFormat string
}

// osLookup adapts os.LookupEnv to the lookupFunc signature.
func osLookup(key string) (string, bool) { return os.LookupEnv(key) }

// Load reads the configuration from the process environment and validates it.
func Load() (*Config, error) {
	return load(osLookup, os.ReadFile)
}

type lookupFunc func(key string) (string, bool)
type readFileFunc func(name string) ([]byte, error)

// load is the testable core: environment access and file reads are injected.
func load(lookup lookupFunc, readFile readFileFunc) (*Config, error) {
	p := &parser{lookup: lookup, readFile: readFile}
	c := &Config{}

	c.ListenAddr = p.str("BB_LISTEN_ADDR", "127.0.0.1:8080")
	c.PublicURL = p.str("BB_PUBLIC_URL", "")
	c.DataDir = p.str("BB_DATA_DIR", "/data")
	c.MetricsAddr = p.str("BB_METRICS_ADDR", "127.0.0.1:9090")
	c.TrustProxyHeader = p.str("BB_TRUST_PROXY_HEADER", "")
	c.TrustedProxies = p.cidrList("BB_TRUSTED_PROXIES")

	c.APIKeyPepper = p.secret("BB_API_KEY_PEPPER")
	c.InviteKey = p.secret("BB_INVITE_KEY")

	c.MaxLands = p.intVal("BB_MAX_LANDS", 32)
	c.MaxEnvelopeBytes = p.int64Val("BB_MAX_ENVELOPE_BYTES", 26214400)
	c.MaxQueueQuotaBytes = p.int64Val("BB_MAX_QUEUE_QUOTA_BYTES", 2147483648)
	c.MaxInboxItemsPerLand = p.intVal("BB_MAX_INBOX_ITEMS_PER_LAND", 10000)
	c.MaxInboxResponseBytes = p.int64Val("BB_MAX_INBOX_RESPONSE_BYTES", 67108864)

	c.InboxTTL = p.dur("BB_INBOX_TTL", 24*time.Hour)
	c.CloseGrace = p.dur("BB_CLOSE_GRACE", 10*time.Minute)
	c.Lease = p.dur("BB_LEASE", 5*time.Minute)
	c.AdminInactivity = p.dur("BB_ADMIN_INACTIVITY", 168*time.Hour)
	c.SweepInterval = p.dur("BB_SWEEP_INTERVAL", 60*time.Second)

	c.ClaimTTL = p.dur("BB_CLAIM_TTL", 30*time.Minute)
	c.JoinCreatePerHour = p.intVal("BB_JOIN_CREATE_PER_HOUR", 5)
	c.MaxPendingBridges = p.intVal("BB_MAX_PENDING_BRIDGES", 200)
	c.MaxPendingBridgesPerPrefix = p.intVal("BB_MAX_PENDING_BRIDGES_PER_PREFIX", 2)
	c.EntryCreatePerHour = p.intVal("BB_ENTRY_CREATE_PER_HOUR", 3)
	c.MaxEntriesPerPrefix = p.intVal("BB_MAX_ENTRIES_PER_PREFIX", 5)
	c.EntryMaxOpenBridges = p.intVal("BB_ENTRY_MAX_OPEN_BRIDGES", 200)
	c.EntryJoinsPerHour = p.intVal("BB_ENTRY_JOINS_PER_HOUR", 60)
	c.EntryJoinPerPrefixPerHour = p.intVal("BB_ENTRY_JOIN_PER_PREFIX_PER_HOUR", 20)
	c.EntryJoinPerPrefixPerEntryPerHour = p.intVal("BB_ENTRY_JOIN_PER_PREFIX_PER_ENTRY_PER_HOUR", 5)
	c.AllowLegacyCreate = p.boolVal("BB_ALLOW_LEGACY_CREATE", false)

	c.WriterBatchMax = p.intVal("BB_WRITER_BATCH_MAX", 512)
	c.WriterFlush = p.dur("BB_WRITER_FLUSH", 5*time.Millisecond)
	c.WriterQueueMax = p.intVal("BB_WRITER_QUEUE_MAX", 10000)

	c.DefaultRateLimitPerMin = p.intVal("BB_RATE_SEND_PER_MIN", 30)
	c.RateInboxPerMin = p.intVal("BB_RATE_INBOX_PER_MIN", 6)
	c.RateAckPerMin = p.intVal("BB_RATE_ACK_PER_MIN", 60)
	c.RateOtherPerMin = p.intVal("BB_RATE_OTHER_PER_MIN", 60)
	c.RateCreateBridgePerHourPerIP = p.intVal("BB_RATE_CREATE_BRIDGE_PER_HOUR_PER_IP", 5)

	c.LogLevel = strings.ToLower(p.str("BB_LOG_LEVEL", "info"))
	c.LogFormat = strings.ToLower(p.str("BB_LOG_FORMAT", "json"))

	// Parse errors first, then semantic validation.
	if err := p.err(); err != nil {
		return nil, err
	}
	if err := c.validate(); err != nil {
		return nil, err
	}
	return c, nil
}

// validate enforces required fields and ranges (DEV_SPEC §9, §14). All problems
// are collected so the operator sees every issue at once.
func (c *Config) validate() error {
	var errs []string
	add := func(format string, args ...any) { errs = append(errs, fmt.Sprintf(format, args...)) }

	if c.PublicURL == "" {
		add("BB_PUBLIC_URL is required (e.g. https://bridge.example)")
	} else if u, err := url.Parse(c.PublicURL); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		add("BB_PUBLIC_URL must be an absolute http(s) URL, got %q", c.PublicURL)
	}

	if len(c.APIKeyPepper) == 0 {
		add("BB_API_KEY_PEPPER is required (base64, >=32 bytes; or BB_API_KEY_PEPPER_FILE)")
	} else if len(c.APIKeyPepper) < 32 {
		add("BB_API_KEY_PEPPER must decode to >=32 bytes, got %d", len(c.APIKeyPepper))
	}

	if len(c.InviteKey) == 0 {
		add("BB_INVITE_KEY is required (base64, exactly 32 bytes; or BB_INVITE_KEY_FILE)")
	} else if len(c.InviteKey) != 32 {
		add("BB_INVITE_KEY must decode to exactly 32 bytes, got %d", len(c.InviteKey))
	}

	if c.MaxLands < 1 || c.MaxLands > MaxLandsCeiling {
		add("BB_MAX_LANDS must be 1..%d, got %d", MaxLandsCeiling, c.MaxLands)
	}
	if c.MaxEnvelopeBytes < 1 {
		add("BB_MAX_ENVELOPE_BYTES must be >=1, got %d", c.MaxEnvelopeBytes)
	}
	if c.MaxQueueQuotaBytes < 1024*1024 {
		add("BB_MAX_QUEUE_QUOTA_BYTES must be >=1 MiB, got %d", c.MaxQueueQuotaBytes)
	}
	if c.MaxInboxItemsPerLand < 1 {
		add("BB_MAX_INBOX_ITEMS_PER_LAND must be >=1, got %d", c.MaxInboxItemsPerLand)
	}
	if c.MaxInboxResponseBytes < 1 {
		add("BB_MAX_INBOX_RESPONSE_BYTES must be >=1, got %d", c.MaxInboxResponseBytes)
	}
	// The last-delivery window is short by design (the server forwards, it
	// does not keep messages): at most 30 minutes.
	if c.CloseGrace < 0 || c.CloseGrace > 30*time.Minute {
		add("BB_CLOSE_GRACE must be between 0 and 30m, got %s", c.CloseGrace)
	}

	// Inbox TTL must be positive and never exceed the 24h hard cap (§1.1 G4).
	if c.InboxTTL <= 0 {
		add("BB_INBOX_TTL must be > 0, got %s", c.InboxTTL)
	} else if c.InboxTTL > MaxInboxTTL {
		add("BB_INBOX_TTL must be <= 24h, got %s", c.InboxTTL)
	}
	if c.Lease <= 0 {
		add("BB_LEASE must be > 0, got %s", c.Lease)
	}
	if c.AdminInactivity <= 0 {
		add("BB_ADMIN_INACTIVITY must be > 0, got %s", c.AdminInactivity)
	}
	if c.SweepInterval <= 0 {
		add("BB_SWEEP_INTERVAL must be > 0, got %s", c.SweepInterval)
	}

	// Claim TTL must be positive and, like the inbox TTL, never exceed 24h: the
	// pending bridge is server-side state awaiting a claim (DEV_SPEC §6 /join).
	if c.ClaimTTL <= 0 {
		add("BB_CLAIM_TTL must be > 0, got %s", c.ClaimTTL)
	} else if c.ClaimTTL > MaxInboxTTL {
		add("BB_CLAIM_TTL must be <= 24h, got %s", c.ClaimTTL)
	}
	if c.JoinCreatePerHour < 1 {
		add("BB_JOIN_CREATE_PER_HOUR must be >=1, got %d", c.JoinCreatePerHour)
	}
	if c.MaxPendingBridges < 1 {
		add("BB_MAX_PENDING_BRIDGES must be >=1, got %d", c.MaxPendingBridges)
	}
	for name, v := range map[string]int{
		"BB_ENTRY_CREATE_PER_HOUR": c.EntryCreatePerHour, "BB_MAX_ENTRIES_PER_PREFIX": c.MaxEntriesPerPrefix,
		"BB_ENTRY_JOIN_PER_PREFIX_PER_HOUR":           c.EntryJoinPerPrefixPerHour,
		"BB_ENTRY_JOIN_PER_PREFIX_PER_ENTRY_PER_HOUR": c.EntryJoinPerPrefixPerEntryPerHour,
	} {
		if v < 1 {
			add("%s must be >=1, got %d", name, v)
		}
	}
	if c.EntryMaxOpenBridges < 1 || c.EntryMaxOpenBridges > 1000 {
		add("BB_ENTRY_MAX_OPEN_BRIDGES must be 1..1000, got %d", c.EntryMaxOpenBridges)
	}
	if c.EntryJoinsPerHour < 1 || c.EntryJoinsPerHour > 600 {
		add("BB_ENTRY_JOINS_PER_HOUR must be 1..600, got %d", c.EntryJoinsPerHour)
	}
	if c.MaxPendingBridgesPerPrefix < 1 {
		add("BB_MAX_PENDING_BRIDGES_PER_PREFIX must be >=1, got %d", c.MaxPendingBridgesPerPrefix)
	}

	if c.WriterBatchMax < 1 {
		add("BB_WRITER_BATCH_MAX must be >=1, got %d", c.WriterBatchMax)
	}
	if c.WriterFlush <= 0 {
		add("BB_WRITER_FLUSH must be > 0, got %s", c.WriterFlush)
	}
	if c.WriterQueueMax < 1 {
		add("BB_WRITER_QUEUE_MAX must be >=1, got %d", c.WriterQueueMax)
	}

	// Per-bridge send default is PATCH-constrained to 1..600 (§6.4, §9).
	if c.DefaultRateLimitPerMin < 1 || c.DefaultRateLimitPerMin > 600 {
		add("BB_RATE_SEND_PER_MIN must be 1..600, got %d", c.DefaultRateLimitPerMin)
	}
	for name, v := range map[string]int{
		"BB_RATE_INBOX_PER_MIN":                 c.RateInboxPerMin,
		"BB_RATE_ACK_PER_MIN":                   c.RateAckPerMin,
		"BB_RATE_OTHER_PER_MIN":                 c.RateOtherPerMin,
		"BB_RATE_CREATE_BRIDGE_PER_HOUR_PER_IP": c.RateCreateBridgePerHourPerIP,
	} {
		if v < 1 {
			add("%s must be >=1, got %d", name, v)
		}
	}

	switch c.LogLevel {
	case "debug", "info", "warn", "error":
	default:
		add("BB_LOG_LEVEL must be one of debug|info|warn|error, got %q", c.LogLevel)
	}
	switch c.LogFormat {
	case "json", "text":
	default:
		add("BB_LOG_FORMAT must be one of json|text, got %q", c.LogFormat)
	}

	if len(errs) == 0 {
		return nil
	}
	sort.Strings(errs)
	return fmt.Errorf("invalid configuration:\n  - %s", strings.Join(errs, "\n  - "))
}

// parser accumulates parse errors while reading typed values from the environment.
type parser struct {
	lookup   lookupFunc
	readFile readFileFunc
	errs     []string
}

func (p *parser) fail(format string, args ...any) {
	p.errs = append(p.errs, fmt.Sprintf(format, args...))
}

func (p *parser) err() error {
	if len(p.errs) == 0 {
		return nil
	}
	sort.Strings(p.errs)
	return errors.New("invalid configuration:\n  - " + strings.Join(p.errs, "\n  - "))
}

func (p *parser) str(key, def string) string {
	if v, ok := p.lookup(key); ok {
		return v
	}
	return def
}

func (p *parser) intVal(key string, def int) int {
	v, ok := p.lookup(key)
	if !ok || v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		p.fail("%s: %q is not an integer", key, v)
		return def
	}
	return n
}

func (p *parser) boolVal(key string, def bool) bool {
	v, ok := p.lookup(key)
	if !ok || v == "" {
		return def
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		p.fail("%s: %q is not a boolean (true/false/1/0)", key, v)
		return def
	}
	return b
}

func (p *parser) int64Val(key string, def int64) int64 {
	v, ok := p.lookup(key)
	if !ok || v == "" {
		return def
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		p.fail("%s: %q is not an integer", key, v)
		return def
	}
	return n
}

func (p *parser) dur(key string, def time.Duration) time.Duration {
	v, ok := p.lookup(key)
	if !ok || v == "" {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		p.fail("%s: %q is not a duration (e.g. 24h, 5m, 60s)", key, v)
		return def
	}
	return d
}

// cidrList parses a comma-separated list of CIDRs (a bare IP is accepted as a
// single-host /32 or /128) into networks (DEV_SPEC §14 BB_TRUSTED_PROXIES).
// Returns nil when unset; invalid entries are reported as parse errors.
func (p *parser) cidrList(key string) []*net.IPNet {
	v, ok := p.lookup(key)
	if !ok || strings.TrimSpace(v) == "" {
		return nil
	}
	var out []*net.IPNet
	for _, item := range strings.Split(v, ",") {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		if !strings.Contains(item, "/") {
			if ip := net.ParseIP(item); ip != nil {
				if ip.To4() != nil {
					item += "/32"
				} else {
					item += "/128"
				}
			}
		}
		_, n, err := net.ParseCIDR(item)
		if err != nil {
			p.fail("%s: %q is not a valid CIDR", key, item)
			continue
		}
		out = append(out, n)
	}
	return out
}

// secret resolves a secret from <key> or, failing that, the file named by
// <key>_FILE. The value is base64-decoded (std encoding, padding optional).
// Returns nil when the secret is absent; validation reports the missing field.
func (p *parser) secret(key string) []byte {
	raw, ok := p.lookup(key)
	if !ok || strings.TrimSpace(raw) == "" {
		if path, ok := p.lookup(key + "_FILE"); ok && strings.TrimSpace(path) != "" {
			b, err := p.readFile(strings.TrimSpace(path))
			if err != nil {
				p.fail("%s_FILE: cannot read %q: %v", key, strings.TrimSpace(path), err)
				return nil
			}
			raw = string(b)
		} else {
			return nil
		}
	}
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	decoded, err := decodeBase64(raw)
	if err != nil {
		p.fail("%s: not valid base64", key)
		return nil
	}
	return decoded
}

// decodeBase64 accepts standard base64 with or without padding.
func decodeBase64(s string) ([]byte, error) {
	if b, err := base64.StdEncoding.DecodeString(s); err == nil {
		return b, nil
	}
	return base64.RawStdEncoding.DecodeString(s)
}
