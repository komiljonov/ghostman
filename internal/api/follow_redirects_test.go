package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

// followRedirectsTarget is one resource carrying the setting: how to PATCH
// it, and how to read the stored value back through each shape that carries
// it.
type followRedirectsTarget struct {
	name  string
	patch string // path of the PATCH endpoint
	reads map[string]func(t *testing.T) string
}

func newFollowRedirectsTargets(t *testing.T, f teamsFixture, token, projectID string) []followRedirectsTarget {
	t.Helper()

	folder := f.createFolder(t, token, projectID, "Auth", nil)
	request := f.createRequest(t, token, projectID, map[string]any{"name": "Login", "folder_id": folder.ID})

	findFolder := func(t *testing.T) string {
		t.Helper()
		for _, entry := range f.listFolders(t, token, projectID) {
			if entry.ID == folder.ID {
				return entry.FollowRedirects
			}
		}
		t.Fatalf("folder %s missing from the folder list", folder.ID)
		return ""
	}
	findRequest := func(t *testing.T) string {
		t.Helper()
		for _, entry := range f.listRequests(t, token, projectID) {
			if entry.ID == request.ID {
				return entry.FollowRedirects
			}
		}
		t.Fatalf("request %s missing from the request list", request.ID)
		return ""
	}

	return []followRedirectsTarget{
		{
			name:  "folder",
			patch: "/api/v1/folders/" + folder.ID,
			reads: map[string]func(t *testing.T) string{"folder list": findFolder},
		},
		{
			name:  "request",
			patch: "/api/v1/requests/" + request.ID,
			reads: map[string]func(t *testing.T) string{
				"request list": findRequest,
				"GET /requests/{id}": func(t *testing.T) string {
					t.Helper()
					return f.getRequestDetail(t, token, request.ID).FollowRedirects
				},
			},
		},
	}
}

func TestFollowRedirectsDefaultsToInherit(t *testing.T) {
	f, owner, _, project := newFolderProject(t, "redirects-default")

	folder := f.createFolder(t, owner.Token, project.ID, "F", nil)
	request := f.createRequest(t, owner.Token, project.ID, map[string]any{"name": "R"})

	if folder.FollowRedirects != "inherit" {
		t.Errorf("new folder follow_redirects = %q, want inherit", folder.FollowRedirects)
	}
	if request.FollowRedirects != "inherit" {
		t.Errorf("new request follow_redirects = %q, want inherit", request.FollowRedirects)
	}

	for _, target := range newFollowRedirectsTargets(t, f, owner.Token, project.ID) {
		for shape, read := range target.reads {
			if got := read(t); got != "inherit" {
				t.Errorf("%s via %s = %q, want inherit", target.name, shape, got)
			}
		}
	}
}

func TestPatchFollowRedirects(t *testing.T) {
	f, owner, _, project := newFolderProject(t, "redirects-patch")

	for _, target := range newFollowRedirectsTargets(t, f, owner.Token, project.ID) {
		t.Run(target.name, func(t *testing.T) {
			for _, value := range []string{"on", "off", "global", "inherit", "off"} {
				rec := do(t, f.handler, http.MethodPatch, target.patch, `{"follow_redirects":"`+value+`"}`, owner.Token)
				if rec.Code != http.StatusOK {
					t.Fatalf("PATCH %q: status = %d, want %d (body: %s)", value, rec.Code, http.StatusOK, rec.Body)
				}

				var patched struct {
					Name            string `json:"name"`
					FollowRedirects string `json:"follow_redirects"`
				}
				if err := json.Unmarshal(rec.Body.Bytes(), &patched); err != nil {
					t.Fatalf("decoding PATCH response: %v", err)
				}
				if patched.FollowRedirects != value {
					t.Errorf("PATCH %q response follow_redirects = %q", value, patched.FollowRedirects)
				}
				// Partial: the name is left alone.
				if patched.Name == "" {
					t.Errorf("PATCH %q cleared the name", value)
				}

				for shape, read := range target.reads {
					if got := read(t); got != value {
						t.Errorf("after PATCH %q, %s = %q", value, shape, got)
					}
				}
			}

			invalid := []struct {
				name string
				body string
			}{
				{"upper case", `{"follow_redirects":"ON"}`},
				{"boolean string", `{"follow_redirects":"true"}`},
				{"empty", `{"follow_redirects":""}`},
				{"unknown word", `{"follow_redirects":"always"}`},
			}
			for _, tt := range invalid {
				rec := do(t, f.handler, http.MethodPatch, target.patch, tt.body, owner.Token)
				if rec.Code != http.StatusBadRequest {
					t.Fatalf("%s: status = %d, want %d (body: %s)", tt.name, rec.Code, http.StatusBadRequest, rec.Body)
				}
				want := "follow_redirects must be one of inherit, global, on, off"
				if msg := decodeEnvelope(t, rec).Error.Message; msg != want {
					t.Errorf("%s: message = %q, want %q", tt.name, msg, want)
				}
			}

			for name, body := range map[string]string{
				"a boolean":             `{"follow_redirects":true}`,
				"a number":              `{"follow_redirects":1}`,
				"null and nothing else": `{"follow_redirects":null}`,
			} {
				if rec := do(t, f.handler, http.MethodPatch, target.patch, body, owner.Token); rec.Code != http.StatusBadRequest {
					t.Errorf("%s: status = %d, want %d (body: %s)", name, rec.Code, http.StatusBadRequest, rec.Body)
				}
			}

			// Nothing refused above was written: still the last good value.
			for shape, read := range target.reads {
				if got := read(t); got != "off" {
					t.Errorf("after refused patches, %s = %q, want off", shape, got)
				}
			}

			// Absent is unchanged: a rename leaves the setting alone.
			if rec := do(t, f.handler, http.MethodPatch, target.patch, `{"name":"Renamed"}`, owner.Token); rec.Code != http.StatusOK {
				t.Fatalf("rename: status = %d, want %d (body: %s)", rec.Code, http.StatusOK, rec.Body)
			}
			for shape, read := range target.reads {
				if got := read(t); got != "off" {
					t.Errorf("after rename, %s = %q, want off", shape, got)
				}
			}
		})
	}
}

func TestFollowRedirectsBumpsRequestUpdatedAt(t *testing.T) {
	f, owner, _, project := newFolderProject(t, "redirects-updated-at")
	request := f.createRequest(t, owner.Token, project.ID, map[string]any{"name": "R"})

	time.Sleep(2 * time.Millisecond)
	patched := f.mustPatchRequest(t, owner.Token, request.ID, `{"follow_redirects":"on"}`)
	if !patched.UpdatedAt.After(request.UpdatedAt) {
		t.Errorf("updated_at %v did not advance past %v", patched.UpdatedAt, request.UpdatedAt)
	}
}

func TestCreateRejectsFollowRedirects(t *testing.T) {
	f, owner, _, project := newFolderProject(t, "redirects-create")

	// A new node always starts at inherit; the setting is changed with PATCH.
	for name, path := range map[string]string{
		"folder":  "/api/v1/projects/" + project.ID + "/folders",
		"request": "/api/v1/projects/" + project.ID + "/requests",
	} {
		rec := do(t, f.handler, http.MethodPost, path, `{"name":"x","follow_redirects":"on"}`, owner.Token)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s create: status = %d, want %d (body: %s)", name, rec.Code, http.StatusBadRequest, rec.Body)
		}
	}
}

// TestFollowRedirectsAccess checks the setting's PATCH is under the same
// access rule as everything else on the node: no project access is a 404.
func TestFollowRedirectsAccess(t *testing.T) {
	f := newAccessMatrixFixture(t)

	folder := f.createFolder(t, f.owner.Token, f.ownerProject.ID, "Hidden", nil)
	request := f.createRequest(t, f.owner.Token, f.ownerProject.ID, map[string]any{"name": "hidden"})

	for _, caller := range []struct {
		name  string
		token string
	}{
		{"restricted member without a grant", f.limited.Token},
		{"outsider", f.outsider.Token},
	} {
		for path, message := range map[string]string{
			"/api/v1/folders/" + folder.ID:   messageFolderNotFound,
			"/api/v1/requests/" + request.ID: messageRequestNotFound,
		} {
			rec := do(t, f.handler, http.MethodPatch, path, `{"follow_redirects":"off"}`, caller.token)
			if rec.Code != http.StatusNotFound {
				t.Errorf("%s PATCH %s: status = %d, want %d", caller.name, path, rec.Code, http.StatusNotFound)
				continue
			}
			if msg := decodeEnvelope(t, rec).Error.Message; msg != message {
				t.Errorf("%s PATCH %s: message = %q, want %q", caller.name, path, msg, message)
			}
		}
	}

	if got := f.getRequestDetail(t, f.owner.Token, request.ID).FollowRedirects; got != "inherit" {
		t.Errorf("hidden request follow_redirects = %q after denied patches, want inherit", got)
	}
}

// TestFollowRedirectsCheckConstraint checks the database refuses an unknown
// value on its own, for both tables.
func TestFollowRedirectsCheckConstraint(t *testing.T) {
	f, owner, _, project := newFolderProject(t, "redirects-check")
	folder := f.createFolder(t, owner.Token, project.ID, "F", nil)
	request := f.createRequest(t, owner.Token, project.ID, map[string]any{"name": "R"})

	for table, id := range map[string]string{"folders": folder.ID, "requests": request.ID} {
		_, err := f.pool.Exec(t.Context(), "UPDATE "+table+" SET follow_redirects = 'maybe' WHERE id = $1", id)

		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != checkViolation || pgErr.ConstraintName != table+"_follow_redirects" {
			t.Errorf("%s: error = %v, want a CHECK violation of %s_follow_redirects", table, err, table)
		}
	}
}
