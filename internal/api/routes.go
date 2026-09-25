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

	mux.Handle("GET /api/v1/me/invitations", a.requireAuth(http.HandlerFunc(a.handleListMyInvitations)))

	mux.Handle("POST /api/v1/teams", a.requireAuth(http.HandlerFunc(a.handleCreateTeam)))
	mux.Handle("GET /api/v1/teams", a.requireAuth(http.HandlerFunc(a.handleListTeams)))
	mux.Handle("GET /api/v1/teams/{id}", a.requireAuth(http.HandlerFunc(a.handleGetTeam)))
	mux.Handle("PATCH /api/v1/teams/{id}", a.requireAuth(http.HandlerFunc(a.handleUpdateTeam)))
	mux.Handle("DELETE /api/v1/teams/{id}", a.requireAuth(http.HandlerFunc(a.handleDeleteTeam)))

	// Collections hang off their parent, one level deep; the individual
	// resources they contain live at a flat path.
	mux.Handle("POST /api/v1/teams/{team_id}/invitations", a.requireAuth(http.HandlerFunc(a.handleCreateInvitation)))
	mux.Handle("GET /api/v1/teams/{team_id}/invitations", a.requireAuth(http.HandlerFunc(a.handleListTeamInvitations)))
	mux.Handle("DELETE /api/v1/teams/{team_id}/members/{user_id}", a.requireAuth(http.HandlerFunc(a.handleDeleteTeamMember)))

	mux.Handle("POST /api/v1/invitations/{id}/accept", a.requireAuth(http.HandlerFunc(a.handleAcceptInvitation)))
	mux.Handle("POST /api/v1/invitations/{id}/reject", a.requireAuth(http.HandlerFunc(a.handleRejectInvitation)))
	mux.Handle("DELETE /api/v1/invitations/{id}", a.requireAuth(http.HandlerFunc(a.handleDeleteInvitation)))

	mux.Handle("POST /api/v1/teams/{team_id}/projects", a.requireAuth(http.HandlerFunc(a.handleCreateProject)))
	mux.Handle("GET /api/v1/teams/{team_id}/projects", a.requireAuth(http.HandlerFunc(a.handleListProjects)))
	mux.Handle("PUT /api/v1/teams/{team_id}/projects/order", a.requireAuth(http.HandlerFunc(a.handleReorderProjects)))

	mux.Handle("GET /api/v1/projects/{id}", a.requireAuth(http.HandlerFunc(a.handleGetProject)))
	mux.Handle("PATCH /api/v1/projects/{id}", a.requireAuth(http.HandlerFunc(a.handleUpdateProject)))
	mux.Handle("DELETE /api/v1/projects/{id}", a.requireAuth(http.HandlerFunc(a.handleDeleteProject)))
	mux.Handle("GET /api/v1/projects/{id}/access", a.requireAuth(http.HandlerFunc(a.handleListProjectAccess)))
	mux.Handle("PUT /api/v1/projects/{id}/access", a.requireAuth(http.HandlerFunc(a.handleSetProjectAccess)))

	mux.Handle("POST /api/v1/projects/{project_id}/folders", a.requireAuth(http.HandlerFunc(a.handleCreateFolder)))
	mux.Handle("GET /api/v1/projects/{project_id}/folders", a.requireAuth(http.HandlerFunc(a.handleListFolders)))

	// The order endpoint is scoped by its body: the siblings being ordered may
	// sit at the project root, and a null parent has no path form.
	mux.Handle("PUT /api/v1/folders/order", a.requireAuth(http.HandlerFunc(a.handleReorderFolders)))
	mux.Handle("PATCH /api/v1/folders/{id}", a.requireAuth(http.HandlerFunc(a.handleUpdateFolder)))
	mux.Handle("DELETE /api/v1/folders/{id}", a.requireAuth(http.HandlerFunc(a.handleDeleteFolder)))
	mux.Handle("POST /api/v1/folders/{id}/move", a.requireAuth(http.HandlerFunc(a.handleMoveFolder)))

	mux.Handle("POST /api/v1/projects/{project_id}/environments", a.requireAuth(http.HandlerFunc(a.handleCreateEnvironment)))
	mux.Handle("GET /api/v1/projects/{project_id}/environments", a.requireAuth(http.HandlerFunc(a.handleListEnvironments)))

	// Like folders/order, the reorder endpoints name their scope in the body.
	mux.Handle("PUT /api/v1/environments/order", a.requireAuth(http.HandlerFunc(a.handleReorderEnvironments)))
	mux.Handle("PATCH /api/v1/environments/{id}", a.requireAuth(http.HandlerFunc(a.handleUpdateEnvironment)))
	mux.Handle("DELETE /api/v1/environments/{id}", a.requireAuth(http.HandlerFunc(a.handleDeleteEnvironment)))

	mux.Handle("POST /api/v1/environments/{env_id}/variables", a.requireAuth(http.HandlerFunc(a.handleCreateVariable)))
	mux.Handle("GET /api/v1/environments/{env_id}/variables", a.requireAuth(http.HandlerFunc(a.handleListVariables)))

	mux.Handle("PUT /api/v1/variables/order", a.requireAuth(http.HandlerFunc(a.handleReorderVariables)))
	mux.Handle("PATCH /api/v1/variables/{id}", a.requireAuth(http.HandlerFunc(a.handleUpdateVariable)))
	mux.Handle("DELETE /api/v1/variables/{id}", a.requireAuth(http.HandlerFunc(a.handleDeleteVariable)))

	// Two levels deep, which the convention otherwise avoids: this configures
	// one member's access across a whole team, so it belongs to neither the
	// member nor any single project on its own.
	mux.Handle("PUT /api/v1/teams/{team_id}/members/{user_id}/access", a.requireAuth(http.HandlerFunc(a.handleSetMemberAccess)))

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
