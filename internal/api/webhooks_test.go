package api

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/komiljonov/ghostman/internal/authz"
)

func TestWebhookCreateAndRead(t *testing.T) {
	f, owner, project := newWebhookProject(t, "webhook-create", nil)

	endpoint := f.createWebhook(t, owner.Token, project.ID, map[string]any{"name": "Payme callbacks"})
	if !isValidSlug(endpoint.Slug) {
		t.Errorf("slug %q is not 10 characters of [a-z0-9]", endpoint.Slug)
	}
	if endpoint.Domain != "" || endpoint.RespStatus != 200 || endpoint.RespBody != "ok" || string(endpoint.RespHeaders) != "[]" {
		t.Errorf("defaults = %+v, want domain \"\", 200, ok, []", endpoint)
	}
	if endpoint.ProjectID != project.ID || endpoint.CreatedBy != owner.User.ID {
		t.Errorf("project/creator = %s/%s", endpoint.ProjectID, endpoint.CreatedBy)
	}

	second := f.createWebhook(t, owner.Token, project.ID, map[string]any{"name": "Stripe", "domain": " HOOK.hooks.test "})
	if second.Domain != "hook.hooks.test" {
		t.Errorf("domain = %q, want it normalised to hook.hooks.test", second.Domain)
	}
	if second.Slug == endpoint.Slug {
		t.Error("two endpoints got the same slug")
	}

	t.Run("GET returns the full object", func(t *testing.T) {
		rec := do(t, f.handler, http.MethodGet, "/api/v1/webhooks/"+endpoint.ID, "", owner.Token)
		var got webhookResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || rec.Code != http.StatusOK {
			t.Fatalf("status = %d, err = %v", rec.Code, err)
		}
		if got.Slug != endpoint.Slug || got.RespBody != "ok" {
			t.Errorf("GET = %+v", got)
		}
	})

	t.Run("the list is summary-only, oldest first", func(t *testing.T) {
		rec := do(t, f.handler, http.MethodGet, "/api/v1/projects/"+project.ID+"/webhooks", "", owner.Token)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d", rec.Code)
		}
		if body := rec.Body.String(); strings.Contains(body, "resp_headers") || strings.Contains(body, "resp_body") {
			t.Errorf("list carries the response config: %s", body)
		}
		var list []webhookSummaryResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
			t.Fatalf("decoding list: %v", err)
		}
		if len(list) != 2 || list[0].ID != endpoint.ID || list[1].Slug != second.Slug || list[1].Domain != "hook.hooks.test" {
			t.Errorf("list = %+v", list)
		}
	})
}

func TestWebhookDomainValidation(t *testing.T) {
	f, owner, project := newWebhookProject(t, "webhook-domain", nil)
	endpoint := f.createWebhook(t, owner.Token, project.ID, map[string]any{"name": "E"})

	wantMessage := "domain must be one of ear.hooks.test, hook.hooks.test, or empty for the default (ear.hooks.test)"
	for _, domain := range []string{"evil.test", "hooks.test", "ear.hooks.test.evil", "https://ear.hooks.test"} {
		rec := do(t, f.handler, http.MethodPost, "/api/v1/projects/"+project.ID+"/webhooks",
			folderBody(t, map[string]any{"name": "x", "domain": domain}), owner.Token)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("create with %q: status = %d, want 400", domain, rec.Code)
			continue
		}
		if msg := decodeEnvelope(t, rec).Error.Message; msg != wantMessage {
			t.Errorf("create with %q: message = %q", domain, msg)
		}

		rec = do(t, f.handler, http.MethodPatch, "/api/v1/webhooks/"+endpoint.ID, folderBody(t, map[string]any{"domain": domain}), owner.Token)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("PATCH with %q: status = %d, want 400", domain, rec.Code)
		}
	}

	if got := f.mustPatchWebhook(t, owner.Token, endpoint.ID, `{"domain":"hook.hooks.test"}`); got.Domain != "hook.hooks.test" {
		t.Errorf("PATCH valid domain: %q", got.Domain)
	}
	if got := f.mustPatchWebhook(t, owner.Token, endpoint.ID, `{"domain":""}`); got.Domain != "" {
		t.Errorf("PATCH empty domain should mean the default, got %q", got.Domain)
	}
}

func TestWebhookDroppedDomainTolerated(t *testing.T) {
	f, owner, project := newWebhookProject(t, "webhook-dropped", nil)
	endpoint := f.createWebhook(t, owner.Token, project.ID, map[string]any{"name": "Legacy", "domain": "hook.hooks.test"})

	// The domain is later removed from HOOKS_DOMAINS: simulated by a stored
	// value that is no longer configured.
	if _, err := f.pool.Exec(t.Context(), "UPDATE webhook_endpoints SET domain = 'gone.hooks.test' WHERE id = $1", endpoint.ID); err != nil {
		t.Fatalf("simulating a dropped domain: %v", err)
	}

	rec := do(t, f.handler, http.MethodGet, "/api/v1/webhooks/"+endpoint.ID, "", owner.Token)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"domain":"gone.hooks.test"`) {
		t.Errorf("GET: status = %d, body = %s; want the stored domain unchanged", rec.Code, rec.Body)
	}
	rec = do(t, f.handler, http.MethodGet, "/api/v1/projects/"+project.ID+"/webhooks", "", owner.Token)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"domain":"gone.hooks.test"`) {
		t.Errorf("list: status = %d, body = %s", rec.Code, rec.Body)
	}

	// A PATCH that does not touch the domain still works.
	f.mustPatchWebhook(t, owner.Token, endpoint.ID, `{"name":"Renamed"}`)

	// And capture never depended on it.
	if rec := f.sendTo(t, http.MethodPost, endpoint.Slug+".ear.hooks.test", "/", "x", nil); rec.Code != http.StatusOK {
		t.Errorf("capture: status = %d", rec.Code)
	}
}

func TestWebhookQuota(t *testing.T) {
	f, owner, project := newWebhookProject(t, "webhook-quota", nil)

	for i := range maxWebhooksPerProject {
		f.createWebhook(t, owner.Token, project.ID, map[string]any{"name": fmt.Sprintf("E%d", i)})
	}

	rec := do(t, f.handler, http.MethodPost, "/api/v1/projects/"+project.ID+"/webhooks", `{"name":"one too many"}`, owner.Token)
	if rec.Code != http.StatusConflict {
		t.Fatalf("21st endpoint: status = %d, want 409 (body: %s)", rec.Code, rec.Body)
	}
	if msg := decodeEnvelope(t, rec).Error.Message; msg != "a project can have at most 20 webhook endpoints" {
		t.Errorf("message = %q", msg)
	}

	// The quota is per project.
	team := f.createTeam(t, owner.Token, "Other team")
	other := f.createProject(t, owner.Token, team.ID, "Other")
	f.createWebhook(t, owner.Token, other.ID, map[string]any{"name": "fine"})
}

func TestWebhookPatch(t *testing.T) {
	f, owner, project := newWebhookProject(t, "webhook-patch", nil)
	endpoint := f.createWebhook(t, owner.Token, project.ID, map[string]any{"name": "E"})

	got := f.mustPatchWebhook(t, owner.Token, endpoint.ID, `{"resp_status":202}`)
	if got.RespStatus != 202 || got.Name != "E" || got.RespBody != "ok" {
		t.Errorf("partial PATCH = %+v, want only resp_status changed", got)
	}

	headers := `[{"key":"X-A","value":"{{not-resolved}}","enabled":true},{"key":"X-A","value":"2","enabled":false}]`
	got = f.mustPatchWebhook(t, owner.Token, endpoint.ID, `{"resp_headers":`+headers+`,"resp_body":""}`)
	assertSameJSON(t, "resp_headers", got.RespHeaders, headers)
	if got.RespBody != "" || got.RespStatus != 202 {
		t.Errorf("after headers/body PATCH = %+v", got)
	}

	got = f.mustPatchWebhook(t, owner.Token, endpoint.ID, `{"resp_headers":null,"name":"Renamed"}`)
	assertSameJSON(t, "resp_headers after null", got.RespHeaders, headers)

	for _, status := range []int{100, 599} {
		f.mustPatchWebhook(t, owner.Token, endpoint.ID, fmt.Sprintf(`{"resp_status":%d}`, status))
	}
	f.mustPatchWebhook(t, owner.Token, endpoint.ID, `{"resp_body":`+jsonString(t, strings.Repeat("é", maxRespBodyLength))+`}`)

	refused := []struct {
		name, body, message string
	}{
		{"status 99", `{"resp_status":99}`, "resp_status must be between 100 and 599"},
		{"status 600", `{"resp_status":600}`, "resp_status must be between 100 and 599"},
		{"body too long", `{"resp_body":` + jsonString(t, strings.Repeat("x", maxRespBodyLength+1)) + `}`, "resp_body must be at most 65536 characters"},
		{"header row without a key", `{"resp_headers":[{"key":" ","value":"","enabled":true}]}`, "resp_headers[0].key is required"},
		{"header row unknown field", `{"resp_headers":[{"key":"a","value":"","enabled":true,"x":1}]}`, "resp_headers[0].x is not a known field"},
		{"empty name", `{"name":"  "}`, "name is required"},
		{"nothing to change", `{}`, "provide at least one of"},
		{"slug is not writable", `{"slug":"aaaaaaaaaa"}`, `body contains unknown field "slug"`},
	}
	for _, tt := range refused {
		rec := do(t, f.handler, http.MethodPatch, "/api/v1/webhooks/"+endpoint.ID, tt.body, owner.Token)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400", tt.name, rec.Code)
			continue
		}
		if msg := decodeEnvelope(t, rec).Error.Message; !strings.HasPrefix(msg, tt.message) {
			t.Errorf("%s: message = %q, want prefix %q", tt.name, msg, tt.message)
		}
	}
}

// newWebhookAccessFixture is the project access matrix with capture domains
// switched on, so the limited member can create endpoints in the project
// they hold a grant for.
func newWebhookAccessFixture(t *testing.T) accessMatrixFixture {
	t.Helper()

	f := newAccessMatrixFixture(t)
	a := &api{
		logger:       slog.New(slog.NewTextHandler(io.Discard, nil)),
		pool:         f.pool,
		store:        f.store,
		authz:        authz.New(f.store),
		hooksDomains: testHooksDomains,
	}
	f.handler = a.routes()
	return f
}

func TestWebhookAccessMatrix(t *testing.T) {
	f := newWebhookAccessFixture(t)

	hidden := f.createWebhook(t, f.owner.Token, f.ownerProject.ID, map[string]any{"name": "hidden"})
	f.sendTo(t, http.MethodPost, hidden.Slug+".ear.hooks.test", "/", "secret payload", nil)
	captureID := f.storedCaptures(t, hidden.ID)[0].ID

	type request struct {
		name, method, path, body, message string
	}
	denied := func(projectID, endpointID string, captureID int64) []request {
		return []request{
			{"create", http.MethodPost, "/api/v1/projects/" + projectID + "/webhooks", `{"name":"x"}`, messageProjectNotFound},
			{"list", http.MethodGet, "/api/v1/projects/" + projectID + "/webhooks", "", messageProjectNotFound},
			{"get", http.MethodGet, "/api/v1/webhooks/" + endpointID, "", messageWebhookNotFound},
			{"patch", http.MethodPatch, "/api/v1/webhooks/" + endpointID, `{"resp_body":"pwned"}`, messageWebhookNotFound},
			{"delete", http.MethodDelete, "/api/v1/webhooks/" + endpointID, "", messageWebhookNotFound},
			{"list captures", http.MethodGet, "/api/v1/webhooks/" + endpointID + "/captures", "", messageWebhookNotFound},
			{"get capture", http.MethodGet, "/api/v1/webhooks/" + endpointID + "/captures/" + strconv.FormatInt(captureID, 10), "", messageWebhookNotFound},
			{"clear captures", http.MethodDelete, "/api/v1/webhooks/" + endpointID + "/captures", "", messageWebhookNotFound},
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

	t.Run("restricted member without a grant", func(t *testing.T) {
		check(t, f.limited.Token, denied(f.ownerProject.ID, hidden.ID, captureID))
	})
	t.Run("outsider", func(t *testing.T) {
		check(t, f.outsider.Token, denied(f.ownerProject.ID, hidden.ID, captureID))
	})
	t.Run("nonexistent ids look the same", func(t *testing.T) {
		check(t, f.owner.Token, denied(uuid.New().String(), uuid.New().String(), 1))
	})

	// Nothing was changed by the denied calls.
	if got := len(f.storedCaptures(t, hidden.ID)); got != 1 {
		t.Errorf("hidden captures = %d, want 1", got)
	}
	rec := do(t, f.handler, http.MethodGet, "/api/v1/webhooks/"+hidden.ID, "", f.owner.Token)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"resp_body":"ok"`) {
		t.Errorf("hidden endpoint changed: %s", rec.Body)
	}

	t.Run("any member with project access has full rights", func(t *testing.T) {
		token, projectID := f.limited.Token, f.grantedProject.ID
		mine := f.createWebhook(t, token, projectID, map[string]any{"name": "mine"})
		f.mustPatchWebhook(t, token, mine.ID, `{"resp_status":201}`)
		for _, req := range []request{
			{"list", http.MethodGet, "/api/v1/projects/" + projectID + "/webhooks", "", ""},
			{"get", http.MethodGet, "/api/v1/webhooks/" + mine.ID, "", ""},
			{"list captures", http.MethodGet, "/api/v1/webhooks/" + mine.ID + "/captures", "", ""},
		} {
			if rec := do(t, f.handler, req.method, req.path, "", token); rec.Code != http.StatusOK {
				t.Errorf("%s: status = %d", req.name, rec.Code)
			}
		}
		if rec := do(t, f.handler, http.MethodDelete, "/api/v1/webhooks/"+mine.ID+"/captures", "", token); rec.Code != http.StatusNoContent {
			t.Errorf("clear captures: status = %d", rec.Code)
		}
		if rec := do(t, f.handler, http.MethodDelete, "/api/v1/webhooks/"+mine.ID, "", token); rec.Code != http.StatusNoContent {
			t.Errorf("delete: status = %d", rec.Code)
		}
	})
}

func TestCapturePollingAndFetch(t *testing.T) {
	f, owner, project := newWebhookProject(t, "webhook-polling", nil)
	endpoint := f.createWebhook(t, owner.Token, project.ID, map[string]any{"name": "Poll"})
	other := f.createWebhook(t, owner.Token, project.ID, map[string]any{"name": "Other"})
	host := endpoint.Slug + ".ear.hooks.test"

	payload := "{\"event\":\"paid\",\"bin\":\"\x00\x01\xff\"}"
	for i, target := range []string{"/one", "/two?x=1", "/three"} {
		f.sendTo(t, http.MethodPost, host, target, payload, func(r *http.Request) {
			r.Header.Set("Content-Type", "application/json")
			r.Header.Add("X-Repeat", "a")
			r.Header.Add("X-Repeat", strconv.Itoa(i))
		})
	}
	f.sendTo(t, http.MethodPost, other.Slug+".ear.hooks.test", "/elsewhere", "x", nil)

	poll := func(t *testing.T, query string) []captureSummaryResponse {
		t.Helper()
		rec := do(t, f.handler, http.MethodGet, "/api/v1/webhooks/"+endpoint.ID+"/captures"+query, "", owner.Token)
		if rec.Code != http.StatusOK {
			t.Fatalf("poll %s: status = %d (body: %s)", query, rec.Code, rec.Body)
		}
		if body := rec.Body.String(); strings.Contains(body, `"headers"`) || strings.Contains(body, `"body"`) {
			t.Errorf("poll %s carries headers or body: %s", query, body)
		}
		var captures []captureSummaryResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &captures); err != nil {
			t.Fatalf("decoding poll: %v", err)
		}
		return captures
	}

	all := poll(t, "?after=0")
	if len(all) != 3 {
		t.Fatalf("poll after=0 returned %d captures, want 3 (this endpoint's only)", len(all))
	}
	for i, path := range []string{"/one", "/two", "/three"} {
		if all[i].Path != path {
			t.Errorf("capture %d path = %q, want %q (ascending order)", i, all[i].Path, path)
		}
		if i > 0 && all[i].ID <= all[i-1].ID {
			t.Errorf("ids not ascending: %d after %d", all[i].ID, all[i-1].ID)
		}
	}
	if all[1].Query != "x=1" || all[0].ContentType != "application/json" || all[0].BodySize != int32(len(payload)) || all[0].SourceIP == "" {
		t.Errorf("summary fields = %+v", all[1])
	}

	if got := poll(t, ""); len(got) != 3 {
		t.Errorf("poll without after: %d captures, want 3", len(got))
	}
	if got := poll(t, "?after="+strconv.FormatInt(all[0].ID, 10)); len(got) != 2 || got[0].ID != all[1].ID {
		t.Errorf("poll after the first = %+v, want the last two", got)
	}
	if got := poll(t, "?after=0&limit=2"); len(got) != 2 || got[1].ID != all[1].ID {
		t.Errorf("poll limit=2 = %+v, want the first two", got)
	}
	if got := poll(t, "?after="+strconv.FormatInt(all[2].ID, 10)); len(got) != 0 {
		t.Errorf("poll after the last = %+v, want []", got)
	}

	for _, query := range []string{"?after=-1", "?after=abc", "?limit=0", "?limit=101", "?limit=x"} {
		if rec := do(t, f.handler, http.MethodGet, "/api/v1/webhooks/"+endpoint.ID+"/captures"+query, "", owner.Token); rec.Code != http.StatusBadRequest {
			t.Errorf("poll %s: status = %d, want 400", query, rec.Code)
		}
	}

	t.Run("full capture: headers and base64 body", func(t *testing.T) {
		rec := do(t, f.handler, http.MethodGet, "/api/v1/webhooks/"+endpoint.ID+"/captures/"+strconv.FormatInt(all[1].ID, 10), "", owner.Token)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d (body: %s)", rec.Code, rec.Body)
		}
		var raw struct {
			Path    string `json:"path"`
			Body    string `json:"body"`
			Headers []struct {
				Key   string `json:"key"`
				Value string `json:"value"`
			} `json:"headers"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
			t.Fatalf("decoding capture: %v", err)
		}
		decoded, err := base64.StdEncoding.DecodeString(raw.Body)
		if err != nil || string(decoded) != payload {
			t.Errorf("body = %q (err %v), want the sent bytes exactly", decoded, err)
		}
		var repeats []string
		var hostHeader string
		for _, h := range raw.Headers {
			switch h.Key {
			case "X-Repeat":
				repeats = append(repeats, h.Value)
			case "Host":
				hostHeader = h.Value
			}
		}
		if strings.Join(repeats, ",") != "a,1" || hostHeader != endpoint.Slug+".ear.hooks.test" {
			t.Errorf("headers: X-Repeat %q, Host %q", repeats, hostHeader)
		}
	})

	t.Run("a capture is only reachable through its own endpoint", func(t *testing.T) {
		otherCapture := f.storedCaptures(t, other.ID)[0].ID
		rec := do(t, f.handler, http.MethodGet, "/api/v1/webhooks/"+endpoint.ID+"/captures/"+strconv.FormatInt(otherCapture, 10), "", owner.Token)
		if rec.Code != http.StatusNotFound || decodeEnvelope(t, rec).Error.Message != messageCaptureNotFound {
			t.Errorf("status = %d, want 404 capture not found", rec.Code)
		}
		for _, bad := range []string{"abc", "0", "-5"} {
			if rec := do(t, f.handler, http.MethodGet, "/api/v1/webhooks/"+endpoint.ID+"/captures/"+bad, "", owner.Token); rec.Code != http.StatusBadRequest {
				t.Errorf("capture id %q: status = %d, want 400", bad, rec.Code)
			}
		}
	})

	t.Run("clear all, then delete the endpoint", func(t *testing.T) {
		if rec := do(t, f.handler, http.MethodDelete, "/api/v1/webhooks/"+endpoint.ID+"/captures", "", owner.Token); rec.Code != http.StatusNoContent {
			t.Fatalf("clear: status = %d", rec.Code)
		}
		if got := poll(t, "?after=0"); len(got) != 0 {
			t.Errorf("after clear: %d captures", len(got))
		}
		if got := len(f.storedCaptures(t, other.ID)); got != 1 {
			t.Errorf("clearing one endpoint touched another: %d", got)
		}

		f.sendTo(t, http.MethodPost, host, "/before-delete", "x", nil)
		if rec := do(t, f.handler, http.MethodDelete, "/api/v1/webhooks/"+endpoint.ID, "", owner.Token); rec.Code != http.StatusNoContent {
			t.Fatalf("delete: status = %d", rec.Code)
		}
		assertPlain404(t, f.sendTo(t, http.MethodPost, host, "/after-delete", "x", nil))
		if got := len(f.storedCaptures(t, endpoint.ID)); got != 0 {
			t.Errorf("captures survived the endpoint: %d", got)
		}
	})
}

func TestMeta(t *testing.T) {
	t.Run("ordered domains, public", func(t *testing.T) {
		f := newWebhookAPI(t, testHooksDomains, nil)
		rec := do(t, f.handler, http.MethodGet, "/api/v1/meta", "", "")
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d", rec.Code)
		}
		if got := strings.TrimSpace(rec.Body.String()); got != `{"hooks_domains":["ear.hooks.test","hook.hooks.test"]}` {
			t.Errorf("meta = %s", got)
		}
	})

	t.Run("feature off is an empty array", func(t *testing.T) {
		f := newWebhookAPI(t, nil, nil)
		rec := do(t, f.handler, http.MethodGet, "/api/v1/meta", "", "")
		if got := strings.TrimSpace(rec.Body.String()); got != `{"hooks_domains":[]}` {
			t.Errorf("meta = %s", got)
		}
	})
}

func TestWebhookRoutesRequireAuth(t *testing.T) {
	f := newWebhookAPI(t, testHooksDomains, nil)
	id := uuid.New().String()

	for _, route := range []struct{ method, path string }{
		{http.MethodPost, "/api/v1/projects/" + id + "/webhooks"},
		{http.MethodGet, "/api/v1/projects/" + id + "/webhooks"},
		{http.MethodGet, "/api/v1/webhooks/" + id},
		{http.MethodPatch, "/api/v1/webhooks/" + id},
		{http.MethodDelete, "/api/v1/webhooks/" + id},
		{http.MethodGet, "/api/v1/webhooks/" + id + "/captures"},
		{http.MethodDelete, "/api/v1/webhooks/" + id + "/captures"},
		{http.MethodGet, "/api/v1/webhooks/" + id + "/captures/1"},
	} {
		if rec := do(t, f.handler, route.method, route.path, "", ""); rec.Code != http.StatusUnauthorized {
			t.Errorf("%s %s: status = %d, want 401", route.method, route.path, rec.Code)
		}
	}
}
