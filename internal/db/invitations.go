package db

// Invitation statuses. These mirror the team_invitations_status CHECK
// constraint; changing one means changing the other in a migration.
const (
	InvitationPending  = "pending"
	InvitationAccepted = "accepted"
	InvitationRejected = "rejected"
)
