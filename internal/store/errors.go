package store

import (
	"errors"
	"fmt"
)

// Sentinel errors returned by store operations. Handlers (M3/M4) translate these
// into the HTTP error codes of DEV_SPEC §6.2.
var (
	// ErrNotFound is returned when a requested row does not exist.
	ErrNotFound = errors.New("store: not found")
	// ErrQuotaExceeded maps to 507 queue_quota_exceeded (DEV_SPEC §6.2, §9):
	// the bridge queue byte quota would be exceeded.
	ErrQuotaExceeded = errors.New("store: queue quota exceeded")
	// ErrInboxFull maps to 507 (DEV_SPEC §9): a recipient already holds the
	// maximum number of inbox items.
	ErrInboxFull = errors.New("store: recipient inbox full")
	// ErrDuplicateMsgID maps to 409 duplicate_msg_id (DEV_SPEC §6.2 / §6.4
	// step 7): the (bridge, sender, msg_id) already exists with different
	// content.
	ErrDuplicateMsgID = errors.New("store: duplicate msg_id with different content")
)

// errPanic wraps a recovered panic value as an error.
func errPanic(r any) error {
	if err, ok := r.(error); ok {
		return fmt.Errorf("store: op panicked: %w", err)
	}
	return fmt.Errorf("store: op panicked: %v", r)
}

// ErrInviteConsumed is returned by CreateLand(ConsumeInvite) when the one-time
// invite code was already used (possibly by a concurrent join).
var ErrInviteConsumed = errors.New("store: invite code already consumed")
