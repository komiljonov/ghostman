-- name: CreateTeam :one
INSERT INTO teams (name, owner_id)
VALUES ($1, $2)
RETURNING *;

-- name: GetTeamByID :one
SELECT *
FROM teams
WHERE id = $1;

-- name: UpdateTeamName :one
UPDATE teams
SET name = $2
WHERE id = $1
RETURNING *;

-- name: DeleteTeam :exec
-- Memberships go with it via ON DELETE CASCADE.
DELETE FROM teams
WHERE id = $1;

-- name: CreateTeamMember :one
INSERT INTO team_members (team_id, user_id, all_projects)
VALUES ($1, $2, $3)
RETURNING *;

-- name: CreateTeamMemberIfAbsent :exec
-- Used when accepting an invitation: an already-present membership must not
-- turn the acceptance into an error.
INSERT INTO team_members (team_id, user_id, all_projects)
VALUES ($1, $2, true)
ON CONFLICT (team_id, user_id) DO NOTHING;

-- name: GetTeamMember :one
SELECT *
FROM team_members
WHERE team_id = $1
  AND user_id = $2;

-- name: DeleteTeamMember :execrows
-- Returns the number of rows removed so callers can tell "removed" from
-- "was never a member".
DELETE FROM team_members
WHERE team_id = $1
  AND user_id = $2;

-- name: ListTeamsForUser :many
-- Teams this user belongs to, with the size of each team. The second join is
-- what counts members; the first is what filters to this user's teams.
SELECT sqlc.embed(teams), count(all_members.user_id) AS member_count
FROM teams
JOIN team_members AS my_membership
  ON my_membership.team_id = teams.id
 AND my_membership.user_id = $1
JOIN team_members AS all_members
  ON all_members.team_id = teams.id
GROUP BY teams.id
ORDER BY teams.created_at;

-- name: ListTeamMembers :many
SELECT team_members.user_id, users.email, users.name, team_members.all_projects
FROM team_members
JOIN users ON users.id = team_members.user_id
WHERE team_members.team_id = $1
ORDER BY users.email;
