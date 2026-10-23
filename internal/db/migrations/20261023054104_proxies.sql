-- +goose Up
-- +goose StatementBegin
-- Proxies are per-project objects, like environments: a named proxy server
-- that folders and requests can route through.
CREATE TABLE proxies (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id uuid        NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
    name       text        NOT NULL,
    proxy_type text        NOT NULL DEFAULT 'http',
    host       text        NOT NULL,
    port       integer     NOT NULL,
    -- The proxy PASSWORD is client-local and never stored here, the same
    -- principle as secret variable values: there is deliberately no column
    -- that could hold it.
    username   text        NOT NULL DEFAULT '',
    -- Comma-separated host patterns that bypass the proxy and go direct, e.g.
    -- "localhost, *.internal.uz". Opaque to the server: matching is client
    -- logic.
    bypass     text        NOT NULL DEFAULT '',
    -- Position within the project's proxy list, 0-based.
    sort_order integer     NOT NULL DEFAULT 0,
    created_at timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT proxies_name_length CHECK (length(trim(name)) BETWEEN 1 AND 100),
    CONSTRAINT proxies_proxy_type CHECK (proxy_type IN ('http', 'https', 'socks5')),
    CONSTRAINT proxies_host_length CHECK (length(host) BETWEEN 1 AND 255),
    CONSTRAINT proxies_port CHECK (port BETWEEN 1 AND 65535)
);

-- Names are unique per project regardless of case, like environments. The
-- index also serves project-scoped lookups.
CREATE UNIQUE INDEX proxies_project_name_idx ON proxies (project_id, lower(name));

-- The proxy setting is a cascading per-node setting on folders and requests,
-- like auth and follow_redirects:
--
--   'inherit' - resolve through the parent chain; the end of the chain is the
--               client's global default. The default.
--   'global'  - skip every ancestor and use the client's global default.
--   'none'    - go direct, no proxy. Explicit: it STOPS the chain.
--   'proxy'   - use proxy_id. If that proxy has been deleted (proxy_id NULL),
--               the client resolves the node as 'inherit'.
--
-- Resolution is CLIENT logic; the server only stores per-node values. A
-- proxy_id always names a proxy of the node's own project (enforced by the
-- application); deleting the proxy sets it to NULL rather than touching the
-- mode, so the node keeps saying "a specific proxy" until someone changes it.
ALTER TABLE requests
    ADD COLUMN proxy_mode text NOT NULL DEFAULT 'inherit',
    ADD COLUMN proxy_id   uuid REFERENCES proxies (id) ON DELETE SET NULL,
    ADD CONSTRAINT requests_proxy_mode CHECK (proxy_mode IN ('inherit', 'global', 'none', 'proxy'));

ALTER TABLE folders
    ADD COLUMN proxy_mode text NOT NULL DEFAULT 'inherit',
    ADD COLUMN proxy_id   uuid REFERENCES proxies (id) ON DELETE SET NULL,
    ADD CONSTRAINT folders_proxy_mode CHECK (proxy_mode IN ('inherit', 'global', 'none', 'proxy'));

-- Serve the ON DELETE SET NULL when a proxy is deleted.
CREATE INDEX requests_proxy_id_idx ON requests (proxy_id);
CREATE INDEX folders_proxy_id_idx ON folders (proxy_id);

COMMENT ON COLUMN requests.proxy_mode IS
    'inherit = parent chain (end = client global default); global = skip ancestors, use global default; none = direct, stops the chain; proxy = use proxy_id (NULL after the proxy is deleted: resolves as inherit). Resolved by the client.';
COMMENT ON COLUMN folders.proxy_mode IS
    'inherit = parent chain (end = client global default); global = skip ancestors, use global default; none = direct, stops the chain; proxy = use proxy_id (NULL after the proxy is deleted: resolves as inherit). Resolved by the client.';
COMMENT ON COLUMN proxies.username IS
    'Proxy user name. The password is client-local and never stored on the server.';
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX IF EXISTS folders_proxy_id_idx;
DROP INDEX IF EXISTS requests_proxy_id_idx;
-- Dropping the columns drops their CHECK constraints and foreign keys.
ALTER TABLE folders
    DROP COLUMN IF EXISTS proxy_id,
    DROP COLUMN IF EXISTS proxy_mode;
ALTER TABLE requests
    DROP COLUMN IF EXISTS proxy_id,
    DROP COLUMN IF EXISTS proxy_mode;
DROP INDEX IF EXISTS proxies_project_name_idx;
DROP TABLE IF EXISTS proxies;
-- +goose StatementEnd
