-- +goose Up
-- +goose StatementBegin
CREATE TABLE environments (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id uuid        NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
    name       text        NOT NULL,
    -- Position within the project's environment list, 0-based.
    sort_order integer     NOT NULL DEFAULT 0,
    created_at timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT environments_name_length CHECK (length(trim(name)) BETWEEN 1 AND 100)
);

-- Two environments called "prod" and "Prod" in one project would only confuse,
-- so names are unique per project regardless of case.
CREATE UNIQUE INDEX environments_project_name_idx ON environments (project_id, lower(name));

CREATE INDEX environments_project_id_idx ON environments (project_id);

-- Secret variables are stored by key only. Their values live solely in the
-- desktop client; the server must never hold one, and the CHECK below makes
-- that a database guarantee rather than an application convention.
CREATE TABLE environment_variables (
    id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    environment_id uuid        NOT NULL REFERENCES environments (id) ON DELETE CASCADE,
    key            text        NOT NULL,
    type           text        NOT NULL DEFAULT 'regular',
    value          text,
    -- Position within the environment's variable list, 0-based.
    sort_order     integer     NOT NULL DEFAULT 0,
    created_at     timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT environment_variables_key_length CHECK (length(trim(key)) BETWEEN 1 AND 200),
    CONSTRAINT environment_variables_type CHECK (type IN ('regular', 'secret')),
    CONSTRAINT environment_variables_secret_no_value CHECK (type <> 'secret' OR value IS NULL)
);

-- base_url and BASE_URL in one environment would make {{base_url}} ambiguous,
-- so keys are unique per environment regardless of case. The key keeps the
-- case it was written in.
CREATE UNIQUE INDEX environment_variables_environment_key_idx
    ON environment_variables (environment_id, lower(key));

CREATE INDEX environment_variables_environment_id_idx ON environment_variables (environment_id);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX IF EXISTS environment_variables_environment_id_idx;
DROP INDEX IF EXISTS environment_variables_environment_key_idx;
DROP TABLE IF EXISTS environment_variables;
DROP INDEX IF EXISTS environments_project_id_idx;
DROP INDEX IF EXISTS environments_project_name_idx;
DROP TABLE IF EXISTS environments;
-- +goose StatementEnd
