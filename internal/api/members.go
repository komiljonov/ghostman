package api

import (
	"net/http"

	"github.com/komiljonov/ghostman/internal/db"
)

// handleDeleteTeamMember covers both removal and leaving, which are the same
// write under different permissions:
//
//   - the owner may remove any member except themselves;
//   - a member may remove themselves (leave);
//   - anything else is reported as missing.
//
// The owner cannot leave, because a team must always have an owner and
// ownership transfer does not exist yet. That is a 400 rather than a 403: the
// request is understood and permitted in principle, but the team would be left
// without an owner.
func (a *api) handleDeleteTeamMember(w http.ResponseWriter, r *http.Request) {
	user, ok := a.authenticatedUser(w, r)
	if !ok {
		return
	}

	teamID, ok := a.pathUUID(w, r, "team_id")
	if !ok {
		return
	}

	targetID, ok := a.pathUUID(w, r, "user_id")
	if !ok {
		return
	}

	// A non-member must not be able to tell an existing team from a missing
	// one, so this comes first.
	if _, err := a.authz.RequireTeamMember(r.Context(), teamID, user.ID); err != nil {
		a.writeAuthzError(w, r, err)
		return
	}

	team, err := a.store.GetTeamByID(r.Context(), teamID)
	if err != nil {
		a.serverError(w, r, err)
		return
	}

	switch {
	case targetID == team.OwnerID:
		a.writeError(w, r, http.StatusBadRequest, codeBadRequest, messageCannotRemoveOwner)
		return

	case team.OwnerID == user.ID:
		// The owner may remove anyone else.

	case targetID == user.ID:
		// A member may leave.

	default:
		// A member trying to remove somebody else learns nothing.
		a.writeError(w, r, http.StatusNotFound, codeNotFound, messageMemberNotFound)
		return
	}

	removed, err := a.store.DeleteTeamMember(r.Context(), db.DeleteTeamMemberParams{
		TeamID: teamID,
		UserID: targetID,
	})
	if err != nil {
		a.serverError(w, r, err)
		return
	}

	if removed == 0 {
		a.writeError(w, r, http.StatusNotFound, codeNotFound, messageMemberNotFound)
		return
	}

	// TODO(step 4): drop this member's project_access rows once that table
	// exists, so leaving a team also revokes per-project access.

	w.WriteHeader(http.StatusNoContent)
}
