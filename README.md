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
| GET    | `/api/v1/teams`          | bearer | The caller's teams: `[{id, name, owner_id, member_count, is_owner}]`. |
| GET    | `/api/v1/teams/{id}`     | bearer | Members only. `{id, name, owner_id, created_at, is_owner, members[]}`. |
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
| POST   | `/api/v1/projects/{project_id}/folders` | bearer | Project access. `{name, parent_id?}` → `201`; appended after its siblings. `400` if the parent is not in this project. |
| GET    | `/api/v1/projects/{project_id}/folders` | bearer | Project access. Every folder as a flat list `[{id, parent_id, name, sort_order, created_at}]`. |
| PATCH  | `/api/v1/folders/{id}`   | bearer | Project access. `{name}` → `200`. |
| POST   | `/api/v1/folders/{id}/move` | bearer | Project access. `{parent_id, sort_order?}` → `200`; `parent_id: null` is the root. `400` on a cycle or another project's parent. |
| PUT    | `/api/v1/folders/order`  | bearer | Project access. `{project_id, parent_id, folder_ids}`; `folder_ids` must be exactly that parent's children. `204`. |
| DELETE | `/api/v1/folders/{id}`   | bearer | Project access. `204`; the whole subtree goes with it. |
| POST   | `/api/v1/projects/{project_id}/environments` | bearer | Project access. `{name}` → `201`. `409` if the name is taken in the project (case-insensitive). |
| GET    | `/api/v1/projects/{project_id}/environments` | bearer | Project access. `[{id, name, sort_order, created_at}]` in order. |
| PATCH  | `/api/v1/environments/{id}` | bearer | Project access. `{name}` → `200`; `409` on a name clash. |
| DELETE | `/api/v1/environments/{id}` | bearer | Project access. `204`; its variables go with it. |
| PUT    | `/api/v1/environments/order` | bearer | Project access. `{project_id, environment_ids}` must list every environment of the project. `204`. |
| POST   | `/api/v1/environments/{env_id}/variables` | bearer | Project access. `{key, type?, value?}` → `201` `{id, key, type, value, sort_order}`. `type` is `regular` (default) or `secret`; a secret with a value is `400`. `409` on a duplicate key (case-insensitive). |
| GET    | `/api/v1/environments/{env_id}/variables` | bearer | Project access. The variables in order; secrets have `value: null`. |
| PATCH  | `/api/v1/variables/{id}` | bearer | Project access. `{key?, type?, value?}` → `200`. Making a variable secret discards its value. |
| DELETE | `/api/v1/variables/{id}` | bearer | Project access. `204`. |
| PUT    | `/api/v1/variables/order` | bearer | Project access. `{environment_id, variable_ids}` must list every variable of the environment. `204`. |
| POST   | `/api/v1/projects/{project_id}/requests` | bearer | Project access. `{name, folder_id?, method?, url?}` → `201`; appended after its siblings. `method` defaults to `GET`. `400` if the folder is not in this project. |
| GET    | `/api/v1/projects/{project_id}/requests` | bearer | Project access. Every request of the project as a flat list, without headers, query params or body. |
| GET    | `/api/v1/requests/{id}`  | bearer | Project access. The full request, including `headers`, `query_params` and `body`. |
| PATCH  | `/api/v1/requests/{id}`  | bearer | Project access. `{name?, method?, url?, headers?, query_params?, body?}` → `200` with the full request; partial update, and a present `headers`, `query_params` or `body` replaces that value whole. |
| POST   | `/api/v1/requests/{id}/move` | bearer | Project access. `{folder_id, sort_order?}` → `200`; `folder_id: null` is the root. |
| PUT    | `/api/v1/requests/order` | bearer | Project access. `{project_id, folder_id, request_ids}`; must be exactly the requests in that folder. `204`. |
| DELETE | `/api/v1/requests/{id}`  | bearer | Project access. `204`. |

Errors always use a single envelope:

```json
{ "error": { "code": "not_found", "message": "the requested resource was not found" } }
```

Codes in use: `bad_request`, `unauthorized`, `forbidden`, `not_found`,
`conflict`, `payload_too_large`, `internal_error`, `service_unavailable`.

Request bodies are limited to 2 MiB. A larger one is refused with `413`
(`payload_too_large`), before it is read when the client declares its
`Content-Length` and as soon as the limit is crossed when it does not.

URL shape, never nested more than one level deep:

- a collection hangs off its parent: `POST /teams/{id}/invitations`
- an individual resource is flat and never repeats the parent id:
  `DELETE /invitations/{id}`
- a state change is a verb sub-path on the flat resource:
  `POST /invitations/{id}/accept`
- `/me/...` addresses the current user's own collections: `GET /me/invitations`

Deliberate exceptions: `DELETE /teams/{team_id}/members/{user_id}`,
`PUT /teams/{team_id}/members/{user_id}/access`,
`PUT /teams/{team_id}/projects/order`, and `PUT /folders/order`, which takes its
scope in the body because the parent being reordered may be the project root.
`PUT /environments/order`, `PUT /variables/order` and `PUT /requests/order`
follow the same body-scoped shape.

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

`GET /teams/{id}` returns the owner's id alongside the members, so a client can
mark the owner in the list; `is_owner` says whether that is the caller:

```json
{
  "id": "5b05cf9c-507f-4c1b-b49a-7c29792aa630",
  "name": "Ghostman",
  "owner_id": "42759db6-7e19-4195-9f14-0dda7e8ee3d4",
  "created_at": "2026-09-25T15:35:23.437808+05:00",
  "is_owner": false,
  "members": [
    { "user_id": "42759db6-7e19-4195-9f14-0dda7e8ee3d4", "email": "ada@example.com", "name": "Ada", "all_projects": true },
    { "user_id": "135e4dc6-92e6-432a-98e7-d10d0e3c8110", "email": "bob@example.com", "name": "Bob", "all_projects": false }
  ]
}
```

`GET /teams` carries `owner_id` on each entry as well.

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
list. Leaving or being removed from a team deletes that member's grants in the
team, in the same transaction as the membership.

## Folders

Folders form a tree inside a project: `parent_id` points at another folder of
the same project, or is null at the root. `GET /projects/{project_id}/folders`
returns the whole tree as one flat list, grouped by parent and ordered by
`sort_order` within each group, and the client assembles it.

Anyone who can reach the project can create, rename, move, reorder and delete
its folders; there is no separate management right. No access is **404**, and a
folder in an unreachable project looks exactly like one that does not exist.

- A new folder, or a moved one without an explicit `sort_order`, goes after its
  siblings.
- A move is rejected with `400` if the new parent is the folder itself or one of
  its descendants, or belongs to another project. Moves within a project are
  serialised, so two concurrent moves cannot combine into a cycle.
- `PUT /folders/order` rewrites one sibling set atomically; the list must be
  exactly that set.
- Deleting a folder deletes its subtree; deleting a project deletes its folders.

## Environments

An environment is a named set of variables inside a project (`dev`, `staging`,
`prod`). Names are unique per project and variable keys unique per environment,
both regardless of case (`base_url` and `BASE_URL` cannot coexist, so a
reference is never ambiguous; the key keeps the case it was written in), and
both lists keep a 0-based
`sort_order`. As with folders, anyone who can reach the project may change them,
and no access is a **404** indistinguishable from a missing id. Deleting an
environment deletes its variables; deleting a project deletes its environments.

**Secret values never reach the server.** A variable is `regular` or `secret`.
For a secret the server stores only the key — the value lives solely in the
desktop client's local storage:

- creating or updating a secret with a non-empty value is refused with `400`;
- changing a regular variable to secret discards its stored value in the same
  update;
- secrets are always returned with `value: null`;
- the database enforces it independently: a CHECK constraint refuses any
  secret row that carries a value.

Resolving `{{variable}}` references, the active environment, and secret values
are all client concerns.

## Requests

Requests are the leaves of a project's folder tree: `folder_id` points at a
folder of the same project, or is null at the root. Like folders, they are open
to anyone who can reach the project, and no access is a **404** that looks
exactly like a missing id.

- `GET /projects/{project_id}/requests` returns every request of the project in
  one flat list, grouped by folder and ordered by `sort_order` (0-based) within
  each; the client places them in the tree. Deleting a folder deletes the
  requests in its subtree.
- The url is opaque to the server: it may be empty and may contain
  `{{variable}}` references. Only its length is limited (8192 characters).
  `method` is one of `GET POST PUT PATCH DELETE HEAD OPTIONS`, in upper case.
- `PATCH` is a partial update of name, method, url, headers and query
  parameters, and returns the full request; `updated_at` moves on
  every update and move.
- Moves and reorders run under the same per-project lock as folder moves.

**Headers and query parameters** share one shape, an ordered array of rows:

```json
[{ "key": "Authorization", "value": "Bearer {{token}}", "enabled": true }]
```

- Set with `PATCH /requests/{id}`: a present `headers` or `query_params`
  replaces that whole array (`[]` clears it); absent or `null` leaves it alone.
  A new request always starts with both empty.
- At most 100 rows. Each row has exactly `key` (non-empty, at most 200
  characters), `value` (may be empty, at most 8192) and `enabled` (boolean);
  anything else is `400`, with a message naming the row, e.g.
  `headers[3].key is required`.
- Duplicate keys are allowed, since HTTP permits repeated headers and query
  parameters. Order is kept, and keys and values are stored exactly as sent:
  what you PATCH is what you GET.

**The body** is a tagged union on `type`, set with `PATCH /requests/{id}`:

```json
{ "type": "none" }
{ "type": "raw", "content_type": "application/json", "content": "{\"id\": {{id}}}" }
{ "type": "form", "fields": [{ "key": "user", "value": "{{user}}", "enabled": true }] }
```

- Each variant has exactly those fields; anything missing or extra is `400`,
  with a message naming the path (`body.content_type is required`,
  `body.fields[2].enabled must be a boolean`).
- A present `body` replaces the whole body, so switching type leaves nothing of
  the old one behind; absent or `null` leaves it alone. A new request starts as
  `{"type": "none"}`.
- `raw`: `content_type` is any non-empty string up to 200 characters;
  `content` is any string up to 1 MiB (counted in bytes), possibly empty. The
  content is **not** checked against its content type — a half-typed JSON body
  is the user's business, and warning about it is the client's.
- `form`: `fields` follows exactly the header/query parameter row rules above.
- File and multipart bodies are deliberately not synced.

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
internal/authz/        permission checks (team membership and ownership; project, folder, environment, variable and request access)
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

Sync and every other product table. Requests have no file or multipart bodies,
no per-request auth, history or duplication, and are never
executed by the server (sending them is the client's job). Auth covers only
registration, login, logout and `/me`: there is no password reset, email
verification, session listing or refresh yet.

Teams have invitations and membership removal, but no roles or per-member
permissions beyond project access, no ownership transfer (so a user who still
owns a team cannot be deleted), and **no email delivery**: an invitation exists
only as a row, and the inviter has to tell the invitee out of band. Invitations
do not expire.

Projects hold folders, requests and environments, and cannot be duplicated or
moved between teams. Folders and requests cannot be duplicated or moved
between projects. Moving a project is deferred deliberately:
its access rows point at users who may not be in the destination team, and that
question is unresolved.

CORS is currently wide open (`Access-Control-Allow-Origin: *`) and there is no
rate limiting on the login endpoint; both must be addressed before the server is
exposed publicly.
