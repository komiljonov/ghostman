package api

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/komiljonov/ghostman/internal/db"
)

func (f teamsFixture) createExample(t *testing.T, token, requestID string, fields map[string]any) exampleResponse {
	t.Helper()

	rec := do(t, f.handler, http.MethodPost, "/api/v1/requests/"+requestID+"/examples", folderBody(t, fields), token)
	if rec.Code != http.StatusCreated {
		t.Fatalf("creating example: status = %d, want 201 (body: %.300s)", rec.Code, rec.Body)
	}

	var example exampleResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &example); err != nil {
		t.Fatalf("decoding example: %v", err)
	}
	return example
}

func (f teamsFixture) getExample(t *testing.T, token, exampleID string) exampleResponse {
	t.Helper()

	rec := do(t, f.handler, http.MethodGet, "/api/v1/examples/"+exampleID, "", token)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET example: status = %d (body: %.300s)", rec.Code, rec.Body)
	}

	var example exampleResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &example); err != nil {
		t.Fatalf("decoding example: %v", err)
	}
	return example
}

func (f teamsFixture) listExamples(t *testing.T, token, requestID string) []exampleSummaryResponse {
	t.Helper()

	rec := do(t, f.handler, http.MethodGet, "/api/v1/requests/"+requestID+"/examples", "", token)
	if rec.Code != http.StatusOK {
		t.Fatalf("listing examples: status = %d (body: %s)", rec.Code, rec.Body)
	}

	var examples []exampleSummaryResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &examples); err != nil {
		t.Fatalf("decoding examples: %v", err)
	}
	return examples
}

func exampleNames(examples []exampleSummaryResponse) string {
	names := make([]string, 0, len(examples))
	for _, e := range examples {
		names = append(names, e.Name)
	}
	return strings.Join(names, ",")
}

func b64(raw []byte) string {
	return base64.StdEncoding.EncodeToString(raw)
}

func TestExampleCRUD(t *testing.T) {
	f, owner, _, project := newFolderProject(t, "example-crud")
	request := f.createRequest(t, owner.Token, project.ID, map[string]any{"name": "R"})

	empty := f.createExample(t, owner.Token, request.ID, map[string]any{"name": " Empty "})
	if empty.Name != "Empty" || empty.Status != 200 || string(empty.Headers) != "[]" || empty.ContentType != "" ||
		empty.Body != nil || empty.BodySize != 0 || empty.SortOrder != 0 || empty.RequestID != request.ID {
		t.Errorf("defaults = %+v, want trimmed name, 200, [], no body, sort 0", empty)
	}

	ok := f.createExample(t, owner.Token, request.ID, map[string]any{
		"name": "OK", "status": 201, "content_type": "application/json",
		"headers": []map[string]any{{"key": "X-Id", "value": "{{id}}", "enabled": true}},
		"body":    b64([]byte(`{"id":1}`)),
	})
	if ok.SortOrder != 1 || ok.Status != 201 || ok.BodySize != 8 || string(ok.Body) != `{"id":1}` {
		t.Errorf("second example = %+v", ok)
	}
	assertSameJSON(t, "headers", ok.Headers, `[{"key":"X-Id","value":"{{id}}","enabled":true}]`)

	if got := exampleNames(f.listExamples(t, owner.Token, request.ID)); got != "Empty,OK" {
		t.Errorf("list = %s, want Empty,OK in order", got)
	}

	t.Run("partial PATCH changes only what it names", func(t *testing.T) {
		time.Sleep(5 * time.Millisecond)
		rec := do(t, f.handler, http.MethodPatch, "/api/v1/examples/"+ok.ID, `{"status":404}`, owner.Token)
		var patched exampleResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &patched); err != nil || rec.Code != http.StatusOK {
			t.Fatalf("PATCH: status = %d (body: %s)", rec.Code, rec.Body)
		}
		if patched.Status != 404 || patched.Name != "OK" || patched.ContentType != "application/json" ||
			string(patched.Body) != `{"id":1}` || patched.BodySize != 8 {
			t.Errorf("after status PATCH = %+v, want only status changed", patched)
		}
		assertSameJSON(t, "headers", patched.Headers, string(ok.Headers))
		if !patched.UpdatedAt.After(ok.UpdatedAt) {
			t.Errorf("updated_at = %v, want after %v", patched.UpdatedAt, ok.UpdatedAt)
		}

		patched = f.patchExample(t, owner.Token, ok.ID, `{"name":"Not found","headers":[],"content_type":" text/plain ","body":"`+b64([]byte("gone"))+`"}`)
		if patched.Name != "Not found" || string(patched.Headers) != "[]" || patched.ContentType != "text/plain" ||
			string(patched.Body) != "gone" || patched.BodySize != 4 || patched.Status != 404 {
			t.Errorf("after full PATCH = %+v", patched)
		}

		// null leaves the body alone; "" clears it.
		patched = f.patchExample(t, owner.Token, ok.ID, `{"body":null,"name":"Still"}`)
		if string(patched.Body) != "gone" || patched.BodySize != 4 {
			t.Errorf("null body changed it: %+v", patched)
		}
		patched = f.patchExample(t, owner.Token, ok.ID, `{"body":""}`)
		if patched.Body != nil || patched.BodySize != 0 {
			t.Errorf("empty body did not clear: %+v", patched)
		}
		if rec := do(t, f.handler, http.MethodGet, "/api/v1/examples/"+ok.ID, "", owner.Token); !strings.Contains(rec.Body.String(), `"body":null`) {
			t.Errorf("cleared body serializes as %s, want null", rec.Body)
		}
	})

	if rec := do(t, f.handler, http.MethodDelete, "/api/v1/examples/"+empty.ID, "", owner.Token); rec.Code != http.StatusNoContent {
		t.Fatalf("DELETE: status = %d", rec.Code)
	}
	if rec := do(t, f.handler, http.MethodGet, "/api/v1/examples/"+empty.ID, "", owner.Token); rec.Code != http.StatusNotFound {
		t.Errorf("GET after delete: status = %d, want 404", rec.Code)
	}
	if got := exampleNames(f.listExamples(t, owner.Token, request.ID)); got != "Still" {
		t.Errorf("list after delete = %s, want Still", got)
	}
}

func (f teamsFixture) patchExample(t *testing.T, token, exampleID, body string) exampleResponse {
	t.Helper()

	rec := do(t, f.handler, http.MethodPatch, "/api/v1/examples/"+exampleID, body, token)
	if rec.Code != http.StatusOK {
		t.Fatalf("PATCH %.200s: status = %d (body: %s)", body, rec.Code, rec.Body)
	}

	var example exampleResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &example); err != nil {
		t.Fatalf("decoding example: %v", err)
	}
	return example
}

func TestExampleBinaryRoundTrip(t *testing.T) {
	f, owner, _, project := newFolderProject(t, "example-binary")
	request := f.createRequest(t, owner.Token, project.ID, map[string]any{"name": "R"})

	// Every byte value, including NUL and invalid UTF-8, then the full 1 MiB.
	every := make([]byte, 256)
	for i := range every {
		every[i] = byte(i)
	}
	full := bytes.Repeat(every, db.MaxExampleBodySize/len(every))

	for name, body := range map[string][]byte{"every byte": every, "exactly 1MB": full} {
		created := f.createExample(t, owner.Token, request.ID, map[string]any{
			"name": name, "content_type": "application/octet-stream", "body": b64(body),
		})
		got := f.getExample(t, owner.Token, created.ID)
		if !bytes.Equal(got.Body, body) || int(got.BodySize) != len(body) {
			t.Errorf("%s: round trip changed the body (size %d, want %d)", name, got.BodySize, len(body))
		}

		updated := f.patchExample(t, owner.Token, created.ID, `{"body":"`+b64(body[1:])+`"}`)
		if !bytes.Equal(updated.Body, body[1:]) || int(updated.BodySize) != len(body)-1 {
			t.Errorf("%s: PATCH round trip changed the body", name)
		}
	}
}

func TestExampleValidation(t *testing.T) {
	f, owner, _, project := newFolderProject(t, "example-validation")
	request := f.createRequest(t, owner.Token, project.ID, map[string]any{"name": "R"})
	existing := f.createExample(t, owner.Token, request.ID, map[string]any{"name": "E", "body": b64([]byte("keep"))})
	createPath := "/api/v1/requests/" + request.ID + "/examples"
	patchPath := "/api/v1/examples/" + existing.ID

	tooBig := b64(make([]byte, db.MaxExampleBodySize+1))

	refused := []struct {
		name, method, path, body, message string
	}{
		{"empty name", http.MethodPost, createPath, `{"name":"  "}`, "name is required"},
		{"name too long", http.MethodPost, createPath, `{"name":"` + strings.Repeat("n", 101) + `"}`, "name must be at most 100"},
		{"status too low", http.MethodPost, createPath, `{"name":"x","status":99}`, "status must be between 100 and 599"},
		{"status too high", http.MethodPost, createPath, `{"name":"x","status":600}`, "status must be between 100 and 599"},
		{"content_type too long", http.MethodPost, createPath, `{"name":"x","content_type":"` + strings.Repeat("c", 201) + `"}`, "content_type must be at most 200"},
		{"headers not rows", http.MethodPost, createPath, `{"name":"x","headers":{"a":"b"}}`, "headers must be an array"},
		{"header row without key", http.MethodPost, createPath, `{"name":"x","headers":[{"value":"v","enabled":true}]}`, "headers[0].key"},
		{"body not base64", http.MethodPost, createPath, `{"name":"x","body":"not base64!"}`, "body must be base64"},
		{"body over 1MB", http.MethodPost, createPath, `{"name":"x","body":"` + tooBig + `"}`, messageExampleBodyTooLarge},
		{"unknown field", http.MethodPost, createPath, `{"name":"x","body_size":3}`, `body contains unknown field "body_size"`},
		{"PATCH nothing", http.MethodPatch, patchPath, `{}`, "provide at least one of"},
		{"PATCH empty name", http.MethodPatch, patchPath, `{"name":""}`, "name is required"},
		{"PATCH bad status", http.MethodPatch, patchPath, `{"status":42}`, "status must be between"},
		{"PATCH body over 1MB", http.MethodPatch, patchPath, `{"body":"` + tooBig + `"}`, messageExampleBodyTooLarge},
	}
	for _, tt := range refused {
		rec := do(t, f.handler, tt.method, tt.path, tt.body, owner.Token)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400 (body: %.300s)", tt.name, rec.Code, rec.Body)
			continue
		}
		if msg := decodeEnvelope(t, rec).Error.Message; !strings.HasPrefix(msg, tt.message) {
			t.Errorf("%s: message = %q, want prefix %q", tt.name, msg, tt.message)
		}
	}

	if got := f.getExample(t, owner.Token, existing.ID); string(got.Body) != "keep" || got.Name != "E" {
		t.Errorf("refused PATCHes changed the example: %+v", got)
	}
	if got := f.listExamples(t, owner.Token, request.ID); len(got) != 1 {
		t.Errorf("refused creates stored %d examples, want 1", len(got))
	}

	t.Run("the CHECK backstops the size and its bookkeeping", func(t *testing.T) {
		for name, query := range map[string]string{
			"size mismatch": "UPDATE request_examples SET body_size = 99 WHERE id = $1",
			"over the cap":  "UPDATE request_examples SET body = repeat('x', 1048577)::bytea, body_size = 1048577 WHERE id = $1",
		} {
			_, err := f.pool.Exec(t.Context(), query, existing.ID)
			var pgErr *pgconn.PgError
			if !errors.As(err, &pgErr) || pgErr.Code != checkViolation || pgErr.ConstraintName != "request_examples_body_size" {
				t.Errorf("%s: error = %v, want a CHECK violation of request_examples_body_size", name, err)
			}
		}
	})
}

func TestExampleListIsSummariesOnly(t *testing.T) {
	f, owner, _, project := newFolderProject(t, "example-summary")
	request := f.createRequest(t, owner.Token, project.ID, map[string]any{"name": "R"})
	f.createExample(t, owner.Token, request.ID, map[string]any{
		"name": "E", "status": 418, "content_type": "text/plain",
		"headers": []map[string]any{{"key": "H", "value": "v", "enabled": true}},
		"body":    b64([]byte("teapot")),
	})

	rec := do(t, f.handler, http.MethodGet, "/api/v1/requests/"+request.ID+"/examples", "", owner.Token)
	var rows []map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &rows); err != nil || len(rows) != 1 {
		t.Fatalf("list: %v (body: %s)", err, rec.Body)
	}

	want := []string{"id", "name", "status", "content_type", "body_size", "sort_order", "updated_at"}
	if len(rows[0]) != len(want) {
		t.Errorf("summary fields = %v, want exactly %v", rows[0], want)
	}
	for _, key := range want {
		if _, ok := rows[0][key]; !ok {
			t.Errorf("summary lacks %q", key)
		}
	}
	for _, key := range []string{"headers", "body"} {
		if _, ok := rows[0][key]; ok {
			t.Errorf("summary carries %q", key)
		}
	}
	if rows[0]["body_size"] != float64(6) || rows[0]["status"] != float64(418) {
		t.Errorf("summary = %v", rows[0])
	}
}

func TestExampleOrder(t *testing.T) {
	f, owner, _, project := newFolderProject(t, "example-order")
	request := f.createRequest(t, owner.Token, project.ID, map[string]any{"name": "R"})
	other := f.createRequest(t, owner.Token, project.ID, map[string]any{"name": "Other"})
	a := f.createExample(t, owner.Token, request.ID, map[string]any{"name": "A"})
	b := f.createExample(t, owner.Token, request.ID, map[string]any{"name": "B"})
	c := f.createExample(t, owner.Token, request.ID, map[string]any{"name": "C"})
	foreign := f.createExample(t, owner.Token, other.ID, map[string]any{"name": "X"})

	put := func(ids ...string) int {
		return do(t, f.handler, http.MethodPut, "/api/v1/examples/order",
			folderBody(t, map[string]any{"request_id": request.ID, "example_ids": ids}), owner.Token).Code
	}

	for name, ids := range map[string][]string{
		"missing one":       {a.ID, b.ID},
		"another request's": {a.ID, b.ID, c.ID, foreign.ID},
		"duplicate":         {a.ID, b.ID, b.ID},
		"not a uuid":        {"nope"},
	} {
		if status := put(ids...); status != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400", name, status)
		}
	}

	if status := put(c.ID, a.ID, b.ID); status != http.StatusNoContent {
		t.Fatalf("reorder: status = %d, want 204", status)
	}
	examples := f.listExamples(t, owner.Token, request.ID)
	if got := exampleNames(examples); got != "C,A,B" {
		t.Errorf("order = %s, want C,A,B", got)
	}
	for i, e := range examples {
		if e.SortOrder != int32(i) {
			t.Errorf("%s sort_order = %d, want %d (0-based, contiguous)", e.Name, e.SortOrder, i)
		}
	}
	if d := f.createExample(t, owner.Token, request.ID, map[string]any{"name": "D"}); d.SortOrder != 3 {
		t.Errorf("new example sort_order = %d, want 3", d.SortOrder)
	}
	if got := exampleNames(f.listExamples(t, owner.Token, other.ID)); got != "X" {
		t.Errorf("other request's examples = %s, want X untouched", got)
	}
}

func TestExampleCountAndCascade(t *testing.T) {
	f, owner, _, project := newFolderProject(t, "example-count")
	request := f.createRequest(t, owner.Token, project.ID, map[string]any{"name": "R"})
	bystander := f.createRequest(t, owner.Token, project.ID, map[string]any{"name": "Bystander"})

	if got := f.getRequestDetail(t, owner.Token, request.ID).ExampleCount; got != 0 {
		t.Errorf("example_count before = %d, want 0", got)
	}

	first := f.createExample(t, owner.Token, request.ID, map[string]any{"name": "A"})
	f.createExample(t, owner.Token, request.ID, map[string]any{"name": "B", "body": b64([]byte("x"))})
	kept := f.createExample(t, owner.Token, bystander.ID, map[string]any{"name": "Kept"})

	if got := f.getRequestDetail(t, owner.Token, request.ID).ExampleCount; got != 2 {
		t.Errorf("GET example_count = %d, want 2", got)
	}
	if got := f.mustPatchRequest(t, owner.Token, request.ID, `{"name":"Renamed"}`).ExampleCount; got != 2 {
		t.Errorf("PATCH example_count = %d, want 2", got)
	}

	if rec := do(t, f.handler, http.MethodDelete, "/api/v1/examples/"+first.ID, "", owner.Token); rec.Code != http.StatusNoContent {
		t.Fatalf("deleting an example: status = %d", rec.Code)
	}
	if got := f.getRequestDetail(t, owner.Token, request.ID).ExampleCount; got != 1 {
		t.Errorf("example_count after delete = %d, want 1", got)
	}

	if rec := do(t, f.handler, http.MethodDelete, "/api/v1/requests/"+request.ID, "", owner.Token); rec.Code != http.StatusNoContent {
		t.Fatalf("deleting the request: status = %d", rec.Code)
	}

	var left int
	if err := f.pool.QueryRow(t.Context(),
		"SELECT count(*) FROM request_examples WHERE request_id = $1", request.ID).Scan(&left); err != nil {
		t.Fatalf("counting: %v", err)
	}
	if left != 0 {
		t.Errorf("%d examples outlived their request, want 0", left)
	}
	if got := f.getExample(t, owner.Token, kept.ID); got.Name != "Kept" {
		t.Errorf("another request's example was touched: %+v", got)
	}
}

func TestExampleAccessMatrix(t *testing.T) {
	f := newAccessMatrixFixture(t)
	hiddenRequest := f.createRequest(t, f.owner.Token, f.ownerProject.ID, map[string]any{"name": "Hidden"})
	hidden := f.createExample(t, f.owner.Token, hiddenRequest.ID, map[string]any{"name": "Hidden", "status": 200})

	type request struct {
		name, method, path, body, message string
	}
	denied := func(requestID, exampleID string) []request {
		return []request{
			{"create", http.MethodPost, "/api/v1/requests/" + requestID + "/examples", `{"name":"x"}`, messageRequestNotFound},
			{"list", http.MethodGet, "/api/v1/requests/" + requestID + "/examples", "", messageRequestNotFound},
			{"order", http.MethodPut, "/api/v1/examples/order", `{"request_id":"` + requestID + `","example_ids":["` + exampleID + `"]}`, messageRequestNotFound},
			{"get", http.MethodGet, "/api/v1/examples/" + exampleID, "", messageExampleNotFound},
			{"patch", http.MethodPatch, "/api/v1/examples/" + exampleID, `{"status":500}`, messageExampleNotFound},
			{"delete", http.MethodDelete, "/api/v1/examples/" + exampleID, "", messageExampleNotFound},
		}
	}
	check := func(t *testing.T, token string, reqs []request) {
		t.Helper()
		for _, req := range reqs {
			rec := do(t, f.handler, req.method, req.path, req.body, token)
			if rec.Code != http.StatusNotFound {
				t.Errorf("%s: status = %d, want 404", req.name, rec.Code)
				continue
			}
			if msg := decodeEnvelope(t, rec).Error.Message; msg != req.message {
				t.Errorf("%s: message = %q, want %q", req.name, msg, req.message)
			}
		}
	}

	t.Run("restricted member without a grant", func(t *testing.T) { check(t, f.limited.Token, denied(hiddenRequest.ID, hidden.ID)) })
	t.Run("outsider", func(t *testing.T) { check(t, f.outsider.Token, denied(hiddenRequest.ID, hidden.ID)) })
	t.Run("nonexistent ids look the same", func(t *testing.T) { check(t, f.owner.Token, denied(uuid.New().String(), uuid.New().String())) })

	if got := f.listExamples(t, f.owner.Token, hiddenRequest.ID); len(got) != 1 || got[0].Status != 200 {
		t.Errorf("hidden example changed: %+v", got)
	}

	t.Run("an open member reaches every project", func(t *testing.T) {
		if got := f.getExample(t, f.openMember.Token, hidden.ID); got.ID != hidden.ID {
			t.Errorf("open member GET = %+v", got)
		}
	})

	t.Run("any member with access has full rights, within their project", func(t *testing.T) {
		token := f.limited.Token
		mine := f.createRequest(t, token, f.grantedProject.ID, map[string]any{"name": "Mine"})
		example := f.createExample(t, token, mine.ID, map[string]any{"name": "E"})
		f.patchExample(t, token, example.ID, `{"status":500}`)

		// An example id from another request cannot be slipped into this
		// request's order.
		rec := do(t, f.handler, http.MethodPut, "/api/v1/examples/order",
			`{"request_id":"`+mine.ID+`","example_ids":["`+example.ID+`","`+hidden.ID+`"]}`, token)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("ordering with a hidden example: status = %d, want 400", rec.Code)
		}

		if rec := do(t, f.handler, http.MethodDelete, "/api/v1/examples/"+example.ID, "", token); rec.Code != http.StatusNoContent {
			t.Errorf("delete: status = %d", rec.Code)
		}
	})
}

func TestExampleRoutesRequireAuth(t *testing.T) {
	f := newTeamsFixture(t)
	id := uuid.New().String()

	for _, route := range []struct{ method, path string }{
		{http.MethodPost, "/api/v1/requests/" + id + "/examples"},
		{http.MethodGet, "/api/v1/requests/" + id + "/examples"},
		{http.MethodPut, "/api/v1/examples/order"},
		{http.MethodGet, "/api/v1/examples/" + id},
		{http.MethodPatch, "/api/v1/examples/" + id},
		{http.MethodDelete, "/api/v1/examples/" + id},
	} {
		if rec := do(t, f.handler, route.method, route.path, "", ""); rec.Code != http.StatusUnauthorized {
			t.Errorf("%s %s: status = %d, want 401", route.method, route.path, rec.Code)
		}
	}
}
