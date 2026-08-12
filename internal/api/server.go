// Package api holds the HTTP layer: server construction, routing, middleware
// and the JSON request/response helpers.
package api

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/komiljonov/ghostman/internal/authz"
	"github.com/komiljonov/ghostman/internal/config"
)

// Server timeouts. ReadHeaderTimeout in particular guards against slowloris.
const (
	readHeaderTimeout = 5 * time.Second
	readTimeout       = 15 * time.Second
	writeTimeout      = 30 * time.Second
	idleTimeout       = 2 * time.Minute
	maxHeaderBytes    = 1 << 20 // 1 MiB
)

// api carries the dependencies shared by all handlers. The pool is kept
// separately from the store because /healthz pings the connection itself.
type api struct {
	logger *slog.Logger
	pool   *pgxpool.Pool
	store  Store
	authz  *authz.Checker
}

// NewServer builds a fully configured *http.Server. The caller owns its
// lifecycle (ListenAndServe and Shutdown).
func NewServer(cfg config.Config, logger *slog.Logger, pool *pgxpool.Pool, store Store) *http.Server {
	a := &api{
		logger: logger,
		pool:   pool,
		store:  store,
		authz:  authz.New(store),
	}

	return &http.Server{
		Addr:              cfg.Addr(),
		Handler:           a.routes(),
		ReadHeaderTimeout: readHeaderTimeout,
		ReadTimeout:       readTimeout,
		WriteTimeout:      writeTimeout,
		IdleTimeout:       idleTimeout,
		MaxHeaderBytes:    maxHeaderBytes,
		// Route net/http's own error output (bad TLS handshakes, malformed
		// requests) into slog instead of the standard logger.
		ErrorLog: slog.NewLogLogger(logger.Handler(), slog.LevelWarn),
	}
}

// errorAttr keeps error logging consistent across handlers.
func errorAttr(err error) slog.Attr {
	return slog.Any("error", err)
}
