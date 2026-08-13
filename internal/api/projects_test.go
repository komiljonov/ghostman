package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"
)

// createProject creates a project as the given user and returns it.
func (f teamsFixture) createProject(t *testing.T, token, teamID, name string) projectResponse {
	t.Helper()

	body, err := json.Marshal(projectRequest{Name: name})
	if err != nil {
		t.Fatalf("marshalling project request: %v", err)
	}

	rec := do(t, f.handler, http.MethodPost, "/api/v1/teams/"+teamID+"/projects", string(body), token)
	if rec.Code != http.StatusCreated {
		t.Fatalf("creating project %q: status = %d, want %d (body: %s)", name, rec.Code, http.StatusCreated, rec.Body)
	}

	var project projectResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &project); err != nil {
		t.Fatalf("decoding project response: %v", err)
	}

	return project
}

func (f teamsFixture) listProjects(t *testing.T, token, teamID string) []projectSummaryResponse {
	t.Helper()

	rec := do(t, f.handler, http.MethodGet, "/api/v1/teams/"+teamID+"/projects", "", token)
	if rec.Code != http.StatusOK {
		t.Fatalf("listing projects: status = %d, want %d (body: %s)", rec.Code, http.StatusOK, rec.Body)
	}

	var projects []projectSummaryResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &projects); err != nil {
		t.Fatalf("decoding project list: %v", err)
	}

	return projects
}

func (f teamsFixture) projectNames(t *testing.T, token, teamID string) []string {
	t.Helper()

	projects := f.listProjects(t, token, teamID)

	names := make([]string, 0, len(projects))
	for _, project := range projects {
		names = append(names, project.Name)
	}

	return names
}

// setMemberAccess drives the team-owner-side access endpoint.
func (f teamsFixture) setMemberAccess(t *testing.T, token, teamID, userID string, allProjects bool, projectIDs []string) int {
	t.Helper()

	body, err := json.Marshal(memberAccessRequest{AllProjects: &allProjects, ProjectIDs: projectIDs})
	if err != nil {
		t.Fatalf("marshalling member access request: %v", err)
	}

	rec := do(t, f.handler, http.MethodPut,
		"/api/v1/teams/"+teamID+"/members/"+userID+"/access", string(body), token)

	return rec.Code
}

// mustSetMemberAccess fails unless the access change is applied.
func (f teamsFixture) mustSetMemberAccess(t *testing.T, token, teamID, userID string, allProjects bool, projectIDs []string) {
	t.Helper()

	if status := f.setMemberAccess(t, token, teamID, userID, allProjects, projectIDs); status != http.StatusOK {
		t.Fatalf("setting member access: status = %d, want %d", status, http.StatusOK)
	}
}

// setProjectAccess drives the project-side access endpoint.
func (f teamsFixture) setProjectAccess(t *testing.T, token, projectID string, userIDs []string) int {
	t.Helper()

	body, err := json.Marshal(projectAccessRequest{UserIDs: userIDs})
	if err != nil {
		t.Fatalf("marshalling project access request: %v", err)
	}

	rec := do(t, f.handler, http.MethodPut, "/api/v1/projects/"+projectID+"/access", string(body), token)

	return rec.Code
}

// accessMatrixFixture is the shared setup for the access rule tests: one team
// with an owner, a member who sees everything, a member who sees nothing by
// default, and an outsider.
type accessMatrixFixture struct {
	teamsFixture

	teamID string

	owner      authResponse // team owner
	openMember authResponse // all_projects = true
	limited    authResponse // all_projects = false
	outsider   authResponse

	ownerProject   projectResponse // owned by the team owner
	limitedProject projectResponse // owned by the limited member
	grantedProject projectResponse // limited member holds an explicit grant
}

func newAccessMatrixFixture(t *testing.T) accessMatrixFixture {
	t.Helper()

	f := newTeamsFixture(t)

	owner := f.newUser(t, "matrix-owner@example.com")
	openMember := f.newUser(t, "matrix-open@example.com")
	limited := f.newUser(t, "matrix-limited@example.com")
	outsider := f.newUser(t, "matrix-outsider@example.com")

	team := f.createTeam(t, owner.Token, "Ghostman")
	f.addMember(t, uuid.MustParse(team.ID), uuid.MustParse(openMember.User.ID))
	f.addMember(t, uuid.MustParse(team.ID), uuid.MustParse(limited.User.ID))

	ownerProject := f.createProject(t, owner.Token, team.ID, "Owner Project")
	grantedProject := f.createProject(t, owner.Token, team.ID, "Granted Project")
	limitedProject := f.createProject(t, limited.Token, team.ID, "Limited Project")

	// The limited member keeps only an explicit grant on one project, plus
	// whatever the rule gives them for free.
	f.mustSetMemberAccess(t, owner.Token, team.ID, limited.User.ID, false, []string{grantedProject.ID})

	return accessMatrixFixture{
		teamsFixture:   f,
		teamID:         team.ID,
		owner:          owner,
		openMember:     openMember,
		limited:        limited,
		outsider:       outsider,
		ownerProject:   ownerProject,
		limitedProject: limitedProject,
		grantedProject: grantedProject,
	}
}

func TestProjectAccessMatrix(t *testing.T) {
	f := newAccessMatrixFixture(t)

	tests := []struct {
		name       string
		token      string
		projectID  string
		wantStatus int
	}{
		{
			name:      "outsider sees nothing",
			token:     f.outsider.Token,
			projectID: f.ownerProject.ID,
			// Not a member of the team at all.
			wantStatus: http.StatusNotFound,
		},
		{
			name:       "member with all_projects",
			token:      f.openMember.Token,
			projectID:  f.ownerProject.ID,
			wantStatus: http.StatusOK,
		},
		{
			name:      "restricted member without a grant",
			token:     f.limited.Token,
			projectID: f.ownerProject.ID,
			// In the team, but this project is not on their list.
			wantStatus: http.StatusNotFound,
		},
		{
			name:       "restricted member with a grant",
			token:      f.limited.Token,
			projectID:  f.grantedProject.ID,
			wantStatus: http.StatusOK,
		},
		{
			name:       "team owner reaches a member's project",
			token:      f.owner.Token,
			projectID:  f.limitedProject.ID,
			wantStatus: http.StatusOK,
		},
		{
			name:      "project owner reaches their own project",
			token:     f.limited.Token,
			projectID: f.limitedProject.ID,
			// Restricted and holds no grant for it, but owns it.
			wantStatus: http.StatusOK,
		},
		{
			name:       "unknown project",
			token:      f.owner.Token,
			projectID:  uuid.New().String(),
			wantStatus: http.StatusNotFound,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := do(t, f.handler, http.MethodGet, "/api/v1/projects/"+tt.projectID, "", tt.token)
			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d (body: %s)", rec.Code, tt.wantStatus, rec.Body)
			}

			if tt.wantStatus == http.StatusNotFound {
				if env := decodeEnvelope(t, rec); env.Error.Message != messageProjectNotFound {
					t.Errorf("message = %q, want %q", env.Error.Message, messageProjectNotFound)
				}
			}
		})
	}
}

func TestListProjectsFiltersPerUser(t *testing.T) {
	f := newAccessMatrixFixture(t)

	tests := []struct {
		name      string
		token     string
		wantNames []string
	}{
		{
			name:      "team owner sees everything",
			token:     f.owner.Token,
			wantNames: []string{"Owner Project", "Granted Project", "Limited Project"},
		},
		{
			name:      "open member sees everything",
			token:     f.openMember.Token,
			wantNames: []string{"Owner Project", "Granted Project", "Limited Project"},
		},
		{
			name:  "restricted member sees only their grant and their own project",
			token: f.limited.Token,
			// No "Owner Project": not granted, not theirs.
			wantNames: []string{"Granted Project", "Limited Project"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := f.projectNames(t, tt.token, f.teamID)
			if strings.Join(got, ",") != strings.Join(tt.wantNames, ",") {
				t.Errorf("projects = %v, want %v", got, tt.wantNames)
			}
		})
	}

	t.Run("outsider cannot list at all", func(t *testing.T) {
		rec := do(t, f.handler, http.MethodGet, "/api/v1/teams/"+f.teamID+"/projects", "", f.outsider.Token)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want %d (body: %s)", rec.Code, http.StatusNotFound, rec.Body)
		}
	})
}

func TestAnyMemberCanCreateProject(t *testing.T) {
	f := newAccessMatrixFixture(t)

	project := f.createProject(t, f.limited.Token, f.teamID, "Member Made")

	if project.OwnerID != f.limited.User.ID {
		t.Errorf("owner_id = %s, want %s", project.OwnerID, f.limited.User.ID)
	}
	if project.TeamID != f.teamID {
		t.Errorf("team_id = %s, want %s", project.TeamID, f.teamID)
	}

	// sort_order counts up within the team.
	if project.SortOrder != 3 {
		t.Errorf("sort_order = %d, want 3", project.SortOrder)
	}

	t.Run("outsider cannot create", func(t *testing.T) {
		body, err := json.Marshal(projectRequest{Name: "Nope"})
		if err != nil {
			t.Fatalf("marshalling project request: %v", err)
		}

		rec := do(t, f.handler, http.MethodPost, "/api/v1/teams/"+f.teamID+"/projects", string(body), f.outsider.Token)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want %d (body: %s)", rec.Code, http.StatusNotFound, rec.Body)
		}
	})
}

func TestProjectManagePermissions(t *testing.T) {
	f := newAccessMatrixFixture(t)

	rename := func(t *testing.T, token, projectID string) int {
		t.Helper()

		body, err := json.Marshal(projectRequest{Name: "Renamed"})
		if err != nil {
			t.Fatalf("marshalling project request: %v", err)
		}

		return do(t, f.handler, http.MethodPatch, "/api/v1/projects/"+projectID, string(body), token).Code
	}

	t.Run("member with access cannot manage", func(t *testing.T) {
		// The open member can open this project but does not own it, and does
		// not own the team: 403, not 404.
		if status := rename(t, f.openMember.Token, f.ownerProject.ID); status != http.StatusForbidden {
			t.Errorf("PATCH status = %d, want %d", status, http.StatusForbidden)
		}

		rec := do(t, f.handler, http.MethodDelete, "/api/v1/projects/"+f.ownerProject.ID, "", f.openMember.Token)
		if rec.Code != http.StatusForbidden {
			t.Errorf("DELETE status = %d, want %d", rec.Code, http.StatusForbidden)
		}

		if env := decodeEnvelope(t, rec); env.Error.Code != codeForbidden {
			t.Errorf("code = %q, want %q", env.Error.Code, codeForbidden)
		}
	})

	t.Run("member without access sees nothing", func(t *testing.T) {
		// The restricted member cannot reach this project at all: 404.
		if status := rename(t, f.limited.Token, f.ownerProject.ID); status != http.StatusNotFound {
			t.Errorf("PATCH status = %d, want %d", status, http.StatusNotFound)
		}
	})

	t.Run("project owner may manage", func(t *testing.T) {
		if status := rename(t, f.limited.Token, f.limitedProject.ID); status != http.StatusOK {
			t.Errorf("PATCH status = %d, want %d", status, http.StatusOK)
		}
	})

	t.Run("team owner may manage a member's project", func(t *testing.T) {
		rec := do(t, f.handler, http.MethodDelete, "/api/v1/projects/"+f.limitedProject.ID, "", f.owner.Token)
		if rec.Code != http.StatusNoContent {
			t.Fatalf("DELETE status = %d, want %d (body: %s)", rec.Code, http.StatusNoContent, rec.Body)
		}

		if after := do(t, f.handler, http.MethodGet, "/api/v1/projects/"+f.limitedProject.ID, "", f.owner.Token); after.Code != http.StatusNotFound {
			t.Errorf("GET after delete: status = %d, want %d", after.Code, http.StatusNotFound)
		}
	})
}

func TestProjectOrder(t *testing.T) {
	f := newTeamsFixture(t)
	owner := f.newUser(t, "order-owner@example.com")
	other := f.newUser(t, "order-other@example.com")
	team := f.createTeam(t, owner.Token, "Ghostman")
	otherTeam := f.createTeam(t, other.Token, "Other")

	first := f.createProject(t, owner.Token, team.ID, "First")
	second := f.createProject(t, owner.Token, team.ID, "Second")
	third := f.createProject(t, owner.Token, team.ID, "Third")
	foreign := f.createProject(t, other.Token, otherTeam.ID, "Foreign")

	orderPath := "/api/v1/teams/" + team.ID + "/projects/order"

	putOrder := func(t *testing.T, token string, ids []string) int {
		t.Helper()

		body, err := json.Marshal(projectOrderRequest{ProjectIDs: ids})
		if err != nil {
			t.Fatalf("marshalling order request: %v", err)
		}

		return do(t, f.handler, http.MethodPut, orderPath, string(body), token).Code
	}

	tests := []struct {
		name string
		ids  []string
	}{
		{name: "missing an id", ids: []string{first.ID, second.ID}},
		{name: "duplicate id", ids: []string{first.ID, first.ID, second.ID}},
		{name: "extra id from another team", ids: []string{first.ID, second.ID, third.ID, foreign.ID}},
		{name: "empty list", ids: []string{}},
		{name: "not a uuid", ids: []string{first.ID, second.ID, "nonsense"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if status := putOrder(t, owner.Token, tt.ids); status != http.StatusBadRequest {
				t.Fatalf("status = %d, want %d", status, http.StatusBadRequest)
			}
		})
	}

	t.Run("valid order is persisted", func(t *testing.T) {
		if status := putOrder(t, owner.Token, []string{third.ID, first.ID, second.ID}); status != http.StatusNoContent {
			t.Fatalf("status = %d, want %d", status, http.StatusNoContent)
		}

		if got := f.projectNames(t, owner.Token, team.ID); strings.Join(got, ",") != "Third,First,Second" {
			t.Errorf("order = %v, want [Third First Second]", got)
		}
	})

	t.Run("outsider cannot reorder", func(t *testing.T) {
		if status := putOrder(t, other.Token, []string{first.ID, second.ID, third.ID}); status != http.StatusNotFound {
			t.Fatalf("status = %d, want %d", status, http.StatusNotFound)
		}
	})
}

func TestTeamDeleteCascadesProjects(t *testing.T) {
	f := newAccessMatrixFixture(t)

	teamID := uuid.MustParse(f.teamID)

	var projectsBefore, accessBefore int
	if err := f.pool.QueryRow(t.Context(),
		"SELECT count(*) FROM projects WHERE team_id = $1", teamID).Scan(&projectsBefore); err != nil {
		t.Fatalf("counting projects: %v", err)
	}
	if err := f.pool.QueryRow(t.Context(), `
		SELECT count(*) FROM project_access
		JOIN projects ON projects.id = project_access.project_id
		WHERE projects.team_id = $1`, teamID).Scan(&accessBefore); err != nil {
		t.Fatalf("counting project access: %v", err)
	}

	if projectsBefore == 0 || accessBefore == 0 {
		t.Fatalf("fixture is not exercising the cascade: %d projects, %d access rows", projectsBefore, accessBefore)
	}

	rec := do(t, f.handler, http.MethodDelete, "/api/v1/teams/"+f.teamID, "", f.owner.Token)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("deleting team: status = %d, want %d (body: %s)", rec.Code, http.StatusNoContent, rec.Body)
	}

	var projectsAfter, accessAfter int
	if err := f.pool.QueryRow(t.Context(),
		"SELECT count(*) FROM projects WHERE team_id = $1", teamID).Scan(&projectsAfter); err != nil {
		t.Fatalf("counting projects: %v", err)
	}
	if err := f.pool.QueryRow(t.Context(),
		"SELECT count(*) FROM project_access").Scan(&accessAfter); err != nil {
		t.Fatalf("counting project access: %v", err)
	}

	if projectsAfter != 0 {
		t.Errorf("projects after team delete = %d, want 0", projectsAfter)
	}
	if accessAfter != 0 {
		t.Errorf("project_access rows after team delete = %d, want 0", accessAfter)
	}
}

func TestProjectPathValidation(t *testing.T) {
	f := newTeamsFixture(t)
	owner := f.newUser(t, "project-path@example.com")
	team := f.createTeam(t, owner.Token, "Ghostman")

	tests := []struct {
		name   string
		method string
		path   string
		body   string
	}{
		{name: "create", method: http.MethodPost, path: "/api/v1/teams/not-a-uuid/projects", body: `{"name":"x"}`},
		{name: "list", method: http.MethodGet, path: "/api/v1/teams/not-a-uuid/projects"},
		{name: "order", method: http.MethodPut, path: "/api/v1/teams/not-a-uuid/projects/order", body: `{"project_ids":[]}`},
		{name: "get", method: http.MethodGet, path: "/api/v1/projects/not-a-uuid"},
		{name: "patch", method: http.MethodPatch, path: "/api/v1/projects/not-a-uuid", body: `{"name":"x"}`},
		{name: "delete", method: http.MethodDelete, path: "/api/v1/projects/not-a-uuid"},
		{name: "get access", method: http.MethodGet, path: "/api/v1/projects/not-a-uuid/access"},
		{name: "put access", method: http.MethodPut, path: "/api/v1/projects/not-a-uuid/access", body: `{"user_ids":[]}`},
		{name: "member access team", method: http.MethodPut, path: "/api/v1/teams/not-a-uuid/members/" + owner.User.ID + "/access", body: `{"all_projects":true,"project_ids":[]}`},
		{name: "member access user", method: http.MethodPut, path: "/api/v1/teams/" + team.ID + "/members/not-a-uuid/access", body: `{"all_projects":true,"project_ids":[]}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := do(t, f.handler, tt.method, tt.path, tt.body, owner.Token)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want %d (body: %s)", rec.Code, http.StatusBadRequest, rec.Body)
			}
		})
	}
}

func TestProjectNameValidation(t *testing.T) {
	f := newTeamsFixture(t)
	owner := f.newUser(t, "project-name@example.com")
	team := f.createTeam(t, owner.Token, "Ghostman")

	tests := []struct {
		name       string
		body       string
		wantStatus int
	}{
		{name: "empty", body: `{"name":""}`, wantStatus: http.StatusBadRequest},
		{name: "whitespace only", body: `{"name":"   "}`, wantStatus: http.StatusBadRequest},
		{name: "101 characters", body: `{"name":"` + strings.Repeat("x", 101) + `"}`, wantStatus: http.StatusBadRequest},
		{name: "100 characters", body: `{"name":"` + strings.Repeat("x", 100) + `"}`, wantStatus: http.StatusCreated},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := do(t, f.handler, http.MethodPost, "/api/v1/teams/"+team.ID+"/projects", tt.body, owner.Token)
			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d (body: %s)", rec.Code, tt.wantStatus, rec.Body)
			}
		})
	}
}

func TestProjectRoutesRequireAuth(t *testing.T) {
	f := newTeamsFixture(t)

	id := uuid.New().String()
	tests := []struct {
		name   string
		method string
		path   string
	}{
		{name: "create", method: http.MethodPost, path: "/api/v1/teams/" + id + "/projects"},
		{name: "list", method: http.MethodGet, path: "/api/v1/teams/" + id + "/projects"},
		{name: "order", method: http.MethodPut, path: "/api/v1/teams/" + id + "/projects/order"},
		{name: "get", method: http.MethodGet, path: "/api/v1/projects/" + id},
		{name: "patch", method: http.MethodPatch, path: "/api/v1/projects/" + id},
		{name: "delete", method: http.MethodDelete, path: "/api/v1/projects/" + id},
		{name: "get access", method: http.MethodGet, path: "/api/v1/projects/" + id + "/access"},
		{name: "put access", method: http.MethodPut, path: "/api/v1/projects/" + id + "/access"},
		{name: "member access", method: http.MethodPut, path: "/api/v1/teams/" + id + "/members/" + id + "/access"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := do(t, f.handler, tt.method, tt.path, "", "")
			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want %d (body: %s)", rec.Code, http.StatusUnauthorized, rec.Body)
			}
		})
	}
}
