-- +goose Up
-- +goose StatementBegin
CREATE TABLE teams (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    name       text        NOT NULL,
    -- No ON DELETE: a user who still owns teams cannot be deleted out from
    -- under them. Transferring ownership comes later.
    owner_id   uuid        NOT NULL REFERENCES users (id),
    created_at timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT teams_name_length CHECK (length(trim(name)) BETWEEN 1 AND 100)
);

-- Membership. There is deliberately no role column: ownership is teams.owner_id
-- and nothing else. The owner also has a row here, so member lookups never need
-- to special-case them.
CREATE TABLE team_members (
    team_id      uuid        NOT NULL REFERENCES teams (id) ON DELETE CASCADE,
    user_id      uuid        NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    all_projects boolean     NOT NULL DEFAULT true,
    created_at   timestamptz NOT NULL DEFAULT now(),

    PRIMARY KEY (team_id, user_id)
);

-- The primary key already covers team-first lookups; this covers the reverse,
-- which is what "list my teams" needs.
CREATE INDEX team_members_user_id_idx ON team_members (user_id);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX IF EXISTS team_members_user_id_idx;
DROP TABLE IF EXISTS team_members;
DROP TABLE IF EXISTS teams;
-- +goose StatementEnd
