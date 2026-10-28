-- +goose Up
-- +goose StatementBegin
-- Saved response examples: named example responses attached to a request,
-- synced like everything else in the project and editable.
CREATE TABLE request_examples (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    request_id   uuid        NOT NULL REFERENCES requests (id) ON DELETE CASCADE,
    name         text        NOT NULL,
    status       integer     NOT NULL DEFAULT 200,
    -- Same {key, value, enabled} row shape as request headers.
    headers      jsonb       NOT NULL DEFAULT '[]',
    content_type text        NOT NULL DEFAULT '',
    -- Examples SYNC to every member's client, so their bodies are hard-capped
    -- at 1 MiB (the handler refuses more; the CHECK below is the backstop).
    -- NULL when there is no body; body_size always matches the stored bytes.
    body         bytea,
    body_size    integer     NOT NULL DEFAULT 0,
    -- Position among the request's examples, 0-based.
    sort_order   integer     NOT NULL DEFAULT 0,
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT request_examples_name_length CHECK (length(trim(name)) BETWEEN 1 AND 100),
    CONSTRAINT request_examples_status CHECK (status BETWEEN 100 AND 599),
    CONSTRAINT request_examples_body_size
        CHECK (body_size = coalesce(octet_length(body), 0) AND body_size <= 1048576)
);

-- Serves the ordered list per request, the count and the cascade.
CREATE INDEX request_examples_request_id_idx ON request_examples (request_id, sort_order);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX IF EXISTS request_examples_request_id_idx;
DROP TABLE IF EXISTS request_examples;
-- +goose StatementEnd
