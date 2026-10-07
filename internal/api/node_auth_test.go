package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

// authTarget is one resource carrying auth: how to PATCH it, and how to read
// its stored auth back through each shape that carries it.
type authTarget struct {
	name  string
	patch string
	reads map[string]func(t *testing.T) nodeAuthResponse
}

func newAuthTargets(t *testing.T, f teamsFixture, token, projectID string) []authTarget {
	t.Helper()

	folder := f.createFolder(t, token, projectID, "Secured", nil)
	request := f.createRequest(t, token, projectID, map[string]any{"name": "Me", "folder_id": folder.ID})

	return []authTarget{
		{
			name:  "folder",
			patch: "/api/v1/folders/" + folder.ID,
			reads: map[string]func(t *testing.T) nodeAuthResponse{
				"folder list": func(t *testing.T) nodeAuthResponse {
					t.Helper()
					for _, entry := range f.listFolders(t, token, projectID) {
						if entry.ID == folder.ID {
							return entry.Auth
						}
					}
					t.Fatalf("folder %s missing from the folder list", folder.ID)
					return nodeAuthResponse{}
				},
			},
		},
		{
			name:  "request",
			patch: "/api/v1/requests/" + request.ID,
			reads: map[string]func(t *testing.T) nodeAuthResponse{
				"request list": func(t *testing.T) nodeAuthResponse {
					t.Helper()
					for _, entry := range f.listRequests(t, token, projectID) {
						if entry.ID == request.ID {
							return entry.Auth
						}
					}
					t.Fatalf("request %s missing from the request list", request.ID)
					return nodeAuthResponse{}
				},
				"GET /requests/{id}": func(t *testing.T) nodeAuthResponse {
					t.Helper()
					return f.getRequestDetail(t, token, request.ID).Auth
				},
			},
		},
	}
}

// patchAuth PATCHes {"auth": <auth>} and returns the auth from the response.
func (f teamsFixture) patchAuth(t *testing.T, token, path, auth string) nodeAuthResponse {
	t.Helper()

	rec := do(t, f.handler, http.MethodPatch, path, `{"auth":`+auth+`}`, token)
	if rec.Code != http.StatusOK {
		t.Fatalf("PATCH auth %s: status = %d, want %d (body: %s)", auth, rec.Code, http.StatusOK, rec.Body)
	}

	var body struct {
		Auth nodeAuthResponse `json:"auth"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decoding PATCH response: %v", err)
	}
	return body.Auth
}

// checkAuthEverywhere reads the target's auth through every shape and
// compares it with want.
func checkAuthEverywhere(t *testing.T, target authTarget, want nodeAuthResponse) {
	t.Helper()

	for shape, read := range target.reads {
		if got := read(t); got != want {
			t.Errorf("%s via %s = %+v, want %+v", target.name, shape, got, want)
		}
	}
}

var defaultAuth = nodeAuthResponse{Type: "inherit", APIKeyIn: "header"}

func TestAuthDefaults(t *testing.T) {
	f, owner, _, project := newFolderProject(t, "auth-defaults")

	folder := f.createFolder(t, owner.Token, project.ID, "F", nil)
	request := f.createRequest(t, owner.Token, project.ID, map[string]any{"name": "R"})
	if folder.Auth != defaultAuth || request.Auth != defaultAuth {
		t.Errorf("new folder auth = %+v, new request auth = %+v, want %+v", folder.Auth, request.Auth, defaultAuth)
	}

	for _, target := range newAuthTargets(t, f, owner.Token, project.ID) {
		checkAuthEverywhere(t, target, defaultAuth)
	}
}

func TestPatchAuthEachType(t *testing.T) {
	f, owner, _, project := newFolderProject(t, "auth-types")

	for _, target := range newAuthTargets(t, f, owner.Token, project.ID) {
		t.Run(target.name, func(t *testing.T) {
			// Each step builds on the stored state, so the expectations show
			// that fields of other types are kept, not cleared.
			want := defaultAuth
			steps := []struct {
				name  string
				patch string
				apply func(*nodeAuthResponse)
			}{
				{"bearer with a {{var}}", `{"type":"bearer","bearer_token":"{{token}}"}`, func(a *nodeAuthResponse) {
					a.Type, a.BearerToken = "bearer", "{{token}}"
				}},
				{"basic", `{"type":"basic","basic_username":"{{user}}","basic_password":"{{password}}"}`, func(a *nodeAuthResponse) {
					a.Type, a.BasicUsername, a.BasicPassword = "basic", "{{user}}", "{{password}}"
				}},
				{"partial merge inside auth", `{"type":"basic","basic_password":"{{other_password}}"}`, func(a *nodeAuthResponse) {
					a.BasicPassword = "{{other_password}}" // username kept
				}},
				{"api_key in query", `{"type":"api_key","api_key_name":"key","api_key_value":"{{api_key}}","api_key_in":"query"}`, func(a *nodeAuthResponse) {
					a.Type, a.APIKeyName, a.APIKeyValue, a.APIKeyIn = "api_key", "key", "{{api_key}}", "query"
				}},
				{"none stops the chain", `{"type":"none"}`, func(a *nodeAuthResponse) {
					a.Type = "none"
				}},
				{"back to bearer restores its token", `{"type":"bearer"}`, func(a *nodeAuthResponse) {
					a.Type = "bearer" // bearer_token is still "{{token}}"
				}},
				{"an empty string clears one field", `{"type":"bearer","bearer_token":""}`, func(a *nodeAuthResponse) {
					a.BearerToken = ""
				}},
				{"inherit", `{"type":"inherit"}`, func(a *nodeAuthResponse) {
					a.Type = "inherit"
				}},
			}

			for _, step := range steps {
				step.apply(&want)
				if got := f.patchAuth(t, owner.Token, target.patch, step.patch); got != want {
					t.Errorf("%s: PATCH response auth = %+v, want %+v", step.name, got, want)
				}
				checkAuthEverywhere(t, target, want)
			}

			// After all that, basic and api_key values are still on file.
			if want.BasicUsername != "{{user}}" || want.APIKeyValue != "{{api_key}}" {
				t.Fatalf("test bookkeeping: preserved fields lost: %+v", want)
			}
		})
	}
}

func TestPatchAuthValidation(t *testing.T) {
	f, owner, _, project := newFolderProject(t, "auth-validation")

	tests := []struct {
		name    string
		auth    string
		message string
	}{
		{"not an object", `"bearer"`, "auth must be an object"},
		{"an array", `[]`, "auth must be an object"},
		{"type missing", `{"bearer_token":"x"}`, "auth.type is required"},
		{"type unknown", `{"type":"oauth2"}`, "auth.type must be one of inherit, none, bearer, basic, api_key"},
		{"type wrong case", `{"type":"Bearer"}`, "auth.type must be one of"},
		{"type not a string", `{"type":1}`, "auth.type must be a string"},
		{"api_key_in unknown", `{"type":"api_key","api_key_in":"cookie"}`, "auth.api_key_in must be one of header, query"},
		{"unknown field", `{"type":"bearer","scope":"read"}`, "auth.scope is not a known field"},
		{"field null", `{"type":"bearer","bearer_token":null}`, "auth.bearer_token must be a string"},
		{"field not a string", `{"type":"basic","basic_password":42}`, "auth.basic_password must be a string"},
		{"value over 8192", `{"type":"bearer","bearer_token":"` + strings.Repeat("t", 8193) + `"}`,
			"auth.bearer_token must be at most 8192 characters"},
	}

	for _, target := range newAuthTargets(t, f, owner.Token, project.ID) {
		t.Run(target.name, func(t *testing.T) {
			for _, tt := range tests {
				rec := do(t, f.handler, http.MethodPatch, target.patch, `{"auth":`+tt.auth+`}`, owner.Token)
				if rec.Code != http.StatusBadRequest {
					t.Errorf("%s: status = %d, want %d (body: %.200s)", tt.name, rec.Code, http.StatusBadRequest, rec.Body)
					continue
				}
				if msg := decodeEnvelope(t, rec).Error.Message; !strings.HasPrefix(msg, tt.message) {
					t.Errorf("%s: message = %q, want it to start with %q", tt.name, msg, tt.message)
				}
			}

			// Nothing refused above was written.
			checkAuthEverywhere(t, target, defaultAuth)

			// The limit itself is fine.
			longest := strings.Repeat("v", 8192)
			if got := f.patchAuth(t, owner.Token, target.patch, `{"type":"api_key","api_key_value":"`+longest+`"}`); got.APIKeyValue != longest {
				t.Errorf("8192-character value not stored whole")
			}

			// A null auth alone is nothing to change; absent auth leaves it.
			if rec := do(t, f.handler, http.MethodPatch, target.patch, `{"auth":null}`, owner.Token); rec.Code != http.StatusBadRequest {
				t.Errorf("auth null alone: status = %d, want %d", rec.Code, http.StatusBadRequest)
			}
			if rec := do(t, f.handler, http.MethodPatch, target.patch, `{"name":"Renamed"}`, owner.Token); rec.Code != http.StatusOK {
				t.Fatalf("rename: status = %d (body: %s)", rec.Code, rec.Body)
			}
			for shape, read := range target.reads {
				if got := read(t); got.Type != "api_key" || got.APIKeyValue != longest {
					t.Errorf("after a rename, %s auth changed: type %q", shape, got.Type)
				}
			}
		})
	}
}

func TestAuthBumpsRequestUpdatedAt(t *testing.T) {
	f, owner, _, project := newFolderProject(t, "auth-updated-at")
	request := f.createRequest(t, owner.Token, project.ID, map[string]any{"name": "R"})

	time.Sleep(2 * time.Millisecond)
	patched := f.mustPatchRequest(t, owner.Token, request.ID, `{"auth":{"type":"none"}}`)
	if !patched.UpdatedAt.After(request.UpdatedAt) {
		t.Errorf("updated_at %v did not advance past %v", patched.UpdatedAt, request.UpdatedAt)
	}
}

func TestCreateRejectsAuth(t *testing.T) {
	f, owner, _, project := newFolderProject(t, "auth-create")

	for name, path := range map[string]string{
		"folder":  "/api/v1/projects/" + project.ID + "/folders",
		"request": "/api/v1/projects/" + project.ID + "/requests",
	} {
		rec := do(t, f.handler, http.MethodPost, path, `{"name":"x","auth":{"type":"none"}}`, owner.Token)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s create: status = %d, want %d (body: %s)", name, rec.Code, http.StatusBadRequest, rec.Body)
		}
	}
}

func TestAuthAccess(t *testing.T) {
	f := newAccessMatrixFixture(t)

	folder := f.createFolder(t, f.owner.Token, f.ownerProject.ID, "Hidden", nil)
	request := f.createRequest(t, f.owner.Token, f.ownerProject.ID, map[string]any{"name": "hidden"})
	f.patchAuth(t, f.owner.Token, "/api/v1/requests/"+request.ID, `{"type":"bearer","bearer_token":"{{token}}"}`)

	for _, token := range []string{f.limited.Token, f.outsider.Token} {
		for path, message := range map[string]string{
			"/api/v1/folders/" + folder.ID:   messageFolderNotFound,
			"/api/v1/requests/" + request.ID: messageRequestNotFound,
		} {
			rec := do(t, f.handler, http.MethodPatch, path, `{"auth":{"type":"none"}}`, token)
			if rec.Code != http.StatusNotFound {
				t.Errorf("PATCH %s: status = %d, want %d", path, rec.Code, http.StatusNotFound)
				continue
			}
			if msg := decodeEnvelope(t, rec).Error.Message; msg != message {
				t.Errorf("PATCH %s: message = %q, want %q", path, msg, message)
			}
		}

		// Nor can they read it: the request is invisible to them.
		if rec := do(t, f.handler, http.MethodGet, "/api/v1/requests/"+request.ID, "", token); rec.Code != http.StatusNotFound {
			t.Errorf("GET hidden request: status = %d, want %d", rec.Code, http.StatusNotFound)
		}
	}

	if got := f.getRequestDetail(t, f.owner.Token, request.ID).Auth; got.Type != "bearer" || got.BearerToken != "{{token}}" {
		t.Errorf("hidden request auth = %+v after denied patches", got)
	}
}

func TestAuthCheckConstraints(t *testing.T) {
	f, owner, _, project := newFolderProject(t, "auth-check")
	folder := f.createFolder(t, owner.Token, project.ID, "F", nil)
	request := f.createRequest(t, owner.Token, project.ID, map[string]any{"name": "R"})

	for table, id := range map[string]string{"folders": folder.ID, "requests": request.ID} {
		for column, constraint := range map[string]string{
			"auth_type":       table + "_auth_type",
			"auth_api_key_in": table + "_auth_api_key_in",
		} {
			_, err := f.pool.Exec(t.Context(), "UPDATE "+table+" SET "+column+" = 'bogus' WHERE id = $1", id)

			var pgErr *pgconn.PgError
			if !errors.As(err, &pgErr) || pgErr.Code != checkViolation || pgErr.ConstraintName != constraint {
				t.Errorf("%s.%s: error = %v, want a CHECK violation of %s", table, column, err, constraint)
			}
		}
	}
}
