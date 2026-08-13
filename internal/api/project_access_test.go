package api

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/google/uuid"
)

// countUserAccessRows reads project_access directly, since the point of these
// tests is what the transaction leaves behind.
func (f teamsFixture) countUserAccessRows(t *testing.T, userID uuid.UUID) int {
	t.Helper()

	var count int
	if err := f.pool.QueryRow(t.Context(),
		"SELECT count(*) FROM project_access WHERE user_id = $1", userID).Scan(&count); err != nil {
		t.Fatalf("counting project access rows: %v", err)
	}

	return count
}

func TestSetMemberAccess(t *testing.T) {
	f := newAccessMatrixFixture(t)
	limitedID := uuid.MustParse(f.limited.User.ID)

	// The fixture leaves the restricted member with exactly one grant.
	if count := f.countUserAccessRows(t, limitedID); count != 1 {
		t.Fatalf("access rows = %d, want 1", count)
	}

	t.Run("switching to all_projects wipes the explicit rows", func(t *testing.T) {
		body, err := json.Marshal(memberAccessRequest{AllProjects: boolPtr(true)})
		if err != nil {
			t.Fatalf("marshalling request: %v", err)
		}

		rec := do(t, f.handler, http.MethodPut,
			"/api/v1/teams/"+f.teamID+"/members/"+f.limited.User.ID+"/access", string(body), f.owner.Token)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d (body: %s)", rec.Code, http.StatusOK, rec.Body)
		}

		var got memberAccessResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatalf("decoding response: %v", err)
		}
		if !got.AllProjects {
			t.Error("all_projects = false, want true")
		}
		if len(got.ProjectIDs) != 0 {
			t.Errorf("project_ids = %v, want empty", got.ProjectIDs)
		}

		if count := f.countUserAccessRows(t, limitedID); count != 0 {
			t.Errorf("access rows = %d, want 0", count)
		}

		// And the member now sees every project in the team.
		if names := f.projectNames(t, f.limited.Token, f.teamID); len(names) != 3 {
			t.Errorf("visible projects = %v, want all 3", names)
		}
	})

	t.Run("switching back replaces the list", func(t *testing.T) {
		f.mustSetMemberAccess(t, f.owner.Token, f.teamID, f.limited.User.ID, false,
			[]string{f.ownerProject.ID, f.grantedProject.ID})

		if count := f.countUserAccessRows(t, limitedID); count != 2 {
			t.Errorf("access rows = %d, want 2", count)
		}

		// Replacing, not merging: the previous list is gone.
		f.mustSetMemberAccess(t, f.owner.Token, f.teamID, f.limited.User.ID, false,
			[]string{f.ownerProject.ID})

		if count := f.countUserAccessRows(t, limitedID); count != 1 {
			t.Errorf("access rows after replace = %d, want 1", count)
		}

		names := f.projectNames(t, f.limited.Token, f.teamID)
		if len(names) != 2 {
			// The granted one plus their own project.
			t.Errorf("visible projects = %v, want 2 (the grant and their own project)", names)
		}
	})

	t.Run("a project from another team is rejected", func(t *testing.T) {
		other := f.newUser(t, "access-other@example.com")
		otherTeam := f.createTeam(t, other.Token, "Other")
		foreign := f.createProject(t, other.Token, otherTeam.ID, "Foreign")

		status := f.setMemberAccess(t, f.owner.Token, f.teamID, f.limited.User.ID, false, []string{foreign.ID})
		if status != http.StatusBadRequest {
			t.Fatalf("status = %d, want %d", status, http.StatusBadRequest)
		}
	})

	t.Run("the team owner's own access cannot be changed", func(t *testing.T) {
		status := f.setMemberAccess(t, f.owner.Token, f.teamID, f.owner.User.ID, false, nil)
		if status != http.StatusBadRequest {
			t.Fatalf("status = %d, want %d", status, http.StatusBadRequest)
		}
	})

	t.Run("target must be a member", func(t *testing.T) {
		status := f.setMemberAccess(t, f.owner.Token, f.teamID, f.outsider.User.ID, true, nil)
		if status != http.StatusNotFound {
			t.Fatalf("status = %d, want %d", status, http.StatusNotFound)
		}
	})

	t.Run("only the team owner may set member access", func(t *testing.T) {
		// A member, even one who sees everything, is not allowed.
		if status := f.setMemberAccess(t, f.openMember.Token, f.teamID, f.limited.User.ID, true, nil); status != http.StatusForbidden {
			t.Errorf("member status = %d, want %d", status, http.StatusForbidden)
		}

		if status := f.setMemberAccess(t, f.outsider.Token, f.teamID, f.limited.User.ID, true, nil); status != http.StatusNotFound {
			t.Errorf("outsider status = %d, want %d", status, http.StatusNotFound)
		}
	})

	t.Run("all_projects is required", func(t *testing.T) {
		// Omitting it would otherwise read as false and silently cut the
		// member off from everything.
		rec := do(t, f.handler, http.MethodPut,
			"/api/v1/teams/"+f.teamID+"/members/"+f.limited.User.ID+"/access", `{"project_ids":[]}`, f.owner.Token)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want %d (body: %s)", rec.Code, http.StatusBadRequest, rec.Body)
		}
	})
}

func TestSetProjectAccess(t *testing.T) {
	f := newAccessMatrixFixture(t)

	t.Run("granting makes a project visible", func(t *testing.T) {
		// The restricted member cannot see this one to begin with.
		before := do(t, f.handler, http.MethodGet, "/api/v1/projects/"+f.ownerProject.ID, "", f.limited.Token)
		if before.Code != http.StatusNotFound {
			t.Fatalf("before grant: status = %d, want %d", before.Code, http.StatusNotFound)
		}

		if status := f.setProjectAccess(t, f.owner.Token, f.ownerProject.ID, []string{f.limited.User.ID}); status != http.StatusOK {
			t.Fatalf("granting: status = %d, want %d", status, http.StatusOK)
		}

		after := do(t, f.handler, http.MethodGet, "/api/v1/projects/"+f.ownerProject.ID, "", f.limited.Token)
		if after.Code != http.StatusOK {
			t.Fatalf("after grant: status = %d, want %d (body: %s)", after.Code, http.StatusOK, after.Body)
		}
	})

	t.Run("the list is replaced, not merged", func(t *testing.T) {
		if status := f.setProjectAccess(t, f.owner.Token, f.ownerProject.ID, []string{f.openMember.User.ID}); status != http.StatusOK {
			t.Fatalf("replacing: status = %d, want %d", status, http.StatusOK)
		}

		rec := do(t, f.handler, http.MethodGet, "/api/v1/projects/"+f.ownerProject.ID+"/access", "", f.owner.Token)
		if rec.Code != http.StatusOK {
			t.Fatalf("listing access: status = %d, want %d", rec.Code, http.StatusOK)
		}

		var users []projectAccessUserResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &users); err != nil {
			t.Fatalf("decoding access list: %v", err)
		}

		if len(users) != 1 || users[0].UserID != f.openMember.User.ID {
			t.Fatalf("access list = %+v, want only the open member", users)
		}
		if users[0].Email != "matrix-open@example.com" {
			t.Errorf("email = %q, want %q", users[0].Email, "matrix-open@example.com")
		}

		// The restricted member lost the grant again.
		gone := do(t, f.handler, http.MethodGet, "/api/v1/projects/"+f.ownerProject.ID, "", f.limited.Token)
		if gone.Code != http.StatusNotFound {
			t.Errorf("after replace: status = %d, want %d", gone.Code, http.StatusNotFound)
		}
	})

	t.Run("an empty list clears access", func(t *testing.T) {
		if status := f.setProjectAccess(t, f.owner.Token, f.ownerProject.ID, []string{}); status != http.StatusOK {
			t.Fatalf("clearing: status = %d, want %d", status, http.StatusOK)
		}

		rec := do(t, f.handler, http.MethodGet, "/api/v1/projects/"+f.ownerProject.ID+"/access", "", f.owner.Token)
		if body := rec.Body.String(); body != "[]\n" {
			t.Errorf("access list = %s, want []", body)
		}
	})

	t.Run("a non-member is rejected", func(t *testing.T) {
		status := f.setProjectAccess(t, f.owner.Token, f.ownerProject.ID, []string{f.outsider.User.ID})
		if status != http.StatusBadRequest {
			t.Fatalf("status = %d, want %d", status, http.StatusBadRequest)
		}
	})

	t.Run("only a manager may set or read the list", func(t *testing.T) {
		// Sees the project, does not manage it.
		if status := f.setProjectAccess(t, f.openMember.Token, f.ownerProject.ID, nil); status != http.StatusForbidden {
			t.Errorf("member PUT status = %d, want %d", status, http.StatusForbidden)
		}

		rec := do(t, f.handler, http.MethodGet, "/api/v1/projects/"+f.ownerProject.ID+"/access", "", f.openMember.Token)
		if rec.Code != http.StatusForbidden {
			t.Errorf("member GET status = %d, want %d", rec.Code, http.StatusForbidden)
		}

		// Cannot see the project at all.
		if status := f.setProjectAccess(t, f.limited.Token, f.ownerProject.ID, nil); status != http.StatusNotFound {
			t.Errorf("restricted PUT status = %d, want %d", status, http.StatusNotFound)
		}
	})

	t.Run("the project owner may manage their own list", func(t *testing.T) {
		status := f.setProjectAccess(t, f.limited.Token, f.limitedProject.ID, []string{f.openMember.User.ID})
		if status != http.StatusOK {
			t.Fatalf("status = %d, want %d", status, http.StatusOK)
		}
	})
}

func boolPtr(value bool) *bool {
	return &value
}
