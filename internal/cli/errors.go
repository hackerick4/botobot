// Package cli implements bot-bridge-cli, the land-side client (DEV_SPEC §12).
// It owns the local keystore (§12.1), an HTTP client with timeouts, the
// subcommands, and the verify/decrypt + replay(seen)/attachment handling that
// §7.8 (steps 1 and 7) and §19 assign to the CLI. Cryptography is delegated to
// internal/protocol so the server and CLI share exactly one implementation.
//
// Output contract (§12.3): machine output is JSON lines on stdout; errors are a
// single JSON line on stderr; --pretty switches stdout to a human-readable form.
// Secrets (API key, private keys, plaintext, invite codes) are never written to
// stderr or logs (§15, §16).
package cli

import "fmt"

// Exit codes (DEV_SPEC §12.3).
const (
	exitOK          = 0 // success, including NO_NEW_MESSAGES
	exitGeneral     = 1 // generic error
	exitUsage       = 2 // usage error
	exitClosed      = 3 // bridge_closed
	exitUnauth      = 4 // unauthenticated / invalid key
	exitRateLimited = 5 // rate_limited / quota
	exitNetwork     = 6 // network error (retryable)
)

// cliError is a failure that maps to a DEV_SPEC §12.3 exit code and is rendered
// as the one-line stderr error object {"type":"error","code","message",
// "retry_after_sec"?}. It never carries secret material in Message.
type cliError struct {
	Code       string // machine-readable code (API §6.2 code, or a CLI-local code)
	Message    string // human-readable, secret-free
	RetryAfter int    // seconds; emitted as retry_after_sec when > 0
	Exit       int    // process exit code

	// closedAt / closeReason are populated for bridge_closed so poll can emit
	// the synthesized bridge_closed event (DEV_SPEC §11, §12.3).
	closedAt    string
	closeReason string
	// entryClosed (v0.6.1): this closed bridge was a public-entry line and
	// the whole entry closed, not only this line.
	entryClosed bool
}

func (e *cliError) Error() string {
	if e.Message == "" {
		return e.Code
	}
	return e.Code + ": " + e.Message
}

// usageErrorf builds a usage error (exit 2).
func usageErrorf(format string, args ...any) *cliError {
	return &cliError{Code: "usage", Message: fmt.Sprintf(format, args...), Exit: exitUsage}
}

// generalErrorf builds a generic error (exit 1).
func generalErrorf(format string, args ...any) *cliError {
	return &cliError{Code: "error", Message: fmt.Sprintf(format, args...), Exit: exitGeneral}
}
