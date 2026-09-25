package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/komiljonov/ghostman/internal/db"
)

// Folders form a tree inside a project. Anyone who can reach the project may
// create, rename, move, reorder and delete its folders; there is no separate
// management right as there is for projects.

// Folder name bounds, matching the folders_name_length CHECK constraint.
const (
	minFolderNameLength = 1
	maxFolderNameLength = 100
)

// parent_id is decoded raw so that null (the project root) can be told apart
// from a missing field, and a malformed uuid reported by field name.

type folderCreateRequest struct {
	Name     string          `json:"name"`
	ParentID json.RawMessage `json:"parent_id"`
}

type folderRenameRequest struct {
	Name string `json:"name"`
}

type folderMoveRequest struct {
	ParentID  json.RawMessage `json:"parent_id"`
	SortOrder *int32          `json:"sort_order"`
}

// folderOrderRequest names its scope in the body rather than the path because
// the scope's parent may be null (the project root), which has no path form.
type folderOrderRequest struct {
	ProjectID string          `json:"project_id"`
	ParentID  json.RawMessage `json:"parent_id"`
	FolderIDs []string        `json:"folder_ids"`
}

// folderResponse is the full shape, used wherever a single folder is the
// subject of the request. parent_id is null at the project root.
type folderResponse struct {
	ID        string    `json:"id"`
	ProjectID string    `json:"project_id"`
	ParentID  *string   `json:"parent_id"`
	Name      string    `json:"name"`
	SortOrder int32     `json:"sort_order"`
	CreatedAt time.Time `json:"created_at"`
}

// folderSummaryResponse is one entry of a project's flat folder list. The
// project is implied by the request path, so it is not repeated.
type folderSummaryResponse struct {
	ID        string    `json:"id"`
	ParentID  *string   `json:"parent_id"`
	Name      string    `json:"name"`
	SortOrder int32     `json:"sort_order"`
	CreatedAt time.Time `json:"created_at"`
}

// badRequestError marks a validation failure raised inside a store
// transaction, so the handler can answer 400 instead of 500.
type badRequestError struct{ err error }

func (e badRequestError) Error() string { return e.err.Error() }

func newFolderResponse(folder db.Folder) folderResponse {
	return folderResponse{
		ID:        folder.ID.String(),
		ProjectID: folder.ProjectID.String(),
		ParentID:  optionalUUIDString(folder.ParentID),
		Name:      folder.Name,
		SortOrder: folder.SortOrder,
		CreatedAt: folder.CreatedAt,
	}
}

// handleCreateFolder creates a folder at the project root or under a parent in
// the same project, after its future siblings.
func (a *api) handleCreateFolder(w http.ResponseWriter, r *http.Request) {
	user, ok := a.authenticatedUser(w, r)
	if !ok {
		return
	}

	projectID, ok := a.pathUUID(w, r, "project_id")
	if !ok {
		return
	}

	if _, err := a.authz.RequireProjectAccess(r.Context(), projectID, user.ID); err != nil {
		a.writeAuthzError(w, r, err)
		return
	}

	var req folderCreateRequest
	if err := readJSON(w, r, &req); err != nil {
		a.writeError(w, r, http.StatusBadRequest, codeBadRequest, err.Error())
		return
	}

	name, err := validateName(req.Name, minFolderNameLength, maxFolderNameLength)
	if err != nil {
		a.writeError(w, r, http.StatusBadRequest, codeBadRequest, err.Error())
		return
	}

	parentID, err := parseNullableUUID(req.ParentID, "parent_id")
	if err != nil {
		a.writeError(w, r, http.StatusBadRequest, codeBadRequest, err.Error())
		return
	}

	folder, err := a.store.CreateFolderInProject(r.Context(), db.CreateFolderParams{
		ProjectID: projectID,
		ParentID:  parentID,
		Name:      name,
	})
	if err != nil {
		a.writeFolderError(w, r, err)
		return
	}

	a.writeJSON(w, r, http.StatusCreated, newFolderResponse(folder))
}

// handleListFolders returns every folder of the project as one flat list,
// grouped by parent and ordered within each group. The client builds the tree.
func (a *api) handleListFolders(w http.ResponseWriter, r *http.Request) {
	user, ok := a.authenticatedUser(w, r)
	if !ok {
		return
	}

	projectID, ok := a.pathUUID(w, r, "project_id")
	if !ok {
		return
	}

	if _, err := a.authz.RequireProjectAccess(r.Context(), projectID, user.ID); err != nil {
		a.writeAuthzError(w, r, err)
		return
	}

	rows, err := a.store.ListFoldersByProject(r.Context(), projectID)
	if err != nil {
		a.serverError(w, r, err)
		return
	}

	folders := make([]folderSummaryResponse, 0, len(rows))
	for _, folder := range rows {
		folders = append(folders, folderSummaryResponse{
			ID:        folder.ID.String(),
			ParentID:  optionalUUIDString(folder.ParentID),
			Name:      folder.Name,
			SortOrder: folder.SortOrder,
			CreatedAt: folder.CreatedAt,
		})
	}

	a.writeJSON(w, r, http.StatusOK, folders)
}

// handleUpdateFolder renames a folder.
func (a *api) handleUpdateFolder(w http.ResponseWriter, r *http.Request) {
	user, ok := a.authenticatedUser(w, r)
	if !ok {
		return
	}

	folderID, ok := a.pathUUID(w, r, "id")
	if !ok {
		return
	}

	if _, err := a.authz.RequireFolderAccess(r.Context(), folderID, user.ID); err != nil {
		a.writeAuthzError(w, r, err)
		return
	}

	var req folderRenameRequest
	if err := readJSON(w, r, &req); err != nil {
		a.writeError(w, r, http.StatusBadRequest, codeBadRequest, err.Error())
		return
	}

	name, err := validateName(req.Name, minFolderNameLength, maxFolderNameLength)
	if err != nil {
		a.writeError(w, r, http.StatusBadRequest, codeBadRequest, err.Error())
		return
	}

	folder, err := a.store.UpdateFolderName(r.Context(), db.UpdateFolderNameParams{
		ID:   folderID,
		Name: name,
	})
	if err != nil {
		a.writeFolderError(w, r, err)
		return
	}

	a.writeJSON(w, r, http.StatusOK, newFolderResponse(folder))
}

// handleMoveFolder re-parents a folder within its project. parent_id is
// required, with null meaning the project root, so that a forgotten field
// cannot silently move a folder to the root.
func (a *api) handleMoveFolder(w http.ResponseWriter, r *http.Request) {
	user, ok := a.authenticatedUser(w, r)
	if !ok {
		return
	}

	folderID, ok := a.pathUUID(w, r, "id")
	if !ok {
		return
	}

	if _, err := a.authz.RequireFolderAccess(r.Context(), folderID, user.ID); err != nil {
		a.writeAuthzError(w, r, err)
		return
	}

	var req folderMoveRequest
	if err := readJSON(w, r, &req); err != nil {
		a.writeError(w, r, http.StatusBadRequest, codeBadRequest, err.Error())
		return
	}

	if len(req.ParentID) == 0 {
		a.writeError(w, r, http.StatusBadRequest, codeBadRequest,
			"parent_id is required; use null to move to the project root")
		return
	}

	parentID, err := parseNullableUUID(req.ParentID, "parent_id")
	if err != nil {
		a.writeError(w, r, http.StatusBadRequest, codeBadRequest, err.Error())
		return
	}

	if req.SortOrder != nil && *req.SortOrder < 0 {
		a.writeError(w, r, http.StatusBadRequest, codeBadRequest, "sort_order must not be negative")
		return
	}

	folder, err := a.store.MoveFolder(r.Context(), folderID, parentID, req.SortOrder)
	if err != nil {
		a.writeFolderError(w, r, err)
		return
	}

	a.writeJSON(w, r, http.StatusOK, newFolderResponse(folder))
}

// handleReorderFolders rewrites the order of one set of siblings: the children
// of parent_id, or the project's root folders when parent_id is null. The list
// must be exactly that sibling set.
func (a *api) handleReorderFolders(w http.ResponseWriter, r *http.Request) {
	user, ok := a.authenticatedUser(w, r)
	if !ok {
		return
	}

	// The scope is in the body, so it has to be read before the access check.
	var req folderOrderRequest
	if err := readJSON(w, r, &req); err != nil {
		a.writeError(w, r, http.StatusBadRequest, codeBadRequest, err.Error())
		return
	}

	projectID, err := uuid.Parse(req.ProjectID)
	if err != nil {
		a.writeError(w, r, http.StatusBadRequest, codeBadRequest, "project_id must be a valid uuid")
		return
	}

	if _, err = a.authz.RequireProjectAccess(r.Context(), projectID, user.ID); err != nil {
		a.writeAuthzError(w, r, err)
		return
	}

	parentID, err := parseNullableUUID(req.ParentID, "parent_id")
	if err != nil {
		a.writeError(w, r, http.StatusBadRequest, codeBadRequest, err.Error())
		return
	}

	folderIDs, err := parseUUIDs(req.FolderIDs, "folder_ids")
	if err != nil {
		a.writeError(w, r, http.StatusBadRequest, codeBadRequest, err.Error())
		return
	}

	err = a.store.ReorderFolders(r.Context(), projectID, parentID, folderIDs, func(siblings []uuid.UUID) error {
		if setErr := checkSameIDSet(folderIDs, siblings,
			"folder_ids", "a folder with this parent", "folders with this parent"); setErr != nil {
			return badRequestError{err: setErr}
		}
		return nil
	})
	if err != nil {
		a.writeFolderError(w, r, err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// handleDeleteFolder deletes a folder and, through the cascade, its subtree.
func (a *api) handleDeleteFolder(w http.ResponseWriter, r *http.Request) {
	user, ok := a.authenticatedUser(w, r)
	if !ok {
		return
	}

	folderID, ok := a.pathUUID(w, r, "id")
	if !ok {
		return
	}

	if _, err := a.authz.RequireFolderAccess(r.Context(), folderID, user.ID); err != nil {
		a.writeAuthzError(w, r, err)
		return
	}

	if err := a.store.DeleteFolder(r.Context(), folderID); err != nil {
		a.serverError(w, r, err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// writeFolderError maps the failures a folder write can report after the
// access check has passed.
func (a *api) writeFolderError(w http.ResponseWriter, r *http.Request, err error) {
	var badRequest badRequestError

	switch {
	case errors.As(err, &badRequest):
		a.writeError(w, r, http.StatusBadRequest, codeBadRequest, badRequest.Error())

	case errors.Is(err, db.ErrInvalidParentFolder), errors.Is(err, db.ErrFolderCycle):
		a.writeError(w, r, http.StatusBadRequest, codeBadRequest, err.Error())

	case errors.Is(err, pgx.ErrNoRows):
		// Deleted between the access check and the write.
		a.writeError(w, r, http.StatusNotFound, codeNotFound, messageFolderNotFound)

	default:
		a.serverError(w, r, err)
	}
}

// parseNullableUUID reads an optional uuid field: absent or null gives nil.
func parseNullableUUID(raw json.RawMessage, field string) (*uuid.UUID, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}

	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, fmt.Errorf("%s must be a uuid string or null", field)
	}

	id, err := uuid.Parse(value)
	if err != nil {
		return nil, fmt.Errorf("%s must be a valid uuid", field)
	}

	return &id, nil
}

// optionalUUIDString renders a nullable uuid for JSON.
func optionalUUIDString(id *uuid.UUID) *string {
	if id == nil {
		return nil
	}

	value := id.String()
	return &value
}
