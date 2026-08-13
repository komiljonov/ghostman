package api

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/komiljonov/ghostman/internal/authz"
	"github.com/komiljonov/ghostman/internal/db"
)

// Team name bounds, matching the teams_name_length CHECK constraint.
const (
	minTeamNameLength = 1
	maxTeamNameLength = 100
)

type teamRequest struct {
	Name string `json:"name"`
}

// teamResponse is the minimal shape, returned on create.
type teamResponse struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// teamSummaryResponse is one entry of the caller's team list.
type teamSummaryResponse struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	MemberCount int64  `json:"member_count"`
	IsOwner     bool   `json:"is_owner"`
}

// teamMemberResponse is one member inside a team's detail response.
type teamMemberResponse struct {
	UserID      string `json:"user_id"`
	Email       string `json:"email"`
	Name        string `json:"name"`
	AllProjects bool   `json:"all_projects"`
}

// teamDetailResponse is what GET and PATCH on a single team return.
type teamDetailResponse struct {
	ID        string               `json:"id"`
	Name      string               `json:"name"`
	CreatedAt time.Time            `json:"created_at"`
	IsOwner   bool                 `json:"is_owner"`
	Members   []teamMemberResponse `json:"members"`
}

// handleCreateTeam creates a team owned by the caller.
func (a *api) handleCreateTeam(w http.ResponseWriter, r *http.Request) {
	user, ok := a.authenticatedUser(w, r)
	if !ok {
		return
	}

	name, ok := a.readTeamName(w, r)
	if !ok {
		return
	}

	// The team row and the owner's membership row are written together.
	team, err := a.store.CreateTeamWithOwner(r.Context(), name, user.ID)
	if err != nil {
		a.serverError(w, r, err)
		return
	}

	a.writeJSON(w, r, http.StatusCreated, teamResponse{
		ID:   team.ID.String(),
		Name: team.Name,
	})
}

// handleListTeams returns the teams the caller belongs to.
func (a *api) handleListTeams(w http.ResponseWriter, r *http.Request) {
	user, ok := a.authenticatedUser(w, r)
	if !ok {
		return
	}

	rows, err := a.store.ListTeamsForUser(r.Context(), user.ID)
	if err != nil {
		a.serverError(w, r, err)
		return
	}

	// Built with make so an empty result serialises as [] rather than null.
	teams := make([]teamSummaryResponse, 0, len(rows))
	for _, row := range rows {
		teams = append(teams, teamSummaryResponse{
			ID:          row.Team.ID.String(),
			Name:        row.Team.Name,
			MemberCount: row.MemberCount,
			IsOwner:     row.Team.OwnerID == user.ID,
		})
	}

	a.writeJSON(w, r, http.StatusOK, teams)
}

// handleGetTeam returns one team with its members. Members only.
func (a *api) handleGetTeam(w http.ResponseWriter, r *http.Request) {
	user, ok := a.authenticatedUser(w, r)
	if !ok {
		return
	}

	teamID, ok := a.pathUUID(w, r, "id")
	if !ok {
		return
	}

	if _, err := a.authz.RequireTeamMember(r.Context(), teamID, user.ID); err != nil {
		a.writeAuthzError(w, r, err)
		return
	}

	team, err := a.store.GetTeamByID(r.Context(), teamID)
	if err != nil {
		a.serverError(w, r, err)
		return
	}

	a.writeTeamDetail(w, r, team, user.ID)
}

// handleUpdateTeam renames a team. Owner only.
func (a *api) handleUpdateTeam(w http.ResponseWriter, r *http.Request) {
	user, ok := a.authenticatedUser(w, r)
	if !ok {
		return
	}

	teamID, ok := a.pathUUID(w, r, "id")
	if !ok {
		return
	}

	name, ok := a.readTeamName(w, r)
	if !ok {
		return
	}

	if _, err := a.authz.RequireTeamOwner(r.Context(), teamID, user.ID); err != nil {
		a.writeAuthzError(w, r, err)
		return
	}

	team, err := a.store.UpdateTeamName(r.Context(), db.UpdateTeamNameParams{
		ID:   teamID,
		Name: name,
	})
	if err != nil {
		a.serverError(w, r, err)
		return
	}

	a.writeTeamDetail(w, r, team, user.ID)
}

// handleDeleteTeam deletes a team and, by cascade, its memberships. Owner only.
func (a *api) handleDeleteTeam(w http.ResponseWriter, r *http.Request) {
	user, ok := a.authenticatedUser(w, r)
	if !ok {
		return
	}

	teamID, ok := a.pathUUID(w, r, "id")
	if !ok {
		return
	}

	if _, err := a.authz.RequireTeamOwner(r.Context(), teamID, user.ID); err != nil {
		a.writeAuthzError(w, r, err)
		return
	}

	if err := a.store.DeleteTeam(r.Context(), teamID); err != nil {
		a.serverError(w, r, err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// writeTeamDetail loads the team's members and writes the detail response.
func (a *api) writeTeamDetail(w http.ResponseWriter, r *http.Request, team db.Team, userID uuid.UUID) {
	rows, err := a.store.ListTeamMembers(r.Context(), team.ID)
	if err != nil {
		a.serverError(w, r, err)
		return
	}

	members := make([]teamMemberResponse, 0, len(rows))
	for _, row := range rows {
		members = append(members, teamMemberResponse{
			UserID:      row.UserID.String(),
			Email:       row.Email,
			Name:        row.Name,
			AllProjects: row.AllProjects,
		})
	}

	a.writeJSON(w, r, http.StatusOK, teamDetailResponse{
		ID:        team.ID.String(),
		Name:      team.Name,
		CreatedAt: team.CreatedAt,
		IsOwner:   team.OwnerID == userID,
		Members:   members,
	})
}

// readTeamName decodes and validates a {name} body, writing the error response
// itself when the body is unusable.
func (a *api) readTeamName(w http.ResponseWriter, r *http.Request) (string, bool) {
	var req teamRequest
	if err := readJSON(w, r, &req); err != nil {
		a.writeError(w, r, http.StatusBadRequest, codeBadRequest, err.Error())
		return "", false
	}

	name := strings.TrimSpace(req.Name)

	// Counted in runes, matching PostgreSQL's length() in the CHECK constraint.
	length := utf8.RuneCountInString(name)
	if length < minTeamNameLength {
		a.writeError(w, r, http.StatusBadRequest, codeBadRequest, "name is required")
		return "", false
	}
	if length > maxTeamNameLength {
		a.writeError(w, r, http.StatusBadRequest, codeBadRequest,
			fmt.Sprintf("name must be at most %d characters", maxTeamNameLength))
		return "", false
	}

	return name, true
}

// authenticatedUser pulls the caller out of the request context. The routes
// using it are all wrapped in requireAuth, so a miss is a wiring bug.
func (a *api) authenticatedUser(w http.ResponseWriter, r *http.Request) (AuthUser, bool) {
	user, ok := UserFromContext(r.Context())
	if !ok {
		a.serverError(w, r, errors.New("handler reached without an authenticated user"))
		return AuthUser{}, false
	}

	return user, true
}

// pathUUID parses a {name} path wildcard, answering 400 rather than 500 when it
// is not a uuid.
func (a *api) pathUUID(w http.ResponseWriter, r *http.Request, name string) (uuid.UUID, bool) {
	id, err := uuid.Parse(r.PathValue(name))
	if err != nil {
		a.writeError(w, r, http.StatusBadRequest, codeBadRequest,
			fmt.Sprintf("%s must be a valid uuid", name))
		return uuid.Nil, false
	}

	return id, true
}

// writeAuthzError maps a permission failure onto the HTTP response. Not being a
// member is a 404 so that outsiders cannot probe for team existence.
func (a *api) writeAuthzError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, authz.ErrNotMember):
		a.writeError(w, r, http.StatusNotFound, codeNotFound, messageTeamNotFound)

	case errors.Is(err, authz.ErrNotOwner):
		a.writeError(w, r, http.StatusForbidden, codeForbidden, messageNotTeamOwner)

	case errors.Is(err, authz.ErrNoProjectAccess):
		a.writeError(w, r, http.StatusNotFound, codeNotFound, messageProjectNotFound)

	case errors.Is(err, authz.ErrNotProjectManager):
		a.writeError(w, r, http.StatusForbidden, codeForbidden, messageNotProjectOwner)

	default:
		a.serverError(w, r, err)
	}
}
