package api

import (
	"context"
	"net/http"
	"time"
)

// healthPingTimeout bounds the database ping performed by /healthz.
const healthPingTimeout = 2 * time.Second

// routes builds the router. Patterns use the Go 1.22 method+path syntax of
// http.ServeMux, so no third-party router is needed.
func (a *api) routes() http.Handler {
	mux := http.NewServeMux()

	// Public.
	mux.HandleFunc("GET /healthz", a.handleHealthz)
	mux.HandleFunc("GET /api/v1/hello", a.handleHello)
	mux.HandleFunc("POST /api/v1/auth/register", a.handleRegister)
	mux.HandleFunc("POST /api/v1/auth/login", a.handleLogin)

	// Authenticated. Paths stay flat: no resource is nested under another.
	mux.Handle("POST /api/v1/auth/logout", a.requireAuth(http.HandlerFunc(a.handleLogout)))
	mux.Handle("GET /api/v1/me", a.requireAuth(http.HandlerFunc(a.handleMe)))

	mux.Handle("POST /api/v1/teams", a.requireAuth(http.HandlerFunc(a.handleCreateTeam)))
	mux.Handle("GET /api/v1/teams", a.requireAuth(http.HandlerFunc(a.handleListTeams)))
	mux.Handle("GET /api/v1/teams/{id}", a.requireAuth(http.HandlerFunc(a.handleGetTeam)))
	mux.Handle("PATCH /api/v1/teams/{id}", a.requireAuth(http.HandlerFunc(a.handleUpdateTeam)))
	mux.Handle("DELETE /api/v1/teams/{id}", a.requireAuth(http.HandlerFunc(a.handleDeleteTeam)))

	// Catch-all so unknown paths get the JSON error envelope instead of
	// ServeMux's plain-text 404.
	mux.HandleFunc("/", a.handleNotFound)

	return chain(mux,
		a.recoverPanics,
		a.withCORS,
		a.logRequests,
	)
}

// handleHealthz reports server liveness and database reachability.
func (a *api) handleHealthz(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), healthPingTimeout)
	defer cancel()

	if err := a.pool.Ping(ctx); err != nil {
		a.logger.ErrorContext(r.Context(), "health check: database ping failed", errorAttr(err))
		a.writeError(w, r, http.StatusServiceUnavailable, codeUnavailable, messageDBUnready)
		return
	}

	a.writeJSON(w, r, http.StatusOK, map[string]string{"status": "ok"})
}

// handleHello is the placeholder API endpoint used to verify routing end to end.
func (a *api) handleHello(w http.ResponseWriter, r *http.Request) {
	a.writeJSON(w, r, http.StatusOK, map[string]string{"message": "hello from ghostman"})
}

func (a *api) handleNotFound(w http.ResponseWriter, r *http.Request) {
	a.writeError(w, r, http.StatusNotFound, codeNotFound, "the requested resource was not found")
}
