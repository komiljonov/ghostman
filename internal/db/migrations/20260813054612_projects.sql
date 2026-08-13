-- +goose Up
-- +goose StatementBegin
CREATE TABLE projects (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    team_id    uuid        NOT NULL REFERENCES teams (id) ON DELETE CASCADE,
    name       text        NOT NULL,
    -- No ON DELETE: a user who still owns projects cannot be deleted out from
    -- under them, matching teams.owner_id.
    owner_id   uuid        NOT NULL REFERENCES users (id),
    -- Position within the team's project list. Shared by the whole team and
    -- not unique: reordering rewrites the block, and ties fall back to
    -- created_at.
    sort_order integer     NOT NULL DEFAULT 0,
    created_at timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT projects_name_length CHECK (length(trim(name)) BETWEEN 1 AND 100)
);

CREATE INDEX projects_team_id_idx ON projects (team_id);

-- Explicit per-project grants. Only consulted for members whose
-- team_members.all_projects is false; rows are kept for everyone regardless, so
-- flipping that flag off restores a previously configured list.
CREATE TABLE project_access (
    project_id uuid        NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
    user_id    uuid        NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    created_at timestamptz NOT NULL DEFAULT now(),

    PRIMARY KEY (project_id, user_id)
);

-- The primary key covers project-first lookups; this covers "what is this user
-- explicitly granted", which is how access is revoked when they leave a team.
CREATE INDEX project_access_user_id_idx ON project_access (user_id);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX IF EXISTS project_access_user_id_idx;
DROP TABLE IF EXISTS project_access;
DROP INDEX IF EXISTS projects_team_id_idx;
DROP TABLE IF EXISTS projects;
-- +goose StatementEnd
