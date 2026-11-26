-- +goose Up
-- +goose StatementBegin
-- Path parameter values: an ordered array of {key, value} rows. The keys come
-- from :segments in the url, which only the client parses; the server stores
-- the rows as given.
ALTER TABLE requests
    ADD COLUMN path_params jsonb NOT NULL DEFAULT '[]';
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE requests
    DROP COLUMN IF EXISTS path_params;
-- +goose StatementEnd
