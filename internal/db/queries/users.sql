-- name: CreateUser :one
-- Relies on the unique index for duplicate detection: callers inspect the
-- unique-violation error rather than pre-checking with a SELECT, which would
-- race.
INSERT INTO users (email, password_hash, name)
VALUES ($1, $2, $3)
RETURNING *;

-- name: GetUserByEmail :one
SELECT *
FROM users
WHERE email = $1;

-- name: GetUserByID :one
SELECT *
FROM users
WHERE id = $1;
