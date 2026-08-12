package api

import (
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/komiljonov/ghostman/internal/db"
)

// invite sends an invitation and returns the recorder, leaving status checks to
// the caller: several tests are about the rejected cases.
func (f teamsFixture) invite(t *testing.T, token, teamID, email string) (invitationResponse, int, string) {
	t.Helper()

	body, err := json.Marshal(invitationRequest{Email: email})
	if err != nil {
		t.Fatalf("marshalling invitation request: %v", err)
	}

	rec := do(t, f.handler, http.MethodPost, "/api/v1/teams/"+teamID+"/invitations", string(body), token)

	var invitation invitationResponse
	if rec.Code == http.StatusCreated {
		if err := json.Unmarshal(rec.Body.Bytes(), &invitation); err != nil {
			t.Fatalf("decoding invitation response: %v", err)
		}
	}

	return invitation, rec.Code, rec.Body.String()
}

// mustInvite fails unless the invitation is created.
func (f teamsFixture) mustInvite(t *testing.T, token, teamID, email string) invitationResponse {
	t.Helper()

	invitation, status, body := f.invite(t, token, teamID, email)
	if status != http.StatusCreated {
		t.Fatalf("inviting %s: status = %d, want %d (body: %s)", email, status, http.StatusCreated, body)
	}

	return invitation
}

func (f teamsFixture) myInvitations(t *testing.T, token string) []myInvitationResponse {
	t.Helper()

	rec := do(t, f.handler, http.MethodGet, "/api/v1/me/invitations", "", token)
	if rec.Code != http.StatusOK {
		t.Fatalf("listing my invitations: status = %d, want %d (body: %s)", rec.Code, http.StatusOK, rec.Body)
	}

	var invitations []myInvitationResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &invitations); err != nil {
		t.Fatalf("decoding invitation list: %v", err)
	}

	return invitations
}

func (f teamsFixture) teamMemberEmails(t *testing.T, token, teamID string) []string {
	t.Helper()

	rec := do(t, f.handler, http.MethodGet, "/api/v1/teams/"+teamID, "", token)
	if rec.Code != http.StatusOK {
		t.Fatalf("reading team: status = %d, want %d (body: %s)", rec.Code, http.StatusOK, rec.Body)
	}

	var detail teamDetailResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &detail); err != nil {
		t.Fatalf("decoding team detail: %v", err)
	}

	emails := make([]string, 0, len(detail.Members))
	for _, member := range detail.Members {
		emails = append(emails, member.Email)
	}

	return emails
}

// TestInviteUnregisteredEmailThroughAccept is the whole intended flow: invite
// somebody who has no account yet, let them register, and let them join.
func TestInviteUnregisteredEmailThroughAccept(t *testing.T) {
	f := newTeamsFixture(t)
	owner := f.newUser(t, "flow-owner@example.com")
	team := f.createTeam(t, owner.Token, "Ghostman")

	// The invitee does not exist yet, which is allowed.
	invitation := f.mustInvite(t, owner.Token, team.ID, "flow-invitee@example.com")

	if invitation.Status != db.InvitationPending {
		t.Errorf("status = %q, want %q", invitation.Status, db.InvitationPending)
	}
	if invitation.Email != "flow-invitee@example.com" {
		t.Errorf("email = %q, want %q", invitation.Email, "flow-invitee@example.com")
	}
	if invitation.TeamID != team.ID {
		t.Errorf("team_id = %q, want %q", invitation.TeamID, team.ID)
	}

	// Registering afterwards must surface the waiting invitation.
	invitee := f.newUser(t, "flow-invitee@example.com")

	pending := f.myInvitations(t, invitee.Token)
	if len(pending) != 1 {
		t.Fatalf("invitee sees %d invitations, want 1", len(pending))
	}

	if pending[0].ID != invitation.ID {
		t.Errorf("invitation id = %q, want %q", pending[0].ID, invitation.ID)
	}
	if pending[0].Team.Name != "Ghostman" {
		t.Errorf("team name = %q, want %q", pending[0].Team.Name, "Ghostman")
	}
	if pending[0].Team.ID != team.ID {
		t.Errorf("team id = %q, want %q", pending[0].Team.ID, team.ID)
	}
	if pending[0].InvitedBy.Email != "flow-owner@example.com" {
		t.Errorf("inviter email = %q, want %q", pending[0].InvitedBy.Email, "flow-owner@example.com")
	}

	rec := do(t, f.handler, http.MethodPost, "/api/v1/invitations/"+invitation.ID+"/accept", "", invitee.Token)
	if rec.Code != http.StatusOK {
		t.Fatalf("accept: status = %d, want %d (body: %s)", rec.Code, http.StatusOK, rec.Body)
	}

	var accepted acceptInvitationResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &accepted); err != nil {
		t.Fatalf("decoding accept response: %v", err)
	}
	if accepted.Status != db.InvitationAccepted {
		t.Errorf("status = %q, want %q", accepted.Status, db.InvitationAccepted)
	}
	if accepted.Team.Name != "Ghostman" {
		t.Errorf("team name = %q, want %q", accepted.Team.Name, "Ghostman")
	}

	// The membership half of the transaction must have landed too.
	emails := f.teamMemberEmails(t, owner.Token, team.ID)
	if len(emails) != 2 {
		t.Fatalf("team has %d members, want 2: %v", len(emails), emails)
	}
	if !slices.Contains(emails, "flow-invitee@example.com") {
		t.Errorf("members = %v, want it to contain the invitee", emails)
	}

	// The invitation is no longer pending on either side.
	if remaining := f.myInvitations(t, invitee.Token); len(remaining) != 0 {
		t.Errorf("invitee still sees %d pending invitations, want 0", len(remaining))
	}

	listRec := do(t, f.handler, http.MethodGet, "/api/v1/teams/"+team.ID+"/invitations", "", owner.Token)
	if body := strings.TrimSpace(listRec.Body.String()); body != "[]" {
		t.Errorf("team invitations = %s, want []", body)
	}
}

func TestInviteRejectedCases(t *testing.T) {
	f := newTeamsFixture(t)
	owner := f.newUser(t, "reject-owner@example.com")
	member := f.newUser(t, "reject-member@example.com")
	team := f.createTeam(t, owner.Token, "Ghostman")

	f.addMember(t, uuid.MustParse(team.ID), uuid.MustParse(member.User.ID))

	t.Run("duplicate pending", func(t *testing.T) {
		f.mustInvite(t, owner.Token, team.ID, "dupe@example.com")

		_, status, body := f.invite(t, owner.Token, team.ID, "dupe@example.com")
		if status != http.StatusConflict {
			t.Fatalf("status = %d, want %d (body: %s)", status, http.StatusConflict, body)
		}
	})

	t.Run("already a member", func(t *testing.T) {
		_, status, body := f.invite(t, owner.Token, team.ID, "reject-member@example.com")
		if status != http.StatusConflict {
			t.Fatalf("status = %d, want %d (body: %s)", status, http.StatusConflict, body)
		}
	})

	t.Run("own email", func(t *testing.T) {
		_, status, body := f.invite(t, owner.Token, team.ID, "reject-owner@example.com")
		if status != http.StatusConflict {
			t.Fatalf("status = %d, want %d (body: %s)", status, http.StatusConflict, body)
		}
	})

	t.Run("case insensitive duplicate", func(t *testing.T) {
		f.mustInvite(t, owner.Token, team.ID, "mixed@example.com")

		// Normalisation happens before the uniqueness check.
		_, status, body := f.invite(t, owner.Token, team.ID, "MIXED@Example.com")
		if status != http.StatusConflict {
			t.Fatalf("status = %d, want %d (body: %s)", status, http.StatusConflict, body)
		}
	})

	t.Run("malformed email", func(t *testing.T) {
		_, status, body := f.invite(t, owner.Token, team.ID, "not-an-email")
		if status != http.StatusBadRequest {
			t.Fatalf("status = %d, want %d (body: %s)", status, http.StatusBadRequest, body)
		}
	})
}

func TestInvitationResponsePermissions(t *testing.T) {
	f := newTeamsFixture(t)
	owner := f.newUser(t, "perm-owner@example.com")
	invitee := f.newUser(t, "perm-invitee@example.com")
	stranger := f.newUser(t, "perm-stranger@example.com")
	team := f.createTeam(t, owner.Token, "Ghostman")

	invitation := f.mustInvite(t, owner.Token, team.ID, "perm-invitee@example.com")

	for _, action := range []string{"accept", "reject"} {
		t.Run("wrong invitee cannot "+action, func(t *testing.T) {
			path := "/api/v1/invitations/" + invitation.ID + "/" + action

			// An invitation addressed to someone else must look missing.
			rec := do(t, f.handler, http.MethodPost, path, "", stranger.Token)
			if rec.Code != http.StatusNotFound {
				t.Fatalf("status = %d, want %d (body: %s)", rec.Code, http.StatusNotFound, rec.Body)
			}

			if env := decodeEnvelope(t, rec); env.Error.Message != messageInvitationNotFound {
				t.Errorf("message = %q, want %q", env.Error.Message, messageInvitationNotFound)
			}
		})
	}

	t.Run("accept twice", func(t *testing.T) {
		path := "/api/v1/invitations/" + invitation.ID + "/accept"

		if rec := do(t, f.handler, http.MethodPost, path, "", invitee.Token); rec.Code != http.StatusOK {
			t.Fatalf("first accept: status = %d, want %d (body: %s)", rec.Code, http.StatusOK, rec.Body)
		}

		rec := do(t, f.handler, http.MethodPost, path, "", invitee.Token)
		if rec.Code != http.StatusConflict {
			t.Fatalf("second accept: status = %d, want %d (body: %s)", rec.Code, http.StatusConflict, rec.Body)
		}

		if env := decodeEnvelope(t, rec); env.Error.Code != codeConflict {
			t.Errorf("code = %q, want %q", env.Error.Code, codeConflict)
		}
	})

	t.Run("reject after accept", func(t *testing.T) {
		rec := do(t, f.handler, http.MethodPost, "/api/v1/invitations/"+invitation.ID+"/reject", "", invitee.Token)
		if rec.Code != http.StatusConflict {
			t.Fatalf("status = %d, want %d (body: %s)", rec.Code, http.StatusConflict, rec.Body)
		}
	})

	t.Run("unknown invitation", func(t *testing.T) {
		rec := do(t, f.handler, http.MethodPost, "/api/v1/invitations/"+uuid.New().String()+"/accept", "", invitee.Token)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
		}
	})
}

func TestTeamInvitationListPermissions(t *testing.T) {
	f := newTeamsFixture(t)
	owner := f.newUser(t, "list-perm-owner@example.com")
	member := f.newUser(t, "list-perm-member@example.com")
	outsider := f.newUser(t, "list-perm-outsider@example.com")
	team := f.createTeam(t, owner.Token, "Ghostman")

	f.addMember(t, uuid.MustParse(team.ID), uuid.MustParse(member.User.ID))
	invitation := f.mustInvite(t, owner.Token, team.ID, "list-perm-invitee@example.com")

	path := "/api/v1/teams/" + team.ID + "/invitations"

	t.Run("member is forbidden", func(t *testing.T) {
		// A member knows the team exists, so this is 403 rather than 404.
		rec := do(t, f.handler, http.MethodGet, path, "", member.Token)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("status = %d, want %d (body: %s)", rec.Code, http.StatusForbidden, rec.Body)
		}
	})

	t.Run("outsider sees nothing", func(t *testing.T) {
		rec := do(t, f.handler, http.MethodGet, path, "", outsider.Token)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want %d (body: %s)", rec.Code, http.StatusNotFound, rec.Body)
		}
	})

	t.Run("member cannot invite", func(t *testing.T) {
		_, status, body := f.invite(t, member.Token, team.ID, "nope@example.com")
		if status != http.StatusForbidden {
			t.Fatalf("status = %d, want %d (body: %s)", status, http.StatusForbidden, body)
		}
	})

	t.Run("outsider cannot invite", func(t *testing.T) {
		_, status, body := f.invite(t, outsider.Token, team.ID, "nope@example.com")
		if status != http.StatusNotFound {
			t.Fatalf("status = %d, want %d (body: %s)", status, http.StatusNotFound, body)
		}
	})

	t.Run("owner sees the pending invitation", func(t *testing.T) {
		rec := do(t, f.handler, http.MethodGet, path, "", owner.Token)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
		}

		var invitations []teamInvitationResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &invitations); err != nil {
			t.Fatalf("decoding invitation list: %v", err)
		}

		if len(invitations) != 1 || invitations[0].ID != invitation.ID {
			t.Fatalf("owner sees %+v, want the one pending invitation", invitations)
		}
	})
}

func TestRevokeInvitation(t *testing.T) {
	f := newTeamsFixture(t)
	owner := f.newUser(t, "revoke-owner@example.com")
	member := f.newUser(t, "revoke-member@example.com")
	invitee := f.newUser(t, "revoke-invitee@example.com")
	team := f.createTeam(t, owner.Token, "Ghostman")

	f.addMember(t, uuid.MustParse(team.ID), uuid.MustParse(member.User.ID))
	invitation := f.mustInvite(t, owner.Token, team.ID, "revoke-invitee@example.com")
	path := "/api/v1/invitations/" + invitation.ID

	t.Run("non-owner member sees nothing", func(t *testing.T) {
		// 404 rather than 403: an invitation is not a member's to know about.
		rec := do(t, f.handler, http.MethodDelete, path, "", member.Token)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want %d (body: %s)", rec.Code, http.StatusNotFound, rec.Body)
		}
	})

	t.Run("invitee cannot revoke", func(t *testing.T) {
		rec := do(t, f.handler, http.MethodDelete, path, "", invitee.Token)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want %d (body: %s)", rec.Code, http.StatusNotFound, rec.Body)
		}
	})

	t.Run("owner revokes", func(t *testing.T) {
		rec := do(t, f.handler, http.MethodDelete, path, "", owner.Token)
		if rec.Code != http.StatusNoContent {
			t.Fatalf("status = %d, want %d (body: %s)", rec.Code, http.StatusNoContent, rec.Body)
		}

		if pending := f.myInvitations(t, invitee.Token); len(pending) != 0 {
			t.Errorf("invitee still sees %d invitations, want 0", len(pending))
		}
	})

	t.Run("revoking twice", func(t *testing.T) {
		rec := do(t, f.handler, http.MethodDelete, path, "", owner.Token)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
		}
	})

	t.Run("answered invitation cannot be revoked", func(t *testing.T) {
		answered := f.mustInvite(t, owner.Token, team.ID, "revoke-invitee@example.com")

		acceptRec := do(t, f.handler, http.MethodPost,
			"/api/v1/invitations/"+answered.ID+"/accept", "", invitee.Token)
		if acceptRec.Code != http.StatusOK {
			t.Fatalf("accept: status = %d, want %d", acceptRec.Code, http.StatusOK)
		}

		rec := do(t, f.handler, http.MethodDelete, "/api/v1/invitations/"+answered.ID, "", owner.Token)
		if rec.Code != http.StatusConflict {
			t.Fatalf("status = %d, want %d (body: %s)", rec.Code, http.StatusConflict, rec.Body)
		}
	})
}

func TestReinviteAfterRejectOrLeave(t *testing.T) {
	f := newTeamsFixture(t)
	owner := f.newUser(t, "reinvite-owner@example.com")
	invitee := f.newUser(t, "reinvite-invitee@example.com")
	team := f.createTeam(t, owner.Token, "Ghostman")

	// Reject, then re-invite: the unique index is partial on pending, so the
	// rejected row must not block a second invitation.
	first := f.mustInvite(t, owner.Token, team.ID, "reinvite-invitee@example.com")

	rejectRec := do(t, f.handler, http.MethodPost, "/api/v1/invitations/"+first.ID+"/reject", "", invitee.Token)
	if rejectRec.Code != http.StatusOK {
		t.Fatalf("reject: status = %d, want %d (body: %s)", rejectRec.Code, http.StatusOK, rejectRec.Body)
	}

	var rejected rejectInvitationResponse
	if err := json.Unmarshal(rejectRec.Body.Bytes(), &rejected); err != nil {
		t.Fatalf("decoding reject response: %v", err)
	}
	if rejected.Status != db.InvitationRejected {
		t.Errorf("status = %q, want %q", rejected.Status, db.InvitationRejected)
	}

	second := f.mustInvite(t, owner.Token, team.ID, "reinvite-invitee@example.com")

	acceptRec := do(t, f.handler, http.MethodPost, "/api/v1/invitations/"+second.ID+"/accept", "", invitee.Token)
	if acceptRec.Code != http.StatusOK {
		t.Fatalf("accept: status = %d, want %d (body: %s)", acceptRec.Code, http.StatusOK, acceptRec.Body)
	}

	// Now leave, and be invited back again.
	leaveRec := do(t, f.handler, http.MethodDelete,
		"/api/v1/teams/"+team.ID+"/members/"+invitee.User.ID, "", invitee.Token)
	if leaveRec.Code != http.StatusNoContent {
		t.Fatalf("leave: status = %d, want %d (body: %s)", leaveRec.Code, http.StatusNoContent, leaveRec.Body)
	}

	third := f.mustInvite(t, owner.Token, team.ID, "reinvite-invitee@example.com")

	rejoinRec := do(t, f.handler, http.MethodPost, "/api/v1/invitations/"+third.ID+"/accept", "", invitee.Token)
	if rejoinRec.Code != http.StatusOK {
		t.Fatalf("rejoin: status = %d, want %d (body: %s)", rejoinRec.Code, http.StatusOK, rejoinRec.Body)
	}

	if emails := f.teamMemberEmails(t, owner.Token, team.ID); len(emails) != 2 {
		t.Errorf("team has %d members, want 2: %v", len(emails), emails)
	}
}

func TestInvitationPathValidation(t *testing.T) {
	f := newTeamsFixture(t)
	owner := f.newUser(t, "invite-path@example.com")
	team := f.createTeam(t, owner.Token, "Ghostman")

	tests := []struct {
		name   string
		method string
		path   string
		body   string
	}{
		{name: "create", method: http.MethodPost, path: "/api/v1/teams/not-a-uuid/invitations", body: `{"email":"x@example.com"}`},
		{name: "list", method: http.MethodGet, path: "/api/v1/teams/not-a-uuid/invitations"},
		{name: "accept", method: http.MethodPost, path: "/api/v1/invitations/not-a-uuid/accept"},
		{name: "reject", method: http.MethodPost, path: "/api/v1/invitations/not-a-uuid/reject"},
		{name: "revoke", method: http.MethodDelete, path: "/api/v1/invitations/not-a-uuid"},
		{name: "remove member team", method: http.MethodDelete, path: "/api/v1/teams/not-a-uuid/members/" + owner.User.ID},
		{name: "remove member user", method: http.MethodDelete, path: "/api/v1/teams/" + team.ID + "/members/not-a-uuid"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := do(t, f.handler, tt.method, tt.path, tt.body, owner.Token)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want %d (body: %s)", rec.Code, http.StatusBadRequest, rec.Body)
			}
		})
	}
}

func TestInvitationRoutesRequireAuth(t *testing.T) {
	f := newTeamsFixture(t)

	id := uuid.New().String()
	tests := []struct {
		name   string
		method string
		path   string
	}{
		{name: "create", method: http.MethodPost, path: "/api/v1/teams/" + id + "/invitations"},
		{name: "list team", method: http.MethodGet, path: "/api/v1/teams/" + id + "/invitations"},
		{name: "list mine", method: http.MethodGet, path: "/api/v1/me/invitations"},
		{name: "accept", method: http.MethodPost, path: "/api/v1/invitations/" + id + "/accept"},
		{name: "reject", method: http.MethodPost, path: "/api/v1/invitations/" + id + "/reject"},
		{name: "revoke", method: http.MethodDelete, path: "/api/v1/invitations/" + id},
		{name: "remove member", method: http.MethodDelete, path: "/api/v1/teams/" + id + "/members/" + id},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := do(t, f.handler, tt.method, tt.path, "", "")
			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want %d (body: %s)", rec.Code, http.StatusUnauthorized, rec.Body)
			}
		})
	}
}
