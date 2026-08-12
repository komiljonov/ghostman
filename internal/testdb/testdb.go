// Package testdb hands out throwaway PostgreSQL databases for tests that need
// real SQL behaviour: transactions, cascades and constraints.
package testdb

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/komiljonov/ghostman/internal/db"
)

// duplicateDatabase is the SQLSTATE for "database already exists".
const duplicateDatabase = "42P04"

// connectTimeout keeps the skip path fast when no server is listening.
const connectTimeout = 5 * time.Second

// New returns a pool for a database dedicated to suffix, with every migration
// applied and every table empty. Give each package its own suffix: `go test`
// runs packages in parallel, and they would otherwise truncate each other.
//
// The test is skipped when DATABASE_URL is unset or no server answers, so the
// suite still runs on a machine without Docker. `task test` loads .env, so
// `task db:up` is enough to make these tests run.
func New(t *testing.T, suffix string) *pgxpool.Pool {
	t.Helper()

	adminURL := envDatabaseURL(t)

	testURL, name, err := testDatabaseURL(adminURL, suffix)
	if err != nil {
		t.Fatalf("deriving test database url: %v", err)
	}

	ctx := t.Context()

	if err = ensureDatabase(ctx, adminURL, name); err != nil {
		t.Skipf("no test database available (%v); run `task db:up`", err)
	}

	pool, err := pgxpool.New(ctx, testURL)
	if err != nil {
		t.Fatalf("connecting to test database %s: %v", name, err)
	}
	t.Cleanup(pool.Close)

	// Migrations are embedded, so the test database is built by exactly the
	// same SQL the server runs.
	if err := db.MigrateUp(ctx, pool, discardLogger()); err != nil {
		t.Fatalf("migrating test database %s: %v", name, err)
	}

	if err := truncateAll(ctx, pool); err != nil {
		t.Fatalf("truncating test database %s: %v", name, err)
	}

	return pool
}

func envDatabaseURL(t *testing.T) string {
	t.Helper()

	// Read through the same variable the server uses; task test loads .env.
	value := strings.TrimSpace(os.Getenv("DATABASE_URL"))
	if value == "" {
		t.Skip("DATABASE_URL is not set; skipping database-backed test")
	}

	return value
}

// testDatabaseURL rewrites a connection string to point at "<database>_test_<suffix>".
func testDatabaseURL(databaseURL, suffix string) (string, string, error) {
	parsed, err := url.Parse(databaseURL)
	if err != nil {
		return "", "", fmt.Errorf("parsing DATABASE_URL: %w", err)
	}

	base := strings.TrimPrefix(parsed.Path, "/")
	if base == "" {
		return "", "", errors.New("DATABASE_URL has no database name")
	}

	name := fmt.Sprintf("%s_test_%s", base, suffix)
	parsed.Path = "/" + name

	return parsed.String(), name, nil
}

// ensureDatabase creates the test database if it does not exist yet, connecting
// through the database named in DATABASE_URL to issue the CREATE.
func ensureDatabase(ctx context.Context, adminURL, name string) error {
	ctx, cancel := context.WithTimeout(ctx, connectTimeout)
	defer cancel()

	conn, err := pgx.Connect(ctx, adminURL)
	if err != nil {
		return fmt.Errorf("connecting to %q: %w", "DATABASE_URL", err)
	}
	defer func() { _ = conn.Close(ctx) }()

	// CREATE DATABASE takes no parameters, and the name is derived from the
	// configured database rather than from user input.
	_, err = conn.Exec(ctx, `CREATE DATABASE `+pgx.Identifier{name}.Sanitize())
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == duplicateDatabase {
			return nil
		}
		return fmt.Errorf("creating database %s: %w", name, err)
	}

	return nil
}

// truncateAll empties every application table, leaving goose's bookkeeping
// alone so migrations are not re-run from scratch on each test.
func truncateAll(ctx context.Context, pool *pgxpool.Pool) error {
	rows, err := pool.Query(ctx, `
		SELECT tablename
		FROM pg_tables
		WHERE schemaname = 'public'
		  AND tablename <> 'goose_db_version'
	`)
	if err != nil {
		return fmt.Errorf("listing tables: %w", err)
	}

	var tables []string
	for rows.Next() {
		var table string
		if err := rows.Scan(&table); err != nil {
			return fmt.Errorf("scanning table name: %w", err)
		}
		tables = append(tables, pgx.Identifier{table}.Sanitize())
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("listing tables: %w", err)
	}

	if len(tables) == 0 {
		return nil
	}

	// Identifiers cannot be bound as parameters; these come from pg_tables and
	// are quoted by Sanitize, not built from user input.
	statement := "TRUNCATE " + strings.Join(tables, ", ") + " RESTART IDENTITY CASCADE"
	if _, err := pool.Exec(ctx, statement); err != nil {
		return fmt.Errorf("truncating tables: %w", err)
	}

	return nil
}

// discardLogger keeps goose's migration output out of the test log.
func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}
