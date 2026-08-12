package db

import (
	"context"
	"errors"
	"log/slog"
	"time"
)

// RunSessionCleanup deletes expired sessions every interval until ctx is
// cancelled. It is meant to be run in its own goroutine for the lifetime of the
// server.
//
// Expired sessions are already unusable (the session lookup filters on
// expires_at), so this is housekeeping rather than a security control, and a
// failed sweep is logged and retried on the next tick.
func RunSessionCleanup(ctx context.Context, queries *Queries, logger *slog.Logger, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	logger.InfoContext(ctx, "session cleanup started", slog.Duration("interval", interval))

	for {
		select {
		case <-ctx.Done():
			logger.InfoContext(ctx, "session cleanup stopped")
			return

		case <-ticker.C:
			deleteExpiredSessionsOnce(ctx, queries, logger)
		}
	}
}

func deleteExpiredSessionsOnce(ctx context.Context, queries *Queries, logger *slog.Logger) {
	deleted, err := queries.DeleteExpiredSessions(ctx)
	if err != nil {
		// A sweep cancelled by shutdown is expected, not a failure.
		if errors.Is(err, context.Canceled) {
			return
		}
		logger.ErrorContext(ctx, "deleting expired sessions", slog.Any("error", err))
		return
	}

	if deleted > 0 {
		logger.InfoContext(ctx, "deleted expired sessions", slog.Int64("count", deleted))
	}
}
