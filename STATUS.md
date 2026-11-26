# Ghostman feature status

Audited 2026-11-26 from the code: server `ghostman` at `fab939e` (main), app
`ghostman-app` at `d9231a4` (main) plus an uncommitted working tree (marked
**WIP** below). Sessions that complete or change a feature must update this
file in the same branch.

Evidence paths: `server:` = this repo, `app:` = `../ghostman-app`.

## Implemented

### Sync / domain (server)

- Accounts: register, login (rate-limited), logout, `GET /me`; bearer tokens stored as SHA-256, 30-day TTL, hourly expired-session sweep — server: `users`/`sessions` (migration `20260812145917`), `internal/auth/token.go`, `internal/db/sessions_cleanup.go`
- Teams: CRUD, owner = `teams.owner_id` (no role column), member removal, per-member project access — server: `teams`/`team_members` (`20260812153301`), `POST|GET|PATCH|DELETE /teams…`, `DELETE /teams/{team_id}/members/{user_id}`, `PUT /teams/{team_id}/members/{user_id}/access`
- Invitations: create/list per team, `GET /me/invitations`, accept/reject/delete, pending-only uniqueness, no email delivery — server: `team_invitations` (`20260812162410`), `internal/api/invitations.go`
- Projects: CRUD, reorder (1-based), explicit access grants, 404-not-403 access rule — server: `projects`/`project_access` (`20260813054612`), `internal/authz/authz.go`
- Folders: tree via nullable `parent_id`, move (cycle-checked), reorder (1-based) under `LockProject` — server: `folders` (`20260925085121`), `internal/api/folders.go`
- Environments + variables: case-insensitive unique names/keys, 0-based order, secrets key-only (CHECK `environment_variables_secret_no_value`) — server: `20260925092937_environments.sql`, `internal/api/environments.go`
- Requests: name/method/url/folder/order, move, 0-based reorder — server: `requests` (`20260925102950`), `internal/api/requests.go`
- Request headers + query params: `{key, value, enabled}` rows, ≤100 rows — server: `internal/api/key_value_rows.go`
- Request body: tagged union `none|raw|form|multipart`; multipart file rows hold a display name only — server: `internal/api/request_body.go`, `multipart_rows.go`
- Path params: `{key, value}` rows stored opaquely, detail-only — server: `20261126132415_request_path_params.sql`, `parsePathParamRows`
- Cascading `follow_redirects` (`inherit|global|on|off`) on folders + requests — server: `20261007061918_follow_redirects.sql`, `internal/api/node_settings.go`
- Cascading auth on folders + requests: `inherit|none|bearer|basic|api_key|jwt` (jwt = template HS256/384/512, client signs) — server: `20261007081844_auth_config.sql`, `20261027054206_auth_jwt.sql`, `internal/api/node_auth.go`
- Cascading proxy setting `{mode: inherit|global|none|proxy, proxy_id}` + per-project proxy objects (`http|https|socks5`, no password column) — server: `proxies` (`20261023054104`), `/projects/{id}/proxies`, `/proxies/{id}`, `PUT /proxies/order`
- Response filter (jq text, opaque, ≤2048) on requests — server: `20261008110019_response_filter.sql`
- Pre/post scripts (accumulating, ≤32768) on folders + requests — server: `20261013090131_scripts.sql`, `internal/api/node_scripts.go`
- Saved response examples: CRUD, summaries list, 0-based order, 1 MiB base64 body cap, `example_count` on request detail + project list — server: `request_examples` (`20261028092416`), `internal/api/examples.go`
- Webhook endpoints: ≤20 per project, random 10-char slug, configured response (status/headers/body ≤64 KiB), captures list/get/clear — server: `webhook_endpoints`/`webhook_captures` (`20261013115319`), `internal/api/webhooks.go`
- Server meta + health: `GET /api/v1/meta` (hooks domains), `GET /healthz` (DB ping + version), `GET /api/v1/hello` — server: `internal/api/routes.go`, `webhooks.go:128`

### Editor (app)

- Tab kinds `request | env | env_list | history | webhook | proxy` (no folder tab; folder settings are a modal) — app: `frontend/src/tabModel.ts:4`, `FolderSettingsModal.tsx`
- Request sub-tabs Params, Headers, Body, Auth, Scripts, Settings (follow redirects, proxy), History, Examples — app: `frontend/src/components/RequestEditor.tsx:96-103`
- Response pane Body / Headers / Console, jq filter bar, timing dropdown, Ctrl+F search — app: `frontend/src/components/ResponseView.tsx`
- Code generation dialog: curl (multiline/one-line, optional `-x`), JS fetch, Python requests, Go net/http — app: `internal/codegen/{curl,fetch,python,gocode}.go`, `CodegenPanel.tsx`
- Split editor groups, per-tab debounced autosave — app: `EditorGroups.tsx`, `frontend/src/autosave.ts`
- Sidebar: project tree (folders → requests → examples), docked Webhooks and Proxies panels; environments in top-bar `EnvSwitcher`; History and Import in the profile menu — app: `ProjectTree.tsx`, `frontend/src/sidebarPanels.ts:13-15`, `MainScreen.tsx:328-340`
- Global shortcuts: Ctrl/Cmd+Enter send, Ctrl+Tab / Ctrl+Shift+Tab, Ctrl/Cmd+W, Ctrl/Cmd+\ split, Ctrl/Cmd+1..9 focus group; tree Ctrl/Cmd+E rename, Ctrl/Cmd+D duplicate, Delete — app: `frontend/src/tabModel.ts:152-161`, `frontend/src/treeNav.ts`
- Request duplicate and "restore from history", composed client-side (create + patch; no server copy endpoint) — app: `internal/api/duplicate.go`
- Import (Postman, OpenAPI) with conversion warnings — app: `internal/importer/`
- Webhook captures polled every 2 s per open webhook tab, paused when unfocused — app: `frontend/src/webhookPoll.ts`

### Engine (app, Go)

- Manual redirect chain, ≤10 hops, per-hop trace (DNS/connect/TLS/wait/download, reuse, remote addr, failed phase), sensitive headers dropped cross-host — app: `internal/engine/engine.go`, `redirect.go`, `trace.go`
- Fixed 30 s send timeout (dial 10 s, TLS 10 s); not user-configurable — app: `internal/engine/engine.go:26`
- Proxy http/https (Basic auth incl. CONNECT) and socks5 (user/pass), bypass list with `*.domain` — app: `internal/engine/proxy.go`
- Auth bearer/basic/api_key(header|query)/jwt; JWT signed at send time with golang-jwt (HS256/384/512, base64 secret) — app: `internal/engine/auth.go`, `jwt.go`
- Multipart streamed from disk, re-opened for 307/308 — app: `internal/engine/multipart.go`
- `{{var}}` resolution: env variables + local secret overlay, unknown tokens left literal and reported; built from the server per send (online-only) — app: `internal/engine/resolve.go`, `internal/envs/envs.go`
- jq filter via gojq (1 s, 256 KiB preview, 200k outputs) — app: `internal/filter/filter.go`
- Scripts: goja VM per script, 2 s budget, `gm.environment/request/response/crypto/console`, no network/fs — app: `internal/scripting/`
- Body limits: 256 KiB UI preview, 20 MB full body — app: `internal/engine/engine.go:28-33`

### Local-only (app SQLite, `internal/store/migrations/`)

- `history`: full send log (template + resolved request, response, timings, auth, console, proxy) with entry/size caps — app: 00001/00006/00008–00010/00012, `internal/store/history.go`
- `settings` key/value: session token, server URL, theme, active env, follow-redirects default, codegen prefs, UI layout — app: 00002
- `secret_values`: secret variable values (never synced) — app: 00003
- `request_files`: multipart file paths (never synced) — app: 00011
- `proxy_passwords`: proxy passwords (never synced) — app: 00012
- `auth_defaults`: last-used auth fields per project/type for prefill — app: 00013, `internal/authmemory`
- `request_settings`: legacy, kept only for the one-time push to the server — app: 00004/00005, `redirects.go:144-149`

### Infra

- Server: stdlib mux, middleware chain recover → CORS (`*`) → request log → 2 MiB body limit — server: `internal/api/routes.go:130-135`, `middleware.go:141`, `json.go:16`
- Webhook host switch before any API middleware (`HOOKS_DOMAINS`; unset = not mounted) — server: `withCapture`, `internal/api/capture.go`
- Rate limiting: login/register 5/min burst 10 per IP; capture 10/s burst 30 per slug; `X-Forwarded-For` trusted only from loopback — server: `internal/ratelimit`, `internal/api/ratelimit.go:18`, `capture.go:54`, `client_ip.go`
- Embedded goose migrations auto-run on startup; sqlc queries — server: `internal/db/migrations/`, `internal/db/queries/`
- Loud-fail test guard: `task test` always sets `DATABASE_URL`; unreachable → `t.Fatal`, unset → skip — server: `internal/testdb/testdb.go:65-101`, `Taskfile.yml:115`
- Linux build + upload (scp protocol over Go's SSH client, password auth, progress output) — server: `Taskfile.yml` (`build-linux`, `upload`), `cmd/upload/main.go:242`
- App: online-only; unreachable server switches the UI to a retry screen — app: `UnreachableScreen.tsx`, `internal/session`

## Partial / flagged

- **WIP, uncommitted in the app:** path-variable substitution (`:id` → value) in the engine and history (`internal/engine/pathvars.go`, `internal/store/migrations/00014_history_path_params.sql`); frontend wiring under way (`frontend/src/urlParams.ts`, `frontend/wailsjs/go/models.ts` modified). Server side is shipped.
- **WIP, uncommitted in the app:** dynamic vars `$uuid $now $now±N $timestamp $isoTimestamp $date $randomInt $randomHex` — `internal/engine/dynamic.go`; no UI hint or completion yet (at HEAD only JWT `{{$now±N}}` exists).
- App `task lint` fails, all in WIP files: `internal/envs/envs_test.go:299,302,306` (errorlint: compare with `errors.Is`), `internal/engine/dynamic.go:82` (gocritic unlambda); one gofmt report was already gone on re-check.
- App `TestMemoryBudget` (`internal/scripting`) failed in the full `task test` run but passes alone — timing/load-sensitive.
- App skips: `internal/engine/multipart_test.go:196` `t.Skip("large upload")` under `-short`; `internal/codegen/roundtrip_test.go:197` `t.Skipf("%s not available")` when curl/node/python/go is missing.
- App "for now" notes: `internal/codegen/codegen.go:41` "Options are the per-target choices (curl only for now)"; `internal/importer/openapi.go:391` "Ghostman has no cookies yet"; OAuth etc. imported as Inherit — `internal/importer/mapper.go:515`, `openapi.go:914`.
- Server: no TODO/FIXME/"not implemented" in code; the only skip is the `DATABASE_URL`-unset guard (`internal/testdb/testdb.go:101`).
- Server: sqlc queries never called — `GetAppMeta`, `SetAppMeta` (table `app_meta` has no reader or writer), `GetProjectByID`.
- Server: stale comment `internal/api/requests.go:21-24` says headers/query_params/body are read-only ("nothing here writes them"); they are writable.
- Server: CLAUDE.md cleanup still open — project/folder reorder write 1-based (`BulkUpdateProjectOrder`, `BulkUpdateFolderOrder`); redundant `environments_project_id_idx` / `environment_variables_environment_id_idx` still exist.
- Server: no single-item GET for folders, environments or variables (lists only); every registered route has a handler.

### Docs vs code

- CLAUDE.md "Status" lists only steps 1–7c (+ path_params); follow_redirects, auth/jwt, proxies, scripts, response_filter, webhooks, examples are described in later sections but not in the status list.
- Not in CLAUDE.md (README covers them): `GET /api/v1/meta`, `GET /api/v1/hello`, 30-day session TTL + expiry sweep, 20-webhooks-per-project cap, `cmd/upload` / `build-linux` / `upload` tasks.
- README "Not implemented yet" is stale: "Sync and every other product table" predates webhooks, proxies and examples; "Projects hold folders, requests and environments" omits proxies, webhooks and examples; "a user who still owns a team cannot be deleted" — there is no user-deletion endpoint at all.
- CLAUDE.md backlog says request duplication is deferred; the app ships request duplicate by composing create + patch (no server endpoint; folders/projects still cannot be duplicated).

## Not present

From the README "Not implemented yet" section and the CLAUDE.md backlog, confirmed absent in code:

- Sync protocol / offline mode / outbox — app is online-only, edits autosave per tab, last write wins
- Password reset, email verification, session listing, token refresh, account deletion
- Team roles, ownership transfer, invitation email delivery, invitation expiry
- Moving projects between teams; duplicating projects or folders; cross-project moves of folders/requests
- Server-side request history (history is local-only in the app); variable history
- Locked-down CORS (still `*`); distributed rate limiting (in-memory per process)
- Also absent in the app: cookie jar, TLS options (insecure / client certs), user-configurable timeout
