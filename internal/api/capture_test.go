package api

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/komiljonov/ghostman/internal/authz"
	"github.com/komiljonov/ghostman/internal/db"
	"github.com/komiljonov/ghostman/internal/ratelimit"
	"github.com/komiljonov/ghostman/internal/testdb"
)

// testHooksDomains are the capture domains used throughout; the first is the
// default.
var testHooksDomains = []string{"ear.hooks.test", "hook.hooks.test"}

// apiHost stands in for the API's own host name.
const apiHost = "api.ghostman.test"

// newWebhookAPI builds the router with the given capture domains and capture
// limiter (nil = off) over a fresh test database.
func newWebhookAPI(t *testing.T, domains []string, captureLimiter *ratelimit.Limiter) teamsFixture {
	t.Helper()

	pool := testdb.New(t, "api")
	store := db.NewStore(pool)
	a := &api{
		logger:         slog.New(slog.NewTextHandler(io.Discard, nil)),
		pool:           pool,
		store:          store,
		authz:          authz.New(store),
		hooksDomains:   domains,
		captureLimiter: captureLimiter,
	}

	return teamsFixture{handler: a.routes(), store: store, pool: pool}
}

// newWebhookProject sets up capture domains, an owner, a team and a project.
func newWebhookProject(t *testing.T, emailPrefix string, captureLimiter *ratelimit.Limiter) (teamsFixture, authResponse, projectResponse) {
	t.Helper()

	f := newWebhookAPI(t, testHooksDomains, captureLimiter)
	owner := f.newUser(t, emailPrefix+"@example.com")
	team := f.createTeam(t, owner.Token, "Ghostman")
	project := f.createProject(t, owner.Token, team.ID, "API")

	return f, owner, project
}

func (f teamsFixture) createWebhook(t *testing.T, token, projectID string, fields map[string]any) webhookResponse {
	t.Helper()

	rec := do(t, f.handler, http.MethodPost, "/api/v1/projects/"+projectID+"/webhooks", folderBody(t, fields), token)
	if rec.Code != http.StatusCreated {
		t.Fatalf("creating webhook %v: status = %d, want %d (body: %s)", fields, rec.Code, http.StatusCreated, rec.Body)
	}

	var endpoint webhookResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &endpoint); err != nil {
		t.Fatalf("decoding webhook response: %v", err)
	}
	return endpoint
}

// sendTo sends a request to host+target the way a webhook sender would: no
// auth, any method. mutate may adjust the request (RemoteAddr, headers).
func (f teamsFixture) sendTo(t *testing.T, method, host, target string, body string, mutate func(*http.Request)) *httptest.ResponseRecorder {
	t.Helper()

	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}

	req := httptest.NewRequestWithContext(t.Context(), method, target, reader)
	req.Host = host
	if mutate != nil {
		mutate(req)
	}

	rec := httptest.NewRecorder()
	f.handler.ServeHTTP(rec, req)
	return rec
}

// storedCapture is a capture row read straight from the database.
type storedCapture struct {
	ID            int64
	Method        string
	Path          string
	Query         string
	BodySize      int32
	BodyTruncated bool
	StoredBytes   int
	SourceIP      string
}

func (f teamsFixture) storedCaptures(t *testing.T, endpointID string) []storedCapture {
	t.Helper()

	rows, err := f.pool.Query(t.Context(), `
		SELECT id, method, path, query, body_size, body_truncated, coalesce(octet_length(body), 0), source_ip
		FROM webhook_captures WHERE endpoint_id = $1 ORDER BY id`, endpointID)
	if err != nil {
		t.Fatalf("reading captures: %v", err)
	}
	defer rows.Close()

	var captures []storedCapture
	for rows.Next() {
		var c storedCapture
		if err := rows.Scan(&c.ID, &c.Method, &c.Path, &c.Query, &c.BodySize, &c.BodyTruncated, &c.StoredBytes, &c.SourceIP); err != nil {
			t.Fatalf("scanning capture: %v", err)
		}
		captures = append(captures, c)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("reading captures: %v", err)
	}
	return captures
}

// assertPlain404 checks a plain-text 404, i.e. not the API's JSON envelope.
func assertPlain404(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (body: %s)", rec.Code, rec.Body)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		t.Errorf("Content-Type = %q, want text/plain (a capture-host 404, not the API's)", ct)
	}
}

// assertAPI404 checks the normal router's JSON 404 envelope.
func assertAPI404(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (body: %s)", rec.Code, rec.Body)
	}
	if env := decodeEnvelope(t, rec); env.Error.Code != codeNotFound {
		t.Errorf("envelope code = %q, want %q", env.Error.Code, codeNotFound)
	}
}

func TestCaptureRouting(t *testing.T) {
	f, owner, project := newWebhookProject(t, "capture-routing", nil)
	endpoint := f.createWebhook(t, owner.Token, project.ID, map[string]any{"name": "Payme"})
	slug := endpoint.Slug

	captured := []struct {
		name     string
		host     string
		target   string
		wantPath string
	}{
		{"subdomain on the first domain", slug + ".ear.hooks.test", "/payme/callback?order=1", "/payme/callback"},
		{"subdomain on the second domain", slug + ".hook.hooks.test", "/", "/"},
		{"/h/ form on the first domain", "ear.hooks.test", "/h/" + slug + "/a/b", "/a/b"},
		{"/h/ form on the second domain", "hook.hooks.test", "/h/" + slug + "/payme/callback", "/payme/callback"},
		{"/h/ form without a rest", "hook.hooks.test", "/h/" + slug, "/"},
		{"host with a port and odd case", strings.ToUpper(slug) + ".EAR.Hooks.Test:443", "/x", "/x"},
		{"API path on a capture subdomain is just a path", slug + ".ear.hooks.test", "/api/v1/hello", "/api/v1/hello"},
	}

	for _, tt := range captured {
		t.Run(tt.name, func(t *testing.T) {
			rec := f.sendTo(t, http.MethodPost, tt.host, tt.target, `{"n":1}`, nil)
			if rec.Code != http.StatusOK || rec.Body.String() != "ok" {
				t.Fatalf("status = %d body = %q, want 200 ok", rec.Code, rec.Body)
			}
			if rec.Header().Get(captureIDHeader) == "" {
				t.Errorf("missing %s", captureIDHeader)
			}
		})
	}

	// One slug, both domains: every capture above landed on the same endpoint.
	stored := f.storedCaptures(t, endpoint.ID)
	if len(stored) != len(captured) {
		t.Fatalf("stored %d captures, want %d", len(stored), len(captured))
	}
	for i, tt := range captured {
		if stored[i].Path != tt.wantPath {
			t.Errorf("%s: recorded path = %q, want %q", tt.name, stored[i].Path, tt.wantPath)
		}
	}
	if stored[0].Query != "order=1" {
		t.Errorf("recorded query = %q, want order=1", stored[0].Query)
	}

	notCaptured := []struct {
		name   string
		host   string
		target string
	}{
		{"capture domain, not /h/", "ear.hooks.test", "/"},
		{"capture domain, API path", "ear.hooks.test", "/api/v1/hello"},
		{"capture domain, unknown slug", "ear.hooks.test", "/h/zzzzzzzzzz"},
		{"capture domain, malformed slug", "ear.hooks.test", "/h/NOT_A_SLUG"},
		{"unknown slug subdomain", "zzzzzzzzzz.hook.hooks.test", "/"},
		{"deeper subdomain", "x." + slug + ".ear.hooks.test", "/"},
	}
	for _, tt := range notCaptured {
		t.Run(tt.name, func(t *testing.T) {
			assertPlain404(t, f.sendTo(t, http.MethodPost, tt.host, tt.target, "x", nil))
		})
	}

	t.Run("/h/ on the API host is the normal router's 404", func(t *testing.T) {
		assertAPI404(t, f.sendTo(t, http.MethodPost, apiHost, "/h/"+slug+"/x", "x", nil))
		assertAPI404(t, f.sendTo(t, http.MethodGet, apiHost, "/h/"+slug, "", nil))
	})

	t.Run("the API is unaffected on its own host", func(t *testing.T) {
		if rec := f.sendTo(t, http.MethodGet, apiHost, "/api/v1/hello", "", nil); rec.Code != http.StatusOK {
			t.Errorf("GET /api/v1/hello: status = %d", rec.Code)
		}
		// And a capture domain that merely contains the API host is not it.
		if rec := f.sendTo(t, http.MethodGet, "notear.hooks.test", "/api/v1/hello", "", nil); rec.Code != http.StatusOK {
			t.Errorf("look-alike host should reach the API: status = %d", rec.Code)
		}
	})

	if got := len(f.storedCaptures(t, endpoint.ID)); got != len(captured) {
		t.Errorf("captures after the 404 cases = %d, want still %d", got, len(captured))
	}
}

func TestCaptureOffWithoutDomains(t *testing.T) {
	f := newWebhookAPI(t, nil, nil)
	owner := f.newUser(t, "capture-off@example.com")
	team := f.createTeam(t, owner.Token, "Ghostman")
	project := f.createProject(t, owner.Token, team.ID, "API")

	rec := do(t, f.handler, http.MethodPost, "/api/v1/projects/"+project.ID+"/webhooks", `{"name":"x"}`, owner.Token)
	if rec.Code != http.StatusConflict {
		t.Fatalf("create: status = %d, want %d (body: %s)", rec.Code, http.StatusConflict, rec.Body)
	}
	if msg := decodeEnvelope(t, rec).Error.Message; msg != messageHooksNotConfigured {
		t.Errorf("message = %q, want %q", msg, messageHooksNotConfigured)
	}

	// An endpoint that exists (say, from before the domains were removed) is
	// still not captured: no capture routing is mounted at all.
	const slug = "abcdefghij"
	if _, err := f.pool.Exec(t.Context(), `
		INSERT INTO webhook_endpoints (project_id, name, slug, created_by)
		VALUES ($1, 'old', $2, $3)`, project.ID, slug, owner.User.ID); err != nil {
		t.Fatalf("inserting endpoint: %v", err)
	}

	assertAPI404(t, f.sendTo(t, http.MethodPost, slug+".ear.hooks.test", "/", "x", nil))
	assertAPI404(t, f.sendTo(t, http.MethodPost, "ear.hooks.test", "/h/"+slug, "x", nil))

	if rec := f.sendTo(t, http.MethodGet, "ear.hooks.test", "/api/v1/hello", "", nil); rec.Code != http.StatusOK {
		t.Errorf("API on any host with capture off: status = %d", rec.Code)
	}

	var count int
	if err := f.pool.QueryRow(t.Context(), "SELECT count(*) FROM webhook_captures").Scan(&count); err != nil {
		t.Fatalf("counting captures: %v", err)
	}
	if count != 0 {
		t.Errorf("captures with the feature off = %d, want 0", count)
	}
}

func TestCaptureEveryMethodAndOddPaths(t *testing.T) {
	f, owner, project := newWebhookProject(t, "capture-methods", nil)
	endpoint := f.createWebhook(t, owner.Token, project.ID, map[string]any{"name": "Any"})
	host := endpoint.Slug + ".ear.hooks.test"

	sends := []struct {
		method, target, wantPath, wantQuery string
	}{
		{http.MethodGet, "/", "/", ""},
		{http.MethodPost, "/orders", "/orders", ""},
		{http.MethodPut, "/orders/42", "/orders/42", ""},
		{http.MethodPatch, "/orders/42?fields=a,b", "/orders/42", "fields=a,b"},
		{http.MethodDelete, "/orders/42/", "/orders/42/", ""},
		{http.MethodHead, "/ping", "/ping", ""},
		{http.MethodOptions, "/cors", "/cors", ""},
		{"PURGE", "/cache", "/cache", ""},
		{http.MethodPost, "/a%20b/c%2Fd", "/a%20b/c%2Fd", ""},
		{http.MethodPost, "/x//y/../z", "/x//y/../z", ""},
		{http.MethodPost, "/q?a=1&a=2&empty=", "/q", "a=1&a=2&empty="},
	}

	for _, s := range sends {
		if rec := f.sendTo(t, s.method, host, s.target, "", nil); rec.Code != http.StatusOK {
			t.Fatalf("%s %s: status = %d, want 200", s.method, s.target, rec.Code)
		}
	}

	stored := f.storedCaptures(t, endpoint.ID)
	if len(stored) != len(sends) {
		t.Fatalf("stored %d captures, want %d", len(stored), len(sends))
	}
	for i, s := range sends {
		if stored[i].Method != s.method || stored[i].Path != s.wantPath || stored[i].Query != s.wantQuery {
			t.Errorf("%s %s recorded as %s %s ?%s, want %s %s ?%s",
				s.method, s.target, stored[i].Method, stored[i].Path, stored[i].Query, s.method, s.wantPath, s.wantQuery)
		}
	}
}

func TestCaptureBodySizes(t *testing.T) {
	f, owner, project := newWebhookProject(t, "capture-bodies", nil)
	endpoint := f.createWebhook(t, owner.Token, project.ID, map[string]any{"name": "Big"})
	host := endpoint.Slug + ".hook.hooks.test"

	sizes := []struct {
		name          string
		size          int
		wantSize      int32
		wantStored    int
		wantTruncated bool
	}{
		{"empty", 0, 0, 0, false},
		{"exactly 1 MiB", maxStoredCaptureBody, maxStoredCaptureBody, maxStoredCaptureBody, false},
		{"1 MiB + 1", maxStoredCaptureBody + 1, maxStoredCaptureBody + 1, maxStoredCaptureBody, true},
		{"3 MiB", 3 << 20, 3 << 20, maxStoredCaptureBody, true},
		{"past the 10 MiB hard cut", maxReadCaptureBody + 5000, maxReadCaptureBody, maxStoredCaptureBody, true},
	}

	for _, s := range sizes {
		rec := f.sendTo(t, http.MethodPost, host, "/upload", strings.Repeat("b", s.size), nil)
		// The sender always gets the configured response, never a 413.
		if rec.Code != http.StatusOK || rec.Body.String() != "ok" {
			t.Fatalf("%s: status = %d body = %.40q, want 200 ok", s.name, rec.Code, rec.Body)
		}
	}

	stored := f.storedCaptures(t, endpoint.ID)
	if len(stored) != len(sizes) {
		t.Fatalf("stored %d captures, want %d", len(stored), len(sizes))
	}
	for i, s := range sizes {
		got := stored[i]
		if got.BodySize != s.wantSize || got.StoredBytes != s.wantStored || got.BodyTruncated != s.wantTruncated {
			t.Errorf("%s: body_size %d, stored %d bytes, truncated %t; want %d, %d, %t",
				s.name, got.BodySize, got.StoredBytes, got.BodyTruncated, s.wantSize, s.wantStored, s.wantTruncated)
		}
	}

	var bodyIsNull bool
	if err := f.pool.QueryRow(t.Context(), "SELECT body IS NULL FROM webhook_captures WHERE id = $1", stored[0].ID).Scan(&bodyIsNull); err != nil {
		t.Fatalf("reading empty body: %v", err)
	}
	if !bodyIsNull {
		t.Error("an empty body should be stored as NULL")
	}
}

func TestCaptureConfiguredResponse(t *testing.T) {
	f, owner, project := newWebhookProject(t, "capture-response", nil)
	endpoint := f.createWebhook(t, owner.Token, project.ID, map[string]any{"name": "Custom"})
	host := endpoint.Slug + ".ear.hooks.test"

	f.mustPatchWebhook(t, owner.Token, endpoint.ID, `{
		"resp_status": 201,
		"resp_headers": [
			{"key": "Content-Type", "value": "application/json", "enabled": true},
			{"key": "X-Shown", "value": "yes", "enabled": true},
			{"key": "X-Shown", "value": "twice", "enabled": true},
			{"key": "X-Hidden", "value": "no", "enabled": false},
			{"key": "Content-Length", "value": "999", "enabled": true},
			{"key": "X-Ghostman-Capture-Id", "value": "forged", "enabled": true}
		],
		"resp_body": "{\"received\":true}"
	}`)

	rec := f.sendTo(t, http.MethodPost, host, "/cb", `{"event":"paid"}`, nil)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201", rec.Code)
	}
	if got := rec.Body.String(); got != `{"received":true}` {
		t.Errorf("body = %q", got)
	}
	if got := rec.Header().Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q", got)
	}
	if got := rec.Header().Values("X-Shown"); strings.Join(got, ",") != "yes,twice" {
		t.Errorf("X-Shown = %q, want both enabled values", got)
	}
	if rec.Header().Get("X-Hidden") != "" {
		t.Error("a disabled header row was sent")
	}
	if rec.Header().Get("Content-Length") == "999" {
		t.Error("a configured Content-Length was sent; framing headers must be skipped")
	}

	stored := f.storedCaptures(t, endpoint.ID)
	if len(stored) != 1 {
		t.Fatalf("stored %d captures, want 1", len(stored))
	}
	if got := rec.Header().Values(captureIDHeader); len(got) != 1 || got[0] != strconv.FormatInt(stored[0].ID, 10) {
		t.Errorf("%s = %q, want exactly the capture id %d", captureIDHeader, got, stored[0].ID)
	}

	t.Run("204 sends no body", func(t *testing.T) {
		f.mustPatchWebhook(t, owner.Token, endpoint.ID, `{"resp_status":204}`)
		rec := f.sendTo(t, http.MethodPost, host, "/cb", "x", nil)
		if rec.Code != http.StatusNoContent || rec.Body.Len() != 0 {
			t.Errorf("status = %d, body = %q; want 204 with no body", rec.Code, rec.Body)
		}
	})
}

func TestCapturePrunesToNewest500(t *testing.T) {
	f, owner, project := newWebhookProject(t, "capture-prune", nil)
	endpoint := f.createWebhook(t, owner.Token, project.ID, map[string]any{"name": "Busy"})

	// 520 older captures, inserted directly to keep the test fast.
	if _, err := f.pool.Exec(t.Context(), `
		INSERT INTO webhook_captures (endpoint_id, method, path, headers, body_size, source_ip)
		SELECT $1, 'POST', '/old/' || n, '[]', 0, '192.0.2.1' FROM generate_series(1, 520) AS n`, endpoint.ID); err != nil {
		t.Fatalf("seeding captures: %v", err)
	}

	before := f.storedCaptures(t, endpoint.ID)
	rec := f.sendTo(t, http.MethodPost, endpoint.Slug+".ear.hooks.test", "/new", "x", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("capture: status = %d", rec.Code)
	}

	after := f.storedCaptures(t, endpoint.ID)
	if len(after) != maxCapturesPerEndpoint {
		t.Fatalf("captures after prune = %d, want %d", len(after), maxCapturesPerEndpoint)
	}
	if newest := after[len(after)-1]; newest.Path != "/new" {
		t.Errorf("newest capture = %q, want the one just sent", newest.Path)
	}
	// The newest 499 of the old ones survive, the oldest 21 go.
	if after[0].ID != before[len(before)-(maxCapturesPerEndpoint-1)].ID {
		t.Errorf("oldest kept id = %d, want %d", after[0].ID, before[len(before)-(maxCapturesPerEndpoint-1)].ID)
	}

	// Another endpoint's captures are untouched by this one's prune.
	other := f.createWebhook(t, owner.Token, project.ID, map[string]any{"name": "Quiet"})
	f.sendTo(t, http.MethodPost, other.Slug+".ear.hooks.test", "/", "x", nil)
	f.sendTo(t, http.MethodPost, endpoint.Slug+".ear.hooks.test", "/again", "x", nil)
	if got := len(f.storedCaptures(t, other.ID)); got != 1 {
		t.Errorf("other endpoint captures = %d, want 1", got)
	}
}

func TestCaptureSourceIP(t *testing.T) {
	f, owner, project := newWebhookProject(t, "capture-ip", nil)
	endpoint := f.createWebhook(t, owner.Token, project.ID, map[string]any{"name": "IP"})
	host := endpoint.Slug + ".ear.hooks.test"

	sends := []struct {
		name       string
		remoteAddr string
		forwarded  string
		want       string
	}{
		{"direct client spoofing X-Forwarded-For", "203.0.113.9:5555", "198.51.100.7", "203.0.113.9"},
		{"trusted proxy: the entry it appended", "127.0.0.1:5555", "6.6.6.6, 198.51.100.7", "198.51.100.7"},
		{"trusted IPv6 proxy", "[::1]:5555", "198.51.100.8", "198.51.100.8"},
		{"trusted proxy, garbage header", "127.0.0.1:5555", "not-an-ip", "127.0.0.1"},
		{"no proxy, no header", "203.0.113.10:5555", "", "203.0.113.10"},
	}

	for _, s := range sends {
		f.sendTo(t, http.MethodPost, host, "/", "x", func(r *http.Request) {
			r.RemoteAddr = s.remoteAddr
			if s.forwarded != "" {
				r.Header.Set("X-Forwarded-For", s.forwarded)
			}
		})
	}

	stored := f.storedCaptures(t, endpoint.ID)
	if len(stored) != len(sends) {
		t.Fatalf("stored %d captures, want %d", len(stored), len(sends))
	}
	for i, s := range sends {
		if stored[i].SourceIP != s.want {
			t.Errorf("%s: source_ip = %q, want %q", s.name, stored[i].SourceIP, s.want)
		}
	}
}

// TestCaptureRateLimit checks what a refusal does: 429, nothing stored, and
// per-slug buckets. The bucket barely refills (one token per 100 s), so the
// test does not depend on how fast captures run; the real 10/s, burst 30
// numbers are exercised with a fake clock in internal/ratelimit.
func TestCaptureRateLimit(t *testing.T) {
	const burst = 30
	f, owner, project := newWebhookProject(t, "capture-limit", ratelimit.New(0.01, burst))
	busy := f.createWebhook(t, owner.Token, project.ID, map[string]any{"name": "Busy"})
	quiet := f.createWebhook(t, owner.Token, project.ID, map[string]any{"name": "Quiet"})

	accepted, limited := 0, 0
	for range 40 {
		rec := f.sendTo(t, http.MethodPost, busy.Slug+".ear.hooks.test", "/", "x", nil)
		switch rec.Code {
		case http.StatusOK:
			accepted++
		case http.StatusTooManyRequests:
			limited++
			if !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/plain") {
				t.Errorf("429 Content-Type = %q, want plain text", rec.Header().Get("Content-Type"))
			}
			if rec.Header().Get(captureIDHeader) != "" {
				t.Error("a limited request was given a capture id")
			}
		default:
			t.Fatalf("unexpected status %d", rec.Code)
		}
	}

	if accepted != burst || limited != 40-burst {
		t.Fatalf("accepted %d, limited %d: want exactly the burst of %d through, then 429s", accepted, limited, burst)
	}
	// Refused requests are not stored.
	if got := len(f.storedCaptures(t, busy.ID)); got != accepted {
		t.Errorf("stored %d captures, want exactly the %d accepted", got, accepted)
	}

	// Buckets are per slug: the other endpoint is unaffected.
	if rec := f.sendTo(t, http.MethodPost, quiet.Slug+".ear.hooks.test", "/", "x", nil); rec.Code != http.StatusOK {
		t.Errorf("other endpoint: status = %d, want 200", rec.Code)
	}
}

func (f teamsFixture) mustPatchWebhook(t *testing.T, token, endpointID, body string) webhookResponse {
	t.Helper()

	rec := do(t, f.handler, http.MethodPatch, "/api/v1/webhooks/"+endpointID, body, token)
	if rec.Code != http.StatusOK {
		t.Fatalf("PATCH webhook %.120s: status = %d (body: %s)", body, rec.Code, rec.Body)
	}

	var endpoint webhookResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &endpoint); err != nil {
		t.Fatalf("decoding webhook response: %v", err)
	}
	return endpoint
}
