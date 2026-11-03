// Command bot-bridge-server is the bot-bridge HTTP server (DEV_SPEC §3, §6).
//
// M0 scope: configuration loading/validation, structured logging, and the
// operational endpoints /healthz and /readyz. Missing required secrets cause a
// clear startup failure (non-zero exit) rather than serving in a broken state.
package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/hackerick4/botobot/internal/clock"
	"github.com/hackerick4/botobot/internal/config"
	"github.com/hackerick4/botobot/internal/server"
	"github.com/hackerick4/botobot/internal/store"
	"github.com/hackerick4/botobot/internal/sweeper"
)

// version is the server version, overridable at build time via
// -ldflags "-X main.version=<v>" (DEV_SPEC §12.4). It is logged at startup so
// operators can confirm which build is running.
var version = "v0.6.0"

func main() {
	if err := run(); err != nil {
		// Startup failures (e.g. missing secrets) print a clear, actionable
		// message to stderr. No secrets are ever included in these messages.
		fmt.Fprintf(os.Stderr, "bot-bridge-server: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	logger := server.NewLogger(cfg.LogLevel, cfg.LogFormat, os.Stderr)
	clk := clock.Real{}

	// Open the database and run migrations at startup (DEV_SPEC §18). The store
	// backs /readyz via a DB ping.
	st, err := store.Open(context.Background(), store.Options{
		Path:                 filepath.Join(cfg.DataDir, "bridge.db"),
		BlobDir:              filepath.Join(cfg.DataDir, "blobs"),
		Clock:                clk,
		BatchMax:             cfg.WriterBatchMax,
		Flush:                cfg.WriterFlush,
		QueueMax:             cfg.WriterQueueMax,
		TTL:                  cfg.InboxTTL,
		Lease:                cfg.Lease,
		MaxInboxItemsPerLand: cfg.MaxInboxItemsPerLand,
	})
	if err != nil {
		return err
	}
	defer func() { _ = st.Close() }()

	srv := server.New(cfg, logger, clk, server.WithStore(st), server.WithReady(st.Ping))

	// Sweeper goroutine: TTL expiry, refcount/blob cleanup, delivery-status and
	// tombstone GC, and the 7-day admin-inactivity close (DEV_SPEC §3.2, §11).
	sw := sweeper.New(sweeper.Options{
		Store:           st,
		Logger:          logger,
		Interval:        cfg.SweepInterval,
		AdminInactivity: cfg.AdminInactivity,
		OnBridgeClosed:  func(string) { srv.ClearKeyCache() },
	})
	sweepCtx, sweepCancel := context.WithCancel(context.Background())
	defer sweepCancel() // backstop; shutdown cancels explicitly and waits below
	sweepDone := make(chan struct{})
	go func() {
		defer close(sweepDone)
		sw.Run(sweepCtx)
	}()

	// HTTP server with the DEV_SPEC §16 hardening timeouts and header cap. The
	// *http.Server is constructed inside the server package so the timeouts are
	// unit-tested (TestHTTPServerTimeouts).
	httpSrv := srv.HTTPServer()

	// Graceful shutdown on SIGINT/SIGTERM (DEV_SPEC §18).
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	errCh := make(chan error, 1)
	go func() {
		logger.Info("server starting",
			"version", version,
			"listen_addr", cfg.ListenAddr,
			"data_dir", cfg.DataDir,
			"log_format", cfg.LogFormat,
		)
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		return fmt.Errorf("listen: %w", err)
	case <-ctx.Done():
		logger.Info("shutdown signal received")
	}

	// Graceful shutdown (DEV_SPEC §18): stop the sweeper, drain in-flight HTTP
	// requests (which commit their writes through the writer), then the deferred
	// st.Close flushes and stops the writer — all within a 30s budget.
	sweepCancel()
	<-sweepDone

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := httpSrv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("graceful shutdown: %w", err)
	}
	logger.Info("server stopped")
	return nil
}
