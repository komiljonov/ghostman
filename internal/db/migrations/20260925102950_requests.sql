-- +goose Up
-- +goose StatementBegin
-- Requests are the leaves of a project's folder tree. The table carries every
-- column the requests step needs, including headers, query parameters and
-- body, so later sub-steps expose them without a schema change.
CREATE TABLE requests (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id   uuid        NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
    -- NULL means the request sits at the project root. Deleting a folder takes
    -- the requests in its subtree with it. A folder always belongs to the same
    -- project; the application enforces that.
    folder_id    uuid        REFERENCES folders (id) ON DELETE CASCADE,
    name         text        NOT NULL,
    method       text        NOT NULL DEFAULT 'GET',
    -- Opaque to the server: it may be empty on a fresh request and may hold
    -- {{variable}} references, which only the client resolves.
    url          text        NOT NULL DEFAULT '',
    headers      jsonb       NOT NULL DEFAULT '[]',
    query_params jsonb       NOT NULL DEFAULT '[]',
    body         jsonb       NOT NULL DEFAULT '{"type":"none"}',
    -- Position among the requests sharing a folder (or the root), 0-based.
    sort_order   integer     NOT NULL DEFAULT 0,
    created_at   timestamptz NOT NULL DEFAULT now(),
    -- Set by the queries that change a request; there is no trigger.
    updated_at   timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT requests_name_length CHECK (length(trim(name)) BETWEEN 1 AND 100),
    CONSTRAINT requests_method CHECK (method IN ('GET', 'POST', 'PUT', 'PATCH', 'DELETE', 'HEAD', 'OPTIONS')),
    CONSTRAINT requests_url_length CHECK (length(url) <= 8192)
);

CREATE INDEX requests_project_id_idx ON requests (project_id);

-- Serves sibling lookups and the ON DELETE CASCADE from folders.
CREATE INDEX requests_folder_id_idx ON requests (folder_id);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX IF EXISTS requests_folder_id_idx;
DROP INDEX IF EXISTS requests_project_id_idx;
DROP TABLE IF EXISTS requests;
-- +goose StatementEnd
