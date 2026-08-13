package api

import (
	"context"
	"errors"

	"github.com/google/uuid"

	"github.com/komiljonov/ghostman/internal/db"
)

// errStubbed is returned by every method of unimplementedTeamStore. A test that
// trips one of these is using the wrong fixture, not exercising a real path.
var errStubbed = errors.New("this store method is stubbed out; use the database-backed tests instead")

// unimplementedTeamStore fills in the non-auth half of Store for tests that
// only exercise auth. Team and invitation behaviour is covered by the
// database-backed tests, where transactions, cascades and partial unique
// indexes are real.
type unimplementedTeamStore struct{}

func (unimplementedTeamStore) CreateTeamWithOwner(_ context.Context, _ string, _ uuid.UUID) (db.Team, error) {
	return db.Team{}, errStubbed
}

func (unimplementedTeamStore) GetTeamByID(_ context.Context, _ uuid.UUID) (db.Team, error) {
	return db.Team{}, errStubbed
}

func (unimplementedTeamStore) UpdateTeamName(_ context.Context, _ db.UpdateTeamNameParams) (db.Team, error) {
	return db.Team{}, errStubbed
}

func (unimplementedTeamStore) DeleteTeam(_ context.Context, _ uuid.UUID) error {
	return errStubbed
}

func (unimplementedTeamStore) GetTeamMember(_ context.Context, _ db.GetTeamMemberParams) (db.TeamMember, error) {
	return db.TeamMember{}, errStubbed
}

func (unimplementedTeamStore) ListTeamsForUser(_ context.Context, _ uuid.UUID) ([]db.ListTeamsForUserRow, error) {
	return nil, errStubbed
}

func (unimplementedTeamStore) ListTeamMembers(_ context.Context, _ uuid.UUID) ([]db.ListTeamMembersRow, error) {
	return nil, errStubbed
}

func (unimplementedTeamStore) DeleteTeamMember(_ context.Context, _ db.DeleteTeamMemberParams) (int64, error) {
	return 0, errStubbed
}

func (unimplementedTeamStore) CreateInvitation(_ context.Context, _ db.CreateInvitationParams) (db.TeamInvitation, error) {
	return db.TeamInvitation{}, errStubbed
}

func (unimplementedTeamStore) GetInvitationByID(_ context.Context, _ uuid.UUID) (db.TeamInvitation, error) {
	return db.TeamInvitation{}, errStubbed
}

func (unimplementedTeamStore) ListPendingInvitationsForEmail(_ context.Context, _ string) ([]db.ListPendingInvitationsForEmailRow, error) {
	return nil, errStubbed
}

func (unimplementedTeamStore) ListPendingInvitationsForTeam(_ context.Context, _ uuid.UUID) ([]db.ListPendingInvitationsForTeamRow, error) {
	return nil, errStubbed
}

func (unimplementedTeamStore) UpdateInvitationStatus(_ context.Context, _ db.UpdateInvitationStatusParams) (db.TeamInvitation, error) {
	return db.TeamInvitation{}, errStubbed
}

func (unimplementedTeamStore) DeleteInvitation(_ context.Context, _ uuid.UUID) error {
	return errStubbed
}

func (unimplementedTeamStore) IsEmailTeamMember(_ context.Context, _ db.IsEmailTeamMemberParams) (bool, error) {
	return false, errStubbed
}

func (unimplementedTeamStore) AcceptInvitation(_ context.Context, _, _ uuid.UUID) (db.TeamInvitation, error) {
	return db.TeamInvitation{}, errStubbed
}

func (unimplementedTeamStore) CreateProject(_ context.Context, _ db.CreateProjectParams) (db.Project, error) {
	return db.Project{}, errStubbed
}

func (unimplementedTeamStore) GetProjectForUser(_ context.Context, _ db.GetProjectForUserParams) (db.GetProjectForUserRow, error) {
	return db.GetProjectForUserRow{}, errStubbed
}

func (unimplementedTeamStore) UpdateProjectName(_ context.Context, _ db.UpdateProjectNameParams) (db.Project, error) {
	return db.Project{}, errStubbed
}

func (unimplementedTeamStore) DeleteProject(_ context.Context, _ uuid.UUID) error {
	return errStubbed
}

func (unimplementedTeamStore) ListAccessibleProjects(_ context.Context, _ db.ListAccessibleProjectsParams) ([]db.ListAccessibleProjectsRow, error) {
	return nil, errStubbed
}

func (unimplementedTeamStore) ListTeamProjectIDs(_ context.Context, _ uuid.UUID) ([]uuid.UUID, error) {
	return nil, errStubbed
}

func (unimplementedTeamStore) BulkUpdateProjectOrder(_ context.Context, _ db.BulkUpdateProjectOrderParams) error {
	return errStubbed
}

func (unimplementedTeamStore) CountProjectsInTeam(_ context.Context, _ db.CountProjectsInTeamParams) (int64, error) {
	return 0, errStubbed
}

func (unimplementedTeamStore) CountTeamMembersInList(_ context.Context, _ db.CountTeamMembersInListParams) (int64, error) {
	return 0, errStubbed
}

func (unimplementedTeamStore) ListProjectAccessUsers(_ context.Context, _ uuid.UUID) ([]db.ListProjectAccessUsersRow, error) {
	return nil, errStubbed
}

func (unimplementedTeamStore) ListUserAccessibleProjectIDs(_ context.Context, _ db.ListUserAccessibleProjectIDsParams) ([]uuid.UUID, error) {
	return nil, errStubbed
}

func (unimplementedTeamStore) ReplaceProjectAccess(_ context.Context, _ uuid.UUID, _ []uuid.UUID) error {
	return errStubbed
}

func (unimplementedTeamStore) SetMemberProjectAccess(_ context.Context, _, _ uuid.UUID, _ bool, _ []uuid.UUID) (db.TeamMember, error) {
	return db.TeamMember{}, errStubbed
}
