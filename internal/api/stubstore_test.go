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

// unimplementedTeamStore fills in the TeamStore half of Store for tests that
// only exercise auth. Team behaviour is covered by the database-backed tests in
// teams_test.go, where transactions and cascades are real.
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
