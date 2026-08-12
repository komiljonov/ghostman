-- +goose Up
-- +goose StatementBegin
CREATE TABLE users (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    email         text        NOT NULL UNIQUE,
    password_hash text        NOT NULL,
    name          text        NOT NULL DEFAULT '',
    created_at    timestamptz NOT NULL DEFAULT now(),

    -- Emails are always stored lowercase so the UNIQUE constraint is also a
    -- case-insensitive one. The application normalises before writing; this
    -- check makes it impossible to bypass.
    CONSTRAINT users_email_lowercase CHECK (email = lower(email))
);

CREATE TABLE sessions (
    -- SHA-256 of the bearer token. The raw token is returned to the client
    -- once and never stored, so a database leak does not yield usable tokens.
    token_hash bytea PRIMARY KEY,
    user_id    uuid        NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    created_at timestamptz NOT NULL DEFAULT now(),
    expires_at timestamptz NOT NULL
);

-- Supports both "all sessions for a user" lookups and the cascade delete.
CREATE INDEX sessions_user_id_idx ON sessions (user_id);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX IF EXISTS sessions_user_id_idx;
DROP TABLE IF EXISTS sessions;
DROP TABLE IF EXISTS users;
-- +goose StatementEnd
