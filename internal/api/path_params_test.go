package api

import (
	"net/http"
	"strings"
	"testing"
)

func TestPathParamsRoundTrip(t *testing.T) {
	f, owner, _, project := newFolderProject(t, "path-roundtrip")
	request := f.createRequest(t, owner.Token, project.ID, map[string]any{
		"name": "User", "url": "{{base}}/users/:id/posts/:post_id",
	})

	if got := f.getRequestDetail(t, owner.Token, request.ID); string(got.PathParams) != `[]` {
		t.Errorf("a new request's path_params = %s, want []", got.PathParams)
	}

	// Duplicate keys, an empty value, {{vars}} and padding: all stored exactly
	// as given, in order. The server does not match keys against the url.
	pathParams := `[
		{"key": "id", "value": "{{user_id}}"},
		{"key": "post_id", "value": ""},
		{"key": "id", "value": "42"},
		{"key": " unused ", "value": " {{ spaced }} "}
	]`

	patched := f.mustPatchRequest(t, owner.Token, request.ID, `{"path_params":`+pathParams+`}`)
	assertSameJSON(t, "PATCH response path_params", patched.PathParams, pathParams)

	got := f.getRequestDetail(t, owner.Token, request.ID)
	assertSameJSON(t, "GET path_params", got.PathParams, pathParams)
	assertSameJSON(t, "headers untouched", got.Headers, `[]`)
	assertSameJSON(t, "query_params untouched", got.QueryParams, `[]`)

	t.Run("absent and null keep, an array replaces, [] clears", func(t *testing.T) {
		got := f.mustPatchRequest(t, owner.Token, request.ID, `{"name":"Renamed"}`)
		assertSameJSON(t, "after a name-only PATCH", got.PathParams, pathParams)

		got = f.mustPatchRequest(t, owner.Token, request.ID, `{"path_params":null,"url":"/x/:y"}`)
		assertSameJSON(t, "after a null PATCH", got.PathParams, pathParams)

		got = f.mustPatchRequest(t, owner.Token, request.ID, `{"path_params":[{"key":"y","value":"1"}]}`)
		assertSameJSON(t, "after a replacing PATCH", got.PathParams, `[{"key":"y","value":"1"}]`)

		got = f.mustPatchRequest(t, owner.Token, request.ID, `{"path_params":[]}`)
		assertSameJSON(t, "after clearing", got.PathParams, `[]`)
	})
}

func TestPathParamsValidation(t *testing.T) {
	f, owner, _, project := newFolderProject(t, "path-validation")
	request := f.createRequest(t, owner.Token, project.ID, map[string]any{"name": "R"})
	kept := `[{"key":"id","value":"7"}]`
	f.mustPatchRequest(t, owner.Token, request.ID, `{"path_params":`+kept+`}`)

	row := `{"key":"k","value":"v"}`
	rows := func(n int) string {
		return "[" + strings.TrimSuffix(strings.Repeat(row+",", n), ",") + "]"
	}

	tests := []struct {
		name, value, message string
	}{
		{"not an array", `{"key":"k"}`, "path_params must be an array of {key, value} rows"},
		{"a string", `"id=1"`, "path_params must be an array"},
		{"row not an object", `[` + row + `, 42]`, "path_params[1] must be an object with key and value"},
		{"row null", `[null]`, "path_params[0] must be an object"},
		{"row missing key", `[{"value":"v"}]`, "path_params[0].key is required"},
		{"row missing value", `[{"key":"k"}]`, "path_params[0].value is required"},
		{"blank key", `[` + row + `,{"key":"  ","value":""}]`, "path_params[1].key is required"},
		{"enabled is not a field here", `[{"key":"k","value":"v","enabled":true}]`, "path_params[0].enabled is not a known field; a row has only key and value"},
		{"unknown row field", `[{"key":"k","value":"v","description":"x"}]`, "path_params[0].description is not a known field"},
		{"key not a string", `[{"key":1,"value":"v"}]`, "path_params[0].key must be a string"},
		{"key null", `[{"key":null,"value":"v"}]`, "path_params[0].key must be a string"},
		{"value null", `[{"key":"k","value":null}]`, "path_params[0].value must be a string"},
		{"101 rows", rows(101), "path_params must have at most 100 rows, got 101"},
		{"key over 200", `[{"key":"` + strings.Repeat("k", 201) + `","value":""}]`, "path_params[0].key must be at most 200 characters"},
		{"value over 8192", `[{"key":"k","value":"` + strings.Repeat("v", 8193) + `"}]`, "path_params[0].value must be at most 8192 characters"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := do(t, f.handler, http.MethodPatch, "/api/v1/requests/"+request.ID,
				`{"path_params":`+tt.value+`}`, owner.Token)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want %d (body: %.300s)", rec.Code, http.StatusBadRequest, rec.Body)
			}
			if msg := decodeEnvelope(t, rec).Error.Message; !strings.HasPrefix(msg, tt.message) {
				t.Errorf("message = %q, want it to start with %q", msg, tt.message)
			}
		})
	}

	// Nothing refused above was written.
	assertSameJSON(t, "path_params after refused patches", f.getRequestDetail(t, owner.Token, request.ID).PathParams, kept)

	t.Run("the limits themselves are accepted", func(t *testing.T) {
		longest := `{"key":"` + strings.Repeat("k", 200) + `","value":"` + strings.Repeat("v", 8192) + `"}`
		got := f.mustPatchRequest(t, owner.Token, request.ID, `{"path_params":`+rows(99)+`}`)
		assertSameJSON(t, "99 rows", got.PathParams, rows(99))

		withLongest := strings.TrimSuffix(rows(99), "]") + "," + longest + "]"
		got = f.mustPatchRequest(t, owner.Token, request.ID, `{"path_params":`+withLongest+`}`)
		assertSameJSON(t, "100 rows, the last at both length limits", got.PathParams, withLongest)
	})

	t.Run("create does not take path_params", func(t *testing.T) {
		rec := do(t, f.handler, http.MethodPost, "/api/v1/projects/"+project.ID+"/requests",
			`{"name":"x","path_params":[{"key":"k","value":"v"}]}`, owner.Token)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("status = %d, want %d", rec.Code, http.StatusBadRequest)
		}
	})
}

func TestRequestListExcludesPathParams(t *testing.T) {
	f, owner, _, project := newFolderProject(t, "path-list")
	request := f.createRequest(t, owner.Token, project.ID, map[string]any{"name": "R", "url": "/a/:id"})
	f.mustPatchRequest(t, owner.Token, request.ID, `{"path_params":[{"key":"id","value":"secret-ish-{{v}}"}]}`)

	rec := do(t, f.handler, http.MethodGet, "/api/v1/projects/"+project.ID+"/requests", "", owner.Token)
	if rec.Code != http.StatusOK {
		t.Fatalf("list: status = %d (body: %s)", rec.Code, rec.Body)
	}
	if body := rec.Body.String(); strings.Contains(body, `"path_params"`) || strings.Contains(body, "secret-ish") {
		t.Errorf("list carries path_params: %s", body)
	}
	if got := f.listRequests(t, owner.Token, project.ID); len(got) != 1 || got[0].URL != "/a/:id" {
		t.Errorf("list = %+v, want the one request", got)
	}
}
