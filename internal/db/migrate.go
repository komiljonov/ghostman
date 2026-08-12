package db

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
)

// migrationsFS embeds every migration into the binary so deployments and
// collaborators never need the goose CLI installed.
//
//go:embed migrations/*.sql
var migrationsFS embed.FS

// MigrationsDir is the on-disk location of the migration files, used by
// CreateMigration. Inside the binary the same files live under this path in
// migrationsFS.
const MigrationsDir = "internal/db/migrations"

// embedDir is the path of the migrations inside migrationsFS.
const embedDir = "migrations"

// MigrateUp applies all pending migrations using the embedded files.
func MigrateUp(ctx context.Context, pool *pgxpool.Pool, logger *slog.Logger) error {
	return withGoose(ctx, pool, logger, func(sqlDB *sql.DB) error {
		return goose.UpContext(ctx, sqlDB, embedDir)
	})
}

// MigrateDown rolls back the most recently applied migration.
func MigrateDown(ctx context.Context, pool *pgxpool.Pool, logger *slog.Logger) error {
	return withGoose(ctx, pool, logger, func(sqlDB *sql.DB) error {
		return goose.DownContext(ctx, sqlDB, embedDir)
	})
}

// MigrateStatus prints the applied/pending state of every migration.
func MigrateStatus(ctx context.Context, pool *pgxpool.Pool, logger *slog.Logger) error {
	return withGoose(ctx, pool, logger, func(sqlDB *sql.DB) error {
		return goose.StatusContext(ctx, sqlDB, embedDir)
	})
}

// CreateMigration writes a new timestamped, empty SQL migration to
// MigrationsDir. It touches the filesystem rather than the embedded FS and so
// must run from the repository root.
func CreateMigration(name string) error {
	// Deliberately not using the embedded FS: this writes a real file.
	goose.SetBaseFS(nil)

	if err := goose.SetDialect("postgres"); err != nil {
		return fmt.Errorf("set goose dialect: %w", err)
	}

	if err := goose.Create(nil, MigrationsDir, name, "sql"); err != nil {
		return fmt.Errorf("create migration: %w", err)
	}

	return nil
}

// withGoose adapts the pgxpool to the *sql.DB that goose requires and runs fn
// with the embedded migration files registered.
func withGoose(ctx context.Context, pool *pgxpool.Pool, logger *slog.Logger, fn func(*sql.DB) error) error {
	goose.SetBaseFS(migrationsFS)
	goose.SetLogger(slogGooseLogger{ctx: ctx, logger: logger})

	if err := goose.SetDialect("postgres"); err != nil {
		return fmt.Errorf("set goose dialect: %w", err)
	}

	// OpenDBFromPool borrows connections from the existing pool; closing this
	// *sql.DB does not close the pool.
	sqlDB := stdlib.OpenDBFromPool(pool)
	defer func() {
		if err := sqlDB.Close(); err != nil {
			logger.WarnContext(ctx, "closing migration db handle", slog.Any("error", err))
		}
	}()

	if err := fn(sqlDB); err != nil {
		return fmt.Errorf("run migrations: %w", err)
	}

	return nil
}

// slogGooseLogger routes goose's printf-style output into slog so migration
// output matches the rest of the server's logs.
type slogGooseLogger struct {
	ctx    context.Context
	logger *slog.Logger
}

func (l slogGooseLogger) Printf(format string, v ...any) {
	l.logger.InfoContext(l.ctx, "goose: "+trimNewline(fmt.Sprintf(format, v...)))
}

func (l slogGooseLogger) Fatalf(format string, v ...any) {
	// goose calls Fatalf for errors it also returns; log instead of exiting so
	// the caller keeps control of shutdown.
	l.logger.ErrorContext(l.ctx, "goose: "+trimNewline(fmt.Sprintf(format, v...)))
}

func trimNewline(s string) string {
	for len(s) > 0 && (s[len(s)-1] == '\n' || s[len(s)-1] == '\r') {
		s = s[:len(s)-1]
	}
	return s
}
