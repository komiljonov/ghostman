-- +goose Up
-- +goose StatementBegin
-- follow_redirects is the first cascading per-node setting. Each folder and
-- request stores one of four values:
--
--   'inherit' - resolve through the parent chain: request -> folder -> ... ->
--               root folder -> the client's global default. The default.
--   'global'  - skip every ancestor and use the client's global default.
--   'on'      - follow redirects, regardless of ancestors.
--   'off'     - do not follow redirects, regardless of ancestors.
--
-- Resolving the effective value is CLIENT logic. The server only stores what
-- each node says and never resolves, so it has no notion of the global
-- default at all.
ALTER TABLE requests
    ADD COLUMN follow_redirects text NOT NULL DEFAULT 'inherit',
    ADD CONSTRAINT requests_follow_redirects
        CHECK (follow_redirects IN ('inherit', 'global', 'on', 'off'));

ALTER TABLE folders
    ADD COLUMN follow_redirects text NOT NULL DEFAULT 'inherit',
    ADD CONSTRAINT folders_follow_redirects
        CHECK (follow_redirects IN ('inherit', 'global', 'on', 'off'));

COMMENT ON COLUMN requests.follow_redirects IS
    'inherit = resolve via parent chain (folder -> ... -> client global default); global = skip ancestors, use client global default; on/off = explicit. Resolved by the client; the server only stores it.';
COMMENT ON COLUMN folders.follow_redirects IS
    'inherit = resolve via parent chain (folder -> ... -> client global default); global = skip ancestors, use client global default; on/off = explicit. Resolved by the client; the server only stores it.';
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
-- Dropping the columns drops their CHECK constraints and comments with them.
ALTER TABLE folders DROP COLUMN IF EXISTS follow_redirects;
ALTER TABLE requests DROP COLUMN IF EXISTS follow_redirects;
-- +goose StatementEnd
