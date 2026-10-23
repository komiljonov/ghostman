package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
)

func (f teamsFixture) createProxy(t *testing.T, token, projectID string, fields map[string]any) proxyResponse {
	t.Helper()

	rec := do(t, f.handler, http.MethodPost, "/api/v1/projects/"+projectID+"/proxies", folderBody(t, fields), token)
	if rec.Code != http.StatusCreated {
		t.Fatalf("creating proxy %v: status = %d, want 201 (body: %s)", fields, rec.Code, rec.Body)
	}

	var proxy proxyResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &proxy); err != nil {
		t.Fatalf("decoding proxy: %v", err)
	}
	return proxy
}

func (f teamsFixture) listProxies(t *testing.T, token, projectID string) []proxyResponse {
	t.Helper()

	rec := do(t, f.handler, http.MethodGet, "/api/v1/projects/"+projectID+"/proxies", "", token)
	if rec.Code != http.StatusOK {
		t.Fatalf("listing proxies: status = %d (body: %s)", rec.Code, rec.Body)
	}

	var proxies []proxyResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &proxies); err != nil {
		t.Fatalf("decoding proxies: %v", err)
	}
	return proxies
}

func proxyNames(proxies []proxyResponse) string {
	names := make([]string, 0, len(proxies))
	for _, p := range proxies {
		names = append(names, p.Name)
	}
	return strings.Join(names, ",")
}

func TestProxyCRUD(t *testing.T) {
	f, owner, _, project := newFolderProject(t, "proxy-crud")

	corp := f.createProxy(t, owner.Token, project.ID, map[string]any{"name": "Corp", "host": " proxy.corp.uz ", "port": 3128})
	if corp.ProxyType != "http" || corp.Username != "" || corp.Bypass != "" || corp.SortOrder != 0 || corp.Host != "proxy.corp.uz" {
		t.Errorf("defaults = %+v, want http, empty username/bypass, sort 0, trimmed host", corp)
	}

	socks := f.createProxy(t, owner.Token, project.ID, map[string]any{
		"name": "Tunnel", "proxy_type": "socks5", "host": "127.0.0.1", "port": 1080,
		"username": "{{proxy_user}}", "bypass": "localhost, *.internal.uz",
	})
	if socks.SortOrder != 1 || socks.Username != "{{proxy_user}}" || socks.Bypass != "localhost, *.internal.uz" {
		t.Errorf("second proxy = %+v", socks)
	}

	if got := proxyNames(f.listProxies(t, owner.Token, project.ID)); got != "Corp,Tunnel" {
		t.Errorf("list = %s, want Corp,Tunnel in order", got)
	}

	rec := do(t, f.handler, http.MethodGet, "/api/v1/proxies/"+socks.ID, "", owner.Token)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"proxy_type":"socks5"`) {
		t.Errorf("GET: status = %d body = %s", rec.Code, rec.Body)
	}
	if strings.Contains(rec.Body.String(), "password") {
		t.Errorf("a proxy response mentions a password: %s", rec.Body)
	}

	rec = do(t, f.handler, http.MethodPatch, "/api/v1/proxies/"+corp.ID, `{"port":8080,"proxy_type":"https"}`, owner.Token)
	var patched proxyResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &patched); err != nil || rec.Code != http.StatusOK {
		t.Fatalf("PATCH: status = %d (body: %s)", rec.Code, rec.Body)
	}
	if patched.Port != 8080 || patched.ProxyType != "https" || patched.Name != "Corp" || patched.Host != "proxy.corp.uz" {
		t.Errorf("partial PATCH = %+v, want only port and type changed", patched)
	}

	if rec := do(t, f.handler, http.MethodDelete, "/api/v1/proxies/"+corp.ID, "", owner.Token); rec.Code != http.StatusNoContent {
		t.Fatalf("DELETE: status = %d", rec.Code)
	}
	if rec := do(t, f.handler, http.MethodGet, "/api/v1/proxies/"+corp.ID, "", owner.Token); rec.Code != http.StatusNotFound {
		t.Errorf("GET after delete: status = %d, want 404", rec.Code)
	}
}

func TestProxyValidation(t *testing.T) {
	f, owner, team, project := newFolderProject(t, "proxy-validation")
	existing := f.createProxy(t, owner.Token, project.ID, map[string]any{"name": "Corp", "host": "h", "port": 80})
	createPath := "/api/v1/projects/" + project.ID + "/proxies"

	refused := []struct {
		name, method, path, body string
		status                   int
		message                  string
	}{
		{"duplicate name, other case", http.MethodPost, createPath, `{"name":"CORP","host":"h","port":80}`, http.StatusConflict, messageProxyNameTaken},
		{"empty name", http.MethodPost, createPath, `{"name":"  ","host":"h","port":80}`, http.StatusBadRequest, "name is required"},
		{"name too long", http.MethodPost, createPath, `{"name":"` + strings.Repeat("n", 101) + `","host":"h","port":80}`, http.StatusBadRequest, "name must be at most 100"},
		{"unknown type", http.MethodPost, createPath, `{"name":"x","proxy_type":"ftp","host":"h","port":80}`, http.StatusBadRequest, "proxy_type must be one of http, https, socks5"},
		{"empty host", http.MethodPost, createPath, `{"name":"x","host":" ","port":80}`, http.StatusBadRequest, "host is required"},
		{"host too long", http.MethodPost, createPath, `{"name":"x","host":"` + strings.Repeat("h", 256) + `","port":80}`, http.StatusBadRequest, "host must be at most 255"},
		{"port missing", http.MethodPost, createPath, `{"name":"x","host":"h"}`, http.StatusBadRequest, "port is required"},
		{"port zero", http.MethodPost, createPath, `{"name":"x","host":"h","port":0}`, http.StatusBadRequest, "port must be between 1 and 65535"},
		{"port too high", http.MethodPost, createPath, `{"name":"x","host":"h","port":65536}`, http.StatusBadRequest, "port must be between 1 and 65535"},
		{"port as a string", http.MethodPost, createPath, `{"name":"x","host":"h","port":"80"}`, http.StatusBadRequest, "body contains the wrong type"},
		// The password is client-local: there is no field for it.
		{"a password", http.MethodPost, createPath, `{"name":"x","host":"h","port":80,"password":"hunter2"}`, http.StatusBadRequest, `body contains unknown field "password"`},
		{"a password on PATCH", http.MethodPatch, "/api/v1/proxies/" + existing.ID, `{"password":"hunter2"}`, http.StatusBadRequest, `body contains unknown field "password"`},
		{"PATCH nothing", http.MethodPatch, "/api/v1/proxies/" + existing.ID, `{}`, http.StatusBadRequest, "provide at least one of"},
		{"PATCH bad port", http.MethodPatch, "/api/v1/proxies/" + existing.ID, `{"port":-1}`, http.StatusBadRequest, "port must be between"},
	}
	for _, tt := range refused {
		rec := do(t, f.handler, tt.method, tt.path, tt.body, owner.Token)
		if rec.Code != tt.status {
			t.Errorf("%s: status = %d, want %d (body: %s)", tt.name, rec.Code, tt.status, rec.Body)
			continue
		}
		if msg := decodeEnvelope(t, rec).Error.Message; !strings.HasPrefix(msg, tt.message) {
			t.Errorf("%s: message = %q, want prefix %q", tt.name, msg, tt.message)
		}
	}

	t.Run("rename onto a taken name is 409, recasing its own is fine", func(t *testing.T) {
		other := f.createProxy(t, owner.Token, project.ID, map[string]any{"name": "Home", "host": "h", "port": 80})
		if rec := do(t, f.handler, http.MethodPatch, "/api/v1/proxies/"+other.ID, `{"name":"corp"}`, owner.Token); rec.Code != http.StatusConflict {
			t.Errorf("rename onto Corp: status = %d, want 409", rec.Code)
		}
		if rec := do(t, f.handler, http.MethodPatch, "/api/v1/proxies/"+existing.ID, `{"name":"CORP"}`, owner.Token); rec.Code != http.StatusOK {
			t.Errorf("recasing its own name: status = %d, want 200", rec.Code)
		}
	})

	t.Run("the same name in another project", func(t *testing.T) {
		otherProject := f.createProject(t, owner.Token, team.ID, "Other")
		f.createProxy(t, owner.Token, otherProject.ID, map[string]any{"name": "Corp", "host": "h", "port": 80})
	})

	t.Run("the limits themselves", func(t *testing.T) {
		f.createProxy(t, owner.Token, project.ID, map[string]any{"name": strings.Repeat("n", 100), "host": strings.Repeat("h", 255), "port": 65535})
		f.createProxy(t, owner.Token, project.ID, map[string]any{"name": "low", "host": "h", "port": 1})
	})
}

func TestProxyOrder(t *testing.T) {
	f, owner, team, project := newFolderProject(t, "proxy-order")
	a := f.createProxy(t, owner.Token, project.ID, map[string]any{"name": "A", "host": "h", "port": 80})
	b := f.createProxy(t, owner.Token, project.ID, map[string]any{"name": "B", "host": "h", "port": 80})
	c := f.createProxy(t, owner.Token, project.ID, map[string]any{"name": "C", "host": "h", "port": 80})
	other := f.createProject(t, owner.Token, team.ID, "Other")
	foreign := f.createProxy(t, owner.Token, other.ID, map[string]any{"name": "X", "host": "h", "port": 80})

	put := func(ids ...string) int {
		return do(t, f.handler, http.MethodPut, "/api/v1/proxies/order",
			folderBody(t, map[string]any{"project_id": project.ID, "proxy_ids": ids}), owner.Token).Code
	}

	for name, ids := range map[string][]string{
		"missing one":       {a.ID, b.ID},
		"another project's": {a.ID, b.ID, c.ID, foreign.ID},
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
	proxies := f.listProxies(t, owner.Token, project.ID)
	if got := proxyNames(proxies); got != "C,A,B" {
		t.Errorf("order = %s, want C,A,B", got)
	}
	for i, p := range proxies {
		if p.SortOrder != int32(i) {
			t.Errorf("%s sort_order = %d, want %d (0-based, contiguous)", p.Name, p.SortOrder, i)
		}
	}
	if d := f.createProxy(t, owner.Token, project.ID, map[string]any{"name": "D", "host": "h", "port": 80}); d.SortOrder != 3 {
		t.Errorf("new proxy sort_order = %d, want 3", d.SortOrder)
	}
}

// proxyTarget is a folder or request carrying a proxy setting.
type proxyTarget struct {
	name  string
	patch string
	reads map[string]func(t *testing.T) nodeProxyResponse
}

func newProxyTargets(t *testing.T, f teamsFixture, token, projectID string) []proxyTarget {
	t.Helper()

	folder := f.createFolder(t, token, projectID, "Routed", nil)
	request := f.createRequest(t, token, projectID, map[string]any{"name": "Routed", "folder_id": folder.ID})

	return []proxyTarget{
		{
			name:  "folder",
			patch: "/api/v1/folders/" + folder.ID,
			reads: map[string]func(t *testing.T) nodeProxyResponse{
				"folder list": func(t *testing.T) nodeProxyResponse {
					t.Helper()
					for _, entry := range f.listFolders(t, token, projectID) {
						if entry.ID == folder.ID {
							return entry.Proxy
						}
					}
					t.Fatalf("folder missing from list")
					return nodeProxyResponse{}
				},
			},
		},
		{
			name:  "request",
			patch: "/api/v1/requests/" + request.ID,
			reads: map[string]func(t *testing.T) nodeProxyResponse{
				"request list": func(t *testing.T) nodeProxyResponse {
					t.Helper()
					for _, entry := range f.listRequests(t, token, projectID) {
						if entry.ID == request.ID {
							return entry.Proxy
						}
					}
					t.Fatalf("request missing from list")
					return nodeProxyResponse{}
				},
				"GET /requests/{id}": func(t *testing.T) nodeProxyResponse {
					t.Helper()
					return f.getRequestDetail(t, token, request.ID).Proxy
				},
			},
		},
	}
}

func checkProxyEverywhere(t *testing.T, target proxyTarget, wantMode string, wantID *string) {
	t.Helper()

	for shape, read := range target.reads {
		got := read(t)
		gotID, want := "<null>", "<null>"
		if got.ProxyID != nil {
			gotID = *got.ProxyID
		}
		if wantID != nil {
			want = *wantID
		}
		if got.Mode != wantMode || gotID != want {
			t.Errorf("%s via %s = {%s, %s}, want {%s, %s}", target.name, shape, got.Mode, gotID, wantMode, want)
		}
	}
}

func TestNodeProxySetting(t *testing.T) {
	f, owner, team, project := newFolderProject(t, "node-proxy")
	corp := f.createProxy(t, owner.Token, project.ID, map[string]any{"name": "Corp", "host": "h", "port": 3128})
	other := f.createProject(t, owner.Token, team.ID, "Other")
	foreign := f.createProxy(t, owner.Token, other.ID, map[string]any{"name": "Foreign", "host": "h", "port": 80})

	for _, target := range newProxyTargets(t, f, owner.Token, project.ID) {
		t.Run(target.name, func(t *testing.T) {
			checkProxyEverywhere(t, target, "inherit", nil)

			patch := func(t *testing.T, proxy string) {
				t.Helper()
				if rec := do(t, f.handler, http.MethodPatch, target.patch, `{"proxy":`+proxy+`}`, owner.Token); rec.Code != http.StatusOK {
					t.Fatalf("PATCH proxy %s: status = %d (body: %s)", proxy, rec.Code, rec.Body)
				}
			}

			patch(t, `{"mode":"proxy","proxy_id":"`+corp.ID+`"}`)
			checkProxyEverywhere(t, target, "proxy", &corp.ID)

			patch(t, `{"mode":"none"}`)
			checkProxyEverywhere(t, target, "none", nil) // the id is forced to null

			patch(t, `{"mode":"proxy","proxy_id":"`+corp.ID+`"}`)
			patch(t, `{"mode":"global","proxy_id":null}`)
			checkProxyEverywhere(t, target, "global", nil)

			patch(t, `{"mode":"inherit"}`)
			checkProxyEverywhere(t, target, "inherit", nil)

			refused := []struct {
				name, proxy, message string
			}{
				{"proxy of another project", `{"mode":"proxy","proxy_id":"` + foreign.ID + `"}`, "proxy.proxy_id is not a proxy of this project"},
				{"unknown proxy", `{"mode":"proxy","proxy_id":"` + uuid.New().String() + `"}`, "proxy.proxy_id is not a proxy of this project"},
				{"proxy mode without an id", `{"mode":"proxy"}`, `proxy.proxy_id is required when mode is "proxy"`},
				{"proxy mode with a null id", `{"mode":"proxy","proxy_id":null}`, `proxy.proxy_id is required when mode is "proxy"`},
				{"an id with another mode", `{"mode":"none","proxy_id":"` + corp.ID + `"}`, `proxy.proxy_id must be null or absent when mode is "none"`},
				{"mode missing", `{"proxy_id":"` + corp.ID + `"}`, "proxy.mode is required"},
				{"mode unknown", `{"mode":"direct"}`, "proxy.mode must be one of inherit, global, none, proxy"},
				{"mode not a string", `{"mode":true}`, "proxy.mode must be a string"},
				{"id not a uuid", `{"mode":"proxy","proxy_id":"corp"}`, "proxy.proxy_id must be a valid uuid"},
				{"unknown field", `{"mode":"none","host":"h"}`, "proxy.host is not a known field"},
				{"not an object", `"none"`, "proxy must be an object"},
			}
			for _, tt := range refused {
				rec := do(t, f.handler, http.MethodPatch, target.patch, `{"proxy":`+tt.proxy+`}`, owner.Token)
				if rec.Code != http.StatusBadRequest {
					t.Errorf("%s: status = %d, want 400 (body: %s)", tt.name, rec.Code, rec.Body)
					continue
				}
				if msg := decodeEnvelope(t, rec).Error.Message; !strings.HasPrefix(msg, tt.message) {
					t.Errorf("%s: message = %q, want prefix %q", tt.name, msg, tt.message)
				}
			}
			checkProxyEverywhere(t, target, "inherit", nil)

			// Absent and null leave it alone.
			patch(t, `{"mode":"proxy","proxy_id":"`+corp.ID+`"}`)
			if rec := do(t, f.handler, http.MethodPatch, target.patch, `{"name":"Renamed","proxy":null}`, owner.Token); rec.Code != http.StatusOK {
				t.Fatalf("rename: status = %d", rec.Code)
			}
			checkProxyEverywhere(t, target, "proxy", &corp.ID)
			if rec := do(t, f.handler, http.MethodPatch, target.patch, `{"proxy":null}`, owner.Token); rec.Code != http.StatusBadRequest {
				t.Errorf("null proxy alone: status = %d, want 400 (nothing to change)", rec.Code)
			}
		})
	}
}

func TestDeletingProxyKeepsModeAndNullsID(t *testing.T) {
	f, owner, _, project := newFolderProject(t, "proxy-delete")
	doomed := f.createProxy(t, owner.Token, project.ID, map[string]any{"name": "Doomed", "host": "h", "port": 80})
	kept := f.createProxy(t, owner.Token, project.ID, map[string]any{"name": "Kept", "host": "h", "port": 80})

	targets := newProxyTargets(t, f, owner.Token, project.ID)
	for _, target := range targets {
		if rec := do(t, f.handler, http.MethodPatch, target.patch,
			`{"proxy":{"mode":"proxy","proxy_id":"`+doomed.ID+`"}}`, owner.Token); rec.Code != http.StatusOK {
			t.Fatalf("%s: setting the proxy: status = %d", target.name, rec.Code)
		}
	}
	bystander := f.createRequest(t, owner.Token, project.ID, map[string]any{"name": "Bystander"})
	f.mustPatchRequest(t, owner.Token, bystander.ID, `{"proxy":{"mode":"proxy","proxy_id":"`+kept.ID+`"}}`)

	if rec := do(t, f.handler, http.MethodDelete, "/api/v1/proxies/"+doomed.ID, "", owner.Token); rec.Code != http.StatusNoContent {
		t.Fatalf("deleting the proxy: status = %d", rec.Code)
	}

	// Mode stays "proxy", the id is gone; the client resolves this as inherit.
	for _, target := range targets {
		checkProxyEverywhere(t, target, "proxy", nil)
	}
	if got := f.getRequestDetail(t, owner.Token, bystander.ID).Proxy; got.Mode != "proxy" || got.ProxyID == nil || *got.ProxyID != kept.ID {
		t.Errorf("a node using another proxy was touched: %+v", got)
	}

	// Such a node can be updated without restating the proxy.
	for _, target := range targets {
		if rec := do(t, f.handler, http.MethodPatch, target.patch, `{"name":"Still fine"}`, owner.Token); rec.Code != http.StatusOK {
			t.Errorf("%s: unrelated PATCH after the proxy went: status = %d", target.name, rec.Code)
		}
	}
}

func TestProxyAccessMatrix(t *testing.T) {
	f := newAccessMatrixFixture(t)
	hidden := f.createProxy(t, f.owner.Token, f.ownerProject.ID, map[string]any{"name": "Hidden", "host": "h", "port": 80})

	type request struct {
		name, method, path, body, message string
	}
	denied := func(projectID, proxyID string) []request {
		return []request{
			{"create", http.MethodPost, "/api/v1/projects/" + projectID + "/proxies", `{"name":"x","host":"h","port":80}`, messageProjectNotFound},
			{"list", http.MethodGet, "/api/v1/projects/" + projectID + "/proxies", "", messageProjectNotFound},
			{"order", http.MethodPut, "/api/v1/proxies/order", `{"project_id":"` + projectID + `","proxy_ids":["` + proxyID + `"]}`, messageProjectNotFound},
			{"get", http.MethodGet, "/api/v1/proxies/" + proxyID, "", messageProxyNotFound},
			{"patch", http.MethodPatch, "/api/v1/proxies/" + proxyID, `{"port":81}`, messageProxyNotFound},
			{"delete", http.MethodDelete, "/api/v1/proxies/" + proxyID, "", messageProxyNotFound},
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

	t.Run("restricted member without a grant", func(t *testing.T) { check(t, f.limited.Token, denied(f.ownerProject.ID, hidden.ID)) })
	t.Run("outsider", func(t *testing.T) { check(t, f.outsider.Token, denied(f.ownerProject.ID, hidden.ID)) })
	t.Run("nonexistent ids look the same", func(t *testing.T) { check(t, f.owner.Token, denied(uuid.New().String(), uuid.New().String())) })

	if got := f.listProxies(t, f.owner.Token, f.ownerProject.ID); len(got) != 1 || got[0].Port != 80 {
		t.Errorf("hidden proxy changed: %+v", got)
	}

	t.Run("any member with access has full rights, within their project", func(t *testing.T) {
		token, projectID := f.limited.Token, f.grantedProject.ID
		mine := f.createProxy(t, token, projectID, map[string]any{"name": "Mine", "host": "h", "port": 80})
		if rec := do(t, f.handler, http.MethodPatch, "/api/v1/proxies/"+mine.ID, `{"port":81}`, token); rec.Code != http.StatusOK {
			t.Errorf("patch: status = %d", rec.Code)
		}

		request := f.createRequest(t, token, projectID, map[string]any{"name": "R"})
		f.mustPatchRequest(t, token, request.ID, `{"proxy":{"mode":"proxy","proxy_id":"`+mine.ID+`"}}`)

		// A proxy id from a project they cannot see is just "not a proxy of
		// this project": it neither works nor reveals anything.
		rec := do(t, f.handler, http.MethodPatch, "/api/v1/requests/"+request.ID,
			`{"proxy":{"mode":"proxy","proxy_id":"`+hidden.ID+`"}}`, token)
		if rec.Code != http.StatusBadRequest || decodeEnvelope(t, rec).Error.Message != errProxyNotInProject.Error() {
			t.Errorf("referencing a hidden project's proxy: status = %d", rec.Code)
		}

		if rec := do(t, f.handler, http.MethodDelete, "/api/v1/proxies/"+mine.ID, "", token); rec.Code != http.StatusNoContent {
			t.Errorf("delete: status = %d", rec.Code)
		}
	})
}

func TestProxyModeCheckConstraints(t *testing.T) {
	f, owner, _, project := newFolderProject(t, "proxy-check")
	folder := f.createFolder(t, owner.Token, project.ID, "F", nil)
	request := f.createRequest(t, owner.Token, project.ID, map[string]any{"name": "R"})

	for table, id := range map[string]string{"folders": folder.ID, "requests": request.ID} {
		_, err := f.pool.Exec(t.Context(), "UPDATE "+table+" SET proxy_mode = 'direct' WHERE id = $1", id)
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != checkViolation || pgErr.ConstraintName != table+"_proxy_mode" {
			t.Errorf("%s: error = %v, want a CHECK violation of %s_proxy_mode", table, err, table)
		}
	}
}

func TestProxyRoutesRequireAuth(t *testing.T) {
	f := newTeamsFixture(t)
	id := uuid.New().String()

	for _, route := range []struct{ method, path string }{
		{http.MethodPost, "/api/v1/projects/" + id + "/proxies"},
		{http.MethodGet, "/api/v1/projects/" + id + "/proxies"},
		{http.MethodPut, "/api/v1/proxies/order"},
		{http.MethodGet, "/api/v1/proxies/" + id},
		{http.MethodPatch, "/api/v1/proxies/" + id},
		{http.MethodDelete, "/api/v1/proxies/" + id},
	} {
		if rec := do(t, f.handler, route.method, route.path, "", ""); rec.Code != http.StatusUnauthorized {
			t.Errorf("%s %s: status = %d, want 401", route.method, route.path, rec.Code)
		}
	}
}
