// Package authz answers "may this user do this?" for team-scoped resources.
// Handlers call these helpers instead of writing permission queries inline, so
// the rules live in exactly one place as more resources are added.
package authz

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/komiljonov/ghostman/internal/db"
)

var (
	// ErrNotMember means the user is not in the team. Callers must answer this
	// with 404, not 403: to a non-member, the team does not exist.
	ErrNotMember = errors.New("authz: user is not a member of the team")

	// ErrNotOwner means the user is in the team but does not own it. This is a
	// 403 — the team's existence is already known to a member.
	ErrNotOwner = errors.New("authz: user is not the owner of the team")
)

// Store is the query subset these checks need.
type Store interface {
	GetTeamByID(ctx context.Context, id uuid.UUID) (db.Team, error)
	GetTeamMember(ctx context.Context, arg db.GetTeamMemberParams) (db.TeamMember, error)
}

// Checker performs permission checks against a Store.
type Checker struct {
	store Store
}

// New returns a Checker backed by store.
func New(store Store) *Checker {
	return &Checker{store: store}
}

// RequireTeamMember returns the membership row, or ErrNotMember if the user is
// not in the team. A missing team is reported as ErrNotMember too, so callers
// cannot accidentally distinguish "no such team" from "not yours".
func (c *Checker) RequireTeamMember(ctx context.Context, teamID, userID uuid.UUID) (db.TeamMember, error) {
	member, err := c.store.GetTeamMember(ctx, db.GetTeamMemberParams{
		TeamID: teamID,
		UserID: userID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return db.TeamMember{}, ErrNotMember
		}
		return db.TeamMember{}, fmt.Errorf("looking up team membership: %w", err)
	}

	return member, nil
}

// RequireTeamOwner returns the team when userID owns it. Non-members get
// ErrNotMember and members who are not the owner get ErrNotOwner, so the caller
// can answer 404 and 403 respectively.
func (c *Checker) RequireTeamOwner(ctx context.Context, teamID, userID uuid.UUID) (db.Team, error) {
	// Membership first: an outsider must not learn whether the team exists.
	if _, err := c.RequireTeamMember(ctx, teamID, userID); err != nil {
		return db.Team{}, err
	}

	team, err := c.store.GetTeamByID(ctx, teamID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// A membership row without its team should be impossible: the
			// foreign key cascades. Treat it as "not yours" rather than 500.
			return db.Team{}, ErrNotMember
		}
		return db.Team{}, fmt.Errorf("looking up team: %w", err)
	}

	if team.OwnerID != userID {
		return db.Team{}, ErrNotOwner
	}

	return team, nil
}
