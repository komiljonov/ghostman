package api

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

// patchBody sends PATCH /requests/{id} with {"body": <body>}.
func (f teamsFixture) patchBody(t *testing.T, token, requestID, body string) *httptest.ResponseRecorder {
	t.Helper()

	return do(t, f.handler, http.MethodPatch, "/api/v1/requests/"+requestID, `{"body":`+body+`}`, token)
}

// jsonString encodes s as a JSON string literal.
func jsonString(t *testing.T, s string) string {
	t.Helper()

	encoded, err := json.Marshal(s)
	if err != nil {
		t.Fatalf("encoding string: %v", err)
	}
	return string(encoded)
}

func TestRequestBodyRoundTripAndSwitching(t *testing.T) {
	f, owner, _, project := newFolderProject(t, "body-roundtrip")
	request := f.createRequest(t, owner.Token, project.ID, map[string]any{"name": "Create user", "method": "POST"})

	// A fresh request starts with no body.
	assertSameJSON(t, "initial body", f.getRequestDetail(t, owner.Token, request.ID).Body, `{"type":"none"}`)

	steps := []struct {
		name string
		body string
	}{
		// {{vars}} inside the content, and content that is not valid JSON
		// despite its content type: both are the user's business.
		{"raw json with vars", `{"type":"raw","content_type":"application/json","content":"{\"name\": \"{{user}}\", \"age\": {{age}}}"}`},
		{"raw not matching its type", `{"type":"raw","content_type":"application/json","content":"{ half-typed"}`},
		{"raw with parameters and empty content", `{"type":"raw","content_type":"text/plain; charset=utf-8","content":""}`},
		{"form with duplicates and a disabled row", `{"type":"form","fields":[
			{"key":"tag","value":"a","enabled":true},
			{"key":"tag","value":"b","enabled":true},
			{"key":"token","value":"{{token}}","enabled":false}]}`},
		{"form with no fields", `{"type":"form","fields":[]}`},
		{"back to raw", `{"type":"raw","content_type":"application/xml","content":"<a>{{x}}</a>"}`},
		{"none", `{"type":"none"}`},
	}

	for _, step := range steps {
		t.Run(step.name, func(t *testing.T) {
			rec := f.patchBody(t, owner.Token, request.ID, step.body)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want %d (body: %s)", rec.Code, http.StatusOK, rec.Body)
			}

			// Compared by value, including the absence of fields: switching
			// variants must leave nothing of the previous one behind.
			assertSameJSON(t, "PATCH response body", decodeRequestDetail(t, rec.Body.Bytes()).Body, step.body)
			assertSameJSON(t, "GET body", f.getRequestDetail(t, owner.Token, request.ID).Body, step.body)
		})
	}
}

func TestRequestBodyValidation(t *testing.T) {
	f, owner, _, project := newFolderProject(t, "body-validation")
	request := f.createRequest(t, owner.Token, project.ID, map[string]any{"name": "R"})

	row := `{"key":"k","value":"v","enabled":true}`
	rows := func(n int) string {
		return "[" + strings.TrimSuffix(strings.Repeat(row+",", n), ",") + "]"
	}
	rawWith := func(content string) string {
		return `{"type":"raw","content_type":"text/plain","content":` + jsonString(t, content) + `}`
	}

	tests := []struct {
		name    string
		body    string
		message string
	}{
		{"a string", `"none"`, "body must be an object"},
		{"an array", `[]`, "body must be an object"},
		{"no type", `{}`, "body.type is required"},
		{"type not a string", `{"type":1}`, "body.type must be a string"},
		{"unknown type", `{"type":"graphql"}`, "body.type must be one of none, raw, form"},
		{"file bodies are not synced", `{"type":"file","path":"/tmp/x"}`, "body.type must be one of none, raw, form"},
		{"extra field on none", `{"type":"none","content":"x"}`, `body.content is not a known field for a "none" body`},
		{"raw without content_type", `{"type":"raw","content":"x"}`, "body.content_type is required"},
		{"raw with blank content_type", `{"type":"raw","content_type":"   ","content":"x"}`, "body.content_type is required"},
		{"raw content_type not a string", `{"type":"raw","content_type":7,"content":"x"}`, "body.content_type must be a string"},
		{"raw content_type over 200", `{"type":"raw","content_type":"` + strings.Repeat("t", 201) + `","content":""}`,
			"body.content_type must be at most 200 characters"},
		{"raw without content", `{"type":"raw","content_type":"text/plain"}`, "body.content is required"},
		{"raw content not a string", `{"type":"raw","content_type":"text/plain","content":{"a":1}}`, "body.content must be a string"},
		{"raw content null", `{"type":"raw","content_type":"text/plain","content":null}`, "body.content must be a string"},
		{"raw with form fields", `{"type":"raw","content_type":"text/plain","content":"","fields":[]}`,
			`body.fields is not a known field for a "raw" body`},
		{"form without fields", `{"type":"form"}`, "body.fields is required"},
		{"form fields null", `{"type":"form","fields":null}`, "body.fields must be an array"},
		{"form fields an object", `{"type":"form","fields":{}}`, "body.fields must be an array"},
		{"form with content", `{"type":"form","fields":[],"content":"x"}`, `body.content is not a known field for a "form" body`},
		{"form row enabled not a bool", `{"type":"form","fields":[` + row + `,` + row + `,{"key":"k","value":"v","enabled":"yes"}]}`,
			"body.fields[2].enabled must be a boolean"},
		{"form row empty key", `{"type":"form","fields":[{"key":"","value":"","enabled":true}]}`, "body.fields[0].key is required"},
		{"form row unknown field", `{"type":"form","fields":[{"key":"k","value":"v","enabled":true,"file":"x"}]}`,
			"body.fields[0].file is not a known field"},
		{"form 101 rows", `{"type":"form","fields":` + rows(101) + `}`, "body.fields must have at most 100 rows"},
		{"content one byte over 1 MiB", rawWith(strings.Repeat("a", maxRawContentBytes+1)), "body.content must be at most 1048576 bytes"},
		// Bytes, not characters: 524,289 two-byte characters are over.
		{"multibyte content over 1 MiB", rawWith(strings.Repeat("é", maxRawContentBytes/2+1)), "body.content must be at most 1048576 bytes"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := f.patchBody(t, owner.Token, request.ID, tt.body)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want %d (body: %.300s)", rec.Code, http.StatusBadRequest, rec.Body)
			}
			if msg := decodeEnvelope(t, rec).Error.Message; !strings.HasPrefix(msg, tt.message) {
				t.Errorf("message = %q, want it to start with %q", msg, tt.message)
			}
		})
	}

	// Nothing refused above was written.
	assertSameJSON(t, "body after refused patches", f.getRequestDetail(t, owner.Token, request.ID).Body, `{"type":"none"}`)

	t.Run("exactly 1 MiB of content is accepted", func(t *testing.T) {
		for _, content := range []string{
			strings.Repeat("a", maxRawContentBytes),
			strings.Repeat("é", maxRawContentBytes/2),
		} {
			body := rawWith(content)
			if rec := f.patchBody(t, owner.Token, request.ID, body); rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want %d (body: %.300s)", rec.Code, http.StatusOK, rec.Body)
			}
			assertSameJSON(t, "stored body", f.getRequestDetail(t, owner.Token, request.ID).Body, body)
		}
	})
}

func TestRequestBodyPartialUpdate(t *testing.T) {
	f, owner, _, project := newFolderProject(t, "body-partial")
	request := f.createRequest(t, owner.Token, project.ID, map[string]any{"name": "R"})

	headers := `[{"key":"Accept","value":"application/json","enabled":true}]`
	params := `[{"key":"q","value":"1","enabled":true}]`
	rawBody := `{"type":"raw","content_type":"application/json","content":"{}"}`
	formBody := `{"type":"form","fields":[{"key":"a","value":"1","enabled":true}]}`

	f.mustPatchRequest(t, owner.Token, request.ID, `{"headers":`+headers+`,"query_params":`+params+`,"body":`+rawBody+`}`)

	steps := []struct {
		name        string
		patch       string
		wantHeaders string
		wantParams  string
		wantBody    string
	}{
		{"body alone leaves headers and query_params", `{"body":` + formBody + `}`, headers, params, formBody},
		{"headers alone leave the body", `{"headers":[]}`, `[]`, params, formBody},
		{"query_params alone leave the body", `{"query_params":[]}`, `[]`, `[]`, formBody},
		{"null body is unchanged", `{"body":null,"name":"Renamed"}`, `[]`, `[]`, formBody},
	}

	for _, step := range steps {
		t.Run(step.name, func(t *testing.T) {
			f.mustPatchRequest(t, owner.Token, request.ID, step.patch)

			got := f.getRequestDetail(t, owner.Token, request.ID)
			assertSameJSON(t, "headers", got.Headers, step.wantHeaders)
			assertSameJSON(t, "query_params", got.QueryParams, step.wantParams)
			assertSameJSON(t, "body", got.Body, step.wantBody)
		})
	}

	t.Run("a body change moves updated_at", func(t *testing.T) {
		before := f.getRequestDetail(t, owner.Token, request.ID)
		time.Sleep(2 * time.Millisecond)

		after := f.mustPatchRequest(t, owner.Token, request.ID, `{"body":{"type":"none"}}`)
		if !after.UpdatedAt.After(before.UpdatedAt) {
			t.Errorf("updated_at %v did not advance past %v", after.UpdatedAt, before.UpdatedAt)
		}
	})

	t.Run("a null body alone is nothing to change", func(t *testing.T) {
		rec := do(t, f.handler, http.MethodPatch, "/api/v1/requests/"+request.ID, `{"body":null}`, owner.Token)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want %d (body: %s)", rec.Code, http.StatusBadRequest, rec.Body)
		}
	})
}

func TestCreateRequestRejectsBody(t *testing.T) {
	f, owner, _, project := newFolderProject(t, "body-create")

	rec := do(t, f.handler, http.MethodPost, "/api/v1/projects/"+project.ID+"/requests",
		`{"name":"x","body":{"type":"none"}}`, owner.Token)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d (body: %s)", rec.Code, http.StatusBadRequest, rec.Body)
	}
	if got := len(f.listRequests(t, owner.Token, project.ID)); got != 0 {
		t.Errorf("refused create left %d requests", got)
	}
}

// TestOversizedPayloadIs413 checks the server-wide 2 MiB limit answers with a
// proper 413 envelope, whether or not the client declared the length.
func TestOversizedPayloadIs413(t *testing.T) {
	f, owner, _, project := newFolderProject(t, "body-413")
	request := f.createRequest(t, owner.Token, project.ID, map[string]any{"name": "R"})

	// 3 MiB of content: well over the payload limit, whatever the envelope.
	payload := `{"body":{"type":"raw","content_type":"text/plain","content":"` + strings.Repeat("a", 3<<20) + `"}}`
	path := "/api/v1/requests/" + request.ID

	send := func(t *testing.T, declareLength bool) *httptest.ResponseRecorder {
		t.Helper()

		req := httptest.NewRequestWithContext(t.Context(), http.MethodPatch, path, strings.NewReader(payload))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+owner.Token)
		if declareLength {
			req.Header.Set("Content-Length", strconv.Itoa(len(payload)))
		} else {
			// As with a chunked upload: the size is only known by reading.
			req.ContentLength = -1
			req.Body = io.NopCloser(strings.NewReader(payload))
		}

		rec := httptest.NewRecorder()
		f.handler.ServeHTTP(rec, req)
		return rec
	}

	for _, tt := range []struct {
		name          string
		declareLength bool
	}{
		{"declared length", true},
		{"undeclared length", false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			rec := send(t, tt.declareLength)
			if rec.Code != http.StatusRequestEntityTooLarge {
				t.Fatalf("status = %d, want %d (body: %s)", rec.Code, http.StatusRequestEntityTooLarge, rec.Body)
			}

			env := decodeEnvelope(t, rec)
			if env.Error.Code != codeTooLarge {
				t.Errorf("code = %q, want %q", env.Error.Code, codeTooLarge)
			}
			if !strings.Contains(env.Error.Message, strconv.Itoa(maxRequestBody)) {
				t.Errorf("message = %q, want it to state the %d-byte limit", env.Error.Message, maxRequestBody)
			}
		})
	}

	assertSameJSON(t, "body after 413", f.getRequestDetail(t, owner.Token, request.ID).Body, `{"type":"none"}`)
}
