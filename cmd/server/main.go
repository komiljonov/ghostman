// Command server is the Ghostman sync server.
//
// Normal start-up loads configuration, opens the database pool, applies the
// embedded migrations and serves HTTP until interrupted. The -migrate flag
// turns the same binary into a migration tool so collaborators do not need the
// goose CLI installed:
//
//	go run ./cmd/server -migrate=up
//	go run ./cmd/server -migrate=down
//	go run ./cmd/server -migrate=status
//	go run ./cmd/server -migrate=create -name add_workspaces
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/komiljonov/ghostman/internal/api"
	"github.com/komiljonov/ghostman/internal/config"
	"github.com/komiljonov/ghostman/internal/db"
)

const (
	// shutdownTimeout is how long in-flight requests get to finish after a
	// signal.
	shutdownTimeout = 15 * time.Second

	// sessionCleanupInterval is how often expired sessions are swept.
	sessionCleanupInterval = time.Hour
)

func main() {
	migrateCmd := flag.String("migrate", "", "run a migration command instead of the server: up, down, status or create")
	migrationName := flag.String("name", "", "migration name, used with -migrate=create")
	flag.Parse()

	if err := run(*migrateCmd, *migrationName); err != nil {
		// The logger may not exist yet, so failures go to stderr directly.
		fmt.Fprintf(os.Stderr, "fatal: %v\n", err)
		os.Exit(1)
	}
}

func run(migrateCmd, migrationName string) error {
	// Creating a migration file needs neither configuration nor a database.
	if migrateCmd == "create" {
		if migrationName == "" {
			return errors.New("-migrate=create requires -name")
		}
		return db.CreateMigration(migrationName)
	}

	cfg, err := config.Load()
	if err != nil {
		return err
	}

	logger := newLogger(cfg)
	slog.SetDefault(logger)

	// Signals cancel this context, which unwinds both the migration commands
	// and the server.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	pool, err := db.NewPool(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()

	logger.InfoContext(ctx, "connected to database")

	switch migrateCmd {
	case "":
		// Normal start-up: migrate, then serve.
		if err := db.MigrateUp(ctx, pool, logger); err != nil {
			return err
		}

		queries := db.New(pool)

		// Expired-session housekeeping runs alongside the server. Its context
		// is cancelled once serve returns, whether that was a signal or a
		// startup failure, and the goroutine is waited for before the pool
		// closes.
		cleanupCtx, stopCleanup := context.WithCancel(ctx)
		cleanupDone := make(chan struct{})

		go func() {
			defer close(cleanupDone)
			db.RunSessionCleanup(cleanupCtx, queries, logger, sessionCleanupInterval)
		}()

		defer func() {
			stopCleanup()
			<-cleanupDone
		}()

		return serve(ctx, cfg, logger, pool, queries)

	case "up":
		return db.MigrateUp(ctx, pool, logger)

	case "down":
		return db.MigrateDown(ctx, pool, logger)

	case "status":
		return db.MigrateStatus(ctx, pool, logger)

	default:
		return fmt.Errorf("unknown -migrate value %q: want up, down, status or create", migrateCmd)
	}
}

// serve runs the HTTP server until ctx is cancelled, then drains connections.
func serve(ctx context.Context, cfg config.Config, logger *slog.Logger, pool *pgxpool.Pool, store api.Store) error {
	srv := api.NewServer(cfg, logger, pool, store)

	// ListenAndServe blocks, so it runs in its own goroutine and reports a
	// startup failure back through this channel.
	serverErr := make(chan error, 1)

	go func() {
		logger.InfoContext(ctx, "http server listening",
			slog.String("addr", srv.Addr),
			slog.String("env", cfg.Env),
		)

		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serverErr <- fmt.Errorf("http server: %w", err)
			return
		}
		serverErr <- nil
	}()

	select {
	case err := <-serverErr:
		return err

	case <-ctx.Done():
		logger.Info("shutdown signal received, draining connections",
			slog.Duration("timeout", shutdownTimeout),
		)

		// A fresh context: the signal context is already cancelled.
		shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()

		if err := srv.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("graceful shutdown: %w", err)
		}

		logger.Info("shutdown complete")
		return <-serverErr
	}
}

// newLogger returns a JSON logger in prod and a human-readable text logger in
// dev.
func newLogger(cfg config.Config) *slog.Logger {
	opts := &slog.HandlerOptions{Level: cfg.SlogLevel()}

	var handler slog.Handler
	if cfg.IsProd() {
		handler = slog.NewJSONHandler(os.Stdout, opts)
	} else {
		handler = slog.NewTextHandler(os.Stdout, opts)
	}

	return slog.New(handler).With(slog.String("service", "ghostman"))
}
