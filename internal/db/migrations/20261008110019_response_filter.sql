-- +goose Up
-- +goose StatementBegin
-- response_filter is a jq query the client applies to a request's response
-- body for display. It is an opaque string to the server: jq syntax is the
-- client's (and its jq engine's) concern and is not validated here. Empty
-- means no filter.
ALTER TABLE requests
    ADD COLUMN response_filter text NOT NULL DEFAULT '',
    ADD CONSTRAINT requests_response_filter_length
        CHECK (length(response_filter) <= 2048);

COMMENT ON COLUMN requests.response_filter IS
    'jq query the client applies to response bodies for display; empty = no filter. Opaque to the server, not validated.';
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
-- Dropping the column drops its CHECK constraint and comment with it.
ALTER TABLE requests DROP COLUMN IF EXISTS response_filter;
-- +goose StatementEnd
