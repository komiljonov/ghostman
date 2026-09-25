package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/komiljonov/ghostman/internal/db"
)

// Requests are the leaves of a project's folder tree. Like folders they are
// working material: anyone who can reach the project may change them.
//
// This sub-step exposes a request's name, method, url, folder and position.
// headers, query_params and body exist in the table and are returned read-only
// by GET /requests/{id}; nothing here writes them.

// Bounds matching the requests_name_length and requests_url_length CHECK
// constraints.
const (
	minRequestNameLength = 1
	maxRequestNameLength = 100
	maxRequestURLLength  = 8192
)

type requestCreateRequest struct {
	Name     string          `json:"name"`
	FolderID json.RawMessage `json:"folder_id"`
	Method   *string         `json:"method"`
	URL      *string         `json:"url"`
}

// requestUpdateRequest is a partial update: a missing (or null) field keeps
// its current value. None of these fields can be null in the table, so null
// has no other meaning to carry.
type requestUpdateRequest struct {
	Name   *string `json:"name"`
	Method *string `json:"method"`
	URL    *string `json:"url"`
}

type requestMoveRequest struct {
	FolderID  json.RawMessage `json:"folder_id"`
	SortOrder *int32          `json:"sort_order"`
}

// requestOrderRequest names its scope in the body, like folders/order: the
// folder being ordered may be the project root, which has no path form.
type requestOrderRequest struct {
	ProjectID  string          `json:"project_id"`
	FolderID   json.RawMessage `json:"folder_id"`
	RequestIDs []string        `json:"request_ids"`
}

// requestResponse is a request without its headers, query parameters and
// body. folder_id is null at the project root.
type requestResponse struct {
	ID        string    `json:"id"`
	ProjectID string    `json:"project_id"`
	FolderID  *string   `json:"folder_id"`
	Name      string    `json:"name"`
	Method    string    `json:"method"`
	URL       string    `json:"url"`
	SortOrder int32     `json:"sort_order"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// requestDetailResponse is the full request, returned only by
// GET /requests/{id}.
type requestDetailResponse struct {
	requestResponse

	Headers     json.RawMessage `json:"headers"`
	QueryParams json.RawMessage `json:"query_params"`
	Body        json.RawMessage `json:"body"`
}

func newRequestResponse(request db.Request) requestResponse {
	return requestResponse{
		ID:        request.ID.String(),
		ProjectID: request.ProjectID.String(),
		FolderID:  optionalUUIDString(request.FolderID),
		Name:      request.Name,
		Method:    request.Method,
		URL:       request.Url,
		SortOrder: request.SortOrder,
		CreatedAt: request.CreatedAt,
		UpdatedAt: request.UpdatedAt,
	}
}

// handleCreateRequest creates a request at the project root or in a folder of
// the same project, after its siblings.
func (a *api) handleCreateRequest(w http.ResponseWriter, r *http.Request) {
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

	var req requestCreateRequest
	if err := readJSON(w, r, &req); err != nil {
		a.writeError(w, r, http.StatusBadRequest, codeBadRequest, err.Error())
		return
	}

	params := db.CreateRequestParams{ProjectID: projectID, Method: http.MethodGet}

	name, err := validateName(req.Name, minRequestNameLength, maxRequestNameLength)
	if err != nil {
		a.writeError(w, r, http.StatusBadRequest, codeBadRequest, err.Error())
		return
	}
	params.Name = name

	if params.FolderID, err = parseNullableUUID(req.FolderID, "folder_id"); err != nil {
		a.writeError(w, r, http.StatusBadRequest, codeBadRequest, err.Error())
		return
	}

	if req.Method != nil {
		if err = validateRequestMethod(*req.Method); err != nil {
			a.writeError(w, r, http.StatusBadRequest, codeBadRequest, err.Error())
			return
		}
		params.Method = *req.Method
	}

	if req.URL != nil {
		if err = validateRequestURL(*req.URL); err != nil {
			a.writeError(w, r, http.StatusBadRequest, codeBadRequest, err.Error())
			return
		}
		params.Url = *req.URL
	}

	request, err := a.store.CreateRequestInProject(r.Context(), params)
	if err != nil {
		a.writeRequestError(w, r, err)
		return
	}

	a.writeJSON(w, r, http.StatusCreated, newRequestResponse(request))
}

// handleListRequests returns every request of the project as one flat list,
// grouped by folder and ordered within each group. The client places them in
// the folder tree.
func (a *api) handleListRequests(w http.ResponseWriter, r *http.Request) {
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

	rows, err := a.store.ListRequestsByProject(r.Context(), projectID)
	if err != nil {
		a.serverError(w, r, err)
		return
	}

	requests := make([]requestResponse, 0, len(rows))
	for _, row := range rows {
		requests = append(requests, requestResponse{
			ID:        row.ID.String(),
			ProjectID: row.ProjectID.String(),
			FolderID:  optionalUUIDString(row.FolderID),
			Name:      row.Name,
			Method:    row.Method,
			URL:       row.Url,
			SortOrder: row.SortOrder,
			CreatedAt: row.CreatedAt,
			UpdatedAt: row.UpdatedAt,
		})
	}

	a.writeJSON(w, r, http.StatusOK, requests)
}

// handleGetRequest returns one full request, including its headers, query
// parameters and body.
func (a *api) handleGetRequest(w http.ResponseWriter, r *http.Request) {
	user, ok := a.authenticatedUser(w, r)
	if !ok {
		return
	}

	requestID, ok := a.pathUUID(w, r, "id")
	if !ok {
		return
	}

	request, err := a.authz.RequireRequestAccess(r.Context(), requestID, user.ID)
	if err != nil {
		a.writeAuthzError(w, r, err)
		return
	}

	a.writeJSON(w, r, http.StatusOK, requestDetailResponse{
		requestResponse: newRequestResponse(request),
		Headers:         request.Headers,
		QueryParams:     request.QueryParams,
		Body:            request.Body,
	})
}

// handleUpdateRequest applies a partial update to a request's name, method
// and url.
func (a *api) handleUpdateRequest(w http.ResponseWriter, r *http.Request) {
	user, ok := a.authenticatedUser(w, r)
	if !ok {
		return
	}

	requestID, ok := a.pathUUID(w, r, "id")
	if !ok {
		return
	}

	if _, err := a.authz.RequireRequestAccess(r.Context(), requestID, user.ID); err != nil {
		a.writeAuthzError(w, r, err)
		return
	}

	var req requestUpdateRequest
	if err := readJSON(w, r, &req); err != nil {
		a.writeError(w, r, http.StatusBadRequest, codeBadRequest, err.Error())
		return
	}

	if req.Name == nil && req.Method == nil && req.URL == nil {
		a.writeError(w, r, http.StatusBadRequest, codeBadRequest, "provide at least one of name, method or url")
		return
	}

	params := db.UpdateRequestBasicsParams{ID: requestID, Method: req.Method, Url: req.URL}

	if req.Name != nil {
		name, err := validateName(*req.Name, minRequestNameLength, maxRequestNameLength)
		if err != nil {
			a.writeError(w, r, http.StatusBadRequest, codeBadRequest, err.Error())
			return
		}
		params.Name = &name
	}

	if req.Method != nil {
		if err := validateRequestMethod(*req.Method); err != nil {
			a.writeError(w, r, http.StatusBadRequest, codeBadRequest, err.Error())
			return
		}
	}

	if req.URL != nil {
		if err := validateRequestURL(*req.URL); err != nil {
			a.writeError(w, r, http.StatusBadRequest, codeBadRequest, err.Error())
			return
		}
	}

	request, err := a.store.UpdateRequestBasics(r.Context(), params)
	if err != nil {
		a.writeRequestError(w, r, err)
		return
	}

	a.writeJSON(w, r, http.StatusOK, newRequestResponse(request))
}

// handleMoveRequest puts a request into a folder of the same project, or at
// the root. folder_id is required, with null meaning the root, so that a
// forgotten field cannot silently move a request to the root.
func (a *api) handleMoveRequest(w http.ResponseWriter, r *http.Request) {
	user, ok := a.authenticatedUser(w, r)
	if !ok {
		return
	}

	requestID, ok := a.pathUUID(w, r, "id")
	if !ok {
		return
	}

	if _, err := a.authz.RequireRequestAccess(r.Context(), requestID, user.ID); err != nil {
		a.writeAuthzError(w, r, err)
		return
	}

	var req requestMoveRequest
	if err := readJSON(w, r, &req); err != nil {
		a.writeError(w, r, http.StatusBadRequest, codeBadRequest, err.Error())
		return
	}

	if len(req.FolderID) == 0 {
		a.writeError(w, r, http.StatusBadRequest, codeBadRequest,
			"folder_id is required; use null to move to the project root")
		return
	}

	folderID, err := parseNullableUUID(req.FolderID, "folder_id")
	if err != nil {
		a.writeError(w, r, http.StatusBadRequest, codeBadRequest, err.Error())
		return
	}

	if req.SortOrder != nil && *req.SortOrder < 0 {
		a.writeError(w, r, http.StatusBadRequest, codeBadRequest, "sort_order must not be negative")
		return
	}

	request, err := a.store.MoveRequest(r.Context(), requestID, folderID, req.SortOrder)
	if err != nil {
		a.writeRequestError(w, r, err)
		return
	}

	a.writeJSON(w, r, http.StatusOK, newRequestResponse(request))
}

// handleReorderRequests rewrites the order of the requests in one folder, or
// at the project root when folder_id is null. The list must be exactly that
// set of requests.
func (a *api) handleReorderRequests(w http.ResponseWriter, r *http.Request) {
	user, ok := a.authenticatedUser(w, r)
	if !ok {
		return
	}

	// The scope is in the body, so it has to be read before the access check.
	var req requestOrderRequest
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

	folderID, err := parseNullableUUID(req.FolderID, "folder_id")
	if err != nil {
		a.writeError(w, r, http.StatusBadRequest, codeBadRequest, err.Error())
		return
	}

	requestIDs, err := parseUUIDs(req.RequestIDs, "request_ids")
	if err != nil {
		a.writeError(w, r, http.StatusBadRequest, codeBadRequest, err.Error())
		return
	}

	err = a.store.ReorderRequests(r.Context(), projectID, folderID, requestIDs, func(siblings []uuid.UUID) error {
		if setErr := checkSameIDSet(requestIDs, siblings,
			"request_ids", "a request in this folder", "requests in this folder"); setErr != nil {
			return badRequestError{err: setErr}
		}
		return nil
	})
	if err != nil {
		a.writeRequestError(w, r, err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// handleDeleteRequest deletes one request.
func (a *api) handleDeleteRequest(w http.ResponseWriter, r *http.Request) {
	user, ok := a.authenticatedUser(w, r)
	if !ok {
		return
	}

	requestID, ok := a.pathUUID(w, r, "id")
	if !ok {
		return
	}

	if _, err := a.authz.RequireRequestAccess(r.Context(), requestID, user.ID); err != nil {
		a.writeAuthzError(w, r, err)
		return
	}

	if err := a.store.DeleteRequest(r.Context(), requestID); err != nil {
		a.serverError(w, r, err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// writeRequestError maps the failures a request write can report after the
// access check has passed.
func (a *api) writeRequestError(w http.ResponseWriter, r *http.Request, err error) {
	var badRequest badRequestError

	switch {
	case errors.As(err, &badRequest):
		a.writeError(w, r, http.StatusBadRequest, codeBadRequest, badRequest.Error())

	case errors.Is(err, db.ErrInvalidRequestFolder):
		a.writeError(w, r, http.StatusBadRequest, codeBadRequest, err.Error())

	case errors.Is(err, pgx.ErrNoRows):
		// Deleted between the access check and the write.
		a.writeError(w, r, http.StatusNotFound, codeNotFound, messageRequestNotFound)

	default:
		a.serverError(w, r, err)
	}
}

// validateRequestMethod accepts exactly the methods the requests_method CHECK
// allows, in upper case.
func validateRequestMethod(method string) error {
	if !slices.Contains(db.RequestMethods, method) {
		return fmt.Errorf("method must be one of %s", strings.Join(db.RequestMethods, ", "))
	}
	return nil
}

// validateRequestURL only bounds the length. The url is opaque to the server:
// it may be empty and may hold {{variable}} references.
func validateRequestURL(url string) error {
	if utf8.RuneCountInString(url) > maxRequestURLLength {
		return fmt.Errorf("url must be at most %d characters", maxRequestURLLength)
	}
	return nil
}
