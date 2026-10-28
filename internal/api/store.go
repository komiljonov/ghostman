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
	FolderStore
	EnvironmentStore
	RequestStore
	WebhookStore
	ProxyStore
	ExampleStore
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

// FolderStore covers the folder tree of a project. CreateFolderInProject checks
// the parent; MoveFolder and ReorderFolders are transactional.
type FolderStore interface {
	GetFolderByID(ctx context.Context, id uuid.UUID) (db.Folder, error)
	ListFoldersByProject(ctx context.Context, projectID uuid.UUID) ([]db.Folder, error)
	UpdateFolder(ctx context.Context, arg db.UpdateFolderParams) (db.Folder, error)
	DeleteFolder(ctx context.Context, id uuid.UUID) error

	CreateFolderInProject(ctx context.Context, arg db.CreateFolderParams) (db.Folder, error)
	MoveFolder(ctx context.Context, id uuid.UUID, parentID *uuid.UUID, sortOrder *int32) (db.Folder, error)
	ReorderFolders(ctx context.Context, projectID uuid.UUID, parentID *uuid.UUID, folderIDs []uuid.UUID, checkSiblings func([]uuid.UUID) error) error
}

// EnvironmentStore covers environments and their variables. The reorders are
// transactional.
type EnvironmentStore interface {
	CreateEnvironment(ctx context.Context, arg db.CreateEnvironmentParams) (db.Environment, error)
	GetEnvironmentByID(ctx context.Context, id uuid.UUID) (db.Environment, error)
	UpdateEnvironmentName(ctx context.Context, arg db.UpdateEnvironmentNameParams) (db.Environment, error)
	DeleteEnvironment(ctx context.Context, id uuid.UUID) error
	ListEnvironmentsByProject(ctx context.Context, projectID uuid.UUID) ([]db.Environment, error)
	ReorderEnvironments(ctx context.Context, projectID uuid.UUID, environmentIDs []uuid.UUID, check func([]uuid.UUID) error) error

	CreateVariable(ctx context.Context, arg db.CreateVariableParams) (db.EnvironmentVariable, error)
	GetVariableByID(ctx context.Context, id uuid.UUID) (db.EnvironmentVariable, error)
	UpdateVariable(ctx context.Context, arg db.UpdateVariableParams) (db.EnvironmentVariable, error)
	DeleteVariable(ctx context.Context, id uuid.UUID) error
	ListVariablesByEnvironment(ctx context.Context, environmentID uuid.UUID) ([]db.EnvironmentVariable, error)
	ReorderVariables(ctx context.Context, environmentID uuid.UUID, variableIDs []uuid.UUID, check func([]uuid.UUID) error) error
}

// RequestStore covers requests. CreateRequestInProject checks the folder;
// MoveRequest and ReorderRequests are transactional under the project lock.
type RequestStore interface {
	GetRequestByID(ctx context.Context, id uuid.UUID) (db.Request, error)
	ListRequestsByProject(ctx context.Context, projectID uuid.UUID) ([]db.ListRequestsByProjectRow, error)
	UpdateRequest(ctx context.Context, arg db.UpdateRequestParams) (db.Request, error)
	DeleteRequest(ctx context.Context, id uuid.UUID) error

	CreateRequestInProject(ctx context.Context, arg db.CreateRequestParams) (db.Request, error)
	MoveRequest(ctx context.Context, id uuid.UUID, folderID *uuid.UUID, sortOrder *int32) (db.Request, error)
	ReorderRequests(ctx context.Context, projectID uuid.UUID, folderID *uuid.UUID, requestIDs []uuid.UUID, checkSiblings func([]uuid.UUID) error) error
}

// WebhookStore covers webhook endpoints and their captures.
// CreateWebhookEndpointWithQuota and CaptureWebhook are transactional.
type WebhookStore interface {
	CreateWebhookEndpointWithQuota(ctx context.Context, arg db.CreateWebhookEndpointParams, maxEndpoints int64) (db.WebhookEndpoint, error)
	GetWebhookEndpointByID(ctx context.Context, id uuid.UUID) (db.WebhookEndpoint, error)
	GetWebhookEndpointBySlug(ctx context.Context, slug string) (db.WebhookEndpoint, error)
	ListWebhookEndpointsByProject(ctx context.Context, projectID uuid.UUID) ([]db.WebhookEndpoint, error)
	UpdateWebhookEndpoint(ctx context.Context, arg db.UpdateWebhookEndpointParams) (db.WebhookEndpoint, error)
	DeleteWebhookEndpoint(ctx context.Context, id uuid.UUID) error

	CaptureWebhook(ctx context.Context, arg db.InsertWebhookCaptureParams, keep int32) (int64, error)
	ListWebhookCapturesAfter(ctx context.Context, arg db.ListWebhookCapturesAfterParams) ([]db.ListWebhookCapturesAfterRow, error)
	GetWebhookCapture(ctx context.Context, arg db.GetWebhookCaptureParams) (db.WebhookCapture, error)
	DeleteWebhookCaptures(ctx context.Context, endpointID uuid.UUID) error
}

// ProxyStore covers a project's proxies. ReorderProxies is transactional.
type ProxyStore interface {
	CreateProxy(ctx context.Context, arg db.CreateProxyParams) (db.Proxy, error)
	GetProxyByID(ctx context.Context, id uuid.UUID) (db.Proxy, error)
	ListProxiesByProject(ctx context.Context, projectID uuid.UUID) ([]db.Proxy, error)
	UpdateProxy(ctx context.Context, arg db.UpdateProxyParams) (db.Proxy, error)
	DeleteProxy(ctx context.Context, id uuid.UUID) error
	ReorderProxies(ctx context.Context, projectID uuid.UUID, proxyIDs []uuid.UUID, check func([]uuid.UUID) error) error
}

// ExampleStore covers saved response examples. ReorderRequestExamples is
// transactional.
type ExampleStore interface {
	CreateRequestExample(ctx context.Context, arg db.CreateRequestExampleParams) (db.RequestExample, error)
	GetRequestExampleByID(ctx context.Context, id uuid.UUID) (db.RequestExample, error)
	ListRequestExampleSummaries(ctx context.Context, requestID uuid.UUID) ([]db.ListRequestExampleSummariesRow, error)
	CountRequestExamples(ctx context.Context, requestID uuid.UUID) (int64, error)
	UpdateRequestExample(ctx context.Context, arg db.UpdateRequestExampleParams) (db.RequestExample, error)
	DeleteRequestExample(ctx context.Context, id uuid.UUID) error
	ReorderRequestExamples(ctx context.Context, requestID uuid.UUID, exampleIDs []uuid.UUID, check func([]uuid.UUID) error) error
}

// The real store must satisfy Store.
var _ Store = (*db.Store)(nil)
