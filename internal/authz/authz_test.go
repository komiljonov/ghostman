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
	team      db.Team
	teamErr   error
	member    db.TeamMember
	memberErr error
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
