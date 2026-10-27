-- +goose Up
-- +goose StatementBegin
-- JWT is the fifth auth type. The CLIENT builds and signs the token at send
-- time; the server stores only the template below. It never signs, and it
-- never sees resolved values:
--
--   auth_jwt_algorithm      HMAC algorithm: HS256, HS384 or HS512
--   auth_jwt_secret         signing secret; may contain {{vars}}, and should be
--                           a {{secret_var}} reference, since like every auth_*
--                           column it is synced in plaintext to the project
--   auth_jwt_secret_base64  whether the (resolved) secret is base64-encoded
--   auth_jwt_payload        the claims as authored: JSON text that may contain
--                           {{vars}} and {{$now+N}} / {{$now-N}}; opaque to the
--                           server, which does not parse it
--   auth_jwt_add_to         send the token in a header or a query parameter
--   auth_jwt_name           that header or parameter's name;
--                           '' = the default (Authorization / token)
--   auth_jwt_prefix         header value prefix; '' = the default "Bearer "
--
-- As with the other types, fields not matching auth_type are kept, so
-- switching away from jwt and back restores them.
ALTER TABLE requests DROP CONSTRAINT requests_auth_type;
ALTER TABLE requests
    ADD CONSTRAINT requests_auth_type
        CHECK (auth_type IN ('inherit', 'none', 'bearer', 'basic', 'api_key', 'jwt')),
    ADD COLUMN auth_jwt_algorithm     text    NOT NULL DEFAULT 'HS256',
    ADD COLUMN auth_jwt_secret        text    NOT NULL DEFAULT '',
    ADD COLUMN auth_jwt_secret_base64 boolean NOT NULL DEFAULT false,
    ADD COLUMN auth_jwt_payload       text    NOT NULL DEFAULT '',
    ADD COLUMN auth_jwt_add_to        text    NOT NULL DEFAULT 'header',
    ADD COLUMN auth_jwt_name          text    NOT NULL DEFAULT '',
    ADD COLUMN auth_jwt_prefix        text    NOT NULL DEFAULT '',
    ADD CONSTRAINT requests_auth_jwt_algorithm
        CHECK (auth_jwt_algorithm IN ('HS256', 'HS384', 'HS512')),
    ADD CONSTRAINT requests_auth_jwt_add_to
        CHECK (auth_jwt_add_to IN ('header', 'query')),
    ADD CONSTRAINT requests_auth_jwt_payload_length
        CHECK (length(auth_jwt_payload) <= 8192);

ALTER TABLE folders DROP CONSTRAINT folders_auth_type;
ALTER TABLE folders
    ADD CONSTRAINT folders_auth_type
        CHECK (auth_type IN ('inherit', 'none', 'bearer', 'basic', 'api_key', 'jwt')),
    ADD COLUMN auth_jwt_algorithm     text    NOT NULL DEFAULT 'HS256',
    ADD COLUMN auth_jwt_secret        text    NOT NULL DEFAULT '',
    ADD COLUMN auth_jwt_secret_base64 boolean NOT NULL DEFAULT false,
    ADD COLUMN auth_jwt_payload       text    NOT NULL DEFAULT '',
    ADD COLUMN auth_jwt_add_to        text    NOT NULL DEFAULT 'header',
    ADD COLUMN auth_jwt_name          text    NOT NULL DEFAULT '',
    ADD COLUMN auth_jwt_prefix        text    NOT NULL DEFAULT '',
    ADD CONSTRAINT folders_auth_jwt_algorithm
        CHECK (auth_jwt_algorithm IN ('HS256', 'HS384', 'HS512')),
    ADD CONSTRAINT folders_auth_jwt_add_to
        CHECK (auth_jwt_add_to IN ('header', 'query')),
    ADD CONSTRAINT folders_auth_jwt_payload_length
        CHECK (length(auth_jwt_payload) <= 8192);

COMMENT ON COLUMN requests.auth_jwt_secret IS
    'JWT signing secret as authored; may contain {{vars}} - use a {{secret_var}} reference: synced in plaintext. The client signs at send time; the server never does.';
COMMENT ON COLUMN folders.auth_jwt_secret IS
    'JWT signing secret as authored; may contain {{vars}} - use a {{secret_var}} reference: synced in plaintext. The client signs at send time; the server never does.';
COMMENT ON COLUMN requests.auth_jwt_payload IS
    'JWT claims as authored (JSON text with {{vars}} and {{$now+N}}); opaque to the server, max 8192.';
COMMENT ON COLUMN folders.auth_jwt_payload IS
    'JWT claims as authored (JSON text with {{vars}} and {{$now+N}}); opaque to the server, max 8192.';
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
-- Lossy by necessity: the old auth_type CHECK cannot hold 'jwt', so nodes
-- using JWT fall back to 'inherit' before it is restored.
UPDATE requests SET auth_type = 'inherit' WHERE auth_type = 'jwt';
UPDATE folders SET auth_type = 'inherit' WHERE auth_type = 'jwt';

-- Dropping the columns drops their CHECK constraints and comments with them.
ALTER TABLE folders
    DROP COLUMN IF EXISTS auth_jwt_prefix,
    DROP COLUMN IF EXISTS auth_jwt_name,
    DROP COLUMN IF EXISTS auth_jwt_add_to,
    DROP COLUMN IF EXISTS auth_jwt_payload,
    DROP COLUMN IF EXISTS auth_jwt_secret_base64,
    DROP COLUMN IF EXISTS auth_jwt_secret,
    DROP COLUMN IF EXISTS auth_jwt_algorithm,
    DROP CONSTRAINT folders_auth_type;
ALTER TABLE folders
    ADD CONSTRAINT folders_auth_type
        CHECK (auth_type IN ('inherit', 'none', 'bearer', 'basic', 'api_key'));

ALTER TABLE requests
    DROP COLUMN IF EXISTS auth_jwt_prefix,
    DROP COLUMN IF EXISTS auth_jwt_name,
    DROP COLUMN IF EXISTS auth_jwt_add_to,
    DROP COLUMN IF EXISTS auth_jwt_payload,
    DROP COLUMN IF EXISTS auth_jwt_secret_base64,
    DROP COLUMN IF EXISTS auth_jwt_secret,
    DROP COLUMN IF EXISTS auth_jwt_algorithm,
    DROP CONSTRAINT requests_auth_type;
ALTER TABLE requests
    ADD CONSTRAINT requests_auth_type
        CHECK (auth_type IN ('inherit', 'none', 'bearer', 'basic', 'api_key'));
-- +goose StatementEnd
