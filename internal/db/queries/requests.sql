-- name: CreateRequest :one
-- Appends the request after its siblings: the requests sharing its project
-- and folder, 0-based. folder_id is NULL at the root, so sibling filters here
-- and below compare it with IS NOT DISTINCT FROM. Concurrent creates may pick
-- the same sort_order, which is harmless: ties fall back to created_at.
INSERT INTO requests (project_id, folder_id, name, method, url, sort_order)
VALUES (
    @project_id,
    sqlc.narg(folder_id),
    @name,
    @method,
    @url,
    (
        SELECT COALESCE(MAX(siblings.sort_order) + 1, 0)::int
        FROM requests AS siblings
        WHERE siblings.project_id = @project_id
          AND siblings.folder_id IS NOT DISTINCT FROM sqlc.narg(folder_id)
    )
)
RETURNING *;

-- name: GetRequestByID :one
SELECT *
FROM requests
WHERE id = $1;

-- name: UpdateRequestBasics :one
-- A partial update merged in SQL, so it is atomic against concurrent edits:
-- NULL keeps the current value. An empty url is a real value, not "keep".
UPDATE requests
SET name = COALESCE(sqlc.narg(name), name),
    method = COALESCE(sqlc.narg(method), method),
    url = COALESCE(sqlc.narg(url), url),
    updated_at = now()
WHERE id = @id
RETURNING *;

-- name: DeleteRequest :exec
DELETE FROM requests
WHERE id = $1;

-- name: ListRequestsByProject :many
-- Every request of the project in one flat list, without headers, query
-- parameters or body; the client places them in the folder tree.
SELECT id, project_id, folder_id, name, method, url, sort_order, created_at, updated_at
FROM requests
WHERE project_id = $1
ORDER BY folder_id NULLS FIRST, sort_order, created_at;

-- name: ListSiblingRequestIDs :many
-- Locked, so a reorder validates and writes against the same sibling set.
SELECT id
FROM requests
WHERE project_id = @project_id
  AND folder_id IS NOT DISTINCT FROM sqlc.narg(folder_id)
ORDER BY sort_order, created_at
FOR UPDATE;

-- name: UpdateRequestFolder :one
-- Without an explicit sort_order the request goes after its new siblings. The
-- request itself is excluded so that a move within the same folder does not
-- count its own position.
UPDATE requests
SET folder_id = sqlc.narg(folder_id),
    sort_order = COALESCE(
        sqlc.narg(sort_order)::int,
        (
            SELECT COALESCE(MAX(siblings.sort_order) + 1, 0)::int
            FROM requests AS siblings
            WHERE siblings.project_id = requests.project_id
              AND siblings.folder_id IS NOT DISTINCT FROM sqlc.narg(folder_id)
              AND siblings.id <> requests.id
        )
    ),
    updated_at = now()
WHERE requests.id = @id
RETURNING *;

-- name: BulkUpdateRequestOrder :exec
-- Position in the array becomes sort_order, 0-based, within one folder scope.
UPDATE requests
SET sort_order = (ordered.position - 1)::int
FROM unnest(@request_ids::uuid[]) WITH ORDINALITY AS ordered(id, position)
WHERE requests.id = ordered.id
  AND requests.project_id = @project_id
  AND requests.folder_id IS NOT DISTINCT FROM sqlc.narg(folder_id);
