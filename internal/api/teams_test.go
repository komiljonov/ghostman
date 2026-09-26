package api

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/komiljonov/ghostman/internal/authz"
	"github.com/komiljonov/ghostman/internal/db"
	"github.com/komiljonov/ghostman/internal/testdb"
)

// Team behaviour runs against a real PostgreSQL: the transaction in
// CreateTeamWithOwner, the ON DELETE CASCADE on team_members and the CHECK on
// name are database behaviour, and a fake store would only be testing itself.

type teamsFixture struct {
	handler http.Handler
	store   *db.Store
	pool    *pgxpool.Pool
}

func newTeamsFixture(t *testing.T) teamsFixture {
	t.Helper()

	pool := testdb.New(t, "api")
	store := db.NewStore(pool)

	a := &api{
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		pool:   pool,
		store:  store,
		authz:  authz.New(store),
	}

	return teamsFixture{handler: a.routes(), store: store, pool: pool}
}

// newUser registers a user through the API and returns it with its token.
func (f teamsFixture) newUser(t *testing.T, email string) authResponse {
	t.Helper()

	user, rec := registerUser(t, f.handler, email, "supersecret", email)
	if rec.Code != http.StatusCreated {
		t.Fatalf("registering %s: status = %d, want %d (body: %s)", email, rec.Code, http.StatusCreated, rec.Body)
	}

	return user
}

// createTeam creates a team as the given user and returns the response.
func (f teamsFixture) createTeam(t *testing.T, token, name string) teamResponse {
	t.Helper()

	body, err := json.Marshal(teamRequest{Name: name})
	if err != nil {
		t.Fatalf("marshalling team request: %v", err)
	}

	rec := do(t, f.handler, http.MethodPost, "/api/v1/teams", string(body), token)
	if rec.Code != http.StatusCreated {
		t.Fatalf("creating team: status = %d, want %d (body: %s)", rec.Code, http.StatusCreated, rec.Body)
	}

	var team teamResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &team); err != nil {
		t.Fatalf("decoding team response: %v", err)
	}

	return team
}

// countTeamMembers reads the membership table directly.
func (f teamsFixture) countTeamMembers(t *testing.T, teamID uuid.UUID) int {
	t.Helper()

	var count int
	err := f.pool.QueryRow(t.Context(),
		"SELECT count(*) FROM team_members WHERE team_id = $1", teamID).Scan(&count)
	if err != nil {
		t.Fatalf("counting team members: %v", err)
	}

	return count
}

// addMember inserts a membership row directly, standing in for the invitation
// flow that does not exist yet.
func (f teamsFixture) addMember(t *testing.T, teamID, userID uuid.UUID) {
	t.Helper()

	if _, err := f.store.CreateTeamMember(t.Context(), db.CreateTeamMemberParams{
		TeamID:      teamID,
		UserID:      userID,
		AllProjects: true,
	}); err != nil {
		t.Fatalf("adding team member: %v", err)
	}
}

func TestCreateTeamWritesOwnerMembership(t *testing.T) {
	f := newTeamsFixture(t)
	owner := f.newUser(t, "owner@example.com")

	team := f.createTeam(t, owner.Token, "  Ghostman  ")

	if team.Name != "Ghostman" {
		t.Errorf("name = %q, want %q (should be trimmed)", team.Name, "Ghostman")
	}

	teamID, err := uuid.Parse(team.ID)
	if err != nil {
		t.Fatalf("team id %q is not a uuid: %v", team.ID, err)
	}

	// Both halves of the transaction must have landed.
	stored, err := f.store.GetTeamByID(t.Context(), teamID)
	if err != nil {
		t.Fatalf("team row missing after create: %v", err)
	}

	ownerID, err := uuid.Parse(owner.User.ID)
	if err != nil {
		t.Fatalf("owner id is not a uuid: %v", err)
	}

	if stored.OwnerID != ownerID {
		t.Errorf("owner_id = %s, want %s", stored.OwnerID, ownerID)
	}

	member, err := f.store.GetTeamMember(t.Context(), db.GetTeamMemberParams{
		TeamID: teamID,
		UserID: ownerID,
	})
	if err != nil {
		t.Fatalf("owner membership row missing after create: %v", err)
	}

	if !member.AllProjects {
		t.Error("owner membership has all_projects = false, want true")
	}
}

func TestListTeams(t *testing.T) {
	f := newTeamsFixture(t)
	owner := f.newUser(t, "list-owner@example.com")
	outsider := f.newUser(t, "list-outsider@example.com")

	team := f.createTeam(t, owner.Token, "Ghostman")

	rec := do(t, f.handler, http.MethodGet, "/api/v1/teams", "", owner.Token)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d (body: %s)", rec.Code, http.StatusOK, rec.Body)
	}

	var teams []teamSummaryResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &teams); err != nil {
		t.Fatalf("decoding team list: %v", err)
	}

	if len(teams) != 1 {
		t.Fatalf("owner sees %d teams, want 1", len(teams))
	}

	got := teams[0]
	if got.ID != team.ID {
		t.Errorf("id = %s, want %s", got.ID, team.ID)
	}
	if got.MemberCount != 1 {
		t.Errorf("member_count = %d, want 1", got.MemberCount)
	}
	if !got.IsOwner {
		t.Error("is_owner = false, want true")
	}
	if got.OwnerID != owner.User.ID {
		t.Errorf("owner_id = %s, want %s", got.OwnerID, owner.User.ID)
	}

	// Someone else's team must not appear, and an empty list is [] not null.
	outsiderRec := do(t, f.handler, http.MethodGet, "/api/v1/teams", "", outsider.Token)
	if outsiderRec.Code != http.StatusOK {
		t.Fatalf("outsider list: status = %d, want %d", outsiderRec.Code, http.StatusOK)
	}
	if body := strings.TrimSpace(outsiderRec.Body.String()); body != "[]" {
		t.Errorf("outsider list body = %s, want []", body)
	}
}

func TestListTeamsCountsAllMembers(t *testing.T) {
	f := newTeamsFixture(t)
	owner := f.newUser(t, "count-owner@example.com")
	member := f.newUser(t, "count-member@example.com")

	team := f.createTeam(t, owner.Token, "Ghostman")
	f.addMember(t, uuid.MustParse(team.ID), uuid.MustParse(member.User.ID))

	for _, user := range []struct {
		name        string
		token       string
		wantIsOwner bool
	}{
		{name: "owner", token: owner.Token, wantIsOwner: true},
		{name: "member", token: member.Token, wantIsOwner: false},
	} {
		t.Run(user.name, func(t *testing.T) {
			rec := do(t, f.handler, http.MethodGet, "/api/v1/teams", "", user.token)

			var teams []teamSummaryResponse
			if err := json.Unmarshal(rec.Body.Bytes(), &teams); err != nil {
				t.Fatalf("decoding team list: %v", err)
			}

			if len(teams) != 1 {
				t.Fatalf("sees %d teams, want 1", len(teams))
			}
			if teams[0].MemberCount != 2 {
				t.Errorf("member_count = %d, want 2", teams[0].MemberCount)
			}
			if teams[0].IsOwner != user.wantIsOwner {
				t.Errorf("is_owner = %t, want %t", teams[0].IsOwner, user.wantIsOwner)
			}
		})
	}
}

func TestGetTeamReturnsMembers(t *testing.T) {
	f := newTeamsFixture(t)
	owner := f.newUser(t, "detail-owner@example.com")
	member := f.newUser(t, "detail-member@example.com")

	team := f.createTeam(t, owner.Token, "Ghostman")
	f.addMember(t, uuid.MustParse(team.ID), uuid.MustParse(member.User.ID))

	rec := do(t, f.handler, http.MethodGet, "/api/v1/teams/"+team.ID, "", member.Token)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d (body: %s)", rec.Code, http.StatusOK, rec.Body)
	}

	var detail teamDetailResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &detail); err != nil {
		t.Fatalf("decoding team detail: %v", err)
	}

	if detail.IsOwner {
		t.Error("is_owner = true for a non-owner member, want false")
	}
	// owner_id names the owner whoever asks, so a member can mark them.
	if detail.OwnerID != owner.User.ID {
		t.Errorf("owner_id = %s, want the owner %s", detail.OwnerID, owner.User.ID)
	}
	if detail.CreatedAt.IsZero() {
		t.Error("created_at is zero")
	}
	if len(detail.Members) != 2 {
		t.Fatalf("members = %d, want 2", len(detail.Members))
	}

	// Ordered by email: detail-member sorts before detail-owner.
	if detail.Members[0].Email != "detail-member@example.com" {
		t.Errorf("first member = %q, want %q", detail.Members[0].Email, "detail-member@example.com")
	}
	if !detail.Members[0].AllProjects {
		t.Error("all_projects = false, want true")
	}

	// The owner is findable in the members list by owner_id.
	if owner := detail.Members[1]; owner.UserID != detail.OwnerID || owner.Email != "detail-owner@example.com" {
		t.Errorf("members[1] = %+v, want the owner with user_id = owner_id %s", owner, detail.OwnerID)
	}
}

func TestOutsiderSeesTeamAsMissing(t *testing.T) {
	f := newTeamsFixture(t)
	owner := f.newUser(t, "hidden-owner@example.com")
	outsider := f.newUser(t, "hidden-outsider@example.com")

	team := f.createTeam(t, owner.Token, "Ghostman")

	tests := []struct {
		name   string
		method string
		body   string
	}{
		{name: "get", method: http.MethodGet},
		{name: "patch", method: http.MethodPatch, body: `{"name":"Stolen"}`},
		{name: "delete", method: http.MethodDelete},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := do(t, f.handler, tt.method, "/api/v1/teams/"+team.ID, tt.body, outsider.Token)

			// 404 rather than 403: the team's existence is not observable.
			if rec.Code != http.StatusNotFound {
				t.Fatalf("status = %d, want %d (body: %s)", rec.Code, http.StatusNotFound, rec.Body)
			}

			env := decodeEnvelope(t, rec)
			if env.Error.Code != codeNotFound {
				t.Errorf("code = %q, want %q", env.Error.Code, codeNotFound)
			}
			if env.Error.Message != messageTeamNotFound {
				t.Errorf("message = %q, want %q", env.Error.Message, messageTeamNotFound)
			}
		})
	}

	// The team must still be intact.
	if count := f.countTeamMembers(t, uuid.MustParse(team.ID)); count != 1 {
		t.Errorf("team_members rows = %d, want 1", count)
	}
}

func TestMemberCannotModifyTeam(t *testing.T) {
	f := newTeamsFixture(t)
	owner := f.newUser(t, "guard-owner@example.com")
	member := f.newUser(t, "guard-member@example.com")

	team := f.createTeam(t, owner.Token, "Ghostman")
	f.addMember(t, uuid.MustParse(team.ID), uuid.MustParse(member.User.ID))

	tests := []struct {
		name   string
		method string
		body   string
	}{
		{name: "patch", method: http.MethodPatch, body: `{"name":"Renamed"}`},
		{name: "delete", method: http.MethodDelete},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := do(t, f.handler, tt.method, "/api/v1/teams/"+team.ID, tt.body, member.Token)

			// 403, not 404: a member already knows the team exists.
			if rec.Code != http.StatusForbidden {
				t.Fatalf("status = %d, want %d (body: %s)", rec.Code, http.StatusForbidden, rec.Body)
			}

			env := decodeEnvelope(t, rec)
			if env.Error.Code != codeForbidden {
				t.Errorf("code = %q, want %q", env.Error.Code, codeForbidden)
			}
		})
	}

	// A member may still read it.
	if rec := do(t, f.handler, http.MethodGet, "/api/v1/teams/"+team.ID, "", member.Token); rec.Code != http.StatusOK {
		t.Errorf("member GET: status = %d, want %d", rec.Code, http.StatusOK)
	}
}

func TestUpdateTeamName(t *testing.T) {
	f := newTeamsFixture(t)
	owner := f.newUser(t, "rename@example.com")

	team := f.createTeam(t, owner.Token, "Ghostman")

	rec := do(t, f.handler, http.MethodPatch, "/api/v1/teams/"+team.ID, `{"name":"  Ghostman Core  "}`, owner.Token)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d (body: %s)", rec.Code, http.StatusOK, rec.Body)
	}

	var detail teamDetailResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &detail); err != nil {
		t.Fatalf("decoding team detail: %v", err)
	}

	if detail.Name != "Ghostman Core" {
		t.Errorf("name = %q, want %q", detail.Name, "Ghostman Core")
	}
	if !detail.IsOwner {
		t.Error("is_owner = false, want true")
	}

	stored, err := f.store.GetTeamByID(t.Context(), uuid.MustParse(team.ID))
	if err != nil {
		t.Fatalf("reading team back: %v", err)
	}
	if stored.Name != "Ghostman Core" {
		t.Errorf("stored name = %q, want %q", stored.Name, "Ghostman Core")
	}
}

func TestDeleteTeamCascadesMemberships(t *testing.T) {
	f := newTeamsFixture(t)
	owner := f.newUser(t, "delete-owner@example.com")
	member := f.newUser(t, "delete-member@example.com")

	team := f.createTeam(t, owner.Token, "Ghostman")
	teamID := uuid.MustParse(team.ID)
	f.addMember(t, teamID, uuid.MustParse(member.User.ID))

	if count := f.countTeamMembers(t, teamID); count != 2 {
		t.Fatalf("team_members rows before delete = %d, want 2", count)
	}

	rec := do(t, f.handler, http.MethodDelete, "/api/v1/teams/"+team.ID, "", owner.Token)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d (body: %s)", rec.Code, http.StatusNoContent, rec.Body)
	}

	// ON DELETE CASCADE must have taken the membership rows with it.
	if count := f.countTeamMembers(t, teamID); count != 0 {
		t.Errorf("team_members rows after delete = %d, want 0", count)
	}

	listRec := do(t, f.handler, http.MethodGet, "/api/v1/teams", "", owner.Token)
	if body := strings.TrimSpace(listRec.Body.String()); body != "[]" {
		t.Errorf("team list after delete = %s, want []", body)
	}
}

func TestTeamNameValidation(t *testing.T) {
	f := newTeamsFixture(t)
	owner := f.newUser(t, "validate@example.com")

	tests := []struct {
		name       string
		body       string
		wantStatus int
	}{
		{name: "empty", body: `{"name":""}`, wantStatus: http.StatusBadRequest},
		{name: "whitespace only", body: `{"name":"   "}`, wantStatus: http.StatusBadRequest},
		{name: "missing field", body: `{}`, wantStatus: http.StatusBadRequest},
		{name: "101 characters", body: `{"name":"` + strings.Repeat("x", 101) + `"}`, wantStatus: http.StatusBadRequest},
		{name: "100 characters", body: `{"name":"` + strings.Repeat("x", 100) + `"}`, wantStatus: http.StatusCreated},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := do(t, f.handler, http.MethodPost, "/api/v1/teams", tt.body, owner.Token)
			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d (body: %s)", rec.Code, tt.wantStatus, rec.Body)
			}

			if tt.wantStatus == http.StatusBadRequest {
				if env := decodeEnvelope(t, rec); env.Error.Code != codeBadRequest {
					t.Errorf("code = %q, want %q", env.Error.Code, codeBadRequest)
				}
			}
		})
	}

	// Renaming is validated the same way.
	team := f.createTeam(t, owner.Token, "Ghostman")
	rec := do(t, f.handler, http.MethodPatch, "/api/v1/teams/"+team.ID, `{"name":"  "}`, owner.Token)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("patch with blank name: status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

func TestTeamPathValidation(t *testing.T) {
	f := newTeamsFixture(t)
	owner := f.newUser(t, "path@example.com")

	tests := []struct {
		name   string
		method string
		body   string
	}{
		{name: "get", method: http.MethodGet},
		{name: "patch", method: http.MethodPatch, body: `{"name":"Valid"}`},
		{name: "delete", method: http.MethodDelete},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// A malformed uuid is the client's mistake: 400, never 500.
			rec := do(t, f.handler, tt.method, "/api/v1/teams/not-a-uuid", tt.body, owner.Token)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want %d (body: %s)", rec.Code, http.StatusBadRequest, rec.Body)
			}

			if env := decodeEnvelope(t, rec); env.Error.Code != codeBadRequest {
				t.Errorf("code = %q, want %q", env.Error.Code, codeBadRequest)
			}
		})
	}

	// A well-formed uuid that is not a team looks like any other non-member
	// case: 404.
	rec := do(t, f.handler, http.MethodGet, "/api/v1/teams/"+uuid.New().String(), "", owner.Token)
	if rec.Code != http.StatusNotFound {
		t.Errorf("unknown team: status = %d, want %d", rec.Code, http.StatusNotFound)
	}
}

func TestTeamRoutesRequireAuth(t *testing.T) {
	f := newTeamsFixture(t)

	tests := []struct {
		name   string
		method string
		path   string
		body   string
	}{
		{name: "create", method: http.MethodPost, path: "/api/v1/teams", body: `{"name":"Ghostman"}`},
		{name: "list", method: http.MethodGet, path: "/api/v1/teams"},
		{name: "get", method: http.MethodGet, path: "/api/v1/teams/" + uuid.New().String()},
		{name: "patch", method: http.MethodPatch, path: "/api/v1/teams/" + uuid.New().String(), body: `{"name":"x"}`},
		{name: "delete", method: http.MethodDelete, path: "/api/v1/teams/" + uuid.New().String()},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := do(t, f.handler, tt.method, tt.path, tt.body, "")
			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want %d (body: %s)", rec.Code, http.StatusUnauthorized, rec.Body)
			}
		})
	}
}
