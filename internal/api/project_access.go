package api

import (
	"context"
	"errors"
	"net/http"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/komiljonov/ghostman/internal/db"
)

// Access is configurable from two directions, and both write the same rows:
// per project ("who may open this?") and per member ("what may this person
// open?"). The project side is open to whoever manages the project; the member
// side, which spans the whole team, is the team owner's alone.

type projectAccessRequest struct {
	UserIDs []string `json:"user_ids"`
}

type memberAccessRequest struct {
	// A pointer so that omitting the field is an error rather than a silent
	// "false", which would quietly cut a member off from everything.
	AllProjects *bool    `json:"all_projects"`
	ProjectIDs  []string `json:"project_ids"`
}

type projectAccessResponse struct {
	UserIDs []string `json:"user_ids"`
}

type projectAccessUserResponse struct {
	UserID string `json:"user_id"`
	Email  string `json:"email"`
	Name   string `json:"name"`
}

type memberAccessResponse struct {
	UserID      string   `json:"user_id"`
	AllProjects bool     `json:"all_projects"`
	ProjectIDs  []string `json:"project_ids"`
}

// handleSetProjectAccess replaces the explicit grant list for one project.
func (a *api) handleSetProjectAccess(w http.ResponseWriter, r *http.Request) {
	user, ok := a.authenticatedUser(w, r)
	if !ok {
		return
	}

	projectID, ok := a.pathUUID(w, r, "id")
	if !ok {
		return
	}

	project, err := a.authz.RequireProjectManage(r.Context(), projectID, user.ID)
	if err != nil {
		a.writeAuthzError(w, r, err)
		return
	}

	var req projectAccessRequest
	if err = readJSON(w, r, &req); err != nil {
		a.writeError(w, r, http.StatusBadRequest, codeBadRequest, err.Error())
		return
	}

	userIDs, err := parseUUIDs(req.UserIDs, "user_ids")
	if err != nil {
		a.writeError(w, r, http.StatusBadRequest, codeBadRequest, err.Error())
		return
	}
	userIDs = dedupeUUIDs(userIDs)

	// Granting access to somebody outside the team would be meaningless: the
	// access rule requires team membership first.
	if err := a.requireAllTeamMembers(r.Context(), project.TeamID, userIDs); err != nil {
		a.writeError(w, r, http.StatusBadRequest, codeBadRequest, err.Error())
		return
	}

	if err := a.store.ReplaceProjectAccess(r.Context(), projectID, userIDs); err != nil {
		a.serverError(w, r, err)
		return
	}

	a.writeJSON(w, r, http.StatusOK, projectAccessResponse{UserIDs: uuidStrings(userIDs)})
}

// handleListProjectAccess returns the project's explicit grant list. It does
// not include people who reach the project some other way (all_projects, team
// ownership, project ownership).
func (a *api) handleListProjectAccess(w http.ResponseWriter, r *http.Request) {
	user, ok := a.authenticatedUser(w, r)
	if !ok {
		return
	}

	projectID, ok := a.pathUUID(w, r, "id")
	if !ok {
		return
	}

	if _, err := a.authz.RequireProjectManage(r.Context(), projectID, user.ID); err != nil {
		a.writeAuthzError(w, r, err)
		return
	}

	rows, err := a.store.ListProjectAccessUsers(r.Context(), projectID)
	if err != nil {
		a.serverError(w, r, err)
		return
	}

	users := make([]projectAccessUserResponse, 0, len(rows))
	for _, row := range rows {
		users = append(users, projectAccessUserResponse{
			UserID: row.UserID.String(),
			Email:  row.Email,
			Name:   row.Name,
		})
	}

	a.writeJSON(w, r, http.StatusOK, users)
}

// handleSetMemberAccess sets what one member of a team may open. Team owner
// only, since it spans every project in the team.
func (a *api) handleSetMemberAccess(w http.ResponseWriter, r *http.Request) {
	user, ok := a.authenticatedUser(w, r)
	if !ok {
		return
	}

	teamID, ok := a.pathUUID(w, r, "team_id")
	if !ok {
		return
	}

	targetID, ok := a.pathUUID(w, r, "user_id")
	if !ok {
		return
	}

	team, err := a.authz.RequireTeamOwner(r.Context(), teamID, user.ID)
	if err != nil {
		a.writeAuthzError(w, r, err)
		return
	}

	// The owner always reaches everything, so there is nothing here to set.
	if targetID == team.OwnerID {
		a.writeError(w, r, http.StatusBadRequest, codeBadRequest, messageOwnerAccessFixed)
		return
	}

	var req memberAccessRequest
	if err = readJSON(w, r, &req); err != nil {
		a.writeError(w, r, http.StatusBadRequest, codeBadRequest, err.Error())
		return
	}

	if req.AllProjects == nil {
		a.writeError(w, r, http.StatusBadRequest, codeBadRequest, "all_projects is required")
		return
	}

	projectIDs, err := parseUUIDs(req.ProjectIDs, "project_ids")
	if err != nil {
		a.writeError(w, r, http.StatusBadRequest, codeBadRequest, err.Error())
		return
	}
	projectIDs = dedupeUUIDs(projectIDs)

	if !*req.AllProjects {
		if err = a.requireAllTeamProjects(r.Context(), teamID, projectIDs); err != nil {
			a.writeError(w, r, http.StatusBadRequest, codeBadRequest, err.Error())
			return
		}
	}

	member, err := a.store.SetMemberProjectAccess(r.Context(), teamID, targetID, *req.AllProjects, projectIDs)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			a.writeError(w, r, http.StatusNotFound, codeNotFound, messageMemberNotFound)
			return
		}
		a.serverError(w, r, err)
		return
	}

	// Report what is stored rather than what was asked for: with all_projects
	// on, the explicit list is deliberately empty.
	granted := projectIDs
	if member.AllProjects {
		granted = nil
	}

	a.writeJSON(w, r, http.StatusOK, memberAccessResponse{
		UserID:      targetID.String(),
		AllProjects: member.AllProjects,
		ProjectIDs:  uuidStrings(granted),
	})
}

// requireAllTeamMembers fails unless every id belongs to a member of the team.
func (a *api) requireAllTeamMembers(ctx context.Context, teamID uuid.UUID, userIDs []uuid.UUID) error {
	if len(userIDs) == 0 {
		return nil
	}

	found, err := a.store.CountTeamMembersInList(ctx, db.CountTeamMembersInListParams{
		TeamID:  teamID,
		UserIds: userIDs,
	})
	if err != nil {
		return err
	}

	if int(found) != len(userIDs) {
		return errors.New("user_ids must all be members of this team")
	}

	return nil
}

// requireAllTeamProjects fails unless every id is a project of the team.
func (a *api) requireAllTeamProjects(ctx context.Context, teamID uuid.UUID, projectIDs []uuid.UUID) error {
	if len(projectIDs) == 0 {
		return nil
	}

	found, err := a.store.CountProjectsInTeam(ctx, db.CountProjectsInTeamParams{
		TeamID:     teamID,
		ProjectIds: projectIDs,
	})
	if err != nil {
		return err
	}

	if int(found) != len(projectIDs) {
		return errors.New("project_ids must all be projects of this team")
	}

	return nil
}

func dedupeUUIDs(ids []uuid.UUID) []uuid.UUID {
	seen := make(map[uuid.UUID]struct{}, len(ids))
	unique := make([]uuid.UUID, 0, len(ids))

	for _, id := range ids {
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		unique = append(unique, id)
	}

	return unique
}

// uuidStrings renders ids for JSON, always as a list so an empty result is []
// rather than null.
func uuidStrings(ids []uuid.UUID) []string {
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		out = append(out, id.String())
	}

	return out
}
