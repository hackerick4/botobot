package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// validBase64 secrets: 32 bytes each (std base64 of 32 zero bytes is 44 chars).
const (
	valid32B = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=" // 32 bytes
	valid16B = "AAAAAAAAAAAAAAAAAAAAAA=="                     // 16 bytes
	valid48B = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=="
)

// mapLookup returns a lookupFunc backed by a map.
func mapLookup(m map[string]string) lookupFunc {
	return func(key string) (string, bool) {
		v, ok := m[key]
		return v, ok
	}
}

// baseEnv is a minimal valid environment.
func baseEnv() map[string]string {
	return map[string]string{
		"BB_PUBLIC_URL":     "https://bridge.example",
		"BB_API_KEY_PEPPER": valid32B,
		"BB_INVITE_KEY":     valid32B,
	}
}

func loadMap(m map[string]string) (*Config, error) {
	return load(mapLookup(m), os.ReadFile)
}

func TestLoadDefaults(t *testing.T) {
	c, err := loadMap(baseEnv())
	if err != nil {
		t.Fatalf("expected valid config, got error: %v", err)
	}
	if c.ListenAddr != "127.0.0.1:8080" {
		t.Errorf("ListenAddr default = %q", c.ListenAddr)
	}
	if c.DataDir != "/data" {
		t.Errorf("DataDir default = %q", c.DataDir)
	}
	if c.MaxLands != 32 {
		t.Errorf("MaxLands default = %d", c.MaxLands)
	}
	if c.MaxEnvelopeBytes != 26214400 {
		t.Errorf("MaxEnvelopeBytes default = %d", c.MaxEnvelopeBytes)
	}
	if c.InboxTTL.Hours() != 24 {
		t.Errorf("InboxTTL default = %s", c.InboxTTL)
	}
	if c.LogLevel != "info" || c.LogFormat != "json" {
		t.Errorf("log defaults = %q/%q", c.LogLevel, c.LogFormat)
	}
	if len(c.APIKeyPepper) != 32 || len(c.InviteKey) != 32 {
		t.Errorf("secret lengths = %d/%d", len(c.APIKeyPepper), len(c.InviteKey))
	}
	if c.ClaimTTL != 30*time.Minute {
		t.Errorf("ClaimTTL default = %s, want 30m", c.ClaimTTL)
	}
	if c.JoinCreatePerHour != 5 {
		t.Errorf("JoinCreatePerHour default = %d, want 5", c.JoinCreatePerHour)
	}
	if c.MaxPendingBridges != 200 {
		t.Errorf("MaxPendingBridges default = %d, want 200", c.MaxPendingBridges)
	}
	if c.MaxPendingBridgesPerPrefix != 2 {
		t.Errorf("MaxPendingBridgesPerPrefix default = %d, want 2", c.MaxPendingBridgesPerPrefix)
	}
	if c.AllowLegacyCreate {
		t.Errorf("AllowLegacyCreate default = true, want false")
	}
}

func TestJoinClaimValidation(t *testing.T) {
	tests := []struct {
		name    string
		env     map[string]string
		wantSub string
	}{
		{"claim ttl over 24h", map[string]string{"BB_CLAIM_TTL": "25h"}, "BB_CLAIM_TTL must be <= 24h"},
		{"claim ttl zero", map[string]string{"BB_CLAIM_TTL": "0s"}, "BB_CLAIM_TTL must be > 0"},
		{"join rate zero", map[string]string{"BB_JOIN_CREATE_PER_HOUR": "0"}, "BB_JOIN_CREATE_PER_HOUR must be >=1"},
		{"pending cap zero", map[string]string{"BB_MAX_PENDING_BRIDGES": "0"}, "BB_MAX_PENDING_BRIDGES must be >=1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := baseEnv()
			for k, v := range tt.env {
				env[k] = v
			}
			_, err := loadMap(env)
			if err == nil || !strings.Contains(err.Error(), tt.wantSub) {
				t.Fatalf("err = %v, want substring %q", err, tt.wantSub)
			}
		})
	}
}

func TestClaimTTLAtCap(t *testing.T) {
	env := baseEnv()
	env["BB_CLAIM_TTL"] = "24h"
	if _, err := loadMap(env); err != nil {
		t.Fatalf("24h claim TTL should be valid, got %v", err)
	}
}

func TestMissingRequiredSecretsRefused(t *testing.T) {
	tests := []struct {
		name    string
		remove  string
		wantSub string
	}{
		{"no public url", "BB_PUBLIC_URL", "BB_PUBLIC_URL is required"},
		{"no pepper", "BB_API_KEY_PEPPER", "BB_API_KEY_PEPPER is required"},
		{"no invite key", "BB_INVITE_KEY", "BB_INVITE_KEY is required"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := baseEnv()
			delete(env, tt.remove)
			_, err := loadMap(env)
			if err == nil {
				t.Fatalf("expected error when %s missing", tt.remove)
			}
			if !strings.Contains(err.Error(), tt.wantSub) {
				t.Errorf("error %q does not mention %q", err.Error(), tt.wantSub)
			}
		})
	}
}

func TestSecretLengthValidation(t *testing.T) {
	t.Run("pepper too short", func(t *testing.T) {
		env := baseEnv()
		env["BB_API_KEY_PEPPER"] = valid16B
		_, err := loadMap(env)
		if err == nil || !strings.Contains(err.Error(), "BB_API_KEY_PEPPER must decode to >=32") {
			t.Fatalf("expected >=32 error, got %v", err)
		}
	})
	t.Run("pepper longer ok", func(t *testing.T) {
		env := baseEnv()
		env["BB_API_KEY_PEPPER"] = valid48B
		if _, err := loadMap(env); err != nil {
			t.Fatalf("48B pepper should be valid: %v", err)
		}
	})
	t.Run("invite key wrong length", func(t *testing.T) {
		env := baseEnv()
		env["BB_INVITE_KEY"] = valid48B // 48 != 32
		_, err := loadMap(env)
		if err == nil || !strings.Contains(err.Error(), "exactly 32 bytes") {
			t.Fatalf("expected exactly-32 error, got %v", err)
		}
	})
	t.Run("bad base64", func(t *testing.T) {
		env := baseEnv()
		env["BB_API_KEY_PEPPER"] = "!!!not base64!!!"
		_, err := loadMap(env)
		if err == nil || !strings.Contains(err.Error(), "not valid base64") {
			t.Fatalf("expected base64 error, got %v", err)
		}
	})
}

func TestInboxTTLCap(t *testing.T) {
	t.Run("over 24h refused", func(t *testing.T) {
		env := baseEnv()
		env["BB_INBOX_TTL"] = "25h"
		_, err := loadMap(env)
		if err == nil || !strings.Contains(err.Error(), "BB_INBOX_TTL must be <= 24h") {
			t.Fatalf("expected TTL cap error, got %v", err)
		}
	})
	t.Run("exactly 24h ok", func(t *testing.T) {
		env := baseEnv()
		env["BB_INBOX_TTL"] = "24h"
		if _, err := loadMap(env); err != nil {
			t.Fatalf("24h should be valid: %v", err)
		}
	})
	t.Run("zero refused", func(t *testing.T) {
		env := baseEnv()
		env["BB_INBOX_TTL"] = "0s"
		_, err := loadMap(env)
		if err == nil || !strings.Contains(err.Error(), "BB_INBOX_TTL must be > 0") {
			t.Fatalf("expected TTL>0 error, got %v", err)
		}
	})
	t.Run("invalid duration refused", func(t *testing.T) {
		env := baseEnv()
		env["BB_INBOX_TTL"] = "notaduration"
		_, err := loadMap(env)
		if err == nil || !strings.Contains(err.Error(), "BB_INBOX_TTL") {
			t.Fatalf("expected duration parse error, got %v", err)
		}
	})
}

func TestRangeValidation(t *testing.T) {
	tests := []struct {
		key, val, wantSub string
	}{
		{"BB_MAX_LANDS", "0", "BB_MAX_LANDS must be 1..32"},
		{"BB_MAX_LANDS", "33", "BB_MAX_LANDS must be 1..32"},
		{"BB_MAX_ENVELOPE_BYTES", "0", "BB_MAX_ENVELOPE_BYTES must be >=1"},
		{"BB_MAX_QUEUE_QUOTA_BYTES", "1000", "BB_MAX_QUEUE_QUOTA_BYTES must be >=1 MiB"},
		{"BB_RATE_SEND_PER_MIN", "601", "BB_RATE_SEND_PER_MIN must be 1..600"},
		{"BB_RATE_INBOX_PER_MIN", "0", "BB_RATE_INBOX_PER_MIN must be >=1"},
		{"BB_LOG_LEVEL", "verbose", "BB_LOG_LEVEL must be one of"},
		{"BB_LOG_FORMAT", "xml", "BB_LOG_FORMAT must be one of"},
	}
	for _, tt := range tests {
		t.Run(tt.key+"="+tt.val, func(t *testing.T) {
			env := baseEnv()
			env[tt.key] = tt.val
			_, err := loadMap(env)
			if err == nil || !strings.Contains(err.Error(), tt.wantSub) {
				t.Fatalf("key %s=%s: expected %q, got %v", tt.key, tt.val, tt.wantSub, err)
			}
		})
	}
}

func TestBadPublicURL(t *testing.T) {
	env := baseEnv()
	env["BB_PUBLIC_URL"] = "not-a-url"
	_, err := loadMap(env)
	if err == nil || !strings.Contains(err.Error(), "BB_PUBLIC_URL must be an absolute http(s) URL") {
		t.Fatalf("expected URL error, got %v", err)
	}
}

func TestMultipleErrorsReported(t *testing.T) {
	// Removing all secrets should surface all three in one message.
	_, err := loadMap(map[string]string{})
	if err == nil {
		t.Fatal("expected error")
	}
	for _, want := range []string{"BB_PUBLIC_URL", "BB_API_KEY_PEPPER", "BB_INVITE_KEY"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("combined error missing %q:\n%s", want, err.Error())
		}
	}
}

func TestSecretFromFile(t *testing.T) {
	dir := t.TempDir()
	pepperPath := filepath.Join(dir, "pepper")
	if err := os.WriteFile(pepperPath, []byte(valid32B+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	env := baseEnv()
	delete(env, "BB_API_KEY_PEPPER")
	env["BB_API_KEY_PEPPER_FILE"] = pepperPath

	c, err := load(mapLookup(env), os.ReadFile)
	if err != nil {
		t.Fatalf("expected config from *_FILE secret: %v", err)
	}
	if len(c.APIKeyPepper) != 32 {
		t.Errorf("pepper from file length = %d", len(c.APIKeyPepper))
	}
}

func TestSecretFileMissing(t *testing.T) {
	env := baseEnv()
	delete(env, "BB_INVITE_KEY")
	env["BB_INVITE_KEY_FILE"] = "/nonexistent/path/to/secret"
	_, err := load(mapLookup(env), os.ReadFile)
	if err == nil || !strings.Contains(err.Error(), "BB_INVITE_KEY_FILE: cannot read") {
		t.Fatalf("expected file read error, got %v", err)
	}
}

func TestEnvOverridesFile(t *testing.T) {
	// When both the env var and *_FILE are set, the env var wins.
	env := baseEnv() // BB_API_KEY_PEPPER set to valid32B
	env["BB_API_KEY_PEPPER_FILE"] = "/nonexistent"
	c, err := load(mapLookup(env), os.ReadFile)
	if err != nil {
		t.Fatalf("env var should take precedence over *_FILE: %v", err)
	}
	if len(c.APIKeyPepper) != 32 {
		t.Errorf("pepper length = %d", len(c.APIKeyPepper))
	}
}
