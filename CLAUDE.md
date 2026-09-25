# Ghostman — sync server

Backend for Ghostman, a lightweight API client (Postman alternative).
The desktop app (Wails + React) lives in a separate repo; this is only the sync/cloud server.

## Stack — fixed decisions, do not substitute
- Go 1.25.7+ (go.mod floor, set by goose/pgx), stdlib net/http routing (method + path patterns). No Gin. No chi unless routes outgrow stdlib.
- PostgreSQL, jackc/pgx/v5 with pgxpool
- sqlc (sql_package: pgx/v5), schema derived from migrations dir — no ORM, ever
- goose migrations: plain SQL, embedded via embed.FS, auto-run on startup
- log/slog for logging; env-var config via caarlos0/env into one Config struct — no Viper
- go-task (Taskfile.yml) for commands; docker-compose runs Postgres only, Go runs on host

## Conventions
- Handlers: plain http.HandlerFunc + shared writeJSON/readJSON helpers, consistent error envelope
- All SQL lives in internal/db/queries/*.sql, regenerated with `task sqlc`
- Must run on Windows and Linux identically — no bash-only tooling in the critical path
- Permission checks live in internal/authz — handlers call the helpers, never inline a permission query

## API conventions
- Collections live under their parent, one level deep: POST/GET /teams/{id}/invitations
- Individual resources use flat paths: /invitations/{id} — never repeat the parent id
- State-changing actions are verb sub-paths on the flat resource: POST /invitations/{id}/accept
- /me/... is for collections addressed to the current user: GET /me/invitations
- Never nest deeper than one level
- Accepted exceptions, deliberate — do not "fix": DELETE /teams/{team_id}/members/{user_id}, PUT /teams/{team_id}/members/{user_id}/access, PUT /teams/{team_id}/projects/order, PUT /folders/order (scope in the body: the parent may be null), PUT /environments/order and PUT /variables/order (same body-scoped shape)

## Domain model
Designed so far: users + sessions (step 1), teams + team_members (step 2),
team_invitations (step 3), projects + project_access (step 4),
folders (step 5: a tree per project via nullable parent_id, NULL = root),
environments + environment_variables (step 6).
Everything else (requests, sync) is NOT designed yet —
do not invent product tables without discussion.
sort_order is 0-based for new code (environments, variables). Older reorder
endpoints (projects, folders) still write 1-based positions.

## Secret variables
The server stores a secret variable's key but NEVER its value — values live only
in the desktop client. A non-empty value on a secret → 400; regular → secret nulls
the value in the same UPDATE; secrets always serialize value: null. The CHECK
environment_variables_secret_no_value enforces it in the database. Never add a
code path, column or log line that could carry a secret value.

## Project access rule
A user can reach a project iff they are a member of its team AND one of:
team_members.all_projects, teams.owner_id, projects.owner_id, or a project_access row.
No access → 404, never 403: project existence must not leak.
Managing a project (rename/delete/access) is team owner or project owner only → 403.