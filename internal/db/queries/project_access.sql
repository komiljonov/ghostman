-- name: GrantProjectAccess :exec
-- Bulk grant for one project. Already-granted users are left alone rather than
-- raising a conflict.
INSERT INTO project_access (project_id, user_id)
SELECT @project_id, unnest(@user_ids::uuid[])
ON CONFLICT (project_id, user_id) DO NOTHING;

-- name: GrantUserProjectAccess :exec
-- Bulk grant for one user across several projects: the same table seen from
-- the member side.
INSERT INTO project_access (project_id, user_id)
SELECT unnest(@project_ids::uuid[]), @user_id
ON CONFLICT (project_id, user_id) DO NOTHING;

-- name: DeleteAllProjectAccess :exec
DELETE FROM project_access
WHERE project_id = $1;

-- name: DeleteAllUserAccessInTeam :exec
-- Clears one user's explicit grants within a single team, leaving grants in
-- other teams untouched.
DELETE FROM project_access
USING projects
WHERE project_access.project_id = projects.id
  AND project_access.user_id = @user_id
  AND projects.team_id = @team_id;

-- name: ListProjectAccessUsers :many
SELECT project_access.user_id, users.email, users.name
FROM project_access
JOIN users ON users.id = project_access.user_id
WHERE project_access.project_id = $1
ORDER BY users.email;

-- name: ListUserAccessibleProjectIDs :many
-- The explicit grant list only: projects this user reaches through
-- all_projects, team ownership or project ownership are not included.
SELECT project_access.project_id
FROM project_access
JOIN projects ON projects.id = project_access.project_id
WHERE project_access.user_id = @user_id
  AND projects.team_id = @team_id
ORDER BY projects.sort_order, projects.created_at;

-- name: CountTeamMembersInList :one
-- Used to reject user ids that are not members of the team.
SELECT count(*)
FROM team_members
WHERE team_id = @team_id
  AND user_id = ANY(@user_ids::uuid[]);

-- name: UpdateMemberAllProjects :one
UPDATE team_members
SET all_projects = @all_projects
WHERE team_id = @team_id
  AND user_id = @user_id
RETURNING *;
