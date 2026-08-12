package db

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Store is the generated query set plus the operations that need a
// transaction. Everything that touches the database goes through it.
type Store struct {
	*Queries

	pool *pgxpool.Pool
}

// NewStore returns a Store backed by pool.
func NewStore(pool *pgxpool.Pool) *Store {
	return &Store{
		Queries: New(pool),
		pool:    pool,
	}
}

// CreateTeamWithOwner creates a team and the owner's membership row atomically.
// A team without its owner's membership would be invisible to every query that
// works through team_members, so the two writes must not be separable.
func (s *Store) CreateTeamWithOwner(ctx context.Context, name string, ownerID uuid.UUID) (Team, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Team{}, fmt.Errorf("begin transaction: %w", err)
	}
	// No-op once the transaction has been committed.
	defer func() { _ = tx.Rollback(ctx) }()

	qtx := s.WithTx(tx)

	team, err := qtx.CreateTeam(ctx, CreateTeamParams{Name: name, OwnerID: ownerID})
	if err != nil {
		return Team{}, fmt.Errorf("create team: %w", err)
	}

	if _, err := qtx.CreateTeamMember(ctx, CreateTeamMemberParams{
		TeamID:      team.ID,
		UserID:      ownerID,
		AllProjects: true,
	}); err != nil {
		return Team{}, fmt.Errorf("create owner membership: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return Team{}, fmt.Errorf("commit transaction: %w", err)
	}

	return team, nil
}

// AcceptInvitation marks an invitation accepted and adds the invitee to the
// team, atomically: an accepted invitation that did not produce a membership
// would leave the invitee with no way back in.
//
// The update is guarded on status = 'pending', so a second concurrent accept
// matches no row and comes back as pgx.ErrNoRows for the caller to answer 409.
// An existing membership row is not an error.
func (s *Store) AcceptInvitation(ctx context.Context, invitationID, userID uuid.UUID) (TeamInvitation, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return TeamInvitation{}, fmt.Errorf("begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	qtx := s.WithTx(tx)

	invitation, err := qtx.UpdateInvitationStatus(ctx, UpdateInvitationStatusParams{
		ID:     invitationID,
		Status: InvitationAccepted,
	})
	if err != nil {
		// pgx.ErrNoRows travels unwrapped: the caller distinguishes it.
		return TeamInvitation{}, err
	}

	if err := qtx.CreateTeamMemberIfAbsent(ctx, CreateTeamMemberIfAbsentParams{
		TeamID: invitation.TeamID,
		UserID: userID,
	}); err != nil {
		return TeamInvitation{}, fmt.Errorf("add invitee to team: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return TeamInvitation{}, fmt.Errorf("commit transaction: %w", err)
	}

	return invitation, nil
}
