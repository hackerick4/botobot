// Package botbridge is the repository root package. Its sole job is to embed the
// SQL migrations so they ship inside the single static server binary.
//
// The embed directive can only reach files at or below the embedding Go file's directory,
// and DEV_SPEC §4 fixes migrations/ at the repository root. Rather than move the
// migrations, this tiny root package lives next to them and re-exports the FS to
// internal/store. See DEV_SPEC §19 (deviation: root embed package).
package botbridge

import "embed"

// Migrations holds every migrations/*.sql file, embedded at build time.
//
//go:embed migrations/*.sql
var Migrations embed.FS
