package api

import (
	"net/http"
	"testing"

	"github.com/google/uuid"
)

// memberPath builds the flat-per-parent path used to remove a membership.
func memberPath(teamID, userID string) string {
	return "/api/v1/teams/" + teamID + "/members/" + userID
}

func TestOwnerCannotBeRemoved(t *testing.T) {
	f := newTeamsFixture(t)
	owner := f.newUser(t, "owner-guard@example.com")
	member := f.newUser(t, "owner-guard-member@example.com")
	team := f.createTeam(t, owner.Token, "Ghostman")

	f.addMember(t, uuid.MustParse(team.ID), uuid.MustParse(member.User.ID))

	t.Run("owner cannot leave", func(t *testing.T) {
		// A team must always have an owner, and ownership transfer does not
		// exist yet, so this is a 400 rather than a permission answer.
		rec := do(t, f.handler, http.MethodDelete, memberPath(team.ID, owner.User.ID), "", owner.Token)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want %d (body: %s)", rec.Code, http.StatusBadRequest, rec.Body)
		}

		if env := decodeEnvelope(t, rec); env.Error.Message != messageCannotRemoveOwner {
			t.Errorf("message = %q, want %q", env.Error.Message, messageCannotRemoveOwner)
		}
	})

	t.Run("member cannot remove the owner", func(t *testing.T) {
		rec := do(t, f.handler, http.MethodDelete, memberPath(team.ID, owner.User.ID), "", member.Token)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want %d (body: %s)", rec.Code, http.StatusBadRequest, rec.Body)
		}
	})

	// The owner is still there.
	if emails := f.teamMemberEmails(t, owner.Token, team.ID); len(emails) != 2 {
		t.Errorf("team has %d members, want 2: %v", len(emails), emails)
	}
}

func TestMemberCanLeave(t *testing.T) {
	f := newTeamsFixture(t)
	owner := f.newUser(t, "leave-owner@example.com")
	member := f.newUser(t, "leave-member@example.com")
	team := f.createTeam(t, owner.Token, "Ghostman")

	f.addMember(t, uuid.MustParse(team.ID), uuid.MustParse(member.User.ID))

	rec := do(t, f.handler, http.MethodDelete, memberPath(team.ID, member.User.ID), "", member.Token)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d (body: %s)", rec.Code, http.StatusNoContent, rec.Body)
	}

	if count := f.countTeamMembers(t, uuid.MustParse(team.ID)); count != 1 {
		t.Errorf("team_members rows = %d, want 1", count)
	}

	// Having left, the team is invisible again.
	if getRec := do(t, f.handler, http.MethodGet, "/api/v1/teams/"+team.ID, "", member.Token); getRec.Code != http.StatusNotFound {
		t.Errorf("GET after leaving: status = %d, want %d", getRec.Code, http.StatusNotFound)
	}
}

func TestOwnerCanRemoveMember(t *testing.T) {
	f := newTeamsFixture(t)
	owner := f.newUser(t, "remove-owner@example.com")
	member := f.newUser(t, "remove-member@example.com")
	team := f.createTeam(t, owner.Token, "Ghostman")

	f.addMember(t, uuid.MustParse(team.ID), uuid.MustParse(member.User.ID))

	rec := do(t, f.handler, http.MethodDelete, memberPath(team.ID, member.User.ID), "", owner.Token)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d (body: %s)", rec.Code, http.StatusNoContent, rec.Body)
	}

	if emails := f.teamMemberEmails(t, owner.Token, team.ID); len(emails) != 1 {
		t.Errorf("team has %d members, want 1: %v", len(emails), emails)
	}

	// Removing again finds nothing to remove.
	if again := do(t, f.handler, http.MethodDelete, memberPath(team.ID, member.User.ID), "", owner.Token); again.Code != http.StatusNotFound {
		t.Errorf("second removal: status = %d, want %d", again.Code, http.StatusNotFound)
	}
}

func TestMemberCannotRemoveOtherMember(t *testing.T) {
	f := newTeamsFixture(t)
	owner := f.newUser(t, "peer-owner@example.com")
	first := f.newUser(t, "peer-first@example.com")
	second := f.newUser(t, "peer-second@example.com")
	outsider := f.newUser(t, "peer-outsider@example.com")
	team := f.createTeam(t, owner.Token, "Ghostman")

	f.addMember(t, uuid.MustParse(team.ID), uuid.MustParse(first.User.ID))
	f.addMember(t, uuid.MustParse(team.ID), uuid.MustParse(second.User.ID))

	t.Run("peer", func(t *testing.T) {
		rec := do(t, f.handler, http.MethodDelete, memberPath(team.ID, second.User.ID), "", first.Token)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want %d (body: %s)", rec.Code, http.StatusNotFound, rec.Body)
		}
	})

	t.Run("outsider", func(t *testing.T) {
		// Not a member of the team at all: the team itself looks missing.
		rec := do(t, f.handler, http.MethodDelete, memberPath(team.ID, second.User.ID), "", outsider.Token)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want %d (body: %s)", rec.Code, http.StatusNotFound, rec.Body)
		}

		if env := decodeEnvelope(t, rec); env.Error.Message != messageTeamNotFound {
			t.Errorf("message = %q, want %q", env.Error.Message, messageTeamNotFound)
		}
	})

	t.Run("outsider cannot leave a team they are not in", func(t *testing.T) {
		rec := do(t, f.handler, http.MethodDelete, memberPath(team.ID, outsider.User.ID), "", outsider.Token)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want %d (body: %s)", rec.Code, http.StatusNotFound, rec.Body)
		}
	})

	// Nobody was removed.
	if count := f.countTeamMembers(t, uuid.MustParse(team.ID)); count != 3 {
		t.Errorf("team_members rows = %d, want 3", count)
	}
}

func TestRemoveMemberFromUnknownTeam(t *testing.T) {
	f := newTeamsFixture(t)
	user := f.newUser(t, "unknown-team@example.com")

	rec := do(t, f.handler, http.MethodDelete, memberPath(uuid.New().String(), user.User.ID), "", user.Token)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d (body: %s)", rec.Code, http.StatusNotFound, rec.Body)
	}
}
