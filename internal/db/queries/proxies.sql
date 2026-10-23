-- name: CreateProxy :one
-- Appends the proxy after the project's others, 0-based. Concurrent creates
-- may pick the same sort_order, which is harmless: ties fall back to
-- created_at.
INSERT INTO proxies (project_id, name, proxy_type, host, port, username, bypass, sort_order)
VALUES (
    @project_id,
    @name,
    @proxy_type,
    @host,
    @port,
    @username,
    @bypass,
    (
        SELECT COALESCE(MAX(existing.sort_order) + 1, 0)::int
        FROM proxies AS existing
        WHERE existing.project_id = @project_id
    )
)
RETURNING *;

-- name: GetProxyByID :one
SELECT *
FROM proxies
WHERE id = $1;

-- name: ListProxiesByProject :many
SELECT *
FROM proxies
WHERE project_id = $1
ORDER BY sort_order, created_at;

-- name: UpdateProxy :one
-- A partial update merged in SQL: NULL keeps the current value.
UPDATE proxies
SET name = COALESCE(sqlc.narg(name), name),
    proxy_type = COALESCE(sqlc.narg(proxy_type), proxy_type),
    host = COALESCE(sqlc.narg(host), host),
    port = COALESCE(sqlc.narg(port)::int, port),
    username = COALESCE(sqlc.narg(username), username),
    bypass = COALESCE(sqlc.narg(bypass), bypass)
WHERE id = @id
RETURNING *;

-- name: DeleteProxy :exec
-- Folders and requests that pointed at it keep proxy_mode 'proxy' with
-- proxy_id set to NULL, via ON DELETE SET NULL.
DELETE FROM proxies
WHERE id = $1;

-- name: ListProxyIDsForUpdate :many
-- Locked, so a reorder validates and writes against the same set.
SELECT id
FROM proxies
WHERE project_id = $1
ORDER BY sort_order, created_at
FOR UPDATE;

-- name: BulkUpdateProxyOrder :exec
-- Position in the array becomes sort_order, 0-based.
UPDATE proxies
SET sort_order = (ordered.position - 1)::int
FROM unnest(@proxy_ids::uuid[]) WITH ORDINALITY AS ordered(id, position)
WHERE proxies.id = ordered.id
  AND proxies.project_id = @project_id;
