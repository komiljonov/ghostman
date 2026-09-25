package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
)

// folderBody builds a JSON body where a nil *string becomes an explicit null,
// which matters for parent_id: null means the project root.
func folderBody(t *testing.T, fields map[string]any) string {
	t.Helper()

	body, err := json.Marshal(fields)
	if err != nil {
		t.Fatalf("marshalling folder request: %v", err)
	}

	return string(body)
}

func (f teamsFixture) postFolder(t *testing.T, token, projectID, name string, parentID *string) *httptest.ResponseRecorder {
	t.Helper()

	body := folderBody(t, map[string]any{"name": name, "parent_id": parentID})
	return do(t, f.handler, http.MethodPost, "/api/v1/projects/"+projectID+"/folders", body, token)
}

// createFolder creates a folder and fails unless it succeeds.
func (f teamsFixture) createFolder(t *testing.T, token, projectID, name string, parentID *string) folderResponse {
	t.Helper()

	rec := f.postFolder(t, token, projectID, name, parentID)
	if rec.Code != http.StatusCreated {
		t.Fatalf("creating folder %q: status = %d, want %d (body: %s)", name, rec.Code, http.StatusCreated, rec.Body)
	}

	var folder folderResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &folder); err != nil {
		t.Fatalf("decoding folder response: %v", err)
	}

	return folder
}

func (f teamsFixture) listFolders(t *testing.T, token, projectID string) []folderSummaryResponse {
	t.Helper()

	rec := do(t, f.handler, http.MethodGet, "/api/v1/projects/"+projectID+"/folders", "", token)
	if rec.Code != http.StatusOK {
		t.Fatalf("listing folders: status = %d, want %d (body: %s)", rec.Code, http.StatusOK, rec.Body)
	}

	var folders []folderSummaryResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &folders); err != nil {
		t.Fatalf("decoding folder list: %v", err)
	}

	return folders
}

// folderChildren groups a flat folder list by parent, keeping list order. The
// root is keyed by "".
func folderChildren(folders []folderSummaryResponse) map[string][]string {
	children := make(map[string][]string)
	for _, folder := range folders {
		parent := ""
		if folder.ParentID != nil {
			parent = *folder.ParentID
		}
		children[parent] = append(children[parent], folder.Name)
	}

	return children
}

func (f teamsFixture) moveFolder(t *testing.T, token, folderID string, fields map[string]any) *httptest.ResponseRecorder {
	t.Helper()

	return do(t, f.handler, http.MethodPost, "/api/v1/folders/"+folderID+"/move", folderBody(t, fields), token)
}

func (f teamsFixture) putFolderOrder(t *testing.T, token string, fields map[string]any) *httptest.ResponseRecorder {
	t.Helper()

	return do(t, f.handler, http.MethodPut, "/api/v1/folders/order", folderBody(t, fields), token)
}

// newFolderProject sets up an owner with a team and one project.
func newFolderProject(t *testing.T, emailPrefix string) (teamsFixture, authResponse, teamResponse, projectResponse) {
	t.Helper()

	f := newTeamsFixture(t)
	owner := f.newUser(t, emailPrefix+"@example.com")
	team := f.createTeam(t, owner.Token, "Ghostman")
	project := f.createProject(t, owner.Token, team.ID, "API")

	return f, owner, team, project
}

func TestFolderTreeBuildsAndListsInOrder(t *testing.T) {
	f, owner, _, project := newFolderProject(t, "tree-owner")

	// Interleaved on purpose: order must come from sort_order, not insertion.
	rootA := f.createFolder(t, owner.Token, project.ID, "Root A", nil)
	a1 := f.createFolder(t, owner.Token, project.ID, "A1", &rootA.ID)
	rootB := f.createFolder(t, owner.Token, project.ID, "Root B", nil)
	a2 := f.createFolder(t, owner.Token, project.ID, "A2", &rootA.ID)
	a1a := f.createFolder(t, owner.Token, project.ID, "A1a", &a1.ID)

	// Each new folder goes after its own siblings, counted per parent.
	for _, tt := range []struct {
		folder folderResponse
		want   int32
	}{{rootA, 0}, {rootB, 1}, {a1, 0}, {a2, 1}, {a1a, 0}} {
		if tt.folder.SortOrder != tt.want {
			t.Errorf("%s sort_order = %d, want %d", tt.folder.Name, tt.folder.SortOrder, tt.want)
		}
	}

	if a1a.ParentID == nil || *a1a.ParentID != a1.ID {
		t.Errorf("A1a parent_id = %v, want %s", a1a.ParentID, a1.ID)
	}
	if a1a.ProjectID != project.ID {
		t.Errorf("A1a project_id = %s, want %s", a1a.ProjectID, project.ID)
	}

	folders := f.listFolders(t, owner.Token, project.ID)
	if len(folders) != 5 {
		t.Fatalf("listed %d folders, want 5", len(folders))
	}

	// Roots come first, then each parent's children as one contiguous run.
	if folders[0].Name != "Root A" || folders[1].Name != "Root B" {
		t.Errorf("first two entries = %q, %q, want the roots in order", folders[0].Name, folders[1].Name)
	}
	for i := 1; i < len(folders); i++ {
		prev, cur := folders[i-1], folders[i]
		if prev.ParentID != nil && cur.ParentID == nil {
			t.Errorf("root %q listed after a nested folder", cur.Name)
		}
	}

	children := folderChildren(folders)
	checks := map[string]string{
		"":       "Root A,Root B",
		rootA.ID: "A1,A2",
		a1.ID:    "A1a",
	}
	for parent, want := range checks {
		if got := strings.Join(children[parent], ","); got != want {
			t.Errorf("children of %q = %s, want %s", parent, got, want)
		}
	}

	// The list is flat: parent_id is present and null at the root, and there
	// is no nesting.
	rec := do(t, f.handler, http.MethodGet, "/api/v1/projects/"+project.ID+"/folders", "", owner.Token)
	if !strings.Contains(rec.Body.String(), `"parent_id":null`) {
		t.Errorf("root folders should carry an explicit null parent_id: %s", rec.Body)
	}
}

func TestCreateFolderParentValidation(t *testing.T) {
	f, owner, team, project := newFolderProject(t, "parent-owner")
	other := f.createProject(t, owner.Token, team.ID, "Other")
	foreign := f.createFolder(t, owner.Token, other.ID, "Foreign", nil)

	missing := uuid.New().String()

	tests := []struct {
		name string
		body string
	}{
		{name: "parent from another project", body: folderBody(t, map[string]any{"name": "x", "parent_id": foreign.ID})},
		{name: "parent does not exist", body: folderBody(t, map[string]any{"name": "x", "parent_id": missing})},
		{name: "parent is not a uuid", body: `{"name":"x","parent_id":"not-a-uuid"}`},
		{name: "parent is not a string", body: `{"name":"x","parent_id":42}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := do(t, f.handler, http.MethodPost, "/api/v1/projects/"+project.ID+"/folders", tt.body, owner.Token)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want %d (body: %s)", rec.Code, http.StatusBadRequest, rec.Body)
			}
		})
	}

	if folders := f.listFolders(t, owner.Token, project.ID); len(folders) != 0 {
		t.Errorf("rejected creates left %d folders behind", len(folders))
	}

	// Omitting parent_id is the same as null: the project root.
	rec := do(t, f.handler, http.MethodPost, "/api/v1/projects/"+project.ID+"/folders", `{"name":"Top"}`, owner.Token)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create without parent_id: status = %d, want %d (body: %s)", rec.Code, http.StatusCreated, rec.Body)
	}
}

func TestMoveFolder(t *testing.T) {
	f, owner, team, project := newFolderProject(t, "move-owner")

	a := f.createFolder(t, owner.Token, project.ID, "A", nil)
	b := f.createFolder(t, owner.Token, project.ID, "B", &a.ID)
	c := f.createFolder(t, owner.Token, project.ID, "C", &b.ID)

	t.Run("cycles are rejected", func(t *testing.T) {
		for _, tt := range []struct {
			name   string
			parent string
		}{
			{name: "into itself", parent: a.ID},
			{name: "into its child", parent: b.ID},
			{name: "into its grandchild", parent: c.ID},
		} {
			t.Run(tt.name, func(t *testing.T) {
				rec := f.moveFolder(t, owner.Token, a.ID, map[string]any{"parent_id": tt.parent})
				if rec.Code != http.StatusBadRequest {
					t.Fatalf("status = %d, want %d (body: %s)", rec.Code, http.StatusBadRequest, rec.Body)
				}
			})
		}
	})

	t.Run("parent from another project", func(t *testing.T) {
		other := f.createProject(t, owner.Token, team.ID, "Other")
		foreign := f.createFolder(t, owner.Token, other.ID, "Foreign", nil)

		rec := f.moveFolder(t, owner.Token, c.ID, map[string]any{"parent_id": foreign.ID})
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want %d (body: %s)", rec.Code, http.StatusBadRequest, rec.Body)
		}
	})

	t.Run("invalid bodies", func(t *testing.T) {
		for _, tt := range []struct {
			name string
			body string
		}{
			// A forgotten parent_id must not silently mean "move to root".
			{name: "parent_id missing", body: `{}`},
			{name: "parent_id not a uuid", body: `{"parent_id":"nope"}`},
			{name: "negative sort_order", body: `{"parent_id":null,"sort_order":-1}`},
		} {
			t.Run(tt.name, func(t *testing.T) {
				rec := do(t, f.handler, http.MethodPost, "/api/v1/folders/"+c.ID+"/move", tt.body, owner.Token)
				if rec.Code != http.StatusBadRequest {
					t.Fatalf("status = %d, want %d (body: %s)", rec.Code, http.StatusBadRequest, rec.Body)
				}
			})
		}
	})

	t.Run("to root appends after the roots", func(t *testing.T) {
		rec := f.moveFolder(t, owner.Token, c.ID, map[string]any{"parent_id": nil})
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d (body: %s)", rec.Code, http.StatusOK, rec.Body)
		}

		var moved folderResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &moved); err != nil {
			t.Fatalf("decoding move response: %v", err)
		}
		if moved.ParentID != nil {
			t.Errorf("parent_id = %s, want null", *moved.ParentID)
		}
		if moved.SortOrder != 1 {
			t.Errorf("sort_order = %d, want 1 (after A)", moved.SortOrder)
		}
	})

	t.Run("former ancestor can now move under it", func(t *testing.T) {
		// C left A's subtree, so A may now go inside C.
		rec := f.moveFolder(t, owner.Token, a.ID, map[string]any{"parent_id": c.ID, "sort_order": 7})
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d (body: %s)", rec.Code, http.StatusOK, rec.Body)
		}

		var moved folderResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &moved); err != nil {
			t.Fatalf("decoding move response: %v", err)
		}
		if moved.SortOrder != 7 {
			t.Errorf("sort_order = %d, want the explicit 7", moved.SortOrder)
		}
	})

	children := folderChildren(f.listFolders(t, owner.Token, project.ID))
	if got := strings.Join(children[""], ","); got != "C" {
		t.Errorf("roots = %s, want C", got)
	}
	if got := strings.Join(children[c.ID], ","); got != "A" {
		t.Errorf("children of C = %s, want A", got)
	}
	if got := strings.Join(children[a.ID], ","); got != "B" {
		t.Errorf("children of A = %s, want B (moves carry the subtree)", got)
	}
}

func TestFolderOrder(t *testing.T) {
	f, owner, team, project := newFolderProject(t, "order-owner")

	r1 := f.createFolder(t, owner.Token, project.ID, "R1", nil)
	r2 := f.createFolder(t, owner.Token, project.ID, "R2", nil)
	r3 := f.createFolder(t, owner.Token, project.ID, "R3", nil)
	k1 := f.createFolder(t, owner.Token, project.ID, "K1", &r1.ID)
	k2 := f.createFolder(t, owner.Token, project.ID, "K2", &r1.ID)

	other := f.createProject(t, owner.Token, team.ID, "Other")
	foreign := f.createFolder(t, owner.Token, other.ID, "Foreign", nil)

	t.Run("wrong sibling sets are rejected", func(t *testing.T) {
		for _, tt := range []struct {
			name   string
			fields map[string]any
		}{
			{name: "missing a sibling", fields: map[string]any{
				"project_id": project.ID, "parent_id": nil, "folder_ids": []string{r1.ID, r2.ID}}},
			{name: "includes a folder from another parent", fields: map[string]any{
				"project_id": project.ID, "parent_id": nil, "folder_ids": []string{r1.ID, r2.ID, r3.ID, k1.ID}}},
			{name: "lists a sibling twice", fields: map[string]any{
				"project_id": project.ID, "parent_id": nil, "folder_ids": []string{r1.ID, r2.ID, r2.ID}}},
			{name: "empty list", fields: map[string]any{
				"project_id": project.ID, "parent_id": nil, "folder_ids": []string{}}},
			{name: "parent from another project", fields: map[string]any{
				"project_id": project.ID, "parent_id": foreign.ID, "folder_ids": []string{}}},
			{name: "invalid folder id", fields: map[string]any{
				"project_id": project.ID, "parent_id": nil, "folder_ids": []string{"nope"}}},
			{name: "invalid project id", fields: map[string]any{
				"project_id": "nope", "parent_id": nil, "folder_ids": []string{}}},
		} {
			t.Run(tt.name, func(t *testing.T) {
				rec := f.putFolderOrder(t, owner.Token, tt.fields)
				if rec.Code != http.StatusBadRequest {
					t.Fatalf("status = %d, want %d (body: %s)", rec.Code, http.StatusBadRequest, rec.Body)
				}
			})
		}

		// Nothing moved.
		if got := strings.Join(folderChildren(f.listFolders(t, owner.Token, project.ID))[""], ","); got != "R1,R2,R3" {
			t.Errorf("roots = %s, want R1,R2,R3 unchanged", got)
		}
	})

	t.Run("root order", func(t *testing.T) {
		rec := f.putFolderOrder(t, owner.Token, map[string]any{
			"project_id": project.ID, "parent_id": nil, "folder_ids": []string{r3.ID, r1.ID, r2.ID}})
		if rec.Code != http.StatusNoContent {
			t.Fatalf("status = %d, want %d (body: %s)", rec.Code, http.StatusNoContent, rec.Body)
		}

		if got := strings.Join(folderChildren(f.listFolders(t, owner.Token, project.ID))[""], ","); got != "R3,R1,R2" {
			t.Errorf("roots = %s, want R3,R1,R2", got)
		}
	})

	t.Run("child order leaves the roots alone", func(t *testing.T) {
		rec := f.putFolderOrder(t, owner.Token, map[string]any{
			"project_id": project.ID, "parent_id": r1.ID, "folder_ids": []string{k2.ID, k1.ID}})
		if rec.Code != http.StatusNoContent {
			t.Fatalf("status = %d, want %d (body: %s)", rec.Code, http.StatusNoContent, rec.Body)
		}

		children := folderChildren(f.listFolders(t, owner.Token, project.ID))
		if got := strings.Join(children[r1.ID], ","); got != "K2,K1" {
			t.Errorf("children of R1 = %s, want K2,K1", got)
		}
		if got := strings.Join(children[""], ","); got != "R3,R1,R2" {
			t.Errorf("roots = %s, want R3,R1,R2 unchanged", got)
		}
	})
}

func TestDeleteFolderRemovesSubtreeOnly(t *testing.T) {
	f, owner, _, project := newFolderProject(t, "delete-owner")

	a := f.createFolder(t, owner.Token, project.ID, "A", nil)
	b := f.createFolder(t, owner.Token, project.ID, "B", &a.ID)
	f.createFolder(t, owner.Token, project.ID, "C", &b.ID)
	f.createFolder(t, owner.Token, project.ID, "D", &a.ID)
	f.createFolder(t, owner.Token, project.ID, "E", nil)

	rec := do(t, f.handler, http.MethodDelete, "/api/v1/folders/"+b.ID, "", owner.Token)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d (body: %s)", rec.Code, http.StatusNoContent, rec.Body)
	}

	var names []string
	for _, folder := range f.listFolders(t, owner.Token, project.ID) {
		names = append(names, folder.Name)
	}
	if got := strings.Join(names, ","); got != "A,E,D" {
		t.Errorf("remaining folders = %s, want A,E,D (B and its child C gone)", got)
	}

	if again := do(t, f.handler, http.MethodDelete, "/api/v1/folders/"+b.ID, "", owner.Token); again.Code != http.StatusNotFound {
		t.Errorf("second delete: status = %d, want %d", again.Code, http.StatusNotFound)
	}
}

func TestProjectDeleteCascadesFolders(t *testing.T) {
	f, owner, _, project := newFolderProject(t, "cascade-owner")

	a := f.createFolder(t, owner.Token, project.ID, "A", nil)
	f.createFolder(t, owner.Token, project.ID, "B", &a.ID)

	if rec := do(t, f.handler, http.MethodDelete, "/api/v1/projects/"+project.ID, "", owner.Token); rec.Code != http.StatusNoContent {
		t.Fatalf("deleting project: status = %d, want %d (body: %s)", rec.Code, http.StatusNoContent, rec.Body)
	}

	var count int
	if err := f.pool.QueryRow(t.Context(), "SELECT count(*) FROM folders").Scan(&count); err != nil {
		t.Fatalf("counting folders: %v", err)
	}
	if count != 0 {
		t.Errorf("folders after project delete = %d, want 0", count)
	}
}

// TestFolderAccessMatrix reuses the project access fixture: every folder
// endpoint must answer 404 to anyone who cannot reach the project, and must
// work for anyone who can, whether or not they manage it.
func TestFolderAccessMatrix(t *testing.T) {
	f := newAccessMatrixFixture(t)

	// ownerProject is out of the limited member's reach; grantedProject is in.
	hidden := f.createFolder(t, f.owner.Token, f.ownerProject.ID, "Hidden", nil)

	deniedRequests := func(folderID, projectID string) []struct {
		name, method, path, body, message string
	} {
		return []struct {
			name, method, path, body, message string
		}{
			{"create", http.MethodPost, "/api/v1/projects/" + projectID + "/folders", `{"name":"x"}`, messageProjectNotFound},
			{"list", http.MethodGet, "/api/v1/projects/" + projectID + "/folders", "", messageProjectNotFound},
			{"order", http.MethodPut, "/api/v1/folders/order",
				`{"project_id":"` + projectID + `","parent_id":null,"folder_ids":["` + folderID + `"]}`, messageProjectNotFound},
			{"rename", http.MethodPatch, "/api/v1/folders/" + folderID, `{"name":"x"}`, messageFolderNotFound},
			{"move", http.MethodPost, "/api/v1/folders/" + folderID + "/move", `{"parent_id":null}`, messageFolderNotFound},
			{"delete", http.MethodDelete, "/api/v1/folders/" + folderID, "", messageFolderNotFound},
		}
	}

	for _, caller := range []struct {
		name  string
		token string
	}{
		{name: "restricted member without a grant", token: f.limited.Token},
		{name: "outsider", token: f.outsider.Token},
	} {
		t.Run(caller.name, func(t *testing.T) {
			for _, req := range deniedRequests(hidden.ID, f.ownerProject.ID) {
				t.Run(req.name, func(t *testing.T) {
					rec := do(t, f.handler, req.method, req.path, req.body, caller.token)
					if rec.Code != http.StatusNotFound {
						t.Fatalf("status = %d, want %d (body: %s)", rec.Code, http.StatusNotFound, rec.Body)
					}
					if env := decodeEnvelope(t, rec); env.Error.Message != req.message {
						t.Errorf("message = %q, want %q", env.Error.Message, req.message)
					}
				})
			}
		})
	}

	t.Run("unknown folder looks the same as a hidden one", func(t *testing.T) {
		rec := do(t, f.handler, http.MethodPatch, "/api/v1/folders/"+uuid.New().String(), `{"name":"x"}`, f.owner.Token)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want %d (body: %s)", rec.Code, http.StatusNotFound, rec.Body)
		}
		if env := decodeEnvelope(t, rec); env.Error.Message != messageFolderNotFound {
			t.Errorf("message = %q, want %q", env.Error.Message, messageFolderNotFound)
		}
	})

	// None of the denied calls touched the folder.
	folders := f.listFolders(t, f.owner.Token, f.ownerProject.ID)
	if len(folders) != 1 || folders[0].Name != "Hidden" {
		t.Errorf("owner project folders = %+v, want only the untouched Hidden", folders)
	}

	t.Run("any member with access has full folder rights", func(t *testing.T) {
		// The restricted member holds a grant but manages neither project nor
		// team; folders need no more than access.
		token := f.limited.Token
		projectID := f.grantedProject.ID

		parent := f.createFolder(t, token, projectID, "Parent", nil)
		child := f.createFolder(t, token, projectID, "Child", nil)

		if rec := do(t, f.handler, http.MethodPatch, "/api/v1/folders/"+child.ID, `{"name":"Renamed"}`, token); rec.Code != http.StatusOK {
			t.Errorf("rename: status = %d, want %d (body: %s)", rec.Code, http.StatusOK, rec.Body)
		}
		if rec := f.moveFolder(t, token, child.ID, map[string]any{"parent_id": parent.ID}); rec.Code != http.StatusOK {
			t.Errorf("move: status = %d, want %d (body: %s)", rec.Code, http.StatusOK, rec.Body)
		}
		if rec := f.putFolderOrder(t, token, map[string]any{
			"project_id": projectID, "parent_id": nil, "folder_ids": []string{parent.ID}}); rec.Code != http.StatusNoContent {
			t.Errorf("order: status = %d, want %d (body: %s)", rec.Code, http.StatusNoContent, rec.Body)
		}
		if names := len(f.listFolders(t, token, projectID)); names != 2 {
			t.Errorf("list: %d folders, want 2", names)
		}
		if rec := do(t, f.handler, http.MethodDelete, "/api/v1/folders/"+parent.ID, "", token); rec.Code != http.StatusNoContent {
			t.Errorf("delete: status = %d, want %d (body: %s)", rec.Code, http.StatusNoContent, rec.Body)
		}
	})
}

func TestFolderNameValidation(t *testing.T) {
	f, owner, _, project := newFolderProject(t, "folder-name")
	folder := f.createFolder(t, owner.Token, project.ID, "Valid", nil)

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
		t.Run("create "+tt.name, func(t *testing.T) {
			rec := do(t, f.handler, http.MethodPost, "/api/v1/projects/"+project.ID+"/folders", tt.body, owner.Token)
			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d (body: %s)", rec.Code, tt.wantStatus, rec.Body)
			}
		})
	}

	t.Run("rename to empty", func(t *testing.T) {
		rec := do(t, f.handler, http.MethodPatch, "/api/v1/folders/"+folder.ID, `{"name":" "}`, owner.Token)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want %d (body: %s)", rec.Code, http.StatusBadRequest, rec.Body)
		}
	})
}

func TestFolderPathValidation(t *testing.T) {
	f := newTeamsFixture(t)
	owner := f.newUser(t, "folder-path@example.com")

	tests := []struct {
		name   string
		method string
		path   string
		body   string
	}{
		{name: "create", method: http.MethodPost, path: "/api/v1/projects/not-a-uuid/folders", body: `{"name":"x"}`},
		{name: "list", method: http.MethodGet, path: "/api/v1/projects/not-a-uuid/folders"},
		{name: "rename", method: http.MethodPatch, path: "/api/v1/folders/not-a-uuid", body: `{"name":"x"}`},
		{name: "move", method: http.MethodPost, path: "/api/v1/folders/not-a-uuid/move", body: `{"parent_id":null}`},
		{name: "delete", method: http.MethodDelete, path: "/api/v1/folders/not-a-uuid"},
		{name: "order", method: http.MethodPut, path: "/api/v1/folders/order", body: `{"project_id":"not-a-uuid","folder_ids":[]}`},
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

func TestFolderRoutesRequireAuth(t *testing.T) {
	f := newTeamsFixture(t)

	id := uuid.New().String()
	tests := []struct {
		name   string
		method string
		path   string
	}{
		{name: "create", method: http.MethodPost, path: "/api/v1/projects/" + id + "/folders"},
		{name: "list", method: http.MethodGet, path: "/api/v1/projects/" + id + "/folders"},
		{name: "rename", method: http.MethodPatch, path: "/api/v1/folders/" + id},
		{name: "move", method: http.MethodPost, path: "/api/v1/folders/" + id + "/move"},
		{name: "delete", method: http.MethodDelete, path: "/api/v1/folders/" + id},
		{name: "order", method: http.MethodPut, path: "/api/v1/folders/order"},
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
