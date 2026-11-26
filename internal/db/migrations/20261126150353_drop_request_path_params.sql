-- +goose Up
-- +goose StatementBegin
-- Path parameter rows are retired days after launch. The data is minimal and
-- the client migrates its urls before this deploys, so the column is dropped
-- without carrying anything over.
ALTER TABLE requests
    DROP COLUMN IF EXISTS path_params;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
-- Re-adds the column empty; dropped rows are not restored.
ALTER TABLE requests
    ADD COLUMN path_params jsonb NOT NULL DEFAULT '[]';
-- +goose StatementEnd
