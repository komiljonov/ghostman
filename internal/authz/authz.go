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

	// ErrNoFolderAccess means the folder does not exist or its project is out
	// of the user's reach. Both are answered with 404 and must look the same.
	ErrNoFolderAccess = errors.New("authz: user cannot access the folder")

	// ErrNoEnvironmentAccess and ErrNoVariableAccess are the same rule for
	// environments and their variables: missing and unreachable look alike.
	ErrNoEnvironmentAccess = errors.New("authz: user cannot access the environment")
	ErrNoVariableAccess    = errors.New("authz: user cannot access the variable")

	// ErrNoRequestAccess is the same rule for requests.
	ErrNoRequestAccess = errors.New("authz: user cannot access the request")
)

// Store is the query subset these checks need.
type Store interface {
	GetTeamByID(ctx context.Context, id uuid.UUID) (db.Team, error)
	GetTeamMember(ctx context.Context, arg db.GetTeamMemberParams) (db.TeamMember, error)
	GetProjectForUser(ctx context.Context, arg db.GetProjectForUserParams) (db.GetProjectForUserRow, error)
	GetFolderByID(ctx context.Context, id uuid.UUID) (db.Folder, error)
	GetEnvironmentByID(ctx context.Context, id uuid.UUID) (db.Environment, error)
	GetVariableByID(ctx context.Context, id uuid.UUID) (db.EnvironmentVariable, error)
	GetRequestByID(ctx context.Context, id uuid.UUID) (db.Request, error)
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

// RequireFolderAccess returns the folder when the user may reach its project.
// Anyone with project access may work with its folders; there is no separate
// management right.
//
// A missing folder and one in an unreachable project both come back as
// ErrNoFolderAccess. Reporting the latter as ErrNoProjectAccess instead would
// tell the caller that the folder id exists.
func (c *Checker) RequireFolderAccess(ctx context.Context, folderID, userID uuid.UUID) (db.Folder, error) {
	folder, err := c.store.GetFolderByID(ctx, folderID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return db.Folder{}, ErrNoFolderAccess
		}
		return db.Folder{}, fmt.Errorf("looking up folder: %w", err)
	}

	if _, err := c.RequireProjectAccess(ctx, folder.ProjectID, userID); err != nil {
		if errors.Is(err, ErrNoProjectAccess) {
			return db.Folder{}, ErrNoFolderAccess
		}
		return db.Folder{}, err
	}

	return folder, nil
}

// RequireEnvironmentAccess returns the environment when the user may reach its
// project. Like folders, environments are working material: project access is
// enough to change them.
//
// A missing environment and one in an unreachable project both come back as
// ErrNoEnvironmentAccess, so the environment id is not observable.
func (c *Checker) RequireEnvironmentAccess(ctx context.Context, environmentID, userID uuid.UUID) (db.Environment, error) {
	environment, err := c.store.GetEnvironmentByID(ctx, environmentID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return db.Environment{}, ErrNoEnvironmentAccess
		}
		return db.Environment{}, fmt.Errorf("looking up environment: %w", err)
	}

	if _, err := c.RequireProjectAccess(ctx, environment.ProjectID, userID); err != nil {
		if errors.Is(err, ErrNoProjectAccess) {
			return db.Environment{}, ErrNoEnvironmentAccess
		}
		return db.Environment{}, err
	}

	return environment, nil
}

// RequireVariableAccess returns the variable when the user may reach the
// project its environment belongs to. A missing variable and an unreachable
// one both come back as ErrNoVariableAccess.
func (c *Checker) RequireVariableAccess(ctx context.Context, variableID, userID uuid.UUID) (db.EnvironmentVariable, error) {
	variable, err := c.store.GetVariableByID(ctx, variableID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return db.EnvironmentVariable{}, ErrNoVariableAccess
		}
		return db.EnvironmentVariable{}, fmt.Errorf("looking up variable: %w", err)
	}

	if _, err := c.RequireEnvironmentAccess(ctx, variable.EnvironmentID, userID); err != nil {
		if errors.Is(err, ErrNoEnvironmentAccess) {
			return db.EnvironmentVariable{}, ErrNoVariableAccess
		}
		return db.EnvironmentVariable{}, err
	}

	return variable, nil
}

// RequireRequestAccess returns the request when the user may reach its
// project. Requests are working material like folders: project access is
// enough to change them.
//
// A missing request and one in an unreachable project both come back as
// ErrNoRequestAccess, so the request id is not observable.
func (c *Checker) RequireRequestAccess(ctx context.Context, requestID, userID uuid.UUID) (db.Request, error) {
	request, err := c.store.GetRequestByID(ctx, requestID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return db.Request{}, ErrNoRequestAccess
		}
		return db.Request{}, fmt.Errorf("looking up request: %w", err)
	}

	if _, err := c.RequireProjectAccess(ctx, request.ProjectID, userID); err != nil {
		if errors.Is(err, ErrNoProjectAccess) {
			return db.Request{}, ErrNoRequestAccess
		}
		return db.Request{}, err
	}

	return request, nil
}
