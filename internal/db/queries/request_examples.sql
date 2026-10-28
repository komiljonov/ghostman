-- name: CreateRequestExample :one
-- Appends the example after the request's others, 0-based. Concurrent
-- creates may pick the same sort_order, which is harmless: ties fall back to
-- created_at.
INSERT INTO request_examples (request_id, name, status, headers, content_type, body, body_size, sort_order)
VALUES (
    @request_id,
    @name,
    @status,
    @headers,
    @content_type,
    sqlc.narg(body),
    @body_size,
    (
        SELECT COALESCE(MAX(existing.sort_order) + 1, 0)::int
        FROM request_examples AS existing
        WHERE existing.request_id = @request_id
    )
)
RETURNING *;

-- name: GetRequestExampleByID :one
SELECT *
FROM request_examples
WHERE id = $1;

-- name: ListRequestExampleSummaries :many
-- Summaries only: no headers, no body.
SELECT id, name, status, content_type, body_size, sort_order, updated_at
FROM request_examples
WHERE request_id = $1
ORDER BY sort_order, created_at;

-- name: CountRequestExamples :one
SELECT count(*)
FROM request_examples
WHERE request_id = $1;

-- name: UpdateRequestExample :one
-- A partial update merged in SQL: NULL keeps the current value. body and
-- body_size are written only when set_body is true, since NULL is itself a
-- real body ("no body").
UPDATE request_examples
SET name = COALESCE(sqlc.narg(name), name),
    status = COALESCE(sqlc.narg(status)::int, status),
    headers = COALESCE(sqlc.narg(headers)::jsonb, headers),
    content_type = COALESCE(sqlc.narg(content_type), content_type),
    body = CASE WHEN @set_body::boolean THEN sqlc.narg(body)::bytea ELSE body END,
    body_size = CASE WHEN @set_body::boolean THEN @body_size::int ELSE body_size END,
    updated_at = now()
WHERE id = @id
RETURNING *;

-- name: DeleteRequestExample :exec
DELETE FROM request_examples
WHERE id = $1;

-- name: ListRequestExampleIDsForUpdate :many
-- Locked, so a reorder validates and writes against the same set.
SELECT id
FROM request_examples
WHERE request_id = $1
ORDER BY sort_order, created_at
FOR UPDATE;

-- name: BulkUpdateRequestExampleOrder :exec
-- Position in the array becomes sort_order, 0-based.
UPDATE request_examples
SET sort_order = (ordered.position - 1)::int
FROM unnest(@example_ids::uuid[]) WITH ORDINALITY AS ordered(id, position)
WHERE request_examples.id = ordered.id
  AND request_examples.request_id = @request_id;
