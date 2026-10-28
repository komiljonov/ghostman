package api

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/komiljonov/ghostman/internal/db"
)

// Saved response examples are named example responses attached to a request.
// Anyone who can reach the request's project may manage them. The body is raw
// bytes, carried as base64 in JSON, and capped at db.MaxExampleBodySize
// because examples sync to every member's client.

// Bounds matching the request_examples CHECK constraints, plus the
// content_type cap, which only the API enforces.
const (
	minExampleNameLength  = 1
	maxExampleNameLength  = 100
	minExampleStatus      = 100
	maxExampleStatus      = 599
	defaultExampleStatus  = 200
	maxExampleContentType = 200
)

type exampleCreateRequest struct {
	Name        string          `json:"name"`
	Status      *int32          `json:"status"`
	Headers     json.RawMessage `json:"headers"`
	ContentType *string         `json:"content_type"`
	Body        *string         `json:"body"`
}

// exampleUpdateRequest is a partial update: an absent (or null) field keeps
// its current value. An empty body string clears the body.
type exampleUpdateRequest struct {
	Name        *string         `json:"name"`
	Status      *int32          `json:"status"`
	Headers     json.RawMessage `json:"headers"`
	ContentType *string         `json:"content_type"`
	Body        *string         `json:"body"`
}

type exampleOrderRequest struct {
	RequestID  string   `json:"request_id"`
	ExampleIDs []string `json:"example_ids"`
}

// exampleSummaryResponse is one entry of a request's example list: no headers
// and no body, which the client fetches per example.
type exampleSummaryResponse struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Status      int32     `json:"status"`
	ContentType string    `json:"content_type"`
	BodySize    int32     `json:"body_size"`
	SortOrder   int32     `json:"sort_order"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// exampleResponse is the whole example. Body is a []byte, so encoding/json
// writes it as base64, or null when there is no body.
type exampleResponse struct {
	ID          string          `json:"id"`
	RequestID   string          `json:"request_id"`
	Name        string          `json:"name"`
	Status      int32           `json:"status"`
	Headers     json.RawMessage `json:"headers"`
	ContentType string          `json:"content_type"`
	Body        []byte          `json:"body"`
	BodySize    int32           `json:"body_size"`
	SortOrder   int32           `json:"sort_order"`
	CreatedAt   time.Time       `json:"created_at"`
	UpdatedAt   time.Time       `json:"updated_at"`
}

func newExampleResponse(example db.RequestExample) exampleResponse {
	return exampleResponse{
		ID:          example.ID.String(),
		RequestID:   example.RequestID.String(),
		Name:        example.Name,
		Status:      example.Status,
		Headers:     example.Headers,
		ContentType: example.ContentType,
		Body:        example.Body,
		BodySize:    example.BodySize,
		SortOrder:   example.SortOrder,
		CreatedAt:   example.CreatedAt,
		UpdatedAt:   example.UpdatedAt,
	}
}

// handleCreateExample adds an example at the end of the request's list.
func (a *api) handleCreateExample(w http.ResponseWriter, r *http.Request) {
	user, ok := a.authenticatedUser(w, r)
	if !ok {
		return
	}

	requestID, ok := a.pathUUID(w, r, "request_id")
	if !ok {
		return
	}

	if _, err := a.authz.RequireRequestAccess(r.Context(), requestID, user.ID); err != nil {
		a.writeAuthzError(w, r, err)
		return
	}

	var req exampleCreateRequest
	if err := readJSON(w, r, &req); err != nil {
		a.writeBodyError(w, r, err)
		return
	}

	params := db.CreateRequestExampleParams{
		RequestID: requestID,
		Status:    defaultExampleStatus,
		Headers:   json.RawMessage(`[]`),
	}

	var err error
	if params.Name, err = validateName(req.Name, minExampleNameLength, maxExampleNameLength); err != nil {
		a.writeError(w, r, http.StatusBadRequest, codeBadRequest, err.Error())
		return
	}
	if req.Status != nil {
		if err = validateExampleStatus(*req.Status); err != nil {
			a.writeError(w, r, http.StatusBadRequest, codeBadRequest, err.Error())
			return
		}
		params.Status = *req.Status
	}
	headers, err := parseKeyValueRows(req.Headers, "headers")
	if err != nil {
		a.writeError(w, r, http.StatusBadRequest, codeBadRequest, err.Error())
		return
	}
	if headers != nil {
		params.Headers = headers
	}
	if req.ContentType != nil {
		if params.ContentType, err = validateExampleContentType(*req.ContentType); err != nil {
			a.writeError(w, r, http.StatusBadRequest, codeBadRequest, err.Error())
			return
		}
	}
	if req.Body != nil {
		if params.Body, err = decodeExampleBody(*req.Body); err != nil {
			a.writeError(w, r, http.StatusBadRequest, codeBadRequest, err.Error())
			return
		}
		params.BodySize = int32(len(params.Body)) //nolint:gosec // at most db.MaxExampleBodySize
	}

	example, err := a.store.CreateRequestExample(r.Context(), params)
	if err != nil {
		a.writeExampleError(w, r, err)
		return
	}

	a.writeJSON(w, r, http.StatusCreated, newExampleResponse(example))
}

// handleListExamples returns the request's examples in order, as summaries.
func (a *api) handleListExamples(w http.ResponseWriter, r *http.Request) {
	user, ok := a.authenticatedUser(w, r)
	if !ok {
		return
	}

	requestID, ok := a.pathUUID(w, r, "request_id")
	if !ok {
		return
	}

	if _, err := a.authz.RequireRequestAccess(r.Context(), requestID, user.ID); err != nil {
		a.writeAuthzError(w, r, err)
		return
	}

	rows, err := a.store.ListRequestExampleSummaries(r.Context(), requestID)
	if err != nil {
		a.serverError(w, r, err)
		return
	}

	examples := make([]exampleSummaryResponse, 0, len(rows))
	for _, row := range rows {
		examples = append(examples, exampleSummaryResponse{
			ID:          row.ID.String(),
			Name:        row.Name,
			Status:      row.Status,
			ContentType: row.ContentType,
			BodySize:    row.BodySize,
			SortOrder:   row.SortOrder,
			UpdatedAt:   row.UpdatedAt,
		})
	}

	a.writeJSON(w, r, http.StatusOK, examples)
}

// handleGetExample returns one whole example, body included.
func (a *api) handleGetExample(w http.ResponseWriter, r *http.Request) {
	example, ok := a.exampleFromPath(w, r)
	if !ok {
		return
	}

	a.writeJSON(w, r, http.StatusOK, newExampleResponse(example))
}

// handleUpdateExample applies a partial update to an example.
func (a *api) handleUpdateExample(w http.ResponseWriter, r *http.Request) {
	example, ok := a.exampleFromPath(w, r)
	if !ok {
		return
	}

	var req exampleUpdateRequest
	if err := readJSON(w, r, &req); err != nil {
		a.writeBodyError(w, r, err)
		return
	}

	headers, err := parseKeyValueRows(req.Headers, "headers")
	if err != nil {
		a.writeError(w, r, http.StatusBadRequest, codeBadRequest, err.Error())
		return
	}

	if req.Name == nil && req.Status == nil && headers == nil && req.ContentType == nil && req.Body == nil {
		a.writeError(w, r, http.StatusBadRequest, codeBadRequest,
			"provide at least one of name, status, headers, content_type or body")
		return
	}

	params := db.UpdateRequestExampleParams{ID: example.ID, Headers: headers}

	if req.Name != nil {
		name, nameErr := validateName(*req.Name, minExampleNameLength, maxExampleNameLength)
		if nameErr != nil {
			a.writeError(w, r, http.StatusBadRequest, codeBadRequest, nameErr.Error())
			return
		}
		params.Name = &name
	}
	if req.Status != nil {
		if err = validateExampleStatus(*req.Status); err != nil {
			a.writeError(w, r, http.StatusBadRequest, codeBadRequest, err.Error())
			return
		}
		params.Status = req.Status
	}
	if req.ContentType != nil {
		contentType, typeErr := validateExampleContentType(*req.ContentType)
		if typeErr != nil {
			a.writeError(w, r, http.StatusBadRequest, codeBadRequest, typeErr.Error())
			return
		}
		params.ContentType = &contentType
	}
	if req.Body != nil {
		if params.Body, err = decodeExampleBody(*req.Body); err != nil {
			a.writeError(w, r, http.StatusBadRequest, codeBadRequest, err.Error())
			return
		}
		params.SetBody = true
		params.BodySize = int32(len(params.Body)) //nolint:gosec // at most db.MaxExampleBodySize
	}

	updated, err := a.store.UpdateRequestExample(r.Context(), params)
	if err != nil {
		a.writeExampleError(w, r, err)
		return
	}

	a.writeJSON(w, r, http.StatusOK, newExampleResponse(updated))
}

// handleDeleteExample deletes an example.
func (a *api) handleDeleteExample(w http.ResponseWriter, r *http.Request) {
	example, ok := a.exampleFromPath(w, r)
	if !ok {
		return
	}

	if err := a.store.DeleteRequestExample(r.Context(), example.ID); err != nil {
		a.serverError(w, r, err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// handleReorderExamples rewrites the request's example order. The list must
// be exactly the request's examples.
func (a *api) handleReorderExamples(w http.ResponseWriter, r *http.Request) {
	user, ok := a.authenticatedUser(w, r)
	if !ok {
		return
	}

	// The scope is in the body, so it has to be read before the access check.
	var req exampleOrderRequest
	if err := readJSON(w, r, &req); err != nil {
		a.writeBodyError(w, r, err)
		return
	}

	requestID, err := uuid.Parse(req.RequestID)
	if err != nil {
		a.writeError(w, r, http.StatusBadRequest, codeBadRequest, "request_id must be a valid uuid")
		return
	}

	if _, err = a.authz.RequireRequestAccess(r.Context(), requestID, user.ID); err != nil {
		a.writeAuthzError(w, r, err)
		return
	}

	exampleIDs, err := parseUUIDs(req.ExampleIDs, "example_ids")
	if err != nil {
		a.writeError(w, r, http.StatusBadRequest, codeBadRequest, err.Error())
		return
	}

	err = a.store.ReorderRequestExamples(r.Context(), requestID, exampleIDs, func(current []uuid.UUID) error {
		if setErr := checkSameIDSet(exampleIDs, current,
			"example_ids", "an example of this request", "examples of this request"); setErr != nil {
			return badRequestError{err: setErr}
		}
		return nil
	})
	if err != nil {
		a.writeExampleError(w, r, err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// exampleFromPath authenticates, parses {id} and checks access. On failure it
// has already written the response.
func (a *api) exampleFromPath(w http.ResponseWriter, r *http.Request) (db.RequestExample, bool) {
	user, ok := a.authenticatedUser(w, r)
	if !ok {
		return db.RequestExample{}, false
	}

	exampleID, ok := a.pathUUID(w, r, "id")
	if !ok {
		return db.RequestExample{}, false
	}

	example, err := a.authz.RequireExampleAccess(r.Context(), exampleID, user.ID)
	if err != nil {
		a.writeAuthzError(w, r, err)
		return db.RequestExample{}, false
	}

	return example, true
}

func validateExampleStatus(status int32) error {
	if status < minExampleStatus || status > maxExampleStatus {
		return fmt.Errorf("status must be between %d and %d", minExampleStatus, maxExampleStatus)
	}
	return nil
}

// validateExampleContentType trims the content type; empty is allowed and
// means "not set".
func validateExampleContentType(raw string) (string, error) {
	return validateTrimmedLength("content_type", raw, 0, maxExampleContentType)
}

// decodeExampleBody decodes a base64 body. An empty string is no body, which
// is stored as NULL.
func decodeExampleBody(raw string) ([]byte, error) {
	if raw == "" {
		return nil, nil
	}

	// Refusing by encoded length first avoids decoding a string that is too
	// big anyway. DecodedLen over-counts by up to two padding bytes.
	if base64.StdEncoding.DecodedLen(len(raw)) > db.MaxExampleBodySize+2 {
		return nil, errors.New(messageExampleBodyTooLarge)
	}

	body, err := base64.StdEncoding.DecodeString(raw)
	if err != nil {
		return nil, errors.New("body must be base64 (standard alphabet, padded)")
	}
	if len(body) > db.MaxExampleBodySize {
		return nil, errors.New(messageExampleBodyTooLarge)
	}

	return body, nil
}

// writeExampleError maps the failures an example write can report after the
// access check has passed.
func (a *api) writeExampleError(w http.ResponseWriter, r *http.Request, err error) {
	var badRequest badRequestError

	switch {
	case errors.As(err, &badRequest):
		a.writeError(w, r, http.StatusBadRequest, codeBadRequest, badRequest.Error())

	case db.IsExampleRequestGone(err):
		// The request was deleted between the access check and the insert.
		a.writeError(w, r, http.StatusNotFound, codeNotFound, messageRequestNotFound)

	case errors.Is(err, pgx.ErrNoRows):
		// Deleted between the access check and the write.
		a.writeError(w, r, http.StatusNotFound, codeNotFound, messageExampleNotFound)

	default:
		a.serverError(w, r, err)
	}
}
