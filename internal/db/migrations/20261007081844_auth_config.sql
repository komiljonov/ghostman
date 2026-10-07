-- +goose Up
-- +goose StatementBegin
-- Authorization is the second cascading per-node setting, after
-- follow_redirects. Each folder and request stores:
--
--   auth_type 'inherit' - resolve through the parent chain: request -> folder
--                         -> ... -> root folder. The end of the chain is no
--                         auth. The default.
--   auth_type 'none'    - explicitly no auth. Unlike 'inherit' this STOPS the
--                         chain: a request under a bearer-auth folder sends
--                         nothing.
--   auth_type 'bearer' / 'basic' / 'api_key' - explicit, using the matching
--                         auth_* fields below.
--
-- The value fields are opaque strings and may contain {{variables}}, which the
-- client substitutes at send time; the intended way to hold a real credential
-- is a secret variable referenced as {{name}}, whose value never reaches the
-- server. Fields not matching auth_type are ignored but deliberately
-- preserved, so switching the type back restores the old values.
--
-- Resolution is CLIENT logic; the server only stores per-node values.
ALTER TABLE requests
    ADD COLUMN auth_type           text NOT NULL DEFAULT 'inherit',
    ADD COLUMN auth_bearer_token   text NOT NULL DEFAULT '',
    ADD COLUMN auth_basic_username text NOT NULL DEFAULT '',
    ADD COLUMN auth_basic_password text NOT NULL DEFAULT '',
    ADD COLUMN auth_api_key_name   text NOT NULL DEFAULT '',
    ADD COLUMN auth_api_key_value  text NOT NULL DEFAULT '',
    ADD COLUMN auth_api_key_in     text NOT NULL DEFAULT 'header',
    ADD CONSTRAINT requests_auth_type
        CHECK (auth_type IN ('inherit', 'none', 'bearer', 'basic', 'api_key')),
    ADD CONSTRAINT requests_auth_api_key_in
        CHECK (auth_api_key_in IN ('header', 'query'));

ALTER TABLE folders
    ADD COLUMN auth_type           text NOT NULL DEFAULT 'inherit',
    ADD COLUMN auth_bearer_token   text NOT NULL DEFAULT '',
    ADD COLUMN auth_basic_username text NOT NULL DEFAULT '',
    ADD COLUMN auth_basic_password text NOT NULL DEFAULT '',
    ADD COLUMN auth_api_key_name   text NOT NULL DEFAULT '',
    ADD COLUMN auth_api_key_value  text NOT NULL DEFAULT '',
    ADD COLUMN auth_api_key_in     text NOT NULL DEFAULT 'header',
    ADD CONSTRAINT folders_auth_type
        CHECK (auth_type IN ('inherit', 'none', 'bearer', 'basic', 'api_key')),
    ADD CONSTRAINT folders_auth_api_key_in
        CHECK (auth_api_key_in IN ('header', 'query'));

COMMENT ON COLUMN requests.auth_type IS
    'inherit = resolve via parent chain, chain end = no auth; none = explicitly no auth (stops the chain); bearer/basic/api_key = explicit. auth_* values may contain {{vars}}, resolved client-side at send; fields not matching auth_type are preserved. Resolved by the client.';
COMMENT ON COLUMN folders.auth_type IS
    'inherit = resolve via parent chain, chain end = no auth; none = explicitly no auth (stops the chain); bearer/basic/api_key = explicit. auth_* values may contain {{vars}}, resolved client-side at send; fields not matching auth_type are preserved. Resolved by the client.';
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
-- Dropping the columns drops their CHECK constraints and comments with them.
ALTER TABLE folders
    DROP COLUMN IF EXISTS auth_api_key_in,
    DROP COLUMN IF EXISTS auth_api_key_value,
    DROP COLUMN IF EXISTS auth_api_key_name,
    DROP COLUMN IF EXISTS auth_basic_password,
    DROP COLUMN IF EXISTS auth_basic_username,
    DROP COLUMN IF EXISTS auth_bearer_token,
    DROP COLUMN IF EXISTS auth_type;

ALTER TABLE requests
    DROP COLUMN IF EXISTS auth_api_key_in,
    DROP COLUMN IF EXISTS auth_api_key_value,
    DROP COLUMN IF EXISTS auth_api_key_name,
    DROP COLUMN IF EXISTS auth_basic_password,
    DROP COLUMN IF EXISTS auth_basic_username,
    DROP COLUMN IF EXISTS auth_bearer_token,
    DROP COLUMN IF EXISTS auth_type;
-- +goose StatementEnd
