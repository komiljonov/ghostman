-- +goose Up
-- +goose StatementBegin
-- Folders form a tree inside one project. The API returns them as a flat list
-- and the client assembles the tree, so there is no path or depth column.
CREATE TABLE folders (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id uuid        NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
    -- NULL means the folder sits at the project root. Deleting a folder takes
    -- its whole subtree with it. A parent always belongs to the same project;
    -- the application enforces that, along with the absence of cycles.
    parent_id  uuid        REFERENCES folders (id) ON DELETE CASCADE,
    name       text        NOT NULL,
    -- Position among siblings. Not unique, like projects.sort_order: ties
    -- fall back to created_at.
    sort_order integer     NOT NULL DEFAULT 0,
    created_at timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT folders_name_length CHECK (length(trim(name)) BETWEEN 1 AND 100)
);

CREATE INDEX folders_project_id_idx ON folders (project_id);

-- Serves sibling lookups and the ON DELETE CASCADE down the tree.
CREATE INDEX folders_parent_id_idx ON folders (parent_id);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX IF EXISTS folders_parent_id_idx;
DROP INDEX IF EXISTS folders_project_id_idx;
DROP TABLE IF EXISTS folders;
-- +goose StatementEnd
