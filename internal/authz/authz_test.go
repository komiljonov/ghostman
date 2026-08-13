package authz

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/komiljonov/ghostman/internal/db"
)

// stubStore returns fixed rows, so these tests pin the decision logic rather
// than the SQL. The queries themselves are exercised by the database-backed
// tests in internal/api.
type stubStore struct {
	team       db.Team
	teamErr    error
	member     db.TeamMember
	memberErr  error
	project    db.GetProjectForUserRow
	projectErr error
}

func (s stubStore) GetTeamByID(_ context.Context, _ uuid.UUID) (db.Team, error) {
	if s.teamErr != nil {
		return db.Team{}, s.teamErr
	}
	return s.team, nil
}

func (s stubStore) GetTeamMember(_ context.Context, _ db.GetTeamMemberParams) (db.TeamMember, error) {
	if s.memberErr != nil {
		return db.TeamMember{}, s.memberErr
	}
	return s.member, nil
}

func (s stubStore) GetProjectForUser(_ context.Context, _ db.GetProjectForUserParams) (db.GetProjectForUserRow, error) {
	if s.projectErr != nil {
		return db.GetProjectForUserRow{}, s.projectErr
	}
	return s.project, nil
}

func TestRequireTeamMember(t *testing.T) {
	teamID, userID := uuid.New(), uuid.New()

	t.Run("member", func(t *testing.T) {
		checker := New(stubStore{member: db.TeamMember{TeamID: teamID, UserID: userID}})

		member, err := checker.RequireTeamMember(t.Context(), teamID, userID)
		if err != nil {
			t.Fatalf("RequireTeamMember() error: %v", err)
		}
		if member.UserID != userID {
			t.Errorf("user_id = %s, want %s", member.UserID, userID)
		}
	})

	t.Run("outsider", func(t *testing.T) {
		checker := New(stubStore{memberErr: pgx.ErrNoRows})

		_, err := checker.RequireTeamMember(t.Context(), teamID, userID)
		if !errors.Is(err, ErrNotMember) {
			t.Fatalf("RequireTeamMember() error = %v, want %v", err, ErrNotMember)
		}
	})

	t.Run("database failure is not a permission answer", func(t *testing.T) {
		boom := errors.New("connection reset")
		checker := New(stubStore{memberErr: boom})

		_, err := checker.RequireTeamMember(t.Context(), teamID, userID)
		if errors.Is(err, ErrNotMember) {
			t.Error("a database failure was reported as ErrNotMember, which would answer 404")
		}
		if !errors.Is(err, boom) {
			t.Errorf("RequireTeamMember() error = %v, want it to wrap %v", err, boom)
		}
	})
}

func TestRequireTeamOwner(t *testing.T) {
	teamID, ownerID, memberID := uuid.New(), uuid.New(), uuid.New()

	t.Run("owner", func(t *testing.T) {
		checker := New(stubStore{
			member: db.TeamMember{TeamID: teamID, UserID: ownerID},
			team:   db.Team{ID: teamID, OwnerID: ownerID},
		})

		team, err := checker.RequireTeamOwner(t.Context(), teamID, ownerID)
		if err != nil {
			t.Fatalf("RequireTeamOwner() error: %v", err)
		}
		if team.ID != teamID {
			t.Errorf("team id = %s, want %s", team.ID, teamID)
		}
	})

	t.Run("member but not owner", func(t *testing.T) {
		checker := New(stubStore{
			member: db.TeamMember{TeamID: teamID, UserID: memberID},
			team:   db.Team{ID: teamID, OwnerID: ownerID},
		})

		// A member already knows the team exists, so this is a 403 case.
		_, err := checker.RequireTeamOwner(t.Context(), teamID, memberID)
		if !errors.Is(err, ErrNotOwner) {
			t.Fatalf("RequireTeamOwner() error = %v, want %v", err, ErrNotOwner)
		}
	})

	t.Run("outsider", func(t *testing.T) {
		checker := New(stubStore{
			memberErr: pgx.ErrNoRows,
			team:      db.Team{ID: teamID, OwnerID: ownerID},
		})

		// Membership is checked first, so an outsider never learns whether the
		// team exists: ErrNotMember maps to 404, not 403.
		_, err := checker.RequireTeamOwner(t.Context(), teamID, memberID)
		if !errors.Is(err, ErrNotMember) {
			t.Fatalf("RequireTeamOwner() error = %v, want %v", err, ErrNotMember)
		}
	})

	t.Run("missing team", func(t *testing.T) {
		checker := New(stubStore{
			member:  db.TeamMember{TeamID: teamID, UserID: ownerID},
			teamErr: pgx.ErrNoRows,
		})

		_, err := checker.RequireTeamOwner(t.Context(), teamID, ownerID)
		if !errors.Is(err, ErrNotMember) {
			t.Fatalf("RequireTeamOwner() error = %v, want %v", err, ErrNotMember)
		}
	})
}

// The access rule itself lives in SQL and is covered against a real database in
// internal/api. What is pinned here is the decision the Checker layers on top:
// no row means 404, and access without ownership means 403.
func TestRequireProjectAccess(t *testing.T) {
	projectID, teamOwnerID, projectOwnerID, viewerID := uuid.New(), uuid.New(), uuid.New(), uuid.New()

	row := db.GetProjectForUserRow{
		Project:     db.Project{ID: projectID, OwnerID: projectOwnerID},
		TeamOwnerID: teamOwnerID,
	}

	t.Run("no row means no access", func(t *testing.T) {
		checker := New(stubStore{projectErr: pgx.ErrNoRows})

		_, err := checker.RequireProjectAccess(t.Context(), projectID, viewerID)
		if !errors.Is(err, ErrNoProjectAccess) {
			t.Fatalf("RequireProjectAccess() error = %v, want %v", err, ErrNoProjectAccess)
		}
	})

	t.Run("database failure is not a permission answer", func(t *testing.T) {
		boom := errors.New("connection reset")
		checker := New(stubStore{projectErr: boom})

		_, err := checker.RequireProjectAccess(t.Context(), projectID, viewerID)
		if errors.Is(err, ErrNoProjectAccess) {
			t.Error("a database failure was reported as ErrNoProjectAccess, which would answer 404")
		}
		if !errors.Is(err, boom) {
			t.Errorf("error = %v, want it to wrap %v", err, boom)
		}
	})

	tests := []struct {
		name          string
		userID        uuid.UUID
		wantCanManage bool
	}{
		{name: "team owner manages", userID: teamOwnerID, wantCanManage: true},
		{name: "project owner manages", userID: projectOwnerID, wantCanManage: true},
		{name: "viewer does not", userID: viewerID, wantCanManage: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			checker := New(stubStore{project: row})

			access, err := checker.RequireProjectAccess(t.Context(), projectID, tt.userID)
			if err != nil {
				t.Fatalf("RequireProjectAccess() error: %v", err)
			}
			if access.CanManage != tt.wantCanManage {
				t.Errorf("CanManage = %t, want %t", access.CanManage, tt.wantCanManage)
			}
			if access.Project.ID != projectID {
				t.Errorf("project id = %s, want %s", access.Project.ID, projectID)
			}
		})
	}
}

func TestRequireProjectManage(t *testing.T) {
	projectID, teamOwnerID, projectOwnerID, viewerID := uuid.New(), uuid.New(), uuid.New(), uuid.New()

	row := db.GetProjectForUserRow{
		Project:     db.Project{ID: projectID, OwnerID: projectOwnerID},
		TeamOwnerID: teamOwnerID,
	}

	t.Run("viewer is forbidden, not hidden", func(t *testing.T) {
		checker := New(stubStore{project: row})

		// The viewer can see the project, so this is 403 rather than 404.
		_, err := checker.RequireProjectManage(t.Context(), projectID, viewerID)
		if !errors.Is(err, ErrNotProjectManager) {
			t.Fatalf("RequireProjectManage() error = %v, want %v", err, ErrNotProjectManager)
		}
	})

	t.Run("no access stays hidden", func(t *testing.T) {
		checker := New(stubStore{projectErr: pgx.ErrNoRows})

		_, err := checker.RequireProjectManage(t.Context(), projectID, viewerID)
		if !errors.Is(err, ErrNoProjectAccess) {
			t.Fatalf("RequireProjectManage() error = %v, want %v", err, ErrNoProjectAccess)
		}
	})

	for _, userID := range []uuid.UUID{teamOwnerID, projectOwnerID} {
		checker := New(stubStore{project: row})

		project, err := checker.RequireProjectManage(t.Context(), projectID, userID)
		if err != nil {
			t.Fatalf("RequireProjectManage() error for %s: %v", userID, err)
		}
		if project.ID != projectID {
			t.Errorf("project id = %s, want %s", project.ID, projectID)
		}
	}
}
