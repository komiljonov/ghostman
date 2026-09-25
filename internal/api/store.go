package api

import (
	"context"

	"github.com/google/uuid"

	"github.com/komiljonov/ghostman/internal/db"
)

// Store is the slice of the database API that the HTTP layer uses. Declaring it
// here (rather than passing *db.Store around) keeps handler tests runnable
// without a database.
type Store interface {
	AuthStore
	TeamStore
	InvitationStore
	ProjectStore
}

// AuthStore covers registration, login and session lookup.
type AuthStore interface {
	CreateUser(ctx context.Context, arg db.CreateUserParams) (db.User, error)
	GetUserByEmail(ctx context.Context, email string) (db.User, error)
	GetUserByID(ctx context.Context, id uuid.UUID) (db.User, error)

	CreateSession(ctx context.Context, arg db.CreateSessionParams) (db.Session, error)
	GetSessionByTokenHash(ctx context.Context, tokenHash []byte) (db.GetSessionByTokenHashRow, error)
	DeleteSession(ctx context.Context, tokenHash []byte) error
}

// TeamStore covers team CRUD. CreateTeamWithOwner and RemoveTeamMember are
// transactional, which is why this interface is satisfied by *db.Store and not
// by *db.Queries.
type TeamStore interface {
	CreateTeamWithOwner(ctx context.Context, name string, ownerID uuid.UUID) (db.Team, error)
	GetTeamByID(ctx context.Context, id uuid.UUID) (db.Team, error)
	UpdateTeamName(ctx context.Context, arg db.UpdateTeamNameParams) (db.Team, error)
	DeleteTeam(ctx context.Context, id uuid.UUID) error

	GetTeamMember(ctx context.Context, arg db.GetTeamMemberParams) (db.TeamMember, error)
	ListTeamsForUser(ctx context.Context, userID uuid.UUID) ([]db.ListTeamsForUserRow, error)
	ListTeamMembers(ctx context.Context, teamID uuid.UUID) ([]db.ListTeamMembersRow, error)
	RemoveTeamMember(ctx context.Context, teamID, userID uuid.UUID) (int64, error)
}

// InvitationStore covers invitations and joining a team through one.
// AcceptInvitation is transactional, for the same reason CreateTeamWithOwner is.
type InvitationStore interface {
	CreateInvitation(ctx context.Context, arg db.CreateInvitationParams) (db.TeamInvitation, error)
	GetInvitationByID(ctx context.Context, id uuid.UUID) (db.TeamInvitation, error)
	ListPendingInvitationsForEmail(ctx context.Context, email string) ([]db.ListPendingInvitationsForEmailRow, error)
	ListPendingInvitationsForTeam(ctx context.Context, teamID uuid.UUID) ([]db.ListPendingInvitationsForTeamRow, error)
	UpdateInvitationStatus(ctx context.Context, arg db.UpdateInvitationStatusParams) (db.TeamInvitation, error)
	DeleteInvitation(ctx context.Context, id uuid.UUID) error
	IsEmailTeamMember(ctx context.Context, arg db.IsEmailTeamMemberParams) (bool, error)

	AcceptInvitation(ctx context.Context, invitationID, userID uuid.UUID) (db.TeamInvitation, error)
}

// ProjectStore covers projects and who may reach them. ReplaceProjectAccess
// and SetMemberProjectAccess are transactional.
type ProjectStore interface {
	CreateProject(ctx context.Context, arg db.CreateProjectParams) (db.Project, error)
	GetProjectForUser(ctx context.Context, arg db.GetProjectForUserParams) (db.GetProjectForUserRow, error)
	UpdateProjectName(ctx context.Context, arg db.UpdateProjectNameParams) (db.Project, error)
	DeleteProject(ctx context.Context, id uuid.UUID) error

	ListAccessibleProjects(ctx context.Context, arg db.ListAccessibleProjectsParams) ([]db.ListAccessibleProjectsRow, error)
	ListTeamProjectIDs(ctx context.Context, teamID uuid.UUID) ([]uuid.UUID, error)
	BulkUpdateProjectOrder(ctx context.Context, arg db.BulkUpdateProjectOrderParams) error

	CountProjectsInTeam(ctx context.Context, arg db.CountProjectsInTeamParams) (int64, error)
	CountTeamMembersInList(ctx context.Context, arg db.CountTeamMembersInListParams) (int64, error)
	ListProjectAccessUsers(ctx context.Context, projectID uuid.UUID) ([]db.ListProjectAccessUsersRow, error)
	ListUserAccessibleProjectIDs(ctx context.Context, arg db.ListUserAccessibleProjectIDsParams) ([]uuid.UUID, error)

	ReplaceProjectAccess(ctx context.Context, projectID uuid.UUID, userIDs []uuid.UUID) error
	SetMemberProjectAccess(ctx context.Context, teamID, userID uuid.UUID, allProjects bool, projectIDs []uuid.UUID) (db.TeamMember, error)
}

// The real store must satisfy Store.
var _ Store = (*db.Store)(nil)
