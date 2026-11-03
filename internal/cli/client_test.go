package cli

import (
	"fmt"
	"testing"
)

// TestMapHTTPErrorExitCodes checks the HTTP-code/error-code to exit-code mapping
// (DEV_SPEC §12.3).
func TestMapHTTPErrorExitCodes(t *testing.T) {
	cases := []struct {
		status   int
		code     string
		wantExit int
	}{
		{410, "bridge_closed", exitClosed},
		{401, "unauthorized", exitUnauth},
		{401, "join_failed", exitUnauth},
		{429, "rate_limited", exitRateLimited},
		{507, "queue_quota_exceeded", exitRateLimited},
		{400, "bad_request", exitGeneral},
		{404, "not_found", exitGeneral},
		{409, "recipients_mismatch", exitGeneral},
		{500, "internal", exitGeneral},
	}
	for _, c := range cases {
		body := []byte(fmt.Sprintf(`{"error":{"code":%q,"message":"m"}}`, c.code))
		e := mapHTTPError(c.status, body)
		if e.Exit != c.wantExit {
			t.Errorf("status %d code %s: exit = %d, want %d", c.status, c.code, e.Exit, c.wantExit)
		}
		if e.Code != c.code {
			t.Errorf("status %d: code = %q, want %q", c.status, e.Code, c.code)
		}
	}
}

// TestMapHTTPErrorRetryAfterAndClosed checks that retry_after_sec and the
// bridge_closed metadata are carried through (DEV_SPEC §12.3).
func TestMapHTTPErrorRetryAfterAndClosed(t *testing.T) {
	e := mapHTTPError(429, []byte(`{"error":{"code":"rate_limited","message":"slow down","retry_after_sec":30}}`))
	if e.RetryAfter != 30 {
		t.Errorf("retry_after = %d, want 30", e.RetryAfter)
	}

	e = mapHTTPError(410, []byte(`{"error":{"code":"bridge_closed","message":"gone","closed_at":"2026-10-29T07:00:00.000Z","close_reason":"admin_closed"}}`))
	if e.Exit != exitClosed {
		t.Fatalf("exit = %d, want %d", e.Exit, exitClosed)
	}
	if e.closeReason != "admin_closed" || e.closedAt == "" {
		t.Errorf("closed metadata not carried: reason=%q at=%q", e.closeReason, e.closedAt)
	}
}

// TestMapHTTPErrorNonJSON falls back to a status-keyed code (DEV_SPEC §12.3).
func TestMapHTTPErrorNonJSON(t *testing.T) {
	e := mapHTTPError(502, []byte("bad gateway"))
	if e.Code != "http_502" || e.Exit != exitGeneral {
		t.Errorf("non-JSON body: code=%q exit=%d", e.Code, e.Exit)
	}
}
