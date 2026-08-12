# Ghostman — sync server

Backend for Ghostman, a lightweight API client (Postman alternative).
The desktop app (Wails + React) lives in a separate repo; this is only the sync/cloud server.

## Stack — fixed decisions, do not substitute
- Go 1.22+, stdlib net/http routing (method + path patterns). No Gin. No chi unless routes outgrow stdlib.
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
- Flat URL paths only: every resource gets its own top-level path (/teams, /projects, ...).
  Never nest resources in paths — no /teams/{id}/projects.
- Relations are expressed with query params: GET /projects?team=<team-id>
- Sub-collections that belong to exactly one parent may be embedded in that parent's
  detail response (e.g. GET /teams/{id} includes its members)

## Domain model
Designed so far: users + sessions (step 1), teams + team_members (step 2).
Everything else (projects, collections, sync) is NOT designed yet — do not invent
product tables without discussion.