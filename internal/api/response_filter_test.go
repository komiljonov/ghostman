package api

import (
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

// responseFilterReads returns the stored filter through each shape that
// carries it: the project request list and GET /requests/{id}.
func (f teamsFixture) responseFilterReads(t *testing.T, token, projectID, requestID string) map[string]string {
	t.Helper()

	reads := map[string]string{"GET /requests/{id}": f.getRequestDetail(t, token, requestID).ResponseFilter}
	for _, entry := range f.listRequests(t, token, projectID) {
		if entry.ID == requestID {
			reads["request list"] = entry.ResponseFilter
		}
	}
	if _, ok := reads["request list"]; !ok {
		t.Fatalf("request %s missing from the request list", requestID)
	}

	return reads
}

func (f teamsFixture) assertResponseFilter(t *testing.T, token, projectID, requestID, want string) {
	t.Helper()

	for shape, got := range f.responseFilterReads(t, token, projectID, requestID) {
		if got != want {
			t.Errorf("%s response_filter = %q, want %q", shape, got, want)
		}
	}
}

func TestResponseFilter(t *testing.T) {
	f, owner, _, project := newFolderProject(t, "response-filter")
	request := f.createRequest(t, owner.Token, project.ID, map[string]any{"name": "Users"})

	t.Run("defaults to empty", func(t *testing.T) {
		if request.ResponseFilter != "" {
			t.Errorf("new request response_filter = %q, want empty", request.ResponseFilter)
		}
		f.assertResponseFilter(t, owner.Token, project.ID, request.ID, "")
	})

	// Opaque: stored exactly as sent, including jq that would not parse.
	for _, filter := range []string{
		`.data[] | {id, name: .attributes.name}`,
		`.items | map(select(.price > 10)) | length`,
		`  .[0]  `,
		`.broken | [`,
	} {
		t.Run("set "+filter, func(t *testing.T) {
			patched := f.mustPatchRequest(t, owner.Token, request.ID, `{"response_filter":`+jsonString(t, filter)+`}`)
			if patched.ResponseFilter != filter {
				t.Errorf("PATCH response response_filter = %q, want %q", patched.ResponseFilter, filter)
			}
			f.assertResponseFilter(t, owner.Token, project.ID, request.ID, filter)
		})
	}

	t.Run("absent and null are unchanged", func(t *testing.T) {
		f.mustPatchRequest(t, owner.Token, request.ID, `{"name":"Renamed"}`)
		f.mustPatchRequest(t, owner.Token, request.ID, `{"response_filter":null,"url":"https://x.test"}`)
		f.assertResponseFilter(t, owner.Token, project.ID, request.ID, `.broken | [`)
	})

	t.Run("the 2048-character limit", func(t *testing.T) {
		longest := strings.Repeat("é", 2048) // characters, not bytes
		f.mustPatchRequest(t, owner.Token, request.ID, `{"response_filter":`+jsonString(t, longest)+`}`)
		f.assertResponseFilter(t, owner.Token, project.ID, request.ID, longest)

		rec := do(t, f.handler, http.MethodPatch, "/api/v1/requests/"+request.ID,
			`{"response_filter":`+jsonString(t, longest+"x")+`}`, owner.Token)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("2049 characters: status = %d, want %d", rec.Code, http.StatusBadRequest)
		}
		if msg := decodeEnvelope(t, rec).Error.Message; msg != "response_filter must be at most 2048 characters" {
			t.Errorf("message = %q", msg)
		}
		f.assertResponseFilter(t, owner.Token, project.ID, request.ID, longest)
	})

	t.Run("wrong types are refused", func(t *testing.T) {
		for _, body := range []string{`{"response_filter":42}`, `{"response_filter":[".a"]}`, `{"response_filter":null}`} {
			if rec := do(t, f.handler, http.MethodPatch, "/api/v1/requests/"+request.ID, body, owner.Token); rec.Code != http.StatusBadRequest {
				t.Errorf("%s: status = %d, want %d", body, rec.Code, http.StatusBadRequest)
			}
		}
	})

	t.Run("an empty string clears it", func(t *testing.T) {
		f.mustPatchRequest(t, owner.Token, request.ID, `{"response_filter":""}`)
		f.assertResponseFilter(t, owner.Token, project.ID, request.ID, "")
	})

	t.Run("create does not accept it", func(t *testing.T) {
		rec := do(t, f.handler, http.MethodPost, "/api/v1/projects/"+project.ID+"/requests",
			`{"name":"x","response_filter":".a"}`, owner.Token)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("status = %d, want %d", rec.Code, http.StatusBadRequest)
		}
	})

	t.Run("the database enforces the length on its own", func(t *testing.T) {
		_, err := f.pool.Exec(t.Context(),
			"UPDATE requests SET response_filter = repeat('x', 2049) WHERE id = $1", request.ID)

		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != checkViolation || pgErr.ConstraintName != "requests_response_filter_length" {
			t.Errorf("error = %v, want a CHECK violation of requests_response_filter_length", err)
		}
	})
}

func TestResponseFilterAccess(t *testing.T) {
	f := newAccessMatrixFixture(t)
	request := f.createRequest(t, f.owner.Token, f.ownerProject.ID, map[string]any{"name": "hidden"})

	for _, token := range []string{f.limited.Token, f.outsider.Token} {
		rec := do(t, f.handler, http.MethodPatch, "/api/v1/requests/"+request.ID, `{"response_filter":".x"}`, token)
		if rec.Code != http.StatusNotFound {
			t.Errorf("status = %d, want %d", rec.Code, http.StatusNotFound)
			continue
		}
		if msg := decodeEnvelope(t, rec).Error.Message; msg != messageRequestNotFound {
			t.Errorf("message = %q, want %q", msg, messageRequestNotFound)
		}
	}

	f.assertResponseFilter(t, f.owner.Token, f.ownerProject.ID, request.ID, "")
}
