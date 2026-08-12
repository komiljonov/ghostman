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
- Collections live under their parent, one level deep: POST/GET /teams/{id}/invitations
- Individual resources use flat paths: /invitations/{id} — never repeat the parent id
- State-changing actions are verb sub-paths on the flat resource: POST /invitations/{id}/accept
- /me/... is for collections addressed to the current user: GET /me/invitations
- Never nest deeper than one level

## Domain model
Designed so far: users + sessions (step 1), teams + team_members (step 2).
Everything else (projects, collections, sync) is NOT designed yet — do not invent
product tables without discussion.