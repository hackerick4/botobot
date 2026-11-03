package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// client is a thin HTTP client for the bot-bridge API (DEV_SPEC §6). It attaches
// the bearer API key, enforces the request timeout via its http.Client, and maps
// error responses to cliError with the right exit code (DEV_SPEC §12.3). It never
// logs request/response bodies, the Authorization header, or the key (§15).
type client struct {
	hc     *http.Client
	base   string // server origin, e.g. https://bridge.example
	apiKey string // bbk_…; empty for unauthenticated calls (create/join)
}

func newClient(hc *http.Client, server, apiKey string) *client {
	return &client{hc: hc, base: strings.TrimRight(server, "/"), apiKey: apiKey}
}

// apiErrorBody is the server's error envelope (DEV_SPEC §6.1). Extra fields
// (retry_after_sec, closed_at, close_reason, land_set_version, recipients) are
// decoded opportunistically.
type apiErrorBody struct {
	Error struct {
		Code        string          `json:"code"`
		Message     string          `json:"message"`
		RetryAfter  int             `json:"retry_after_sec"`
		ClosedAt    string          `json:"closed_at"`
		CloseReason string          `json:"close_reason"`
		EntryClosed bool            `json:"entry_closed"`
		Raw         json.RawMessage `json:"-"`
	} `json:"error"`
}

// do performs a request against path (joined to base), optionally sending body
// as JSON, and decodes a 2xx response into out (when non-nil). Non-2xx responses
// are mapped to a cliError. A transport failure maps to a network error (exit 6).
func (c *client) do(ctx context.Context, method, path string, body, out any) *cliError {
	var reqBody io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return generalErrorf("encode request: %v", err)
		}
		reqBody = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, reqBody)
	if err != nil {
		return generalErrorf("build request: %v", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json; charset=utf-8")
	}
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}

	resp, err := c.hc.Do(req)
	if err != nil {
		// Transport errors (DNS, refused, timeout) are retryable (DEV_SPEC §12.3).
		return &cliError{Code: "network", Message: networkMessage(err), Exit: exitNetwork}
	}
	defer resp.Body.Close()
	// Cap the response read to a generous bound; inbox responses are the largest
	// and already server-capped at MAX_INBOX_RESPONSE_BYTES (64 MiB default).
	data, err := io.ReadAll(io.LimitReader(resp.Body, 128<<20))
	if err != nil {
		return &cliError{Code: "network", Message: "reading response body failed", Exit: exitNetwork}
	}

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		if out != nil {
			if err := json.Unmarshal(data, out); err != nil {
				return generalErrorf("decode response: %v", err)
			}
		}
		return nil
	}
	return mapHTTPError(resp.StatusCode, data)
}

// networkMessage returns a secret-free description of a transport error. The
// error from net/http can include the URL but never headers or body.
func networkMessage(err error) string {
	return "request failed: " + err.Error()
}

// mapHTTPError converts a non-2xx response into a cliError with the DEV_SPEC
// §12.3 exit code. The body is parsed as the standard error envelope; if it is
// not JSON, a generic error keyed by status is returned.
func mapHTTPError(status int, body []byte) *cliError {
	var env apiErrorBody
	code, msg := "", ""
	retryAfter := 0
	closedAt, closeReason := "", ""
	if err := json.Unmarshal(body, &env); err == nil && env.Error.Code != "" {
		code = env.Error.Code
		msg = env.Error.Message
		retryAfter = env.Error.RetryAfter
		closedAt = env.Error.ClosedAt
		closeReason = env.Error.CloseReason
	}
	if code == "" {
		code = fmt.Sprintf("http_%d", status)
	}
	if msg == "" {
		msg = fmt.Sprintf("server returned status %d", status)
	}

	e := &cliError{Code: code, Message: msg, RetryAfter: retryAfter, closedAt: closedAt, closeReason: closeReason, entryClosed: env.Error.EntryClosed}
	switch {
	case code == "claim_consumed":
		// A consumed claim token is terminal but not a closed bridge: plain error.
		e.Exit = exitGeneral
	case status == http.StatusGone || code == "bridge_closed":
		e.Exit = exitClosed
	case status == http.StatusUnauthorized || code == "unauthorized" || code == "join_failed" || code == "claim_failed":
		e.Exit = exitUnauth
	case status == http.StatusTooManyRequests || code == "rate_limited" ||
		status == http.StatusInsufficientStorage || code == "queue_quota_exceeded":
		e.Exit = exitRateLimited
	default:
		e.Exit = exitGeneral
	}
	return e
}
