package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func (f teamsFixture) postRequest(t *testing.T, token, projectID string, fields map[string]any) *httptest.ResponseRecorder {
	t.Helper()

	return do(t, f.handler, http.MethodPost, "/api/v1/projects/"+projectID+"/requests", folderBody(t, fields), token)
}

// createRequest creates a request and fails unless it succeeds.
func (f teamsFixture) createRequest(t *testing.T, token, projectID string, fields map[string]any) requestResponse {
	t.Helper()

	rec := f.postRequest(t, token, projectID, fields)
	if rec.Code != http.StatusCreated {
		t.Fatalf("creating request %v: status = %d, want %d (body: %s)", fields, rec.Code, http.StatusCreated, rec.Body)
	}

	return decodeRequest(t, rec)
}

func decodeRequest(t *testing.T, rec *httptest.ResponseRecorder) requestResponse {
	t.Helper()

	var request requestResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &request); err != nil {
		t.Fatalf("decoding request response: %v (body: %s)", err, rec.Body)
	}

	return request
}

func (f teamsFixture) listRequests(t *testing.T, token, projectID string) []requestResponse {
	t.Helper()

	rec := do(t, f.handler, http.MethodGet, "/api/v1/projects/"+projectID+"/requests", "", token)
	if rec.Code != http.StatusOK {
		t.Fatalf("listing requests: status = %d, want %d (body: %s)", rec.Code, http.StatusOK, rec.Body)
	}

	var requests []requestResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &requests); err != nil {
		t.Fatalf("decoding request list: %v", err)
	}

	return requests
}

// requestPlacement renders a list as "name@folder:sort_order" entries, with
// the root written as "root", to compare placement at a glance.
func requestPlacement(requests []requestResponse, folderNames map[string]string) string {
	parts := make([]string, 0, len(requests))
	for _, request := range requests {
		folder := "root"
		if request.FolderID != nil {
			folder = folderNames[*request.FolderID]
		}
		parts = append(parts, request.Name+"@"+folder+":"+strconv.Itoa(int(request.SortOrder)))
	}
	return strings.Join(parts, ",")
}

func (f teamsFixture) moveRequest(t *testing.T, token, requestID string, fields map[string]any) *httptest.ResponseRecorder {
	t.Helper()

	return do(t, f.handler, http.MethodPost, "/api/v1/requests/"+requestID+"/move", folderBody(t, fields), token)
}

func (f teamsFixture) putRequestOrder(t *testing.T, token string, fields map[string]any) *httptest.ResponseRecorder {
	t.Helper()

	return do(t, f.handler, http.MethodPut, "/api/v1/requests/order", folderBody(t, fields), token)
}

func TestRequestCRUDRoundtrip(t *testing.T) {
	f, owner, _, project := newFolderProject(t, "request-crud")
	folder := f.createFolder(t, owner.Token, project.ID, "Auth", nil)

	// A bare request gets the defaults: GET, empty url, the project root.
	bare := f.createRequest(t, owner.Token, project.ID, map[string]any{"name": "Untitled"})
	if bare.Method != "GET" || bare.URL != "" || bare.FolderID != nil || bare.SortOrder != 0 {
		t.Errorf("bare request = %+v, want GET, empty url, root, sort_order 0", bare)
	}
	if bare.ProjectID != project.ID {
		t.Errorf("project_id = %s, want %s", bare.ProjectID, project.ID)
	}
	if !bare.UpdatedAt.Equal(bare.CreatedAt) {
		t.Errorf("fresh request: updated_at %v != created_at %v", bare.UpdatedAt, bare.CreatedAt)
	}

	// The url is opaque: {{vars}} and anything else pass through untouched.
	login := f.createRequest(t, owner.Token, project.ID, map[string]any{
		"name": "Login", "method": "POST", "url": "{{base_url}}/auth/login?x={{y}}", "folder_id": folder.ID,
	})
	if login.Method != "POST" || login.URL != "{{base_url}}/auth/login?x={{y}}" {
		t.Errorf("login = %+v, want POST with the url as given", login)
	}
	if login.FolderID == nil || *login.FolderID != folder.ID {
		t.Errorf("folder_id = %v, want %s", login.FolderID, folder.ID)
	}

	t.Run("single GET includes headers, query_params and body", func(t *testing.T) {
		rec := do(t, f.handler, http.MethodGet, "/api/v1/requests/"+login.ID, "", owner.Token)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d (body: %s)", rec.Code, http.StatusOK, rec.Body)
		}

		var detail struct {
			requestResponse
			Headers     json.RawMessage `json:"headers"`
			QueryParams json.RawMessage `json:"query_params"`
			Body        json.RawMessage `json:"body"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &detail); err != nil {
			t.Fatalf("decoding request detail: %v", err)
		}
		if string(detail.Headers) != "[]" || string(detail.QueryParams) != "[]" {
			t.Errorf("headers = %s, query_params = %s, want [] and []", detail.Headers, detail.QueryParams)
		}
		if compact(t, detail.Body) != `{"type":"none"}` {
			t.Errorf("body = %s, want {\"type\":\"none\"}", detail.Body)
		}
		if detail.Name != "Login" || detail.Method != "POST" {
			t.Errorf("detail = %+v, want the login request", detail.requestResponse)
		}
	})

	t.Run("PATCH updates and DELETE removes", func(t *testing.T) {
		rec := do(t, f.handler, http.MethodPatch, "/api/v1/requests/"+bare.ID, `{"name":"Health","url":"/healthz"}`, owner.Token)
		if rec.Code != http.StatusOK {
			t.Fatalf("patch: status = %d, want %d (body: %s)", rec.Code, http.StatusOK, rec.Body)
		}
		if got := decodeRequest(t, rec); got.Name != "Health" || got.URL != "/healthz" {
			t.Errorf("patched = %+v, want Health /healthz", got)
		}

		if del := do(t, f.handler, http.MethodDelete, "/api/v1/requests/"+bare.ID, "", owner.Token); del.Code != http.StatusNoContent {
			t.Fatalf("delete: status = %d, want %d (body: %s)", del.Code, http.StatusNoContent, del.Body)
		}
		if get := do(t, f.handler, http.MethodGet, "/api/v1/requests/"+bare.ID, "", owner.Token); get.Code != http.StatusNotFound {
			t.Errorf("GET after delete: status = %d, want %d", get.Code, http.StatusNotFound)
		}
	})
}

func compact(t *testing.T, raw json.RawMessage) string {
	t.Helper()

	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		t.Fatalf("decoding json %s: %v", raw, err)
	}
	out, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("encoding json: %v", err)
	}
	return string(out)
}

func TestRequestPatchMergesPartially(t *testing.T) {
	f, owner, _, project := newFolderProject(t, "request-patch")
	request := f.createRequest(t, owner.Token, project.ID, map[string]any{
		"name": "Users", "method": "GET", "url": "https://api.test/users",
	})

	patch := func(t *testing.T, body string) requestResponse {
		t.Helper()
		rec := do(t, f.handler, http.MethodPatch, "/api/v1/requests/"+request.ID, body, owner.Token)
		if rec.Code != http.StatusOK {
			t.Fatalf("patch %s: status = %d, want %d (body: %s)", body, rec.Code, http.StatusOK, rec.Body)
		}
		return decodeRequest(t, rec)
	}

	// Like the variables endpoint: an absent field keeps its value.
	onlyMethod := patch(t, `{"method":"DELETE"}`)
	if onlyMethod.Method != "DELETE" || onlyMethod.Name != "Users" || onlyMethod.URL != "https://api.test/users" {
		t.Errorf("after method-only patch = %+v, want name and url kept", onlyMethod)
	}
	if !onlyMethod.UpdatedAt.After(request.UpdatedAt) {
		t.Errorf("updated_at %v did not advance past %v", onlyMethod.UpdatedAt, request.UpdatedAt)
	}
	if !onlyMethod.CreatedAt.Equal(request.CreatedAt) {
		t.Errorf("created_at changed from %v to %v", request.CreatedAt, onlyMethod.CreatedAt)
	}

	// An empty url is a value, not "keep".
	if cleared := patch(t, `{"url":""}`); cleared.URL != "" || cleared.Method != "DELETE" {
		t.Errorf("after url clear = %+v, want empty url and DELETE kept", cleared)
	}

	// Null is the same as absent here: none of these columns can be null.
	if renamed := patch(t, `{"name":"Delete user","url":null}`); renamed.Name != "Delete user" || renamed.URL != "" {
		t.Errorf("after rename = %+v, want new name, url kept", renamed)
	}

	for _, body := range []string{`{}`, `{"url":null}`} {
		t.Run("nothing to change "+body, func(t *testing.T) {
			rec := do(t, f.handler, http.MethodPatch, "/api/v1/requests/"+request.ID, body, owner.Token)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want %d (body: %s)", rec.Code, http.StatusBadRequest, rec.Body)
			}
		})
	}
}

func TestRequestValidation(t *testing.T) {
	f, owner, team, project := newFolderProject(t, "request-validation")
	request := f.createRequest(t, owner.Token, project.ID, map[string]any{"name": "R"})

	other := f.createProject(t, owner.Token, team.ID, "Other")
	foreignFolder := f.createFolder(t, owner.Token, other.ID, "Foreign", nil)

	createPath := "/api/v1/projects/" + project.ID + "/requests"
	tests := []struct {
		name   string
		method string
		path   string
		body   string
	}{
		{name: "lowercase method", method: http.MethodPost, path: createPath, body: `{"name":"x","method":"get"}`},
		{name: "unsupported method", method: http.MethodPost, path: createPath, body: `{"name":"x","method":"CONNECT"}`},
		{name: "empty method", method: http.MethodPost, path: createPath, body: `{"name":"x","method":""}`},
		{name: "empty name", method: http.MethodPost, path: createPath, body: `{"name":"  "}`},
		{name: "name too long", method: http.MethodPost, path: createPath, body: `{"name":"` + strings.Repeat("n", 101) + `"}`},
		{name: "url too long", method: http.MethodPost, path: createPath, body: `{"name":"x","url":"` + strings.Repeat("u", 8193) + `"}`},
		{name: "folder from another project", method: http.MethodPost, path: createPath,
			body: `{"name":"x","folder_id":"` + foreignFolder.ID + `"}`},
		{name: "folder does not exist", method: http.MethodPost, path: createPath,
			body: `{"name":"x","folder_id":"` + uuid.New().String() + `"}`},
		{name: "folder not a uuid", method: http.MethodPost, path: createPath, body: `{"name":"x","folder_id":"nope"}`},
		{name: "headers are not writable", method: http.MethodPost, path: createPath, body: `{"name":"x","headers":[]}`},
		{name: "patch lowercase method", method: http.MethodPatch, path: "/api/v1/requests/" + request.ID, body: `{"method":"post"}`},
		{name: "patch url too long", method: http.MethodPatch, path: "/api/v1/requests/" + request.ID,
			body: `{"url":"` + strings.Repeat("u", 8193) + `"}`},
		{name: "patch body is not writable", method: http.MethodPatch, path: "/api/v1/requests/" + request.ID, body: `{"body":{"type":"raw"}}`},
		{name: "create bad project id", method: http.MethodPost, path: "/api/v1/projects/not-a-uuid/requests", body: `{"name":"x"}`},
		{name: "list bad project id", method: http.MethodGet, path: "/api/v1/projects/not-a-uuid/requests"},
		{name: "get bad id", method: http.MethodGet, path: "/api/v1/requests/not-a-uuid"},
		{name: "patch bad id", method: http.MethodPatch, path: "/api/v1/requests/not-a-uuid", body: `{"name":"x"}`},
		{name: "move bad id", method: http.MethodPost, path: "/api/v1/requests/not-a-uuid/move", body: `{"folder_id":null}`},
		{name: "delete bad id", method: http.MethodDelete, path: "/api/v1/requests/not-a-uuid"},
		{name: "order bad project id", method: http.MethodPut, path: "/api/v1/requests/order", body: `{"project_id":"nope","request_ids":[]}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := do(t, f.handler, tt.method, tt.path, tt.body, owner.Token)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want %d (body: %s)", rec.Code, http.StatusBadRequest, rec.Body)
			}
		})
	}

	t.Run("every allowed method and the longest url are accepted", func(t *testing.T) {
		for _, method := range []string{"GET", "POST", "PUT", "PATCH", "DELETE", "HEAD", "OPTIONS"} {
			f.createRequest(t, owner.Token, project.ID, map[string]any{"name": method, "method": method})
		}
		f.createRequest(t, owner.Token, project.ID, map[string]any{"name": "long", "url": strings.Repeat("u", 8192)})
	})

	// Only the one valid request plus the accepted ones exist: nothing
	// rejected above was written.
	if got := len(f.listRequests(t, owner.Token, project.ID)); got != 1+7+1 {
		t.Errorf("requests in project = %d, want 9", got)
	}
}

func TestMoveRequest(t *testing.T) {
	f, owner, team, project := newFolderProject(t, "request-move")
	fA := f.createFolder(t, owner.Token, project.ID, "A", nil)
	fB := f.createFolder(t, owner.Token, project.ID, "B", &fA.ID)
	names := map[string]string{fA.ID: "A", fB.ID: "B"}

	r1 := f.createRequest(t, owner.Token, project.ID, map[string]any{"name": "R1"})
	f.createRequest(t, owner.Token, project.ID, map[string]any{"name": "R2", "folder_id": fA.ID})

	moveOK := func(t *testing.T, requestID string, fields map[string]any) requestResponse {
		t.Helper()
		rec := f.moveRequest(t, owner.Token, requestID, fields)
		if rec.Code != http.StatusOK {
			t.Fatalf("move %v: status = %d, want %d (body: %s)", fields, rec.Code, http.StatusOK, rec.Body)
		}
		return decodeRequest(t, rec)
	}

	t.Run("root into a folder appends after its requests", func(t *testing.T) {
		moved := moveOK(t, r1.ID, map[string]any{"folder_id": fA.ID})
		if moved.FolderID == nil || *moved.FolderID != fA.ID || moved.SortOrder != 1 {
			t.Errorf("moved = %+v, want in A at 1 (after R2)", moved)
		}
		if !moved.UpdatedAt.After(r1.UpdatedAt) {
			t.Errorf("updated_at %v did not advance past %v", moved.UpdatedAt, r1.UpdatedAt)
		}
	})

	t.Run("between folders with an explicit position", func(t *testing.T) {
		moved := moveOK(t, r1.ID, map[string]any{"folder_id": fB.ID, "sort_order": 5})
		if moved.FolderID == nil || *moved.FolderID != fB.ID || moved.SortOrder != 5 {
			t.Errorf("moved = %+v, want in B at 5", moved)
		}
	})

	t.Run("back to the root", func(t *testing.T) {
		moved := moveOK(t, r1.ID, map[string]any{"folder_id": nil})
		if moved.FolderID != nil || moved.SortOrder != 0 {
			t.Errorf("moved = %+v, want at the root at 0", moved)
		}
	})

	t.Run("refused moves", func(t *testing.T) {
		other := f.createProject(t, owner.Token, team.ID, "Other")
		foreign := f.createFolder(t, owner.Token, other.ID, "Foreign", nil)

		for name, body := range map[string]string{
			"folder from another project": `{"folder_id":"` + foreign.ID + `"}`,
			"folder does not exist":       `{"folder_id":"` + uuid.New().String() + `"}`,
			"folder_id missing":           `{}`,
			"negative sort_order":         `{"folder_id":null,"sort_order":-1}`,
		} {
			t.Run(name, func(t *testing.T) {
				rec := do(t, f.handler, http.MethodPost, "/api/v1/requests/"+r1.ID+"/move", body, owner.Token)
				if rec.Code != http.StatusBadRequest {
					t.Fatalf("status = %d, want %d (body: %s)", rec.Code, http.StatusBadRequest, rec.Body)
				}
			})
		}
	})

	if got := requestPlacement(f.listRequests(t, owner.Token, project.ID), names); got != "R1@root:0,R2@A:0" {
		t.Errorf("placement = %s, want R1@root:0,R2@A:0", got)
	}
}

func TestDeletingFoldersCascadesToRequests(t *testing.T) {
	f, owner, _, project := newFolderProject(t, "request-cascade")
	fA := f.createFolder(t, owner.Token, project.ID, "A", nil)
	fB := f.createFolder(t, owner.Token, project.ID, "B", &fA.ID)
	fC := f.createFolder(t, owner.Token, project.ID, "C", nil)

	f.createRequest(t, owner.Token, project.ID, map[string]any{"name": "in A", "folder_id": fA.ID})
	f.createRequest(t, owner.Token, project.ID, map[string]any{"name": "in B", "folder_id": fB.ID})
	f.createRequest(t, owner.Token, project.ID, map[string]any{"name": "in C", "folder_id": fC.ID})
	f.createRequest(t, owner.Token, project.ID, map[string]any{"name": "at root"})

	// Deleting A takes B with it, and the requests in both.
	if rec := do(t, f.handler, http.MethodDelete, "/api/v1/folders/"+fA.ID, "", owner.Token); rec.Code != http.StatusNoContent {
		t.Fatalf("delete folder: status = %d, want %d (body: %s)", rec.Code, http.StatusNoContent, rec.Body)
	}

	var left []string
	for _, request := range f.listRequests(t, owner.Token, project.ID) {
		left = append(left, request.Name)
	}
	if got := strings.Join(left, ","); got != "at root,in C" {
		t.Errorf("requests left = %s, want at root,in C", got)
	}

	if rec := do(t, f.handler, http.MethodDelete, "/api/v1/projects/"+project.ID, "", owner.Token); rec.Code != http.StatusNoContent {
		t.Fatalf("delete project: status = %d, want %d (body: %s)", rec.Code, http.StatusNoContent, rec.Body)
	}
	if count := f.countRows(t, "SELECT count(*) FROM requests"); count != 0 {
		t.Errorf("requests after project delete = %d, want 0", count)
	}
}

func TestRequestOrder(t *testing.T) {
	f, owner, team, project := newFolderProject(t, "request-order")
	folder := f.createFolder(t, owner.Token, project.ID, "F", nil)
	names := map[string]string{folder.ID: "F"}

	a := f.createRequest(t, owner.Token, project.ID, map[string]any{"name": "a", "folder_id": folder.ID})
	b := f.createRequest(t, owner.Token, project.ID, map[string]any{"name": "b", "folder_id": folder.ID})
	c := f.createRequest(t, owner.Token, project.ID, map[string]any{"name": "c", "folder_id": folder.ID})
	root1 := f.createRequest(t, owner.Token, project.ID, map[string]any{"name": "x"})
	root2 := f.createRequest(t, owner.Token, project.ID, map[string]any{"name": "y"})

	other := f.createProject(t, owner.Token, team.ID, "Other")
	foreignFolder := f.createFolder(t, owner.Token, other.ID, "Foreign", nil)

	scoped := func(folderID any, ids ...string) map[string]any {
		return map[string]any{"project_id": project.ID, "folder_id": folderID, "request_ids": ids}
	}

	t.Run("wrong sibling sets", func(t *testing.T) {
		for name, fields := range map[string]map[string]any{
			"missing one":                 scoped(folder.ID, a.ID, b.ID),
			"includes a root request":     scoped(folder.ID, a.ID, b.ID, c.ID, root1.ID),
			"duplicate":                   scoped(folder.ID, a.ID, b.ID, b.ID),
			"empty":                       scoped(folder.ID),
			"folder from another project": scoped(foreignFolder.ID),
			"not a uuid":                  scoped(folder.ID, "nope"),
		} {
			t.Run(name, func(t *testing.T) {
				if rec := f.putRequestOrder(t, owner.Token, fields); rec.Code != http.StatusBadRequest {
					t.Fatalf("status = %d, want %d (body: %s)", rec.Code, http.StatusBadRequest, rec.Body)
				}
			})
		}
	})

	t.Run("folder scope, 0-based and contiguous", func(t *testing.T) {
		if rec := f.putRequestOrder(t, owner.Token, scoped(folder.ID, c.ID, a.ID, b.ID)); rec.Code != http.StatusNoContent {
			t.Fatalf("status = %d, want %d (body: %s)", rec.Code, http.StatusNoContent, rec.Body)
		}
	})

	t.Run("root scope leaves the folder alone", func(t *testing.T) {
		if rec := f.putRequestOrder(t, owner.Token, scoped(nil, root2.ID, root1.ID)); rec.Code != http.StatusNoContent {
			t.Fatalf("status = %d, want %d (body: %s)", rec.Code, http.StatusNoContent, rec.Body)
		}
	})

	if got := requestPlacement(f.listRequests(t, owner.Token, project.ID), names); got != "y@root:0,x@root:1,c@F:0,a@F:1,b@F:2" {
		t.Errorf("placement = %s, want y@root:0,x@root:1,c@F:0,a@F:1,b@F:2", got)
	}
}

func TestListRequestsSpansAllFolders(t *testing.T) {
	f, owner, _, project := newFolderProject(t, "request-list")
	fA := f.createFolder(t, owner.Token, project.ID, "A", nil)
	fB := f.createFolder(t, owner.Token, project.ID, "B", &fA.ID)
	names := map[string]string{fA.ID: "A", fB.ID: "B"}

	f.createRequest(t, owner.Token, project.ID, map[string]any{"name": "deep", "folder_id": fB.ID})
	f.createRequest(t, owner.Token, project.ID, map[string]any{"name": "top"})
	f.createRequest(t, owner.Token, project.ID, map[string]any{"name": "mid", "folder_id": fA.ID})

	// Root first; the order of folder groups is by folder id, so compare as a
	// set of placements after the root.
	got := requestPlacement(f.listRequests(t, owner.Token, project.ID), names)
	if !strings.HasPrefix(got, "top@root:0,") || !strings.Contains(got, "mid@A:0") || !strings.Contains(got, "deep@B:0") {
		t.Errorf("placement = %s, want all three requests with their folders", got)
	}

	rec := do(t, f.handler, http.MethodGet, "/api/v1/projects/"+project.ID+"/requests", "", owner.Token)
	for _, field := range []string{`"headers"`, `"query_params"`, `"body"`} {
		if strings.Contains(rec.Body.String(), field) {
			t.Errorf("list response carries %s; it is only for GET /requests/{id}", field)
		}
	}
}

// TestRequestAccessMatrix reuses the project access fixture: every endpoint
// answers 404 to anyone who cannot reach the project, with the same message a
// nonexistent id gets, and works for anyone who can.
func TestRequestAccessMatrix(t *testing.T) {
	f := newAccessMatrixFixture(t)

	hidden := f.createRequest(t, f.owner.Token, f.ownerProject.ID, map[string]any{"name": "hidden", "url": "https://secret.test"})

	type request struct {
		name, method, path, body, message string
	}

	denied := func(projectID, requestID string) []request {
		return []request{
			{"create", http.MethodPost, "/api/v1/projects/" + projectID + "/requests", `{"name":"x"}`, messageProjectNotFound},
			{"list", http.MethodGet, "/api/v1/projects/" + projectID + "/requests", "", messageProjectNotFound},
			{"order", http.MethodPut, "/api/v1/requests/order",
				`{"project_id":"` + projectID + `","folder_id":null,"request_ids":["` + requestID + `"]}`, messageProjectNotFound},
			{"get", http.MethodGet, "/api/v1/requests/" + requestID, "", messageRequestNotFound},
			{"patch", http.MethodPatch, "/api/v1/requests/" + requestID, `{"name":"x"}`, messageRequestNotFound},
			{"move", http.MethodPost, "/api/v1/requests/" + requestID + "/move", `{"folder_id":null}`, messageRequestNotFound},
			{"delete", http.MethodDelete, "/api/v1/requests/" + requestID, "", messageRequestNotFound},
		}
	}

	check := func(t *testing.T, token string, reqs []request) {
		t.Helper()
		for _, req := range reqs {
			t.Run(req.name, func(t *testing.T) {
				rec := do(t, f.handler, req.method, req.path, req.body, token)
				if rec.Code != http.StatusNotFound {
					t.Fatalf("status = %d, want %d (body: %s)", rec.Code, http.StatusNotFound, rec.Body)
				}
				if msg := decodeEnvelope(t, rec).Error.Message; msg != req.message {
					t.Errorf("message = %q, want %q", msg, req.message)
				}
			})
		}
	}

	t.Run("restricted member without a grant", func(t *testing.T) {
		check(t, f.limited.Token, denied(f.ownerProject.ID, hidden.ID))
	})
	t.Run("outsider", func(t *testing.T) {
		check(t, f.outsider.Token, denied(f.ownerProject.ID, hidden.ID))
	})
	t.Run("nonexistent ids look the same", func(t *testing.T) {
		check(t, f.owner.Token, denied(uuid.New().String(), uuid.New().String()))
	})

	// None of the denied calls changed anything.
	if requests := f.listRequests(t, f.owner.Token, f.ownerProject.ID); len(requests) != 1 || requests[0].Name != "hidden" {
		t.Errorf("owner project requests = %+v, want the untouched hidden request", requests)
	}

	t.Run("any member with access has full rights", func(t *testing.T) {
		token := f.limited.Token
		projectID := f.grantedProject.ID
		folder := f.createFolder(t, token, projectID, "Mine", nil)

		created := f.createRequest(t, token, projectID, map[string]any{"name": "mine"})
		for _, step := range []struct {
			name   string
			rec    *httptest.ResponseRecorder
			status int
		}{
			{"get", do(t, f.handler, http.MethodGet, "/api/v1/requests/"+created.ID, "", token), http.StatusOK},
			{"patch", do(t, f.handler, http.MethodPatch, "/api/v1/requests/"+created.ID, `{"method":"PUT"}`, token), http.StatusOK},
			{"move", f.moveRequest(t, token, created.ID, map[string]any{"folder_id": folder.ID}), http.StatusOK},
			{"order", f.putRequestOrder(t, token, map[string]any{
				"project_id": projectID, "folder_id": folder.ID, "request_ids": []string{created.ID}}), http.StatusNoContent},
			{"delete", do(t, f.handler, http.MethodDelete, "/api/v1/requests/"+created.ID, "", token), http.StatusNoContent},
		} {
			if step.rec.Code != step.status {
				t.Errorf("%s: status = %d, want %d (body: %s)", step.name, step.rec.Code, step.status, step.rec.Body)
			}
		}
	})
}

func TestRequestRoutesRequireAuth(t *testing.T) {
	f := newTeamsFixture(t)

	id := uuid.New().String()
	tests := []struct {
		method string
		path   string
	}{
		{http.MethodPost, "/api/v1/projects/" + id + "/requests"},
		{http.MethodGet, "/api/v1/projects/" + id + "/requests"},
		{http.MethodPut, "/api/v1/requests/order"},
		{http.MethodGet, "/api/v1/requests/" + id},
		{http.MethodPatch, "/api/v1/requests/" + id},
		{http.MethodDelete, "/api/v1/requests/" + id},
		{http.MethodPost, "/api/v1/requests/" + id + "/move"},
	}

	for _, tt := range tests {
		t.Run(tt.method+" "+tt.path, func(t *testing.T) {
			rec := do(t, f.handler, tt.method, tt.path, "", "")
			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want %d (body: %s)", rec.Code, http.StatusUnauthorized, rec.Body)
			}
		})
	}
}
