// Package store is the bot-bridge persistence layer (DEV_SPEC §5, §9, §10): the
// SQLite schema and migrations, a single-writer group-commit path, and the
// queries over bridges / lands / keys / api_keys / messages / inbox /
// delivery_status.
//
// It uses the pure-Go, cgo-free driver modernc.org/sqlite so the server builds
// with CGO_ENABLED=0 (DEV_SPEC §3.4). All mutations flow through the writer
// goroutine; pure reads use a separate read-only connection pool, which SQLite's
// WAL mode allows to run concurrently with the writer.
package store

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"github.com/hackerick4/botobot/internal/clock"

	_ "modernc.org/sqlite"
)

// blobThreshold is the ciphertext size above which an envelope's ciphertext is
// stored in a file under the blob dir instead of inline in the DB (DEV_SPEC
// §3.2: ciphertext > 256 KiB).
const blobThreshold = 256 * 1024

// defaultReadPoolSize bounds concurrent SQLite readers. The target VM is small
// (1 GB RAM, DEV_SPEC §17.4 lists the loadtest box), so this stays modest.
const defaultReadPoolSize = 8

// Options configures a Store. The server builds this from config (DEV_SPEC §14);
// keeping it a plain struct avoids a store->config dependency.
type Options struct {
	// Path is the SQLite database file (e.g. <BB_DATA_DIR>/bridge.db).
	Path string
	// BlobDir holds large-envelope ciphertext files (e.g. <BB_DATA_DIR>/blobs).
	BlobDir string
	// Clock is the injectable time source for TTL/lease/activity (DEV_SPEC §8).
	Clock clock.Clock

	// Writer group-commit tuning (DEV_SPEC §14).
	BatchMax int
	Flush    time.Duration
	QueueMax int

	// ReadPoolSize caps concurrent readers; <=0 uses defaultReadPoolSize.
	ReadPoolSize int

	// TTL is the inbox hard time-to-live (DEV_SPEC §10; <=0 and >24h are
	// rejected by config, so this is always 0<TTL<=24h; 0 falls back to 24h).
	TTL time.Duration
	// Lease is how long a fetched-but-unacked item is hidden (DEV_SPEC §10).
	Lease time.Duration
	// MaxInboxItemsPerLand caps queued items per recipient (DEV_SPEC §9).
	MaxInboxItemsPerLand int
}

// Store owns the database connections and the writer goroutine.
type Store struct {
	read   *sql.DB
	write  *sql.DB
	writer *writer
	clk    clock.Clock

	blobDir       string
	blobThreshold int64

	ttl                  time.Duration
	lease                time.Duration
	maxInboxItemsPerLand int
}

// Open opens (creating if needed) the database at opts.Path, applies pragmas,
// runs migrations, and starts the writer. The returned Store must be closed.
func Open(ctx context.Context, opts Options) (*Store, error) {
	if opts.Clock == nil {
		opts.Clock = clock.Real{}
	}
	if opts.BatchMax < 1 {
		opts.BatchMax = 512
	}
	if opts.Flush <= 0 {
		opts.Flush = 5 * time.Millisecond
	}
	if opts.QueueMax < 1 {
		opts.QueueMax = 10000
	}
	if opts.ReadPoolSize <= 0 {
		opts.ReadPoolSize = defaultReadPoolSize
	}
	if opts.TTL <= 0 {
		opts.TTL = 24 * time.Hour
	}
	if opts.Lease <= 0 {
		opts.Lease = 5 * time.Minute
	}
	if opts.MaxInboxItemsPerLand < 1 {
		opts.MaxInboxItemsPerLand = 10000
	}

	if dir := filepath.Dir(opts.Path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, fmt.Errorf("store: create data dir: %w", err)
		}
	}
	if opts.BlobDir != "" {
		if err := os.MkdirAll(opts.BlobDir, 0o700); err != nil {
			return nil, fmt.Errorf("store: create blob dir: %w", err)
		}
	}

	// Writer connection: one connection, transactions take the write lock
	// immediately (_txlock=immediate) so batches never deadlock on upgrade.
	write, err := sql.Open("sqlite", dsn(opts.Path, true))
	if err != nil {
		return nil, fmt.Errorf("store: open write db: %w", err)
	}
	write.SetMaxOpenConns(1)
	write.SetMaxIdleConns(1)
	write.SetConnMaxLifetime(0)

	read, err := sql.Open("sqlite", dsn(opts.Path, false))
	if err != nil {
		_ = write.Close()
		return nil, fmt.Errorf("store: open read db: %w", err)
	}
	read.SetMaxOpenConns(opts.ReadPoolSize)
	read.SetMaxIdleConns(opts.ReadPoolSize)
	read.SetConnMaxLifetime(0)

	s := &Store{
		read:                 read,
		write:                write,
		clk:                  opts.Clock,
		blobDir:              opts.BlobDir,
		blobThreshold:        blobThreshold,
		ttl:                  opts.TTL,
		lease:                opts.Lease,
		maxInboxItemsPerLand: opts.MaxInboxItemsPerLand,
	}

	if err := write.PingContext(ctx); err != nil {
		_ = s.closeDBs()
		return nil, fmt.Errorf("store: ping write db: %w", err)
	}
	if err := s.migrate(ctx); err != nil {
		_ = s.closeDBs()
		return nil, err
	}

	s.writer = newWriter(write, opts.BatchMax, opts.Flush, opts.QueueMax)
	return s, nil
}

// dsn builds the modernc.org/sqlite connection string with the pragmas required
// by DEV_SPEC §5 (WAL, synchronous=NORMAL, foreign_keys, busy_timeout) plus a
// modest page cache to suit the 1 GB target VM. The write connection also takes
// the write lock immediately on BEGIN.
func dsn(path string, write bool) string {
	q := url.Values{}
	q.Add("_pragma", "journal_mode(WAL)")
	q.Add("_pragma", "synchronous(NORMAL)")
	q.Add("_pragma", "foreign_keys(ON)")
	q.Add("_pragma", "busy_timeout(5000)")
	// Negative cache_size is in KiB. Keep caches small on a 1 GB box.
	if write {
		q.Add("_pragma", "cache_size(-8000)") // ~8 MiB for the writer
	} else {
		q.Add("_pragma", "cache_size(-2000)") // ~2 MiB per reader
	}
	if write {
		q.Set("_txlock", "immediate")
	}
	return "file:" + path + "?" + q.Encode()
}

// Ping verifies the database is reachable and writable, backing /readyz
// (DEV_SPEC §6.4). It round-trips through the writer so a stuck write path is
// also detected.
func (s *Store) Ping(ctx context.Context) error {
	if err := s.read.PingContext(ctx); err != nil {
		return err
	}
	_, err := s.writer.Submit(ctx, func(tx *sql.Tx) (any, error) {
		return nil, tx.QueryRowContext(ctx, "SELECT 1").Scan(new(int))
	})
	return err
}

// Close stops the writer and closes both connection pools.
func (s *Store) Close() error {
	if s.writer != nil {
		s.writer.Stop()
	}
	return s.closeDBs()
}

func (s *Store) closeDBs() error {
	var first error
	if s.read != nil {
		if err := s.read.Close(); err != nil {
			first = err
		}
	}
	if s.write != nil {
		if err := s.write.Close(); err != nil && first == nil {
			first = err
		}
	}
	return first
}

// now returns the current time in Unix milliseconds from the injected clock.
func (s *Store) nowMillis() int64 { return s.clk.Now().UnixMilli() }
