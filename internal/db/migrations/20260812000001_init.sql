-- +goose Up
-- +goose StatementBegin
CREATE EXTENSION IF NOT EXISTS pgcrypto;

-- app_meta is a small key/value table for server-owned bookkeeping (schema
-- marker, feature flags, one-off state). It also gives sqlc something real to
-- generate against until the domain model lands.
CREATE TABLE app_meta (
    key        text PRIMARY KEY,
    value      text        NOT NULL,
    updated_at timestamptz NOT NULL DEFAULT now()
);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS app_meta;
-- The pgcrypto extension is intentionally left installed: other objects may
-- depend on it and dropping it is not this migration's business.
-- +goose StatementEnd
