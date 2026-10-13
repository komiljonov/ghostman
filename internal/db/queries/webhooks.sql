-- name: CountWebhookEndpointsInProject :one
SELECT count(*)
FROM webhook_endpoints
WHERE project_id = $1;

-- name: CreateWebhookEndpoint :one
INSERT INTO webhook_endpoints (project_id, name, slug, domain, created_by)
VALUES ($1, $2, $3, $4, $5)
RETURNING *;

-- name: GetWebhookEndpointByID :one
SELECT *
FROM webhook_endpoints
WHERE id = $1;

-- name: GetWebhookEndpointBySlug :one
-- The capture path's only lookup.
SELECT *
FROM webhook_endpoints
WHERE slug = $1;

-- name: ListWebhookEndpointsByProject :many
SELECT *
FROM webhook_endpoints
WHERE project_id = $1
ORDER BY created_at, id;

-- name: UpdateWebhookEndpoint :one
-- A partial update merged in SQL: NULL keeps the current value.
UPDATE webhook_endpoints
SET name = COALESCE(sqlc.narg(name), name),
    domain = COALESCE(sqlc.narg(domain), domain),
    resp_status = COALESCE(sqlc.narg(resp_status)::int, resp_status),
    resp_headers = COALESCE(sqlc.narg(resp_headers)::jsonb, resp_headers),
    resp_body = COALESCE(sqlc.narg(resp_body), resp_body)
WHERE id = @id
RETURNING *;

-- name: DeleteWebhookEndpoint :exec
-- Captures go with it via ON DELETE CASCADE.
DELETE FROM webhook_endpoints
WHERE id = $1;

-- name: InsertWebhookCapture :one
INSERT INTO webhook_captures (
    endpoint_id, method, path, query, headers, content_type,
    body, body_size, body_truncated, source_ip
)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
RETURNING id;

-- name: PruneWebhookCaptures :exec
-- Keeps only the newest captures of the endpoint: @oldest_kept_offset is the
-- number to keep minus one, i.e. the offset of the oldest capture that stays.
-- With fewer captures than that the subquery is NULL and nothing is deleted.
DELETE FROM webhook_captures
WHERE webhook_captures.endpoint_id = @endpoint_id
  AND webhook_captures.id < (
      SELECT kept.id
      FROM webhook_captures AS kept
      WHERE kept.endpoint_id = @endpoint_id
      ORDER BY kept.id DESC
      LIMIT 1 OFFSET @oldest_kept_offset
  );

-- name: ListWebhookCapturesAfter :many
-- The polling cursor: captures newer than @after, oldest first, summary
-- columns only (no headers, no body).
SELECT id, method, path, query, content_type, body_size, body_truncated, source_ip, received_at
FROM webhook_captures
WHERE endpoint_id = @endpoint_id
  AND id > @after
ORDER BY id
LIMIT @max_rows;

-- name: GetWebhookCapture :one
SELECT *
FROM webhook_captures
WHERE id = @id
  AND endpoint_id = @endpoint_id;

-- name: DeleteWebhookCaptures :exec
DELETE FROM webhook_captures
WHERE endpoint_id = $1;
