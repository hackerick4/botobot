package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"
)

// openWriterDB opens a single-connection SQLite database with one trivial table,
// used to exercise the writer in isolation (no Store, no migrations).
func openWriterDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "w.db")+"?_txlock=immediate")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec("CREATE TABLE t (id INTEGER PRIMARY KEY, v TEXT)"); err != nil {
		t.Fatalf("create table: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func noopOp(*sql.Tx) (any, error) { return nil, nil }

// TestWriterBackpressure proves a full queue is shed as ErrBackpressure (which
// the HTTP layer maps to 503) rather than blocking the caller (DEV_SPEC §6.2).
func TestWriterBackpressure(t *testing.T) {
	ctx := context.Background()
	db := openWriterDB(t)
	// batchMax=1 so the first op is committed immediately; queueMax=1 so exactly
	// one further op can wait; flush is effectively disabled for the test.
	w := newWriter(db, 1, time.Hour, 1)
	t.Cleanup(w.Stop)

	started := make(chan struct{})
	release := make(chan struct{})
	done := make(chan struct{})

	// op1 occupies the writer goroutine until released.
	go func() {
		_, _ = w.Submit(ctx, func(*sql.Tx) (any, error) {
			close(started)
			<-release
			return nil, nil
		})
		close(done)
	}()
	<-started // writer is now busy executing op1

	// op2 fills the single queue slot (blocks awaiting its result).
	go func() { _, _ = w.Submit(ctx, noopOp) }()
	for len(w.queue) < 1 {
		runtime.Gosched()
	}

	// op3 must be shed as backpressure.
	if _, err := w.Submit(ctx, noopOp); !errors.Is(err, ErrBackpressure) {
		t.Fatalf("op3 err = %v, want ErrBackpressure", err)
	}

	close(release)
	<-done
}

// TestWriterGroupCommitConcurrent submits many ops concurrently; the writer must
// batch and commit them all, returning each op's own result.
func TestWriterGroupCommitConcurrent(t *testing.T) {
	ctx := context.Background()
	db := openWriterDB(t)
	w := newWriter(db, 512, 2*time.Millisecond, 10000)
	t.Cleanup(w.Stop)

	const n = 200
	var wg sync.WaitGroup
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := w.Submit(ctx, func(tx *sql.Tx) (any, error) {
				_, e := tx.ExecContext(ctx, "INSERT INTO t(id, v) VALUES(?, ?)", i, fmt.Sprintf("v%d", i))
				return nil, e
			})
			errs[i] = err
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("op %d failed: %v", i, err)
		}
	}
	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM t").Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != n {
		t.Errorf("rows committed = %d, want %d", count, n)
	}
}

// TestWriterSavepointIsolation verifies that within a single batch one failing
// op is rolled back (via its SAVEPOINT) without affecting a sibling op that
// commits. commitBatch is driven directly for determinism.
func TestWriterSavepointIsolation(t *testing.T) {
	ctx := context.Background()
	db := openWriterDB(t)
	w := &writer{db: db} // no goroutine; we call commitBatch directly

	boom := errors.New("boom")
	good := writeOp{ctx: ctx, res: make(chan opResult, 1), fn: func(tx *sql.Tx) (any, error) {
		_, e := tx.ExecContext(ctx, "INSERT INTO t(id, v) VALUES(1, 'ok')")
		return "ok", e
	}}
	bad := writeOp{ctx: ctx, res: make(chan opResult, 1), fn: func(tx *sql.Tx) (any, error) {
		if _, e := tx.ExecContext(ctx, "INSERT INTO t(id, v) VALUES(2, 'bad')"); e != nil {
			return nil, e
		}
		return nil, boom // insert happened, but the op fails -> must roll back
	}}

	w.commitBatch([]writeOp{good, bad})

	if r := <-good.res; r.err != nil || r.val != "ok" {
		t.Fatalf("good op result = %+v, want {ok,nil}", r)
	}
	if r := <-bad.res; !errors.Is(r.err, boom) {
		t.Fatalf("bad op err = %v, want boom", r.err)
	}

	// Row 1 (good) committed; row 2 (bad) rolled back by its SAVEPOINT.
	var okCount, badCount int
	_ = db.QueryRow("SELECT COUNT(*) FROM t WHERE id=1").Scan(&okCount)
	_ = db.QueryRow("SELECT COUNT(*) FROM t WHERE id=2").Scan(&badCount)
	if okCount != 1 {
		t.Errorf("good row count = %d, want 1", okCount)
	}
	if badCount != 0 {
		t.Errorf("bad row count = %d, want 0 (rolled back)", badCount)
	}
}
