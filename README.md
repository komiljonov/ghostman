# Ghostman

Sync server for the Ghostman API client.

This repository is currently a **scaffold**: HTTP server, configuration, database
pool, migrations and code generation are wired end to end, but no domain model
exists yet.

## Stack

| Concern       | Choice                                                  |
| ------------- | ------------------------------------------------------- |
| Routing       | stdlib `net/http` (`http.ServeMux` method+path patterns) |
| Database      | PostgreSQL 16 via `jackc/pgx/v5` + `pgxpool`             |
| Queries       | `sqlc` (`sql_package: pgx/v5`), generated into `internal/db` |
| Migrations    | `goose`, plain SQL, embedded with `embed.FS`, auto-applied on startup |
| Logging       | `log/slog` (text in dev, JSON in prod)                   |
| Configuration | Environment variables only, via `caarlos0/env/v11`       |
| Task runner   | `go-task` (cross-platform)                               |

## Prerequisites

| Tool          | Version           | Notes                                                     |
| ------------- | ----------------- | --------------------------------------------------------- |
| Go            | 1.25.7 or newer   | The code targets the Go 1.22 language baseline; the floor comes from goose/pgx. With the default `GOTOOLCHAIN=auto`, an older install fetches the right toolchain automatically. |
| Docker        | with Compose v2   | Runs PostgreSQL only.                                      |
| go-task       | v3                | `go install github.com/go-task/task/v3/cmd/task@latest`    |
| golangci-lint | v2                | `go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@latest` (only needed for `task lint`) |
| sqlc          | v1.30 or newer    | `go install github.com/sqlc-dev/sqlc/cmd/sqlc@latest` (only needed to regenerate queries; the output is committed) |
| goose CLI     | not required      | Migrations are embedded in the binary and applied on startup; `task migrate:*` drives them through `cmd/server`. |

Make sure the Go bin directory is on your `PATH`:

- Windows (PowerShell): `$env:Path += ";$(go env GOPATH)\bin"` (add it to your
  profile to make it permanent)
- macOS / Linux: `export PATH="$PATH:$(go env GOPATH)/bin"`

## Quickstart

```sh
# 1. Configuration
#    Windows (PowerShell):  Copy-Item .env.example .env
#    macOS / Linux:         cp .env.example .env

# 2. Start PostgreSQL (waits until the healthcheck passes)
task db:up

# 3. Run the server (applies embedded migrations, then listens on :8080)
task dev
```

Then, in a second terminal:

```sh
curl http://localhost:8080/healthz
# {"status":"ok"}

curl http://localhost:8080/api/v1/hello
# {"message":"hello from ghostman"}
```

On Windows, use `curl.exe` rather than `curl` in PowerShell: bare `curl` is an
alias for `Invoke-WebRequest`, which takes different flags.

## Endpoints

| Method | Path                     | Auth   | Description                          |
| ------ | ------------------------ | ------ | ------------------------------------ |
| GET    | `/healthz`               | public | Liveness plus a database ping. Returns `503` with an error envelope when the database is unreachable. |
| GET    | `/api/v1/hello`          | public | Placeholder endpoint.                |
| POST   | `/api/v1/auth/register`  | public | `{email, password, name}` → `201` with `{user, token}`. `409` if the email is taken, `400` on validation failure. |
| POST   | `/api/v1/auth/login`     | public | `{email, password}` → `200` with `{user, token}`, `401` otherwise. |
| POST   | `/api/v1/auth/logout`    | bearer | Deletes the session behind the current token. `204`. |
| GET    | `/api/v1/me`             | bearer | The authenticated user: `{id, email, name}`. |
| POST   | `/api/v1/teams`          | bearer | `{name}` → `201` with `{id, name}`. Creates the team and the caller's membership in one transaction. |
| GET    | `/api/v1/teams`          | bearer | The caller's teams: `[{id, name, member_count, is_owner}]`. |
| GET    | `/api/v1/teams/{id}`     | bearer | Members only. `{id, name, created_at, is_owner, members[]}`. |
| PATCH  | `/api/v1/teams/{id}`     | bearer | Owner only. `{name}` → `200` with the team detail. |
| DELETE | `/api/v1/teams/{id}`     | bearer | Owner only. `204`; memberships cascade.  |
| POST   | `/api/v1/teams/{team_id}/invitations` | bearer | Owner only. `{email}` → `201`. `409` if that email is already a member, is your own, or already has a pending invitation. |
| GET    | `/api/v1/teams/{team_id}/invitations` | bearer | Owner only. The team's pending invitations. |
| DELETE | `/api/v1/teams/{team_id}/members/{user_id}` | bearer | Remove a member (owner) or leave (yourself). `204`. |
| GET    | `/api/v1/me/invitations` | bearer | Pending invitations addressed to your email, with team and inviter. |
| POST   | `/api/v1/invitations/{id}/accept` | bearer | Invitee only. Joins the team. `200`. |
| POST   | `/api/v1/invitations/{id}/reject` | bearer | Invitee only. `200`. |
| DELETE | `/api/v1/invitations/{id}` | bearer | Owner only, while pending. `204`. |
| POST   | `/api/v1/teams/{team_id}/projects` | bearer | Any member. `{name}` → `201`; the creator becomes the project's owner. |
| GET    | `/api/v1/teams/{team_id}/projects` | bearer | The team's projects **that you can reach**, in `sort_order`. |
| PUT    | `/api/v1/teams/{team_id}/projects/order` | bearer | Any member. `{project_ids}` must list every project in the team. `204`. |
| GET    | `/api/v1/projects/{id}`  | bearer | Requires project access. |
| PATCH  | `/api/v1/projects/{id}`  | bearer | Team owner or project owner. `{name}` → `200`. |
| DELETE | `/api/v1/projects/{id}`  | bearer | Team owner or project owner. `204`; access rows cascade. |
| GET    | `/api/v1/projects/{id}/access` | bearer | Team owner or project owner. The explicit grant list. |
| PUT    | `/api/v1/projects/{id}/access` | bearer | Team owner or project owner. `{user_ids}` replaces the grant list. |
| PUT    | `/api/v1/teams/{team_id}/members/{user_id}/access` | bearer | Team owner only. `{all_projects, project_ids}`. |

Errors always use a single envelope:

```json
{ "error": { "code": "not_found", "message": "the requested resource was not found" } }
```

Codes in use: `bad_request`, `unauthorized`, `forbidden`, `not_found`,
`conflict`, `internal_error`, `service_unavailable`.

URL shape, never nested more than one level deep:

- a collection hangs off its parent: `POST /teams/{id}/invitations`
- an individual resource is flat and never repeats the parent id:
  `DELETE /invitations/{id}`
- a state change is a verb sub-path on the flat resource:
  `POST /invitations/{id}/accept`
- `/me/...` addresses the current user's own collections: `GET /me/invitations`

## Authentication

Authenticated requests carry the session token as a bearer token:

```sh
curl http://localhost:8080/api/v1/me -H "Authorization: Bearer $TOKEN"
```

- **Passwords** are hashed with argon2id (`m=64MiB, t=1, p=4`, 16-byte salt,
  32-byte key) and stored in the standard `$argon2id$v=19$...` encoding.
  Verification reads the cost parameters from the stored string, so they can be
  raised later without invalidating existing passwords.
- **Session tokens** are 32 random bytes, base64url-encoded, and shown to the
  client exactly once. The database stores only their SHA-256, so a leaked dump
  yields no usable tokens. Sessions last 30 days.
- **Login** answers an unknown email and a wrong password identically, in both
  message and timing.
- A background sweep deletes expired sessions every hour. Expiry itself is
  enforced by the session query, not by the sweep.

## Teams

A team has exactly one owner (`teams.owner_id`) and a set of members
(`team_members`). There is no role column: the owner also holds a membership
row, so member lookups never special-case them.

Permission checks live in `internal/authz` and handlers call them rather than
querying permissions inline:

- `RequireTeamMember` → `ErrNotMember`, which handlers answer with **404**. A
  non-member must not be able to tell an existing team from a missing one.
- `RequireTeamOwner` → `ErrNotOwner` for a member who is not the owner, which
  handlers answer with **403**, since a member already knows the team exists.

### Invitations

Invitations are addressed to an **email**, not a user, so inviting somebody who
has not registered yet is valid: the invitation waits for them and shows up in
`GET /me/invitations` the moment they sign up with that address.

- A partial unique index on `(team_id, email) WHERE status = 'pending'` allows
  exactly one live invitation per address per team, while leaving answered ones
  in place as history. Re-inviting after a rejection or a departure just works.
- Accepting is one transaction: the invitation is marked accepted **and** the
  membership row is written. The update is guarded on `status = 'pending'`, so a
  second concurrent accept touches no row and comes back as `409`.
- An invitation addressed to someone else answers `404`, not `403` — the caller
  must not learn it exists. The same goes for revoking one as a non-owner.

Removal and leaving are the same endpoint under different permissions: the owner
may remove any member, and a member may remove themselves. The owner cannot
leave (`400`), because a team must always have an owner and ownership transfer
does not exist yet.

## Projects

Projects belong to a team and are ordered by a team-wide `sort_order`. Any
member can create one and becomes its owner.

**Who can reach a project.** A user can open a project if they are a member of
its team **and** any one of:

- their `team_members.all_projects` flag is set (the default for new members);
- they own the team;
- they own the project;
- they hold an explicit `project_access` row for it.

The whole rule is one SQL query, applied both to a single project and to the
team's project list, so a list never shows something a direct fetch would
refuse. No access is answered with **404** rather than 403 — a project a user
cannot reach must not be distinguishable from one that does not exist.

**Who can change it.** Renaming, deleting and editing the access list require
the team's owner or the project's owner. Someone who can see the project but is
neither gets **403**: they already know it exists.

Access is configured from either direction, writing the same `project_access`
rows:

- `PUT /projects/{id}/access` — "who may open this project", available to
  whoever manages the project.
- `PUT /teams/{team_id}/members/{user_id}/access` — "what may this person
  open", available to the team owner only, since it spans the whole team.
  Setting `all_projects: true` clears their explicit rows, as those would be
  redundant while the flag is on.

Explicit grants only matter for members whose `all_projects` is false; they are
stored regardless, so turning the flag off restores a previously configured
list.

## Testing

`task test` runs everything. Tests that need real SQL behaviour (transactions,
cascades, constraints) create their own database next to the configured one —
`ghostman_test_api` for `internal/api` — apply the embedded migrations and
truncate between tests, so they never touch the development data.

They read `DATABASE_URL`, which `task test` loads from `.env`. A bare
`go test ./...` has no environment and **skips** them; use `task test`, or set
`DATABASE_URL` yourself. When no database answers at all, they skip with a
message rather than failing, so the suite still runs without Docker.

## Tasks

| Task                          | Description                                        |
| ----------------------------- | -------------------------------------------------- |
| `task dev`                    | Start the database if needed, then run the server.  |
| `task db:up` / `task db:down` | Start / stop PostgreSQL (data is preserved).        |
| `task db:reset`               | Drop the volume and start a fresh database.         |
| `task db:psql`                | Open `psql` inside the container.                   |
| `task migrate:up`             | Apply pending migrations.                           |
| `task migrate:down`           | Roll back the most recent migration.                |
| `task migrate:status`         | Show applied / pending migrations.                  |
| `task migrate:create -- name` | Create a new timestamped migration file.            |
| `task sqlc`                   | Regenerate query code from `internal/db/queries`.   |
| `task lint`                   | Run golangci-lint.                                  |
| `task test`                   | Run all tests.                                      |
| `task tidy`                   | `go mod tidy` and `go mod verify`.                  |

`task` loads `.env` automatically, so no shell-specific environment setup is
needed on any platform.

## Layout

```
cmd/server/            entrypoint: config, logger, pool, migrations, HTTP server, shutdown
internal/api/          HTTP server, routes, middleware, handlers, JSON helpers
internal/auth/         argon2id password hashing and session tokens (no HTTP, no SQL)
internal/authz/        permission checks (team membership and ownership)
internal/config/       Config struct and Load()
internal/db/           pgxpool setup, goose runner, transactional Store, sqlc output
internal/db/migrations goose SQL migrations (embedded)
internal/db/queries/   sqlc query definitions
internal/testdb/       throwaway test databases for database-backed tests
```

## Database workflow

1. Create a migration: `task migrate:create -- add_workspaces`
2. Write the SQL between the `-- +goose Up` / `-- +goose Down` markers.
3. Write queries in `internal/db/queries/*.sql` with sqlc `-- name:` annotations.
4. Regenerate: `task sqlc` (sqlc reads the schema straight from the migration
   files, so the generated code always matches what the server applies).
5. Apply: `task migrate:up`, or just restart the server, which migrates on start.

Generated sqlc code is committed so that building the project never requires the
sqlc binary.

`internal/db/queries/app_meta.sql` is a working example of this flow: it defines
`GetAppMeta`/`SetAppMeta` against the `app_meta` table created by the initial
migration. Nothing in the API uses it yet.

The migration commands also work directly through the binary, without go-task:

```sh
go run ./cmd/server -migrate=up
go run ./cmd/server -migrate=down
go run ./cmd/server -migrate=status
go run ./cmd/server -migrate=create -name add_workspaces
```

## Debugging

`.vscode/launch.json` is committed. Start the database with `task db:up`, then
run the **Debug server** configuration; it loads `.env` through `envFile` and
launches `cmd/server` under delve. The server runs on the host (not in Docker)
specifically to keep this path simple.

## Configuration

All configuration comes from environment variables; see `.env.example` for the
full documented list.

| Variable            | Default | Description                                |
| ------------------- | ------- | ------------------------------------------ |
| `DATABASE_URL`      | –       | Required. PostgreSQL connection string.    |
| `PORT`              | `8080`  | HTTP listen port.                          |
| `ENV`               | `dev`   | `dev` (text logs) or `prod` (JSON logs).   |
| `LOG_LEVEL`         | `info`  | `debug`, `info`, `warn` or `error`.        |
| `POSTGRES_USER`     | `ghostman` | Used by docker-compose only.             |
| `POSTGRES_PASSWORD` | `ghostman` | Used by docker-compose only.             |
| `POSTGRES_DB`       | `ghostman` | Used by docker-compose only.             |

## Not implemented yet

Projects, collections, sync and every other product table. Auth covers only
registration, login, logout and `/me`: there is no password reset, email
verification, session listing or refresh yet.

Teams have invitations and membership removal, but no roles or per-member
permissions beyond project access, no ownership transfer (so a user who still
owns a team cannot be deleted), and **no email delivery**: an invitation exists
only as a row, and the inviter has to tell the invitee out of band. Invitations
do not expire.

Projects hold nothing yet — no folders, requests or environments — and cannot be
duplicated or moved between teams. Moving a project is deferred deliberately:
its access rows point at users who may not be in the destination team, and that
question is unresolved.

CORS is currently wide open (`Access-Control-Allow-Origin: *`) and there is no
rate limiting on the login endpoint; both must be addressed before the server is
exposed publicly.
