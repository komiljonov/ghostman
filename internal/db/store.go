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

// ReplaceProjectAccess makes the explicit grant list for one project exactly
// userIDs. Clearing and re-granting must be atomic: a reader between the two
// statements would otherwise see a project with nobody granted.
func (s *Store) ReplaceProjectAccess(ctx context.Context, projectID uuid.UUID, userIDs []uuid.UUID) error {
	return s.inTx(ctx, func(qtx *Queries) error {
		if err := qtx.DeleteAllProjectAccess(ctx, projectID); err != nil {
			return fmt.Errorf("clear project access: %w", err)
		}

		if len(userIDs) == 0 {
			return nil
		}

		if err := qtx.GrantProjectAccess(ctx, GrantProjectAccessParams{
			ProjectID: projectID,
			UserIds:   userIDs,
		}); err != nil {
			return fmt.Errorf("grant project access: %w", err)
		}

		return nil
	})
}

// SetMemberProjectAccess sets one member's access within a single team: the
// all_projects flag and, when it is false, their explicit grant list.
//
// Turning all_projects on drops the explicit rows, because they would be
// meaningless while the flag is set and stale if it were ever turned back off.
func (s *Store) SetMemberProjectAccess(
	ctx context.Context,
	teamID, userID uuid.UUID,
	allProjects bool,
	projectIDs []uuid.UUID,
) (TeamMember, error) {
	var member TeamMember

	err := s.inTx(ctx, func(qtx *Queries) error {
		updated, err := qtx.UpdateMemberAllProjects(ctx, UpdateMemberAllProjectsParams{
			TeamID:      teamID,
			UserID:      userID,
			AllProjects: allProjects,
		})
		if err != nil {
			// pgx.ErrNoRows travels unwrapped: the target is not a member.
			return err
		}
		member = updated

		// Either way the old list goes: on for being redundant, off for being
		// replaced.
		if err := qtx.DeleteAllUserAccessInTeam(ctx, DeleteAllUserAccessInTeamParams{
			UserID: userID,
			TeamID: teamID,
		}); err != nil {
			return fmt.Errorf("clear member project access: %w", err)
		}

		if allProjects || len(projectIDs) == 0 {
			return nil
		}

		if err := qtx.GrantUserProjectAccess(ctx, GrantUserProjectAccessParams{
			UserID:     userID,
			ProjectIds: projectIDs,
		}); err != nil {
			return fmt.Errorf("grant member project access: %w", err)
		}

		return nil
	})
	if err != nil {
		return TeamMember{}, err
	}

	return member, nil
}

// RemoveTeamMember deletes a membership together with the member's explicit
// project grants in that team, and reports how many membership rows were
// removed (0 when the user was not a member).
//
// The grants must go in the same transaction: left behind, they would come back
// to life if the user rejoined and was later switched off all_projects.
func (s *Store) RemoveTeamMember(ctx context.Context, teamID, userID uuid.UUID) (int64, error) {
	var removed int64

	err := s.inTx(ctx, func(qtx *Queries) error {
		var err error
		removed, err = qtx.DeleteTeamMember(ctx, DeleteTeamMemberParams{
			TeamID: teamID,
			UserID: userID,
		})
		if err != nil {
			return fmt.Errorf("delete team member: %w", err)
		}

		if removed == 0 {
			return nil
		}

		if err := qtx.DeleteAllUserAccessInTeam(ctx, DeleteAllUserAccessInTeamParams{
			UserID: userID,
			TeamID: teamID,
		}); err != nil {
			return fmt.Errorf("clear member project access: %w", err)
		}

		return nil
	})
	if err != nil {
		return 0, err
	}

	return removed, nil
}

// inTx runs fn inside a transaction, rolling back unless it returns nil.
func (s *Store) inTx(ctx context.Context, fn func(*Queries) error) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	// No-op once the transaction has been committed.
	defer func() { _ = tx.Rollback(ctx) }()

	if err := fn(s.WithTx(tx)); err != nil {
		return err
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit transaction: %w", err)
	}

	return nil
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
