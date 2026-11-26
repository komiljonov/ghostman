package api

import (
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/komiljonov/ghostman/internal/db"
)

// emptySource is a row with nothing in it: valid empty JSON for the jsonb
// columns, the way a fresh request stores them.
func emptySource() db.ListProjectVariableSourcesRow {
	return db.ListProjectVariableSourcesRow{
		Headers:     json.RawMessage(`[]`),
		QueryParams: json.RawMessage(`[]`),
		Body:        json.RawMessage(`{"type":"none"}`),
	}
}

func mustCollect(t *testing.T, rows ...db.ListProjectVariableSourcesRow) []string {
	t.Helper()

	keys, err := collectVariableKeys(rows)
	if err != nil {
		t.Fatalf("collectVariableKeys: %v", err)
	}
	return keys
}

func TestVariableKeysPerField(t *testing.T) {
	rowsJSON := func(text string) json.RawMessage {
		return json.RawMessage(`[{"key":"k-` + text + `","value":"v-` + text + `","enabled":false}]`)
	}

	tests := []struct {
		name string
		set  func(r *db.ListProjectVariableSourcesRow)
		want []string
	}{
		{"url", func(r *db.ListProjectVariableSourcesRow) { r.Url = "{{base}}/users/{{id}}" }, []string{"base", "id"}},
		{"header key and value, disabled too", func(r *db.ListProjectVariableSourcesRow) { r.Headers = rowsJSON("{{hk}}-{{hv}}") }, []string{"hk", "hv"}},
		{"query param key and value", func(r *db.ListProjectVariableSourcesRow) { r.QueryParams = rowsJSON("{{qk}}") }, []string{"qk"}},
		{"raw content, not content_type", func(r *db.ListProjectVariableSourcesRow) {
			r.Body = json.RawMessage(`{"type":"raw","content_type":"{{ct}}","content":"{\"id\": {{rid}}}"}`)
		}, []string{"rid"}},
		{"form field key and value", func(r *db.ListProjectVariableSourcesRow) {
			r.Body = json.RawMessage(`{"type":"form","fields":[{"key":"{{fk}}","value":"{{fv}}","enabled":true}]}`)
		}, []string{"fk", "fv"}},
		{"multipart text value and file key, not file_name", func(r *db.ListProjectVariableSourcesRow) {
			r.Body = json.RawMessage(`{"type":"multipart","fields":[` +
				`{"key":"note","kind":"text","value":"{{mv}}","enabled":true},` +
				`{"key":"{{mk}}","kind":"file","file_name":"{{fname}}.png","enabled":true}]}`)
		}, []string{"mk", "mv"}},
		{"bearer", func(r *db.ListProjectVariableSourcesRow) { r.AuthBearerToken = "{{token}}" }, []string{"token"}},
		{"basic", func(r *db.ListProjectVariableSourcesRow) {
			r.AuthBasicUsername, r.AuthBasicPassword = "{{user}}", "{{pass}}"
		}, []string{"pass", "user"}},
		{"api key", func(r *db.ListProjectVariableSourcesRow) {
			r.AuthApiKeyName, r.AuthApiKeyValue = "X-{{kname}}", "{{kvalue}}"
		}, []string{"kname", "kvalue"}},
		{"jwt", func(r *db.ListProjectVariableSourcesRow) {
			r.AuthJwtSecret = "{{jwt_secret}}"
			r.AuthJwtPayload = `{"sub": "{{sub}}", "exp": {{$now+60}}}`
			r.AuthJwtName, r.AuthJwtPrefix = "{{jname}}", "{{jprefix}} "
		}, []string{"jname", "jprefix", "jwt_secret", "sub"}},
		{"script literals only", func(r *db.ListProjectVariableSourcesRow) {
			r.PreScript = `gm.environment.set("token", res.json().token); const u = "{{not_a_key}}";`
			r.PostScript = `gm.environment.unset('old')`
		}, []string{"old", "token"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			row := emptySource()
			tt.set(&row)
			if got := mustCollect(t, row); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("keys = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestVariableKeysTokenGrammar(t *testing.T) {
	tests := []struct {
		url  string
		want []string
	}{
		{"{{a}}{{b}}", []string{"a", "b"}},
		{"{{ spaced }}", []string{" spaced "}}, // exact, no trimming
		{"{{Base_URL}} {{base_url}}", []string{"Base_URL", "base_url"}},
		{"{{$uuid}} {{$now-60}} {{$unknown}} {{a$b}}", []string{"a$b"}}, // only a leading $ is generated
		{"{{}} {{ {{a {{a{b}} }}", []string{}},                          // incomplete tokens are literal
		{"{{{x}}}", []string{"x"}},
		{"{{line\nbreak}}", []string{"line\nbreak"}},
		{"plain text, :id and ${template}", []string{}},
	}

	for _, tt := range tests {
		row := emptySource()
		row.Url = tt.url
		if got := mustCollect(t, row); !reflect.DeepEqual(got, tt.want) {
			t.Errorf("%q: keys = %q, want %q", tt.url, got, tt.want)
		}
	}
}

func TestVariableKeysScriptLiterals(t *testing.T) {
	tests := []struct {
		name, script string
		want         []string
	}{
		{"get set unset", `gm.environment.get("a"); gm.environment.set("b", 1); gm.environment.unset("c")`, []string{"a", "b", "c"}},
		{"single and template quotes", "gm.environment.get('single'); gm.environment.get(`tmpl`)", []string{"single", "tmpl"}},
		{"whitespace and newlines", "gm . environment . set (\n  \"spaced\" ,\n 2)", []string{"spaced"}},
		{"escapes", `gm.environment.get("say \"hi\"!"); gm.environment.get('it\'s')`, []string{"it's", `say "hi"!`}},
		{"not a literal", `gm.environment.get(name); gm.environment.get("a" + b); gm.environment.get(` + "`${x}`" + `)`, []string{"a"}},
		{"empty literal", `gm.environment.get("")`, []string{}},
		{"tokens in scripts do not count", `const url = "{{base}}/x"; console.log("{{token}}")`, []string{}},
		{"other objects do not count", `pm.environment.get("pm"); gm.globals.get("g"); environment.get("bare")`, []string{}},
		{"literal is taken as is, $ too", `gm.environment.get("{{x}}"); gm.environment.get("$now")`, []string{"$now", "{{x}}"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			row := emptySource()
			row.PreScript = tt.script
			if got := mustCollect(t, row); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("keys = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestVariableKeysDedupeAndSort(t *testing.T) {
	a, b := emptySource(), emptySource()
	a.Url = "{{zeta}}/{{alpha}}/{{zeta}}"
	a.AuthBearerToken = "{{alpha}}"
	a.PreScript = `gm.environment.get("alpha")`
	b.Headers = json.RawMessage(`[{"key":"X","value":"{{mid}}{{zeta}}","enabled":true}]`)
	b.PostScript = `gm.environment.set("mid", 1)`

	if got, want := mustCollect(t, a, b), []string{"alpha", "mid", "zeta"}; !reflect.DeepEqual(got, want) {
		t.Errorf("keys = %q, want %q", got, want)
	}
	if got := mustCollect(t); got == nil || len(got) != 0 {
		t.Errorf("no rows: keys = %#v, want an empty, non-nil slice", got)
	}
}

func (f teamsFixture) variableKeys(t *testing.T, token, projectID string) []string {
	t.Helper()

	rec := do(t, f.handler, http.MethodGet, "/api/v1/projects/"+projectID+"/variable-keys", "", token)
	if rec.Code != http.StatusOK {
		t.Fatalf("variable-keys: status = %d (body: %s)", rec.Code, rec.Body)
	}
	if !strings.HasPrefix(rec.Body.String(), `{"keys":[`) {
		t.Errorf("body = %s, want {\"keys\": [...]}", rec.Body)
	}

	var resp variableKeysResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decoding variable keys: %v", err)
	}
	return resp.Keys
}

// TestVariableKeysFromStoredProject goes through the real write paths and the
// one-pass query, requests and folders together.
func TestVariableKeysFromStoredProject(t *testing.T) {
	f, owner, team, project := newFolderProject(t, "varkeys")

	if got := f.variableKeys(t, owner.Token, project.ID); len(got) != 0 {
		t.Fatalf("empty project: keys = %q, want []", got)
	}

	folder := f.createFolder(t, owner.Token, project.ID, "F", nil)
	rec := do(t, f.handler, http.MethodPatch, "/api/v1/folders/"+folder.ID,
		`{"auth":{"type":"basic","basic_username":"{{folder_user}}","basic_password":"{{$randomHex}}"},`+
			`"pre_script":"gm.environment.set(\"folder_set\", 1)","post_script":"// {{ignored}}"}`, owner.Token)
	if rec.Code != http.StatusOK {
		t.Fatalf("patching folder: status = %d (body: %s)", rec.Code, rec.Body)
	}

	request := f.createRequest(t, owner.Token, project.ID, map[string]any{
		"name": "R", "folder_id": folder.ID, "url": "{{base_url}}/users/{{user_id}}?t={{$timestamp}}",
	})
	f.mustPatchRequest(t, owner.Token, request.ID, `{`+
		`"headers":[{"key":"Authorization","value":"Bearer {{token}}","enabled":false}],`+
		`"query_params":[{"key":"{{q_key}}","value":"x","enabled":true}],`+
		`"body":{"type":"raw","content_type":"application/json","content":"{\"a\": \"{{body_var}}\"}"},`+
		`"pre_script":"gm.environment.get('pre_key')",`+
		`"auth":{"type":"bearer","bearer_token":"{{bearer_tok}}"}}`)
	// Switching to jwt keeps the bearer token stored, so it still counts.
	f.mustPatchRequest(t, owner.Token, request.ID,
		`{"auth":{"type":"jwt","jwt_secret":"{{jwt_key}}","jwt_payload":"{\"sub\":\"{{sub}}\"}"}}`)

	form := f.createRequest(t, owner.Token, project.ID, map[string]any{"name": "Form"})
	f.mustPatchRequest(t, owner.Token, form.ID,
		`{"body":{"type":"multipart","fields":[`+
			`{"key":"{{part}}","kind":"text","value":"{{part_value}}","enabled":true},`+
			`{"key":"avatar","kind":"file","file_name":"{{not_resolved}}","enabled":true}]}}`)

	// Another project's keys never leak in.
	other := f.createProject(t, owner.Token, team.ID, "Other")
	f.createRequest(t, owner.Token, other.ID, map[string]any{"name": "X", "url": "{{other_project}}"})

	want := []string{
		"base_url", "bearer_tok", "body_var", "folder_set", "folder_user", "jwt_key",
		"part", "part_value", "pre_key", "q_key", "sub", "token", "user_id",
	}
	if got := f.variableKeys(t, owner.Token, project.ID); !reflect.DeepEqual(got, want) {
		t.Errorf("keys =\n  %q\nwant\n  %q", got, want)
	}
}

func TestVariableKeysAccess(t *testing.T) {
	f := newAccessMatrixFixture(t)
	f.createRequest(t, f.owner.Token, f.ownerProject.ID, map[string]any{"name": "Hidden", "url": "{{hidden}}"})

	for name, tc := range map[string]struct{ token, projectID string }{
		"restricted member without a grant": {f.limited.Token, f.ownerProject.ID},
		"outsider":                          {f.outsider.Token, f.ownerProject.ID},
		"nonexistent project":               {f.owner.Token, uuid.New().String()},
	} {
		rec := do(t, f.handler, http.MethodGet, "/api/v1/projects/"+tc.projectID+"/variable-keys", "", tc.token)
		if rec.Code != http.StatusNotFound || decodeEnvelope(t, rec).Error.Message != messageProjectNotFound {
			t.Errorf("%s: status = %d (body: %s), want 404 project not found", name, rec.Code, rec.Body)
		}
	}

	if got := f.variableKeys(t, f.openMember.Token, f.ownerProject.ID); !reflect.DeepEqual(got, []string{"hidden"}) {
		t.Errorf("open member keys = %q, want [hidden]", got)
	}
	if rec := do(t, f.handler, http.MethodGet, "/api/v1/projects/"+f.ownerProject.ID+"/variable-keys", "", ""); rec.Code != http.StatusUnauthorized {
		t.Errorf("anonymous: status = %d, want 401", rec.Code)
	}
}

func TestPathParamsAreGone(t *testing.T) {
	f, owner, _, project := newFolderProject(t, "path-gone")
	request := f.createRequest(t, owner.Token, project.ID, map[string]any{"name": "R", "url": "/users/:id"})

	rec := do(t, f.handler, http.MethodPatch, "/api/v1/requests/"+request.ID,
		`{"path_params":[{"key":"id","value":"1"}]}`, owner.Token)
	if rec.Code != http.StatusBadRequest || decodeEnvelope(t, rec).Error.Message != `body contains unknown field "path_params"` {
		t.Errorf("PATCH path_params: status = %d (body: %s), want 400 unknown field", rec.Code, rec.Body)
	}

	rec = do(t, f.handler, http.MethodGet, "/api/v1/requests/"+request.ID, "", owner.Token)
	if strings.Contains(rec.Body.String(), "path_params") {
		t.Errorf("GET still carries path_params: %s", rec.Body)
	}

	var exists bool
	if err := f.pool.QueryRow(t.Context(), `SELECT EXISTS (SELECT 1 FROM information_schema.columns
		WHERE table_name = 'requests' AND column_name = 'path_params')`).Scan(&exists); err != nil {
		t.Fatalf("checking the column: %v", err)
	}
	if exists {
		t.Error("requests.path_params still exists")
	}
}
