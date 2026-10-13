-- +goose Up
-- +goose StatementBegin
-- Pre- and post-request scripts: JavaScript the CLIENT runs before and after
-- a send. The server only stores the text; it never executes or validates it.
--
-- Unlike auth and follow_redirects, scripts do NOT inherit or override. They
-- ACCUMULATE: every folder's script runs for every request under it, the
-- outermost folder first, then each inner folder, then the request's own. An
-- empty script contributes nothing to the chain; it does not stop it.
ALTER TABLE requests
    ADD COLUMN pre_script  text NOT NULL DEFAULT '',
    ADD COLUMN post_script text NOT NULL DEFAULT '',
    ADD CONSTRAINT requests_pre_script_length  CHECK (length(pre_script) <= 32768),
    ADD CONSTRAINT requests_post_script_length CHECK (length(post_script) <= 32768);

ALTER TABLE folders
    ADD COLUMN pre_script  text NOT NULL DEFAULT '',
    ADD COLUMN post_script text NOT NULL DEFAULT '',
    ADD CONSTRAINT folders_pre_script_length  CHECK (length(pre_script) <= 32768),
    ADD CONSTRAINT folders_post_script_length CHECK (length(post_script) <= 32768);

COMMENT ON COLUMN requests.pre_script IS
    'JavaScript run by the client before a send. Accumulates: outer folder scripts first, then the request''s own (not inherit/override). Stored only, never executed by the server.';
COMMENT ON COLUMN requests.post_script IS
    'JavaScript run by the client after a send. Accumulates: outer folder scripts first, then the request''s own (not inherit/override). Stored only, never executed by the server.';
COMMENT ON COLUMN folders.pre_script IS
    'JavaScript run by the client before every send of a request under this folder, outer folders first. Accumulates, does not override. Stored only.';
COMMENT ON COLUMN folders.post_script IS
    'JavaScript run by the client after every send of a request under this folder, outer folders first. Accumulates, does not override. Stored only.';
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
-- Dropping the columns drops their CHECK constraints and comments with them.
ALTER TABLE folders
    DROP COLUMN IF EXISTS post_script,
    DROP COLUMN IF EXISTS pre_script;

ALTER TABLE requests
    DROP COLUMN IF EXISTS post_script,
    DROP COLUMN IF EXISTS pre_script;
-- +goose StatementEnd
