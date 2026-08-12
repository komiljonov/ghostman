-- +goose Up
-- +goose StatementBegin
CREATE TABLE team_invitations (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    team_id      uuid        NOT NULL REFERENCES teams (id) ON DELETE CASCADE,
    -- Invitations are addressed to an email, not a user: inviting someone who
    -- has not registered yet is valid, and the invitation waits for them.
    email        text        NOT NULL,
    invited_by   uuid        NOT NULL REFERENCES users (id),
    status       text        NOT NULL DEFAULT 'pending',
    created_at   timestamptz NOT NULL DEFAULT now(),
    responded_at timestamptz,

    CONSTRAINT team_invitations_email_lowercase CHECK (email = lower(email)),
    CONSTRAINT team_invitations_status CHECK (status IN ('pending', 'accepted', 'rejected'))
);

-- One live invitation per email per team. Partial, so that a rejected or
-- accepted invitation does not block a later re-invite.
CREATE UNIQUE INDEX team_invitations_pending_team_email_idx
    ON team_invitations (team_id, email)
    WHERE status = 'pending';

-- Serves "what am I invited to?", which is keyed on email alone.
CREATE INDEX team_invitations_pending_email_idx
    ON team_invitations (email)
    WHERE status = 'pending';
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX IF EXISTS team_invitations_pending_email_idx;
DROP INDEX IF EXISTS team_invitations_pending_team_email_idx;
DROP TABLE IF EXISTS team_invitations;
-- +goose StatementEnd
