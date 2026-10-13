-- +goose Up
-- +goose StatementBegin
-- Webhook capture: an endpoint is a random URL on a capture domain that
-- records every request sent to it and answers with a configured response.
CREATE TABLE webhook_endpoints (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id   uuid        NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
    name         text        NOT NULL,
    -- 10 characters of [a-z0-9] from crypto/rand: DNS-label-safe, so it works
    -- both as <slug>.<domain> and as /h/<slug>. Never user-chosen.
    slug         text        NOT NULL,
    -- The PREFERRED display domain; '' means the server default (the first
    -- HOOKS_DOMAINS entry). A display preference only: a slug is captured on
    -- every configured domain, so changing this never breaks URLs already
    -- handed out.
    domain       text        NOT NULL DEFAULT '',
    resp_status  integer     NOT NULL DEFAULT 200,
    -- Same {key, value, enabled} row shape as request headers.
    resp_headers jsonb       NOT NULL DEFAULT '[]',
    resp_body    text        NOT NULL DEFAULT 'ok',
    created_by   uuid        NOT NULL REFERENCES users (id),
    created_at   timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT webhook_endpoints_slug_key UNIQUE (slug),
    CONSTRAINT webhook_endpoints_name_length CHECK (length(trim(name)) BETWEEN 1 AND 100),
    CONSTRAINT webhook_endpoints_resp_status CHECK (resp_status BETWEEN 100 AND 599),
    CONSTRAINT webhook_endpoints_resp_body_length CHECK (length(resp_body) <= 65536)
);

-- Listing a project's endpoints and counting them for the quota.
CREATE INDEX webhook_endpoints_project_id_idx ON webhook_endpoints (project_id);

CREATE TABLE webhook_captures (
    -- Monotonic, so it doubles as the polling cursor: "everything after id N".
    id             bigserial PRIMARY KEY,
    endpoint_id    uuid        NOT NULL REFERENCES webhook_endpoints (id) ON DELETE CASCADE,
    method         text        NOT NULL,
    path           text        NOT NULL,
    query          text        NOT NULL DEFAULT '',
    headers        jsonb       NOT NULL,
    content_type   text        NOT NULL DEFAULT '',
    -- At most 1 MiB is stored; body_size is what was received (up to the
    -- 10 MiB hard cut) and body_truncated says the stored body is partial.
    body           bytea,
    body_size      integer     NOT NULL,
    body_truncated boolean     NOT NULL DEFAULT false,
    source_ip      text        NOT NULL,
    received_at    timestamptz NOT NULL DEFAULT now()
);

-- Serves the polling cursor and the prune (newest N per endpoint).
CREATE INDEX webhook_captures_endpoint_id_idx ON webhook_captures (endpoint_id, id DESC);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX IF EXISTS webhook_captures_endpoint_id_idx;
DROP TABLE IF EXISTS webhook_captures;
DROP INDEX IF EXISTS webhook_endpoints_project_id_idx;
DROP TABLE IF EXISTS webhook_endpoints;
-- +goose StatementEnd
