package api

import (
	"errors"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/komiljonov/ghostman/internal/authz"
	"github.com/komiljonov/ghostman/internal/db"
)

type invitationRequest struct {
	Email string `json:"email"`
}

// invitationResponse is returned when an invitation is created.
type invitationResponse struct {
	ID        string    `json:"id"`
	TeamID    string    `json:"team_id"`
	Email     string    `json:"email"`
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"created_at"`
}

// teamInvitationResponse is one row of a team's pending invitation list. The
// team is implied by the request path, so it is not repeated here.
type teamInvitationResponse struct {
	ID        string    `json:"id"`
	Email     string    `json:"email"`
	CreatedAt time.Time `json:"created_at"`
}

// invitationTeamRef and invitationInviterRef identify the team and the person
// who invited you, for someone who cannot yet see either.
type invitationTeamRef struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type invitationInviterRef struct {
	Name  string `json:"name"`
	Email string `json:"email"`
}

// myInvitationResponse is one row of the caller's own pending invitations.
type myInvitationResponse struct {
	ID        string               `json:"id"`
	Team      invitationTeamRef    `json:"team"`
	InvitedBy invitationInviterRef `json:"invited_by"`
	CreatedAt time.Time            `json:"created_at"`
}

// acceptInvitationResponse tells the client which team it just joined.
type acceptInvitationResponse struct {
	ID     string            `json:"id"`
	Status string            `json:"status"`
	Team   invitationTeamRef `json:"team"`
}

type rejectInvitationResponse struct {
	ID     string `json:"id"`
	Status string `json:"status"`
}

// handleCreateInvitation invites an email address to a team. Owner only.
func (a *api) handleCreateInvitation(w http.ResponseWriter, r *http.Request) {
	user, ok := a.authenticatedUser(w, r)
	if !ok {
		return
	}

	teamID, ok := a.pathUUID(w, r, "team_id")
	if !ok {
		return
	}

	if _, err := a.authz.RequireTeamOwner(r.Context(), teamID, user.ID); err != nil {
		a.writeAuthzError(w, r, err)
		return
	}

	var req invitationRequest
	if err := readJSON(w, r, &req); err != nil {
		a.writeBodyError(w, r, err)
		return
	}

	email := normalizeEmail(req.Email)
	if err := validateEmail(email); err != nil {
		a.writeError(w, r, http.StatusBadRequest, codeBadRequest, err.Error())
		return
	}

	if email == user.Email {
		a.writeError(w, r, http.StatusConflict, codeConflict, messageCannotInviteSelf)
		return
	}

	// The invitee need not have an account: an invitation to an unregistered
	// address simply waits until that person registers.
	isMember, err := a.store.IsEmailTeamMember(r.Context(), db.IsEmailTeamMemberParams{
		TeamID: teamID,
		Email:  email,
	})
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	if isMember {
		a.writeError(w, r, http.StatusConflict, codeConflict, messageAlreadyMember)
		return
	}

	invitation, err := a.store.CreateInvitation(r.Context(), db.CreateInvitationParams{
		TeamID:    teamID,
		Email:     email,
		InvitedBy: user.ID,
	})
	if err != nil {
		// The partial unique index is what detects a duplicate pending
		// invitation; checking first would race with a concurrent invite.
		if isUniqueViolation(err) {
			a.writeError(w, r, http.StatusConflict, codeConflict, messageInvitationPending)
			return
		}
		a.serverError(w, r, err)
		return
	}

	a.writeJSON(w, r, http.StatusCreated, invitationResponse{
		ID:        invitation.ID.String(),
		TeamID:    invitation.TeamID.String(),
		Email:     invitation.Email,
		Status:    invitation.Status,
		CreatedAt: invitation.CreatedAt,
	})
}

// handleListTeamInvitations lists a team's pending invitations. Owner only.
func (a *api) handleListTeamInvitations(w http.ResponseWriter, r *http.Request) {
	user, ok := a.authenticatedUser(w, r)
	if !ok {
		return
	}

	teamID, ok := a.pathUUID(w, r, "team_id")
	if !ok {
		return
	}

	if _, err := a.authz.RequireTeamOwner(r.Context(), teamID, user.ID); err != nil {
		a.writeAuthzError(w, r, err)
		return
	}

	rows, err := a.store.ListPendingInvitationsForTeam(r.Context(), teamID)
	if err != nil {
		a.serverError(w, r, err)
		return
	}

	invitations := make([]teamInvitationResponse, 0, len(rows))
	for _, row := range rows {
		invitations = append(invitations, teamInvitationResponse{
			ID:        row.ID.String(),
			Email:     row.Email,
			CreatedAt: row.CreatedAt,
		})
	}

	a.writeJSON(w, r, http.StatusOK, invitations)
}

// handleListMyInvitations lists the pending invitations addressed to the
// caller's email.
func (a *api) handleListMyInvitations(w http.ResponseWriter, r *http.Request) {
	user, ok := a.authenticatedUser(w, r)
	if !ok {
		return
	}

	rows, err := a.store.ListPendingInvitationsForEmail(r.Context(), user.Email)
	if err != nil {
		a.serverError(w, r, err)
		return
	}

	invitations := make([]myInvitationResponse, 0, len(rows))
	for _, row := range rows {
		invitations = append(invitations, myInvitationResponse{
			ID: row.ID.String(),
			Team: invitationTeamRef{
				ID:   row.TeamID.String(),
				Name: row.TeamName,
			},
			InvitedBy: invitationInviterRef{
				Name:  row.InviterName,
				Email: row.InviterEmail,
			},
			CreatedAt: row.CreatedAt,
		})
	}

	a.writeJSON(w, r, http.StatusOK, invitations)
}

// handleAcceptInvitation accepts an invitation and joins the team.
func (a *api) handleAcceptInvitation(w http.ResponseWriter, r *http.Request) {
	user, invitation, ok := a.pendingInvitationForCaller(w, r)
	if !ok {
		return
	}

	accepted, err := a.store.AcceptInvitation(r.Context(), invitation.ID, user.ID)
	if err != nil {
		// Lost a race with another accept or reject: the guarded update
		// matched nothing.
		if errors.Is(err, pgx.ErrNoRows) {
			a.writeError(w, r, http.StatusConflict, codeConflict, messageInvitationAnswered)
			return
		}
		a.serverError(w, r, err)
		return
	}

	team, err := a.store.GetTeamByID(r.Context(), accepted.TeamID)
	if err != nil {
		a.serverError(w, r, err)
		return
	}

	a.writeJSON(w, r, http.StatusOK, acceptInvitationResponse{
		ID:     accepted.ID.String(),
		Status: accepted.Status,
		Team: invitationTeamRef{
			ID:   team.ID.String(),
			Name: team.Name,
		},
	})
}

// handleRejectInvitation declines an invitation.
func (a *api) handleRejectInvitation(w http.ResponseWriter, r *http.Request) {
	_, invitation, ok := a.pendingInvitationForCaller(w, r)
	if !ok {
		return
	}

	rejected, err := a.store.UpdateInvitationStatus(r.Context(), db.UpdateInvitationStatusParams{
		ID:     invitation.ID,
		Status: db.InvitationRejected,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			a.writeError(w, r, http.StatusConflict, codeConflict, messageInvitationAnswered)
			return
		}
		a.serverError(w, r, err)
		return
	}

	a.writeJSON(w, r, http.StatusOK, rejectInvitationResponse{
		ID:     rejected.ID.String(),
		Status: rejected.Status,
	})
}

// handleDeleteInvitation revokes a pending invitation. Owner only, and a
// non-owner gets 404 rather than 403: an invitation is not theirs to see.
func (a *api) handleDeleteInvitation(w http.ResponseWriter, r *http.Request) {
	user, ok := a.authenticatedUser(w, r)
	if !ok {
		return
	}

	invitationID, ok := a.pathUUID(w, r, "id")
	if !ok {
		return
	}

	invitation, err := a.store.GetInvitationByID(r.Context(), invitationID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			a.writeError(w, r, http.StatusNotFound, codeNotFound, messageInvitationNotFound)
			return
		}
		a.serverError(w, r, err)
		return
	}

	if _, err := a.authz.RequireTeamOwner(r.Context(), invitation.TeamID, user.ID); err != nil {
		// Both "not a member" and "not the owner" collapse to 404 here: only
		// the owner has any business knowing this invitation exists.
		if errors.Is(err, authz.ErrNotMember) || errors.Is(err, authz.ErrNotOwner) {
			a.writeError(w, r, http.StatusNotFound, codeNotFound, messageInvitationNotFound)
			return
		}
		a.serverError(w, r, err)
		return
	}

	if invitation.Status != db.InvitationPending {
		a.writeError(w, r, http.StatusConflict, codeConflict, messageInvitationAnswered)
		return
	}

	if err := a.store.DeleteInvitation(r.Context(), invitationID); err != nil {
		a.serverError(w, r, err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// pendingInvitationForCaller loads the invitation named in the path and checks
// that it is addressed to the caller and still open. It writes the error
// response itself when either check fails.
//
// An invitation addressed to somebody else is reported as missing: the caller
// must not learn that it exists.
func (a *api) pendingInvitationForCaller(w http.ResponseWriter, r *http.Request) (AuthUser, db.TeamInvitation, bool) {
	user, ok := a.authenticatedUser(w, r)
	if !ok {
		return AuthUser{}, db.TeamInvitation{}, false
	}

	invitationID, ok := a.pathUUID(w, r, "id")
	if !ok {
		return AuthUser{}, db.TeamInvitation{}, false
	}

	invitation, err := a.store.GetInvitationByID(r.Context(), invitationID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			a.writeError(w, r, http.StatusNotFound, codeNotFound, messageInvitationNotFound)
			return AuthUser{}, db.TeamInvitation{}, false
		}
		a.serverError(w, r, err)
		return AuthUser{}, db.TeamInvitation{}, false
	}

	if invitation.Email != user.Email {
		a.writeError(w, r, http.StatusNotFound, codeNotFound, messageInvitationNotFound)
		return AuthUser{}, db.TeamInvitation{}, false
	}

	if invitation.Status != db.InvitationPending {
		a.writeError(w, r, http.StatusConflict, codeConflict, messageInvitationAnswered)
		return AuthUser{}, db.TeamInvitation{}, false
	}

	return user, invitation, true
}
