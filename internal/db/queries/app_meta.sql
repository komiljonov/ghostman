-- name: GetAppMeta :one
SELECT key, value, updated_at
FROM app_meta
WHERE key = $1;

-- name: SetAppMeta :one
INSERT INTO app_meta (key, value)
VALUES ($1, $2)
ON CONFLICT (key) DO UPDATE
SET value = EXCLUDED.value,
    updated_at = now()
RETURNING key, value, updated_at;
