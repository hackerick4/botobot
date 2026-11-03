package store

import (
	"context"
	"database/sql"
	"errors"
	"sync"
	"time"
)

// ErrBackpressure is returned by the writer when the pending-op queue is full
// (DEV_SPEC §6.2 / §14 BB_WRITER_QUEUE_MAX). Handlers map it to HTTP 503
// `backpressure` with Retry-After: 1.
var ErrBackpressure = errors.New("store: writer queue full (backpressure)")

// ErrWriterClosed is returned once the writer has been stopped.
var ErrWriterClosed = errors.New("store: writer closed")

// opFunc is a single unit of work run inside a shared batch transaction. It
// must only touch the database via tx. Its return value is handed back to the
// caller of Submit. An error aborts just this op (the surrounding SAVEPOINT is
// rolled back); sibling ops in the same batch are unaffected.
type opFunc func(tx *sql.Tx) (any, error)

// writeOp couples an op with the channel used to return its result.
type writeOp struct {
	ctx context.Context
	fn  opFunc
	res chan opResult
}

type opResult struct {
	val any
	err error
}

// writer implements group commit (DEV_SPEC §3.2): all mutations funnel through a
// single goroutine that batches queued ops into one transaction and commits when
// the batch reaches batchMax or flush elapses, whichever comes first. The HTTP
// handler blocks on Submit until its op's batch has committed, giving durability.
type writer struct {
	db       *sql.DB
	queue    chan writeOp
	batchMax int
	flush    time.Duration

	done      chan struct{}
	wg        sync.WaitGroup
	closeOnce sync.Once
}

// newWriter starts the writer goroutine. db must be a single-connection pool so
// all batches serialise on one SQLite write connection.
func newWriter(db *sql.DB, batchMax int, flush time.Duration, queueMax int) *writer {
	w := &writer{
		db:       db,
		queue:    make(chan writeOp, queueMax),
		batchMax: batchMax,
		flush:    flush,
		done:     make(chan struct{}),
	}
	w.wg.Add(1)
	go w.run()
	return w
}

// Submit enqueues fn and blocks until its batch commits, returning fn's value or
// its error. If the queue is full it returns ErrBackpressure immediately without
// blocking (DEV_SPEC §6.2 503). It also respects ctx cancellation.
func (w *writer) Submit(ctx context.Context, fn opFunc) (any, error) {
	op := writeOp{ctx: ctx, fn: fn, res: make(chan opResult, 1)}
	select {
	case <-w.done:
		return nil, ErrWriterClosed
	default:
	}
	select {
	case w.queue <- op:
	case <-w.done:
		return nil, ErrWriterClosed
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
		// Queue full: shed load rather than block (backpressure -> 503).
		return nil, ErrBackpressure
	}
	select {
	case r := <-op.res:
		return r.val, r.err
	case <-ctx.Done():
		// The op may still run and commit; its result is discarded. Callers
		// treat a cancelled context as failure.
		return nil, ctx.Err()
	}
}

// run is the single writer goroutine. It gathers a batch then flushes it.
func (w *writer) run() {
	defer w.wg.Done()
	for {
		// Block for the first op (or shutdown).
		var first writeOp
		select {
		case <-w.done:
			w.drain()
			return
		case first = <-w.queue:
		}

		batch := []writeOp{first}
		timer := time.NewTimer(w.flush)
	gather:
		for len(batch) < w.batchMax {
			select {
			case op := <-w.queue:
				batch = append(batch, op)
			case <-timer.C:
				break gather
			case <-w.done:
				break gather
			}
		}
		timer.Stop()
		w.commitBatch(batch)
	}
}

// drain fails any ops still queued at shutdown.
func (w *writer) drain() {
	for {
		select {
		case op := <-w.queue:
			op.res <- opResult{err: ErrWriterClosed}
		default:
			return
		}
	}
}

// commitBatch runs every op in one transaction, each inside its own SAVEPOINT so
// that one op's failure rolls back only that op. The whole batch commits once.
func (w *writer) commitBatch(batch []writeOp) {
	ctx := context.Background()
	tx, err := w.db.BeginTx(ctx, nil)
	if err != nil {
		for _, op := range batch {
			op.res <- opResult{err: err}
		}
		return
	}

	results := make([]opResult, len(batch))
	for i, op := range batch {
		// Skip ops whose caller already gave up.
		if op.ctx != nil && op.ctx.Err() != nil {
			results[i] = opResult{err: op.ctx.Err()}
			continue
		}
		sp := savepointName(i)
		if _, err := tx.Exec("SAVEPOINT " + sp); err != nil {
			results[i] = opResult{err: err}
			continue
		}
		val, opErr := runOp(tx, op)
		if opErr != nil {
			// Roll back just this op; keep the batch alive.
			_, _ = tx.Exec("ROLLBACK TO SAVEPOINT " + sp)
			_, _ = tx.Exec("RELEASE SAVEPOINT " + sp)
			results[i] = opResult{err: opErr}
			continue
		}
		if _, err := tx.Exec("RELEASE SAVEPOINT " + sp); err != nil {
			results[i] = opResult{err: err}
			continue
		}
		results[i] = opResult{val: val}
	}

	if err := tx.Commit(); err != nil {
		// Commit failure invalidates every op's result.
		for _, op := range batch {
			op.res <- opResult{err: err}
		}
		return
	}
	for i, op := range batch {
		op.res <- results[i]
	}
}

// runOp invokes op.fn, converting a panic into an error so a single bad op never
// takes down the writer goroutine.
func runOp(tx *sql.Tx, op writeOp) (val any, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = errPanic(r)
		}
	}()
	return op.fn(tx)
}

// Stop shuts the writer down and waits for the goroutine to exit.
func (w *writer) Stop() {
	w.closeOnce.Do(func() { close(w.done) })
	w.wg.Wait()
}

// savepointName returns a fixed, SQL-safe savepoint identifier for batch index i.
func savepointName(i int) string {
	// Base26 lowercase letters keep the identifier alphanumeric and bounded.
	const letters = "abcdefghijklmnopqrstuvwxyz"
	if i < len(letters) {
		return "sp_" + string(letters[i])
	}
	return "sp_" + string(letters[i/len(letters)]) + string(letters[i%len(letters)])
}
