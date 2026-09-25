-- name: CreateFolder :one
-- Appends the folder after its siblings: the folders sharing its project and
-- parent. parent_id is NULL at the root, so sibling filters here and below
-- compare it with IS NOT DISTINCT FROM. Concurrent creates may pick the same
-- sort_order, which is harmless: ties fall back to created_at.
INSERT INTO folders (project_id, parent_id, name, sort_order)
VALUES (
    @project_id,
    sqlc.narg(parent_id),
    @name,
    (
        SELECT COALESCE(MAX(siblings.sort_order) + 1, 0)::int
        FROM folders AS siblings
        WHERE siblings.project_id = @project_id
          AND siblings.parent_id IS NOT DISTINCT FROM sqlc.narg(parent_id)
    )
)
RETURNING *;

-- name: GetFolderByID :one
SELECT *
FROM folders
WHERE id = $1;

-- name: UpdateFolderName :one
UPDATE folders
SET name = $2
WHERE id = $1
RETURNING *;

-- name: DeleteFolder :exec
-- The subtree goes with it via ON DELETE CASCADE on parent_id.
DELETE FROM folders
WHERE id = $1;

-- name: ListFoldersByProject :many
-- Every folder of the project in one flat list; the client builds the tree.
SELECT *
FROM folders
WHERE project_id = $1
ORDER BY parent_id NULLS FIRST, sort_order, created_at;

-- name: ListSiblingFolderIDs :many
-- Locked, so a reorder validates and writes against the same sibling set.
SELECT id
FROM folders
WHERE project_id = @project_id
  AND parent_id IS NOT DISTINCT FROM sqlc.narg(parent_id)
ORDER BY sort_order, created_at
FOR UPDATE;

-- name: UpdateFolderParent :one
-- Without an explicit sort_order the folder goes after its new siblings. The
-- folder itself is excluded so that a move within the same parent does not
-- count its own position.
UPDATE folders
SET parent_id = sqlc.narg(parent_id),
    sort_order = COALESCE(
        sqlc.narg(sort_order)::int,
        (
            SELECT COALESCE(MAX(siblings.sort_order) + 1, 0)::int
            FROM folders AS siblings
            WHERE siblings.project_id = folders.project_id
              AND siblings.parent_id IS NOT DISTINCT FROM sqlc.narg(parent_id)
              AND siblings.id <> folders.id
        )
    )
WHERE folders.id = @id
RETURNING *;

-- name: BulkUpdateFolderOrder :exec
-- Position in the array becomes sort_order, within one parent only.
UPDATE folders
SET sort_order = ordered.position::int
FROM unnest(@folder_ids::uuid[]) WITH ORDINALITY AS ordered(id, position)
WHERE folders.id = ordered.id
  AND folders.project_id = @project_id
  AND folders.parent_id IS NOT DISTINCT FROM sqlc.narg(parent_id);

-- name: IsDescendant :one
-- Reports whether folder_id lies in the subtree rooted at root_id, counting
-- root_id itself. Walks up from folder_id, so the cost is the depth of the
-- tree rather than the size of the subtree. UNION rather than UNION ALL, so
-- the walk terminates even if a cycle ever slipped in.
WITH RECURSIVE ancestors AS (
    SELECT folders.id, folders.parent_id
    FROM folders
    WHERE folders.id = @folder_id::uuid

    UNION

    SELECT parent.id, parent.parent_id
    FROM folders AS parent
    JOIN ancestors ON parent.id = ancestors.parent_id
)
SELECT EXISTS (
    SELECT 1 FROM ancestors WHERE ancestors.id = @root_id::uuid
) AS is_descendant;
