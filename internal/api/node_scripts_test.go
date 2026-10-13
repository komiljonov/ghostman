package api

import (
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

// scriptTarget is one resource carrying scripts: where to PATCH it and how to
// read its stored scripts back.
type scriptTarget struct {
	name  string
	patch string
	read  func(t *testing.T) (pre, post string)
}

func newScriptTargets(t *testing.T, f teamsFixture, token, projectID string) []scriptTarget {
	t.Helper()

	folder := f.createFolder(t, token, projectID, "Scripted", nil)
	request := f.createRequest(t, token, projectID, map[string]any{"name": "Scripted", "folder_id": folder.ID})

	return []scriptTarget{
		{
			name:  "folder",
			patch: "/api/v1/folders/" + folder.ID,
			// Folders carry their scripts in the list.
			read: func(t *testing.T) (string, string) {
				t.Helper()
				for _, entry := range f.listFolders(t, token, projectID) {
					if entry.ID == folder.ID {
						return entry.PreScript, entry.PostScript
					}
				}
				t.Fatalf("folder %s missing from the folder list", folder.ID)
				return "", ""
			},
		},
		{
			name:  "request",
			patch: "/api/v1/requests/" + request.ID,
			// Requests carry them only in GET /requests/{id}.
			read: func(t *testing.T) (string, string) {
				t.Helper()
				detail := f.getRequestDetail(t, token, request.ID)
				return detail.PreScript, detail.PostScript
			},
		},
	}
}

const samplePreScript = `// Sign the request.
const ts = Date.now().toString();
pm.request.headers.add({ key: "X-Timestamp", value: ts });
pm.variables.set("nonce", "ñ-" + Math.random()); // unicode, quotes: "'` + "`"

const samplePostScript = `pm.test("status is 200", () => {
  pm.expect(pm.response.code).to.equal(200);
});`

func TestScriptInclusionRules(t *testing.T) {
	f, owner, _, project := newFolderProject(t, "scripts-inclusion")

	folder := f.createFolder(t, owner.Token, project.ID, "F", nil)
	request := f.createRequest(t, owner.Token, project.ID, map[string]any{"name": "R", "folder_id": folder.ID})
	f.mustPatchRequest(t, owner.Token, request.ID, `{"pre_script":`+jsonString(t, samplePreScript)+`}`)

	get := func(t *testing.T, path string) string {
		t.Helper()
		rec := do(t, f.handler, http.MethodGet, path, "", owner.Token)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s: status = %d (body: %s)", path, rec.Code, rec.Body)
		}
		return rec.Body.String()
	}

	if body := get(t, "/api/v1/requests/"+request.ID); !strings.Contains(body, `"pre_script":`) || !strings.Contains(body, `"post_script":`) {
		t.Errorf("GET /requests/{id} lacks the scripts: %.300s", body)
	}
	if body := get(t, "/api/v1/projects/"+project.ID+"/requests"); strings.Contains(body, `"pre_script"`) || strings.Contains(body, `"post_script"`) {
		t.Errorf("the request list carries scripts; like headers and body they belong in GET /requests/{id} only: %.300s", body)
	}
	if body := get(t, "/api/v1/projects/"+project.ID+"/folders"); !strings.Contains(body, `"pre_script":""`) || !strings.Contains(body, `"post_script":""`) {
		t.Errorf("the folder list lacks the scripts: %.300s", body)
	}

	// Defaults: empty everywhere.
	if folder.PreScript != "" || folder.PostScript != "" {
		t.Errorf("new folder scripts = %q / %q, want empty", folder.PreScript, folder.PostScript)
	}
}

func TestPatchScripts(t *testing.T) {
	f, owner, _, project := newFolderProject(t, "scripts-patch")

	for _, target := range newScriptTargets(t, f, owner.Token, project.ID) {
		t.Run(target.name, func(t *testing.T) {
			check := func(t *testing.T, step, wantPre, wantPost string) {
				t.Helper()
				if pre, post := target.read(t); pre != wantPre || post != wantPost {
					t.Errorf("%s: scripts = %.40q / %.40q, want %.40q / %.40q", step, pre, post, wantPre, wantPost)
				}
			}
			patch := func(t *testing.T, body string) {
				t.Helper()
				if rec := do(t, f.handler, http.MethodPatch, target.patch, body, owner.Token); rec.Code != http.StatusOK {
					t.Fatalf("PATCH %.80s: status = %d (body: %s)", body, rec.Code, rec.Body)
				}
			}

			check(t, "default", "", "")

			// Stored byte for byte: newlines, unicode, quotes and backticks.
			patch(t, `{"pre_script":`+jsonString(t, samplePreScript)+`,"post_script":`+jsonString(t, samplePostScript)+`}`)
			check(t, "set both", samplePreScript, samplePostScript)

			patch(t, `{"post_script":"// changed"}`)
			check(t, "only post_script", samplePreScript, "// changed")

			patch(t, `{"name":"Renamed"}`)
			check(t, "absent is unchanged", samplePreScript, "// changed")

			patch(t, `{"pre_script":null,"name":"Again"}`)
			check(t, "null is unchanged", samplePreScript, "// changed")

			patch(t, `{"pre_script":""}`)
			check(t, `"" clears`, "", "// changed")

			// The limit counts characters: 32768 two-byte characters fit.
			longest := strings.Repeat("é", maxScriptLength)
			patch(t, `{"pre_script":`+jsonString(t, longest)+`}`)
			check(t, "exactly at the limit", longest, "// changed")

			refused := []struct {
				name    string
				body    string
				message string
			}{
				{"pre_script over the limit", `{"pre_script":` + jsonString(t, longest+"x") + `}`, "pre_script must be at most 32768 characters"},
				{"post_script over the limit", `{"post_script":` + jsonString(t, strings.Repeat("x", maxScriptLength+1)) + `}`, "post_script must be at most 32768 characters"},
				{"not a string", `{"pre_script":42}`, ""},
				{"null alone is nothing to change", `{"pre_script":null}`, ""},
			}
			for _, tt := range refused {
				rec := do(t, f.handler, http.MethodPatch, target.patch, tt.body, owner.Token)
				if rec.Code != http.StatusBadRequest {
					t.Errorf("%s: status = %d, want %d", tt.name, rec.Code, http.StatusBadRequest)
					continue
				}
				if tt.message != "" {
					if msg := decodeEnvelope(t, rec).Error.Message; msg != tt.message {
						t.Errorf("%s: message = %q, want %q", tt.name, msg, tt.message)
					}
				}
			}
			check(t, "after refused patches", longest, "// changed")
		})
	}
}

func TestScriptsBumpRequestUpdatedAt(t *testing.T) {
	f, owner, _, project := newFolderProject(t, "scripts-updated-at")
	request := f.createRequest(t, owner.Token, project.ID, map[string]any{"name": "R"})

	time.Sleep(2 * time.Millisecond)
	patched := f.mustPatchRequest(t, owner.Token, request.ID, `{"post_script":"pm.test('ok', () => {})"}`)
	if !patched.UpdatedAt.After(request.UpdatedAt) {
		t.Errorf("updated_at %v did not advance past %v", patched.UpdatedAt, request.UpdatedAt)
	}
	if patched.PostScript != "pm.test('ok', () => {})" {
		t.Errorf("PATCH response post_script = %q", patched.PostScript)
	}
}

func TestCreateRejectsScripts(t *testing.T) {
	f, owner, _, project := newFolderProject(t, "scripts-create")

	for name, path := range map[string]string{
		"folder":  "/api/v1/projects/" + project.ID + "/folders",
		"request": "/api/v1/projects/" + project.ID + "/requests",
	} {
		for _, field := range []string{"pre_script", "post_script"} {
			rec := do(t, f.handler, http.MethodPost, path, `{"name":"x","`+field+`":"// x"}`, owner.Token)
			if rec.Code != http.StatusBadRequest {
				t.Errorf("%s create with %s: status = %d, want %d", name, field, rec.Code, http.StatusBadRequest)
			}
		}
	}
}

func TestScriptsAccess(t *testing.T) {
	f := newAccessMatrixFixture(t)

	folder := f.createFolder(t, f.owner.Token, f.ownerProject.ID, "Hidden", nil)
	request := f.createRequest(t, f.owner.Token, f.ownerProject.ID, map[string]any{"name": "hidden"})
	f.mustPatchRequest(t, f.owner.Token, request.ID, `{"pre_script":"// owner's"}`)

	for _, token := range []string{f.limited.Token, f.outsider.Token} {
		for path, message := range map[string]string{
			"/api/v1/folders/" + folder.ID:   messageFolderNotFound,
			"/api/v1/requests/" + request.ID: messageRequestNotFound,
		} {
			rec := do(t, f.handler, http.MethodPatch, path, `{"pre_script":"// injected"}`, token)
			if rec.Code != http.StatusNotFound {
				t.Errorf("PATCH %s: status = %d, want %d", path, rec.Code, http.StatusNotFound)
				continue
			}
			if msg := decodeEnvelope(t, rec).Error.Message; msg != message {
				t.Errorf("PATCH %s: message = %q, want %q", path, msg, message)
			}
		}
	}

	if got := f.getRequestDetail(t, f.owner.Token, request.ID).PreScript; got != "// owner's" {
		t.Errorf("hidden request pre_script = %q after denied patches", got)
	}

	var folderPre string
	for _, entry := range f.listFolders(t, f.owner.Token, f.ownerProject.ID) {
		if entry.ID == folder.ID {
			folderPre = entry.PreScript
		}
	}
	if folderPre != "" {
		t.Errorf("hidden folder pre_script = %q after denied patches", folderPre)
	}
}

func TestScriptCheckConstraints(t *testing.T) {
	f, owner, _, project := newFolderProject(t, "scripts-check")
	folder := f.createFolder(t, owner.Token, project.ID, "F", nil)
	request := f.createRequest(t, owner.Token, project.ID, map[string]any{"name": "R"})

	for table, id := range map[string]string{"folders": folder.ID, "requests": request.ID} {
		for _, column := range []string{"pre_script", "post_script"} {
			_, err := f.pool.Exec(t.Context(), "UPDATE "+table+" SET "+column+" = repeat('x', 32769) WHERE id = $1", id)

			want := table + "_" + column + "_length"
			var pgErr *pgconn.PgError
			if !errors.As(err, &pgErr) || pgErr.Code != checkViolation || pgErr.ConstraintName != want {
				t.Errorf("%s.%s: error = %v, want a CHECK violation of %s", table, column, err, want)
			}
		}
	}
}
