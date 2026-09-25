package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"
)

// requestDetail is the decoded GET/PATCH /requests/{id} response.
type requestDetail struct {
	requestResponse
	Headers     json.RawMessage `json:"headers"`
	QueryParams json.RawMessage `json:"query_params"`
	Body        json.RawMessage `json:"body"`
}

func (f teamsFixture) getRequestDetail(t *testing.T, token, requestID string) requestDetail {
	t.Helper()

	rec := do(t, f.handler, http.MethodGet, "/api/v1/requests/"+requestID, "", token)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET request: status = %d, want %d (body: %s)", rec.Code, http.StatusOK, rec.Body)
	}

	return decodeRequestDetail(t, rec.Body.Bytes())
}

func decodeRequestDetail(t *testing.T, body []byte) requestDetail {
	t.Helper()

	var detail requestDetail
	if err := json.Unmarshal(body, &detail); err != nil {
		t.Fatalf("decoding request detail: %v (body: %s)", err, body)
	}

	return detail
}

// mustPatchRequest PATCHes a request and returns the full response.
func (f teamsFixture) mustPatchRequest(t *testing.T, token, requestID, body string) requestDetail {
	t.Helper()

	rec := do(t, f.handler, http.MethodPatch, "/api/v1/requests/"+requestID, body, token)
	if rec.Code != http.StatusOK {
		t.Fatalf("PATCH %s: status = %d, want %d (body: %s)", body, rec.Code, http.StatusOK, rec.Body)
	}

	return decodeRequestDetail(t, rec.Body.Bytes())
}

// assertSameJSON compares two JSON documents by value: object key order is
// irrelevant (jsonb does not keep it), array order and duplicates are not.
func assertSameJSON(t *testing.T, label string, got json.RawMessage, want string) {
	t.Helper()

	var gotValue, wantValue any
	if err := json.Unmarshal(got, &gotValue); err != nil {
		t.Fatalf("%s: decoding %s: %v", label, got, err)
	}
	if err := json.Unmarshal([]byte(want), &wantValue); err != nil {
		t.Fatalf("%s: decoding expected %s: %v", label, want, err)
	}

	if !reflect.DeepEqual(gotValue, wantValue) {
		t.Errorf("%s = %s, want %s", label, got, want)
	}
}

func TestHeadersAndQueryParamsRoundTrip(t *testing.T) {
	f, owner, _, project := newFolderProject(t, "kv-roundtrip")
	request := f.createRequest(t, owner.Token, project.ID, map[string]any{"name": "Search"})

	// Duplicate keys, empty values, {{vars}}, a disabled row, and a key with
	// surrounding spaces: all stored exactly as given, in order.
	headers := `[
		{"key": "Authorization", "value": "Bearer {{token}}", "enabled": true},
		{"key": "X-Trace", "value": "", "enabled": false},
		{"key": "Accept", "value": "application/json", "enabled": true},
		{"key": "Accept", "value": "text/plain", "enabled": true},
		{"key": " X-Padded ", "value": " {{ spaced }} ", "enabled": true}
	]`
	queryParams := `[
		{"key": "tag", "value": "a", "enabled": true},
		{"key": "tag", "value": "b", "enabled": true},
		{"key": "page", "value": "", "enabled": false},
		{"key": "q", "value": "{{search}}", "enabled": true}
	]`

	patched := f.mustPatchRequest(t, owner.Token, request.ID,
		`{"headers":`+headers+`,"query_params":`+queryParams+`}`)
	assertSameJSON(t, "PATCH response headers", patched.Headers, headers)
	assertSameJSON(t, "PATCH response query_params", patched.QueryParams, queryParams)

	got := f.getRequestDetail(t, owner.Token, request.ID)
	assertSameJSON(t, "GET headers", got.Headers, headers)
	assertSameJSON(t, "GET query_params", got.QueryParams, queryParams)
	assertSameJSON(t, "GET body", got.Body, `{"type":"none"}`)

	// The list shape is unchanged: still no headers or query_params.
	rec := do(t, f.handler, http.MethodGet, "/api/v1/projects/"+project.ID+"/requests", "", owner.Token)
	if strings.Contains(rec.Body.String(), `"headers"`) || strings.Contains(rec.Body.String(), `"query_params"`) {
		t.Errorf("list carries headers or query_params: %s", rec.Body)
	}
}

func TestHeadersAndQueryParamsValidation(t *testing.T) {
	f, owner, _, project := newFolderProject(t, "kv-validation")
	request := f.createRequest(t, owner.Token, project.ID, map[string]any{"name": "R"})

	row := `{"key":"k","value":"v","enabled":true}`
	rows := func(n int) string {
		return "[" + strings.TrimSuffix(strings.Repeat(row+",", n), ",") + "]"
	}

	tests := []struct {
		name    string
		value   string
		message string // with FIELD standing in for headers / query_params
	}{
		{"not an array", `{"key":"k"}`, "FIELD must be an array"},
		{"a string", `"Authorization: x"`, "FIELD must be an array"},
		{"row not an object", `[` + row + `, 42]`, "FIELD[1] must be an object"},
		{"row missing enabled", `[{"key":"k","value":"v"}]`, "FIELD[0].enabled is required"},
		{"row missing key", `[{"value":"v","enabled":true}]`, "FIELD[0].key is required"},
		{"row missing value", `[{"key":"k","enabled":true}]`, "FIELD[0].value is required"},
		{"empty key", `[` + row + `,` + row + `,{"key":"   ","value":"","enabled":true}]`, "FIELD[2].key is required"},
		{"unknown row field", `[{"key":"k","value":"v","enabled":true,"description":"x"}]`, "FIELD[0].description is not a known field"},
		{"enabled not a bool", `[{"key":"k","value":"v","enabled":"yes"}]`, "FIELD[0].enabled must be a boolean"},
		{"enabled null", `[{"key":"k","value":"v","enabled":null}]`, "FIELD[0].enabled must be a boolean"},
		{"key not a string", `[{"key":1,"value":"v","enabled":true}]`, "FIELD[0].key must be a string"},
		{"value null", `[{"key":"k","value":null,"enabled":true}]`, "FIELD[0].value must be a string"},
		{"101 rows", rows(101), "FIELD must have at most 100 rows"},
		{"key over 200", `[{"key":"` + strings.Repeat("k", 201) + `","value":"","enabled":true}]`, "FIELD[0].key must be at most 200 characters"},
		{"value over 8192", `[{"key":"k","value":"` + strings.Repeat("v", 8193) + `","enabled":true}]`, "FIELD[0].value must be at most 8192 characters"},
	}

	for _, field := range []string{"headers", "query_params"} {
		for _, tt := range tests {
			t.Run(field+" "+tt.name, func(t *testing.T) {
				rec := do(t, f.handler, http.MethodPatch, "/api/v1/requests/"+request.ID,
					fmt.Sprintf(`{%q:%s}`, field, tt.value), owner.Token)
				if rec.Code != http.StatusBadRequest {
					t.Fatalf("status = %d, want %d (body: %s)", rec.Code, http.StatusBadRequest, rec.Body)
				}

				want := strings.ReplaceAll(tt.message, "FIELD", field)
				if msg := decodeEnvelope(t, rec).Error.Message; !strings.HasPrefix(msg, want) {
					t.Errorf("message = %q, want it to start with %q", msg, want)
				}
			})
		}
	}

	// Nothing refused above was written.
	got := f.getRequestDetail(t, owner.Token, request.ID)
	assertSameJSON(t, "headers after refused patches", got.Headers, `[]`)
	assertSameJSON(t, "query_params after refused patches", got.QueryParams, `[]`)

	t.Run("the limits themselves are accepted", func(t *testing.T) {
		longest := `{"key":"` + strings.Repeat("k", 200) + `","value":"` + strings.Repeat("v", 8192) + `","enabled":false}`
		f.mustPatchRequest(t, owner.Token, request.ID, `{"headers":`+rows(100)+`,"query_params":[`+longest+`]}`)

		got := f.getRequestDetail(t, owner.Token, request.ID)
		assertSameJSON(t, "100 headers", got.Headers, rows(100))
		assertSameJSON(t, "longest query param", got.QueryParams, "["+longest+"]")
	})
}

func TestHeadersAndQueryParamsPartialUpdate(t *testing.T) {
	f, owner, _, project := newFolderProject(t, "kv-partial")
	request := f.createRequest(t, owner.Token, project.ID, map[string]any{"name": "R", "url": "https://x.test"})

	headersA := `[{"key":"A","value":"1","enabled":true}]`
	headersB := `[{"key":"B","value":"2","enabled":false}]`
	paramsA := `[{"key":"p","value":"1","enabled":true}]`
	paramsB := `[{"key":"p","value":"1","enabled":true},{"key":"p","value":"2","enabled":true}]`

	f.mustPatchRequest(t, owner.Token, request.ID, `{"headers":`+headersA+`,"query_params":`+paramsA+`}`)

	steps := []struct {
		name        string
		body        string
		wantHeaders string
		wantParams  string
		wantURL     string
	}{
		{"only headers leaves query_params", `{"headers":` + headersB + `}`, headersB, paramsA, "https://x.test"},
		{"only query_params leaves headers", `{"query_params":` + paramsB + `}`, headersB, paramsB, "https://x.test"},
		{"null headers is unchanged", `{"headers":null,"url":"https://y.test"}`, headersB, paramsB, "https://y.test"},
		{"other fields leave both alone", `{"name":"Renamed"}`, headersB, paramsB, "https://y.test"},
		{"an empty array clears only headers", `{"headers":[]}`, `[]`, paramsB, "https://y.test"},
		{"an empty array clears query_params", `{"query_params":[]}`, `[]`, `[]`, "https://y.test"},
	}

	for _, step := range steps {
		t.Run(step.name, func(t *testing.T) {
			f.mustPatchRequest(t, owner.Token, request.ID, step.body)

			got := f.getRequestDetail(t, owner.Token, request.ID)
			assertSameJSON(t, "headers", got.Headers, step.wantHeaders)
			assertSameJSON(t, "query_params", got.QueryParams, step.wantParams)
			if got.URL != step.wantURL {
				t.Errorf("url = %q, want %q", got.URL, step.wantURL)
			}
		})
	}

	t.Run("nulls alone are nothing to change", func(t *testing.T) {
		rec := do(t, f.handler, http.MethodPatch, "/api/v1/requests/"+request.ID,
			`{"headers":null,"query_params":null}`, owner.Token)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want %d (body: %s)", rec.Code, http.StatusBadRequest, rec.Body)
		}
	})
}

func TestHeadersAndQueryParamsBumpUpdatedAt(t *testing.T) {
	f, owner, _, project := newFolderProject(t, "kv-updated-at")
	request := f.createRequest(t, owner.Token, project.ID, map[string]any{"name": "R"})

	last := request.UpdatedAt
	for _, body := range []string{
		`{"headers":[{"key":"A","value":"1","enabled":true}]}`,
		`{"query_params":[{"key":"q","value":"1","enabled":true}]}`,
	} {
		// Timestamps have microsecond resolution; make sure one passes.
		time.Sleep(2 * time.Millisecond)

		patched := f.mustPatchRequest(t, owner.Token, request.ID, body)
		if !patched.UpdatedAt.After(last) {
			t.Errorf("after %s: updated_at %v did not advance past %v", body, patched.UpdatedAt, last)
		}
		if !patched.CreatedAt.Equal(request.CreatedAt) {
			t.Errorf("after %s: created_at changed", body)
		}
		last = patched.UpdatedAt
	}
}

func TestCreateRequestRejectsHeadersAndQueryParams(t *testing.T) {
	f, owner, _, project := newFolderProject(t, "kv-create")

	// A new request always starts empty; the rows are set with PATCH.
	for _, field := range []string{"headers", "query_params"} {
		t.Run(field, func(t *testing.T) {
			rec := do(t, f.handler, http.MethodPost, "/api/v1/projects/"+project.ID+"/requests",
				`{"name":"x",`+fmt.Sprintf("%q", field)+`:[{"key":"k","value":"v","enabled":true}]}`, owner.Token)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want %d (body: %s)", rec.Code, http.StatusBadRequest, rec.Body)
			}
		})
	}

	if got := len(f.listRequests(t, owner.Token, project.ID)); got != 0 {
		t.Errorf("refused creates left %d requests", got)
	}
}
