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

	// ErrNoProjectAccess means the user cannot reach the project, whether
	// because it does not exist, because they are not in its team, or because
	// their access does not extend to it. Callers answer all three with 404:
	// the three cases must not be distinguishable.
	ErrNoProjectAccess = errors.New("authz: user cannot access the project")

	// ErrNotProjectManager means the user can see the project but may not
	// change it. This is a 403 — they already know it exists.
	ErrNotProjectManager = errors.New("authz: user cannot manage the project")
)

// Store is the query subset these checks need.
type Store interface {
	GetTeamByID(ctx context.Context, id uuid.UUID) (db.Team, error)
	GetTeamMember(ctx context.Context, arg db.GetTeamMemberParams) (db.TeamMember, error)
	GetProjectForUser(ctx context.Context, arg db.GetProjectForUserParams) (db.GetProjectForUserRow, error)
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

// ProjectAccess is the outcome of a successful project access check: the
// project itself, plus whether this user may modify it.
type ProjectAccess struct {
	Project db.Project

	// CanManage is true for the team's owner and the project's owner.
	CanManage bool
}

// RequireProjectAccess returns the project when the user may see it. The whole
// rule lives in one query: team membership, plus one of all_projects, team
// ownership, project ownership or an explicit grant.
//
// A project that does not exist and one the user simply cannot reach both come
// back as ErrNoProjectAccess, so no caller can tell them apart.
func (c *Checker) RequireProjectAccess(ctx context.Context, projectID, userID uuid.UUID) (ProjectAccess, error) {
	row, err := c.store.GetProjectForUser(ctx, db.GetProjectForUserParams{
		ProjectID: projectID,
		UserID:    userID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ProjectAccess{}, ErrNoProjectAccess
		}
		return ProjectAccess{}, fmt.Errorf("looking up project access: %w", err)
	}

	return ProjectAccess{
		Project:   row.Project,
		CanManage: row.TeamOwnerID == userID || row.Project.OwnerID == userID,
	}, nil
}

// RequireProjectManage returns the project when the user may modify it: the
// team's owner or the project's owner. Someone with access but no management
// right gets ErrNotProjectManager (403), while someone without access at all
// gets ErrNoProjectAccess (404).
func (c *Checker) RequireProjectManage(ctx context.Context, projectID, userID uuid.UUID) (db.Project, error) {
	access, err := c.RequireProjectAccess(ctx, projectID, userID)
	if err != nil {
		return db.Project{}, err
	}

	if !access.CanManage {
		return db.Project{}, ErrNotProjectManager
	}

	return access.Project, nil
}
