package api

import (
	"fmt"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/komiljonov/ghostman/internal/db"
)

// Project name bounds, matching the projects_name_length CHECK constraint.
const (
	minProjectNameLength = 1
	maxProjectNameLength = 100
)

type projectRequest struct {
	Name string `json:"name"`
}

type projectOrderRequest struct {
	ProjectIDs []string `json:"project_ids"`
}

// projectResponse is the full shape, used wherever a single project is the
// subject of the request.
type projectResponse struct {
	ID        string    `json:"id"`
	TeamID    string    `json:"team_id"`
	Name      string    `json:"name"`
	OwnerID   string    `json:"owner_id"`
	SortOrder int32     `json:"sort_order"`
	CreatedAt time.Time `json:"created_at"`
}

// projectSummaryResponse is one entry of a team's project list. The team is
// implied by the request path, so it is not repeated.
type projectSummaryResponse struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	OwnerID   string    `json:"owner_id"`
	SortOrder int32     `json:"sort_order"`
	CreatedAt time.Time `json:"created_at"`
}

func newProjectResponse(project db.Project) projectResponse {
	return projectResponse{
		ID:        project.ID.String(),
		TeamID:    project.TeamID.String(),
		Name:      project.Name,
		OwnerID:   project.OwnerID.String(),
		SortOrder: project.SortOrder,
		CreatedAt: project.CreatedAt,
	}
}

// handleCreateProject creates a project inside a team. Any member may do this,
// and becomes the project's owner.
func (a *api) handleCreateProject(w http.ResponseWriter, r *http.Request) {
	user, ok := a.authenticatedUser(w, r)
	if !ok {
		return
	}

	teamID, ok := a.pathUUID(w, r, "team_id")
	if !ok {
		return
	}

	if _, err := a.authz.RequireTeamMember(r.Context(), teamID, user.ID); err != nil {
		a.writeAuthzError(w, r, err)
		return
	}

	name, ok := a.readProjectName(w, r)
	if !ok {
		return
	}

	project, err := a.store.CreateProject(r.Context(), db.CreateProjectParams{
		TeamID:  teamID,
		Name:    name,
		OwnerID: user.ID,
	})
	if err != nil {
		a.serverError(w, r, err)
		return
	}

	a.writeJSON(w, r, http.StatusCreated, newProjectResponse(project))
}

// handleListProjects lists the projects of a team that the caller can actually
// reach, which is usually a subset of the team's projects.
func (a *api) handleListProjects(w http.ResponseWriter, r *http.Request) {
	user, ok := a.authenticatedUser(w, r)
	if !ok {
		return
	}

	teamID, ok := a.pathUUID(w, r, "team_id")
	if !ok {
		return
	}

	if _, err := a.authz.RequireTeamMember(r.Context(), teamID, user.ID); err != nil {
		a.writeAuthzError(w, r, err)
		return
	}

	rows, err := a.store.ListAccessibleProjects(r.Context(), db.ListAccessibleProjectsParams{
		TeamID: teamID,
		UserID: user.ID,
	})
	if err != nil {
		a.serverError(w, r, err)
		return
	}

	projects := make([]projectSummaryResponse, 0, len(rows))
	for _, row := range rows {
		projects = append(projects, projectSummaryResponse{
			ID:        row.Project.ID.String(),
			Name:      row.Project.Name,
			OwnerID:   row.Project.OwnerID.String(),
			SortOrder: row.Project.SortOrder,
			CreatedAt: row.Project.CreatedAt,
		})
	}

	a.writeJSON(w, r, http.StatusOK, projects)
}

// handleGetProject returns one project the caller can reach.
func (a *api) handleGetProject(w http.ResponseWriter, r *http.Request) {
	user, ok := a.authenticatedUser(w, r)
	if !ok {
		return
	}

	projectID, ok := a.pathUUID(w, r, "id")
	if !ok {
		return
	}

	access, err := a.authz.RequireProjectAccess(r.Context(), projectID, user.ID)
	if err != nil {
		a.writeAuthzError(w, r, err)
		return
	}

	a.writeJSON(w, r, http.StatusOK, newProjectResponse(access.Project))
}

// handleUpdateProject renames a project. Team owner or project owner only.
func (a *api) handleUpdateProject(w http.ResponseWriter, r *http.Request) {
	user, ok := a.authenticatedUser(w, r)
	if !ok {
		return
	}

	projectID, ok := a.pathUUID(w, r, "id")
	if !ok {
		return
	}

	name, ok := a.readProjectName(w, r)
	if !ok {
		return
	}

	if _, err := a.authz.RequireProjectManage(r.Context(), projectID, user.ID); err != nil {
		a.writeAuthzError(w, r, err)
		return
	}

	project, err := a.store.UpdateProjectName(r.Context(), db.UpdateProjectNameParams{
		ID:   projectID,
		Name: name,
	})
	if err != nil {
		a.serverError(w, r, err)
		return
	}

	a.writeJSON(w, r, http.StatusOK, newProjectResponse(project))
}

// handleDeleteProject deletes a project and its access rows. Team owner or
// project owner only.
func (a *api) handleDeleteProject(w http.ResponseWriter, r *http.Request) {
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

	if err := a.store.DeleteProject(r.Context(), projectID); err != nil {
		a.serverError(w, r, err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// handleReorderProjects rewrites the team's project order. The order is shared
// by the whole team, so any member may set it, and the request must therefore
// account for every project in the team — including ones the caller cannot
// open.
func (a *api) handleReorderProjects(w http.ResponseWriter, r *http.Request) {
	user, ok := a.authenticatedUser(w, r)
	if !ok {
		return
	}

	teamID, ok := a.pathUUID(w, r, "team_id")
	if !ok {
		return
	}

	if _, err := a.authz.RequireTeamMember(r.Context(), teamID, user.ID); err != nil {
		a.writeAuthzError(w, r, err)
		return
	}

	var req projectOrderRequest
	if err := readJSON(w, r, &req); err != nil {
		a.writeBodyError(w, r, err)
		return
	}

	projectIDs, err := parseUUIDs(req.ProjectIDs, "project_ids")
	if err != nil {
		a.writeError(w, r, http.StatusBadRequest, codeBadRequest, err.Error())
		return
	}

	teamProjectIDs, err := a.store.ListTeamProjectIDs(r.Context(), teamID)
	if err != nil {
		a.serverError(w, r, err)
		return
	}

	if err := checkSameIDSet(projectIDs, teamProjectIDs,
		"project_ids", "a project of this team", "projects of this team"); err != nil {
		a.writeError(w, r, http.StatusBadRequest, codeBadRequest, err.Error())
		return
	}

	if err := a.store.BulkUpdateProjectOrder(r.Context(), db.BulkUpdateProjectOrderParams{
		TeamID:     teamID,
		ProjectIds: projectIDs,
	}); err != nil {
		a.serverError(w, r, err)
		return
	}

	// No body: the caller may not be allowed to see every project it just
	// ordered, so echoing the list back would leak names.
	w.WriteHeader(http.StatusNoContent)
}

// readProjectName decodes and validates a {name} body.
func (a *api) readProjectName(w http.ResponseWriter, r *http.Request) (string, bool) {
	var req projectRequest
	if err := readJSON(w, r, &req); err != nil {
		a.writeBodyError(w, r, err)
		return "", false
	}

	name, err := validateName(req.Name, minProjectNameLength, maxProjectNameLength)
	if err != nil {
		a.writeError(w, r, http.StatusBadRequest, codeBadRequest, err.Error())
		return "", false
	}

	return name, true
}

// validateName trims a project, folder or environment name and checks its
// length against the table's CHECK constraint.
func validateName(raw string, minLength, maxLength int) (string, error) {
	return validateTrimmedLength("name", raw, minLength, maxLength)
}

// validateTrimmedLength trims a text field and checks its length, naming the
// field in any error.
func validateTrimmedLength(field, raw string, minLength, maxLength int) (string, error) {
	value := strings.TrimSpace(raw)

	// Counted in runes, matching PostgreSQL's length() in the CHECK constraint.
	length := utf8.RuneCountInString(value)
	if length < minLength {
		return "", fmt.Errorf("%s is required", field)
	}
	if length > maxLength {
		return "", fmt.Errorf("%s must be at most %d characters", field, maxLength)
	}

	return value, nil
}

// parseUUIDs converts a JSON list of ids, naming the field in any error so the
// client can tell which list was wrong.
func parseUUIDs(raw []string, field string) ([]uuid.UUID, error) {
	ids := make([]uuid.UUID, 0, len(raw))

	for _, value := range raw {
		id, err := uuid.Parse(value)
		if err != nil {
			return nil, fmt.Errorf("%s contains %q, which is not a valid uuid", field, value)
		}
		ids = append(ids, id)
	}

	return ids, nil
}

// checkSameIDSet reports whether got is exactly want, with no duplicates and
// nothing missing or extra. field names the request list; one and all describe
// a single member of want and the whole of it, for the error messages.
func checkSameIDSet(got, want []uuid.UUID, field, one, all string) error {
	expected := make(map[uuid.UUID]struct{}, len(want))
	for _, id := range want {
		expected[id] = struct{}{}
	}

	seen := make(map[uuid.UUID]struct{}, len(got))
	for _, id := range got {
		if _, duplicate := seen[id]; duplicate {
			return fmt.Errorf("%s lists %s more than once", field, id)
		}
		seen[id] = struct{}{}

		if _, ok := expected[id]; !ok {
			return fmt.Errorf("%s contains %s, which is not %s", field, id, one)
		}
	}

	if len(seen) != len(expected) {
		return fmt.Errorf("%s must list all %d %s, got %d",
			field, len(expected), all, len(seen))
	}

	return nil
}
