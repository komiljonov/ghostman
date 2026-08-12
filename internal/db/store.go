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
