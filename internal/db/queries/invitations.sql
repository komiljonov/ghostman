-- name: CreateInvitation :one
-- Duplicate pending invitations are caught by the partial unique index rather
-- than by a pre-SELECT, which would race.
INSERT INTO team_invitations (team_id, email, invited_by)
VALUES ($1, $2, $3)
RETURNING *;

-- name: GetInvitationByID :one
SELECT *
FROM team_invitations
WHERE id = $1;

-- name: ListPendingInvitationsForEmail :many
SELECT
    team_invitations.id,
    team_invitations.created_at,
    teams.id   AS team_id,
    teams.name AS team_name,
    inviter.name  AS inviter_name,
    inviter.email AS inviter_email
FROM team_invitations
JOIN teams ON teams.id = team_invitations.team_id
JOIN users AS inviter ON inviter.id = team_invitations.invited_by
WHERE team_invitations.email = $1
  AND team_invitations.status = 'pending'
ORDER BY team_invitations.created_at;

-- name: ListPendingInvitationsForTeam :many
SELECT id, email, created_at
FROM team_invitations
WHERE team_id = $1
  AND status = 'pending'
ORDER BY created_at;

-- name: UpdateInvitationStatus :one
-- The status guard makes responding idempotent under concurrency: a second
-- accept or reject matches no row instead of overwriting the first answer.
UPDATE team_invitations
SET status = $2,
    responded_at = now()
WHERE id = $1
  AND status = 'pending'
RETURNING *;

-- name: DeleteInvitation :exec
DELETE FROM team_invitations
WHERE id = $1;

-- name: IsEmailTeamMember :one
SELECT EXISTS (
    SELECT 1
    FROM team_members
    JOIN users ON users.id = team_members.user_id
    WHERE team_members.team_id = $1
      AND users.email = $2
) AS is_member;
