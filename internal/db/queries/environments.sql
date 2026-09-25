-- name: CreateEnvironment :one
-- Appends the environment after the project's others, 0-based. Concurrent
-- creates may pick the same sort_order, which is harmless: ties fall back to
-- created_at.
INSERT INTO environments (project_id, name, sort_order)
VALUES (
    @project_id,
    @name,
    (
        SELECT COALESCE(MAX(existing.sort_order) + 1, 0)::int
        FROM environments AS existing
        WHERE existing.project_id = @project_id
    )
)
RETURNING *;

-- name: GetEnvironmentByID :one
SELECT *
FROM environments
WHERE id = $1;

-- name: UpdateEnvironmentName :one
UPDATE environments
SET name = $2
WHERE id = $1
RETURNING *;

-- name: DeleteEnvironment :exec
-- Variables go with it via ON DELETE CASCADE.
DELETE FROM environments
WHERE id = $1;

-- name: ListEnvironmentsByProject :many
SELECT *
FROM environments
WHERE project_id = $1
ORDER BY sort_order, created_at;

-- name: ListEnvironmentIDsForUpdate :many
-- Locked, so a reorder validates and writes against the same set.
SELECT id
FROM environments
WHERE project_id = $1
ORDER BY sort_order, created_at
FOR UPDATE;

-- name: BulkUpdateEnvironmentOrder :exec
-- Position in the array becomes sort_order, 0-based.
UPDATE environments
SET sort_order = (ordered.position - 1)::int
FROM unnest(@environment_ids::uuid[]) WITH ORDINALITY AS ordered(id, position)
WHERE environments.id = ordered.id
  AND environments.project_id = @project_id;

-- name: CreateVariable :one
-- Appends the variable after the environment's others, 0-based. The caller has
-- already refused a secret with a value; the table's CHECK refuses it again.
INSERT INTO environment_variables (environment_id, key, type, value, sort_order)
VALUES (
    @environment_id,
    @key,
    @type,
    sqlc.narg(value),
    (
        SELECT COALESCE(MAX(existing.sort_order) + 1, 0)::int
        FROM environment_variables AS existing
        WHERE existing.environment_id = @environment_id
    )
)
RETURNING *;

-- name: GetVariableByID :one
SELECT *
FROM environment_variables
WHERE id = $1;

-- name: UpdateVariable :one
-- A partial update merged in SQL, so it is atomic against concurrent edits.
-- NULL key or type keeps the current one. value is only written when
-- set_value is true, since null is itself a valid new value.
--
-- Whatever the request says, a variable that ends up secret ends up with no
-- value: that is how a regular -> secret change clears the stored value.
UPDATE environment_variables
SET key = COALESCE(sqlc.narg(key), key),
    type = COALESCE(sqlc.narg(type), type),
    value = CASE
        WHEN COALESCE(sqlc.narg(type), type) = 'secret' THEN NULL
        WHEN @set_value::boolean THEN sqlc.narg(value)
        ELSE value
    END
WHERE id = @id
RETURNING *;

-- name: DeleteVariable :exec
DELETE FROM environment_variables
WHERE id = $1;

-- name: ListVariablesByEnvironment :many
SELECT *
FROM environment_variables
WHERE environment_id = $1
ORDER BY sort_order, created_at;

-- name: ListVariableIDsForUpdate :many
-- Locked, so a reorder validates and writes against the same set.
SELECT id
FROM environment_variables
WHERE environment_id = $1
ORDER BY sort_order, created_at
FOR UPDATE;

-- name: BulkUpdateVariableOrder :exec
-- Position in the array becomes sort_order, 0-based.
UPDATE environment_variables
SET sort_order = (ordered.position - 1)::int
FROM unnest(@variable_ids::uuid[]) WITH ORDINALITY AS ordered(id, position)
WHERE environment_variables.id = ordered.id
  AND environment_variables.environment_id = @environment_id;
