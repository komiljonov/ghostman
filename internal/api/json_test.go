package api

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// testAPI returns an api whose logs are discarded. Handlers that do not touch
// the database work with a nil pool.
func testAPI() *api {
	return &api{logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
}

func TestWriteJSON(t *testing.T) {
	a := testAPI()
	rec := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil)

	a.writeJSON(rec, req, http.StatusCreated, map[string]string{"message": "hi"})

	if rec.Code != http.StatusCreated {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusCreated)
	}

	if got, want := rec.Header().Get("Content-Type"), "application/json; charset=utf-8"; got != want {
		t.Errorf("Content-Type = %q, want %q", got, want)
	}

	if got, want := rec.Body.String(), "{\"message\":\"hi\"}\n"; got != want {
		t.Errorf("body = %q, want %q", got, want)
	}
}

func TestWriteError(t *testing.T) {
	a := testAPI()
	rec := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil)

	a.writeError(rec, req, http.StatusBadRequest, codeBadRequest, "body must not be empty")

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}

	var got errorEnvelope
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decoding envelope: %v", err)
	}

	if got.Error.Code != codeBadRequest {
		t.Errorf("code = %q, want %q", got.Error.Code, codeBadRequest)
	}

	if got.Error.Message != "body must not be empty" {
		t.Errorf("message = %q, want %q", got.Error.Message, "body must not be empty")
	}
}

func TestReadJSON(t *testing.T) {
	type payload struct {
		Name string `json:"name"`
	}

	tests := []struct {
		name    string
		body    string
		want    string // expected name on success
		wantErr string // substring of the expected error
	}{
		{name: "valid", body: `{"name":"ghost"}`, want: "ghost"},
		{name: "empty", body: ``, wantErr: "must not be empty"},
		{name: "malformed", body: `{"name":`, wantErr: "malformed JSON"},
		{name: "wrong type", body: `{"name":42}`, wantErr: `wrong type for field "name"`},
		{name: "unknown field", body: `{"nope":"x"}`, wantErr: "unknown field"},
		{name: "trailing data", body: `{"name":"a"}{"name":"b"}`, wantErr: "single JSON value"},
		{name: "too large", body: `{"name":"` + strings.Repeat("x", maxRequestBody) + `"}`, wantErr: "must not be larger than"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/", strings.NewReader(tt.body))

			var dst payload
			err := readJSON(rec, req, &dst)

			if tt.wantErr != "" {
				if err == nil {
					t.Fatalf("readJSON() = nil, want error containing %q", tt.wantErr)
				}
				if !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("readJSON() error = %q, want it to contain %q", err, tt.wantErr)
				}
				return
			}

			if err != nil {
				t.Fatalf("readJSON() unexpected error: %v", err)
			}

			if dst.Name != tt.want {
				t.Errorf("name = %q, want %q", dst.Name, tt.want)
			}
		})
	}
}
