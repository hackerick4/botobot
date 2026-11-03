package store

import (
	"context"
	"database/sql"
	"fmt"
	"io/fs"
	"sort"
	"strconv"
	"strings"

	botbridge "github.com/hackerick4/botobot"
)

// migration is one versioned SQL file from the embedded migrations/ directory.
type migration struct {
	version int
	name    string
	sql     string
}

// migrate applies every embedded migration whose version has not yet been
// recorded in schema_migrations, in ascending order, each in its own
// transaction (DEV_SPEC §18: migrations run at startup and only move forward).
// It runs on the write connection before the writer goroutine starts.
func (s *Store) migrate(ctx context.Context) error {
	migs, err := loadMigrations()
	if err != nil {
		return err
	}

	applied, err := appliedVersions(ctx, s.write)
	if err != nil {
		return err
	}

	for _, m := range migs {
		if applied[m.version] {
			continue
		}
		if err := applyMigration(ctx, s.write, m, s.nowMillis()); err != nil {
			return fmt.Errorf("store: migration %s: %w", m.name, err)
		}
	}
	return nil
}

// loadMigrations reads and sorts the embedded migration files.
func loadMigrations() ([]migration, error) {
	entries, err := fs.ReadDir(botbridge.Migrations, "migrations")
	if err != nil {
		return nil, fmt.Errorf("store: read embedded migrations: %w", err)
	}
	var migs []migration
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		ver, err := parseVersion(e.Name())
		if err != nil {
			return nil, err
		}
		b, err := botbridge.Migrations.ReadFile("migrations/" + e.Name())
		if err != nil {
			return nil, fmt.Errorf("store: read migration %s: %w", e.Name(), err)
		}
		migs = append(migs, migration{version: ver, name: e.Name(), sql: string(b)})
	}
	sort.Slice(migs, func(i, j int) bool { return migs[i].version < migs[j].version })
	return migs, nil
}

// parseVersion extracts the leading integer from a name like "0001_init.sql".
func parseVersion(name string) (int, error) {
	i := strings.IndexByte(name, '_')
	if i <= 0 {
		return 0, fmt.Errorf("store: migration %q: missing version prefix", name)
	}
	v, err := strconv.Atoi(name[:i])
	if err != nil {
		return 0, fmt.Errorf("store: migration %q: bad version prefix: %w", name, err)
	}
	return v, nil
}

// appliedVersions returns the set of already-applied migration versions. Before
// the very first migration the schema_migrations table does not exist yet, in
// which case the set is empty.
func appliedVersions(ctx context.Context, db *sql.DB) (map[int]bool, error) {
	var exists string
	err := db.QueryRowContext(ctx,
		"SELECT name FROM sqlite_master WHERE type='table' AND name='schema_migrations'").Scan(&exists)
	if err == sql.ErrNoRows {
		return map[int]bool{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("store: check schema_migrations: %w", err)
	}

	rows, err := db.QueryContext(ctx, "SELECT version FROM schema_migrations")
	if err != nil {
		return nil, fmt.Errorf("store: read schema_migrations: %w", err)
	}
	defer rows.Close()

	applied := map[int]bool{}
	for rows.Next() {
		var v int
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		applied[v] = true
	}
	return applied, rows.Err()
}

// applyMigration runs one migration's SQL and records it, atomically.
func applyMigration(ctx context.Context, db *sql.DB, m migration, nowMillis int64) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, m.sql); err != nil {
		_ = tx.Rollback()
		return err
	}
	if _, err := tx.ExecContext(ctx,
		"INSERT INTO schema_migrations(version, applied_at) VALUES(?, ?)", m.version, nowMillis); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

// SchemaVersion returns the highest applied migration version. It exposes no
// data, only which migrations ran (each migration applies atomically with its
// schema_migrations row, so version >= N proves migration N's statements ran).
func (s *Store) SchemaVersion(ctx context.Context) (int, error) {
	var v sql.NullInt64
	if err := s.read.QueryRowContext(ctx, "SELECT MAX(version) FROM schema_migrations").Scan(&v); err != nil {
		return 0, err
	}
	return int(v.Int64), nil
}
