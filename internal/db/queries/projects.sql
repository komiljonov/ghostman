-- name: CreateProject :one
-- sort_order is computed in the statement so two clients creating projects at
-- once cannot both read the same "max" and then both write it.
INSERT INTO projects (team_id, name, owner_id, sort_order)
VALUES (
    @team_id,
    @name,
    @owner_id,
    COALESCE((SELECT max(sort_order) + 1 FROM projects WHERE team_id = @team_id), 0)
)
RETURNING *;

-- name: GetProjectByID :one
SELECT *
FROM projects
WHERE id = $1;

-- name: GetProjectForUser :one
-- The whole access rule in one query: the user must be a member of the
-- project's team, and then either see everything, own the team, own the
-- project, or hold an explicit grant. No row means "no access", which callers
-- answer with 404 — a user who cannot reach a project must not learn it exists.
--
-- The team's owner rides along because every caller that needs access also
-- needs to know whether this user may modify the project, and comparing two
-- ids in Go beats a computed column that sqlc can only type as nullable.
SELECT
    sqlc.embed(projects),
    teams.owner_id AS team_owner_id
FROM projects
JOIN teams ON teams.id = projects.team_id
JOIN team_members
  ON team_members.team_id = projects.team_id
 AND team_members.user_id = @user_id
WHERE projects.id = @project_id
  AND (
        team_members.all_projects
     OR teams.owner_id = @user_id
     OR projects.owner_id = @user_id
     OR EXISTS (
            SELECT 1
            FROM project_access
            WHERE project_access.project_id = projects.id
              AND project_access.user_id = @user_id
        )
  );

-- name: ListAccessibleProjects :many
-- Same rule as GetProjectForUser, applied across one team.
SELECT sqlc.embed(projects)
FROM projects
JOIN teams ON teams.id = projects.team_id
JOIN team_members
  ON team_members.team_id = projects.team_id
 AND team_members.user_id = @user_id
WHERE projects.team_id = @team_id
  AND (
        team_members.all_projects
     OR teams.owner_id = @user_id
     OR projects.owner_id = @user_id
     OR EXISTS (
            SELECT 1
            FROM project_access
            WHERE project_access.project_id = projects.id
              AND project_access.user_id = @user_id
        )
  )
ORDER BY projects.sort_order, projects.created_at;

-- name: ListTeamProjectIDs :many
-- Every project in the team, access ignored: reordering is a team-wide
-- operation and the caller must account for all of them.
SELECT id
FROM projects
WHERE team_id = $1;

-- name: CountProjectsInTeam :one
-- Used to reject project ids that belong to a different team.
SELECT count(*)
FROM projects
WHERE team_id = @team_id
  AND id = ANY(@project_ids::uuid[]);

-- name: UpdateProjectName :one
UPDATE projects
SET name = $2
WHERE id = $1
RETURNING *;

-- name: DeleteProject :exec
-- project_access rows go with it via ON DELETE CASCADE.
DELETE FROM projects
WHERE id = $1;

-- name: BulkUpdateProjectOrder :exec
-- Position in the array becomes sort_order. One statement, so the new order
-- lands atomically without an explicit transaction.
UPDATE projects
SET sort_order = ordered.position::int
FROM unnest(@project_ids::uuid[]) WITH ORDINALITY AS ordered(id, position)
WHERE projects.id = ordered.id
  AND projects.team_id = @team_id;
