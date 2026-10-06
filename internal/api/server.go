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

// BuildInfo identifies the running binary. The values are stamped into
// package main at link time by `task build-linux`; a plain `go run` leaves
// the defaults.
type BuildInfo struct {
	Version   string
	BuildTime string
}

// api carries the dependencies shared by all handlers. The pool is kept
// separately from the store because /healthz pings the connection itself.
type api struct {
	logger *slog.Logger
	pool   *pgxpool.Pool
	store  Store
	authz  *authz.Checker
	build  BuildInfo
}

// NewServer builds a fully configured *http.Server. The caller owns its
// lifecycle (ListenAndServe and Shutdown).
func NewServer(cfg config.Config, logger *slog.Logger, pool *pgxpool.Pool, store Store, build BuildInfo) *http.Server {
	a := &api{
		logger: logger,
		pool:   pool,
		store:  store,
		authz:  authz.New(store),
		build:  build,
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
