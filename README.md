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

Errors always use a single envelope:

```json
{ "error": { "code": "not_found", "message": "the requested resource was not found" } }
```

Codes in use: `bad_request`, `unauthorized`, `not_found`, `conflict`,
`internal_error`, `service_unavailable`.

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
internal/api/          HTTP server, routes, middleware, auth handlers, JSON helpers
internal/auth/         argon2id password hashing and session tokens (no HTTP, no SQL)
internal/config/       Config struct and Load()
internal/db/           pgxpool setup, goose runner, sqlc output (db.go, models.go, *.sql.go)
internal/db/migrations goose SQL migrations (embedded)
internal/db/queries/   sqlc query definitions
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

Collections, sync and every other product table. Auth covers only registration,
login, logout and `/me`: there is no password reset, email verification, session
listing or refresh yet.

CORS is currently wide open (`Access-Control-Allow-Origin: *`) and there is no
rate limiting on the login endpoint; both must be addressed before the server is
exposed publicly.
