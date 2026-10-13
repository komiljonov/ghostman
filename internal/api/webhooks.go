package api

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"github.com/komiljonov/ghostman/internal/db"
)

// Webhook endpoints are managed per project like folders: anyone who can
// reach the project may create, configure and delete them and read their
// captures. Capturing itself happens on a separate host (capture.go).

const (
	// maxWebhooksPerProject is the endpoint quota per project.
	maxWebhooksPerProject = 20

	// Bounds matching the webhook_endpoints CHECK constraints.
	minWebhookNameLength = 1
	maxWebhookNameLength = 100
	minRespStatus        = 100
	maxRespStatus        = 599
	maxRespBodyLength    = 65536

	// Paging of the capture list.
	defaultCapturePage = 100
	maxCapturePage     = 100

	// slugAttempts bounds retries after a (vanishingly unlikely) collision.
	slugAttempts = 5
)

type webhookCreateRequest struct {
	Name   string  `json:"name"`
	Domain *string `json:"domain"`
}

// webhookUpdateRequest is a partial update: an absent (or null) field keeps
// its current value. resp_headers is decoded raw for the row validator.
type webhookUpdateRequest struct {
	Name        *string         `json:"name"`
	Domain      *string         `json:"domain"`
	RespStatus  *int32          `json:"resp_status"`
	RespHeaders json.RawMessage `json:"resp_headers"`
	RespBody    *string         `json:"resp_body"`
}

// webhookSummaryResponse is one entry of a project's endpoint list.
type webhookSummaryResponse struct {
	ID         string    `json:"id"`
	Name       string    `json:"name"`
	Slug       string    `json:"slug"`
	Domain     string    `json:"domain"`
	RespStatus int32     `json:"resp_status"`
	CreatedAt  time.Time `json:"created_at"`
}

// webhookResponse is the full endpoint, response configuration included.
// domain is returned as stored, even if it is no longer a configured domain:
// the client falls back to the default for display.
type webhookResponse struct {
	ID          string          `json:"id"`
	ProjectID   string          `json:"project_id"`
	Name        string          `json:"name"`
	Slug        string          `json:"slug"`
	Domain      string          `json:"domain"`
	RespStatus  int32           `json:"resp_status"`
	RespHeaders json.RawMessage `json:"resp_headers"`
	RespBody    string          `json:"resp_body"`
	CreatedBy   string          `json:"created_by"`
	CreatedAt   time.Time       `json:"created_at"`
}

// captureSummaryResponse is one entry of the capture poll: no headers, no body.
type captureSummaryResponse struct {
	ID            int64     `json:"id"`
	Method        string    `json:"method"`
	Path          string    `json:"path"`
	Query         string    `json:"query"`
	ContentType   string    `json:"content_type"`
	BodySize      int32     `json:"body_size"`
	BodyTruncated bool      `json:"body_truncated"`
	SourceIP      string    `json:"source_ip"`
	ReceivedAt    time.Time `json:"received_at"`
}

// captureResponse is one full capture. body is []byte, so it is base64 in
// JSON (null when the request had no body).
type captureResponse struct {
	captureSummaryResponse

	Headers json.RawMessage `json:"headers"`
	Body    []byte          `json:"body"`
}

// metaResponse tells clients what this server offers.
type metaResponse struct {
	// HooksDomains is ordered; the first is the default. Empty: capture off.
	HooksDomains []string `json:"hooks_domains"`
}

func newWebhookResponse(endpoint db.WebhookEndpoint) webhookResponse {
	return webhookResponse{
		ID:          endpoint.ID.String(),
		ProjectID:   endpoint.ProjectID.String(),
		Name:        endpoint.Name,
		Slug:        endpoint.Slug,
		Domain:      endpoint.Domain,
		RespStatus:  endpoint.RespStatus,
		RespHeaders: endpoint.RespHeaders,
		RespBody:    endpoint.RespBody,
		CreatedBy:   endpoint.CreatedBy.String(),
		CreatedAt:   endpoint.CreatedAt,
	}
}

// handleMeta is public: the capture domains, in order.
func (a *api) handleMeta(w http.ResponseWriter, r *http.Request) {
	domains := a.hooksDomains
	if domains == nil {
		domains = []string{}
	}
	a.writeJSON(w, r, http.StatusOK, metaResponse{HooksDomains: domains})
}

// handleCreateWebhook creates an endpoint with a random slug.
func (a *api) handleCreateWebhook(w http.ResponseWriter, r *http.Request) {
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

	if len(a.hooksDomains) == 0 {
		a.writeError(w, r, http.StatusConflict, codeConflict, messageHooksNotConfigured)
		return
	}

	var req webhookCreateRequest
	if err := readJSON(w, r, &req); err != nil {
		a.writeBodyError(w, r, err)
		return
	}

	name, err := validateName(req.Name, minWebhookNameLength, maxWebhookNameLength)
	if err != nil {
		a.writeError(w, r, http.StatusBadRequest, codeBadRequest, err.Error())
		return
	}

	domain, err := a.validateWebhookDomain(req.Domain)
	if err != nil {
		a.writeError(w, r, http.StatusBadRequest, codeBadRequest, err.Error())
		return
	}

	for attempt := 1; ; attempt++ {
		slug, slugErr := newWebhookSlug()
		if slugErr != nil {
			a.serverError(w, r, slugErr)
			return
		}

		endpoint, createErr := a.store.CreateWebhookEndpointWithQuota(r.Context(), db.CreateWebhookEndpointParams{
			ProjectID: projectID,
			Name:      name,
			Slug:      slug,
			Domain:    domain,
			CreatedBy: user.ID,
		}, maxWebhooksPerProject)

		switch {
		case createErr == nil:
			a.writeJSON(w, r, http.StatusCreated, newWebhookResponse(endpoint))
			return

		case errors.Is(createErr, db.ErrWebhookQuota):
			a.writeError(w, r, http.StatusConflict, codeConflict,
				fmt.Sprintf("a project can have at most %d webhook endpoints", maxWebhooksPerProject))
			return

		case db.IsWebhookSlugCollision(createErr) && attempt < slugAttempts:
			continue

		default:
			a.serverError(w, r, createErr)
			return
		}
	}
}

// handleListWebhooks lists a project's endpoints, oldest first.
func (a *api) handleListWebhooks(w http.ResponseWriter, r *http.Request) {
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

	rows, err := a.store.ListWebhookEndpointsByProject(r.Context(), projectID)
	if err != nil {
		a.serverError(w, r, err)
		return
	}

	endpoints := make([]webhookSummaryResponse, 0, len(rows))
	for _, endpoint := range rows {
		endpoints = append(endpoints, webhookSummaryResponse{
			ID:         endpoint.ID.String(),
			Name:       endpoint.Name,
			Slug:       endpoint.Slug,
			Domain:     endpoint.Domain,
			RespStatus: endpoint.RespStatus,
			CreatedAt:  endpoint.CreatedAt,
		})
	}

	a.writeJSON(w, r, http.StatusOK, endpoints)
}

// handleGetWebhook returns one endpoint with its response configuration.
func (a *api) handleGetWebhook(w http.ResponseWriter, r *http.Request) {
	endpoint, ok := a.webhookFromPath(w, r)
	if !ok {
		return
	}

	a.writeJSON(w, r, http.StatusOK, newWebhookResponse(endpoint))
}

// handleUpdateWebhook applies a partial update to an endpoint.
func (a *api) handleUpdateWebhook(w http.ResponseWriter, r *http.Request) {
	endpoint, ok := a.webhookFromPath(w, r)
	if !ok {
		return
	}

	var req webhookUpdateRequest
	if err := readJSON(w, r, &req); err != nil {
		a.writeBodyError(w, r, err)
		return
	}

	params := db.UpdateWebhookEndpointParams{ID: endpoint.ID, RespStatus: req.RespStatus, RespBody: req.RespBody}

	var err error
	if params.RespHeaders, err = parseKeyValueRows(req.RespHeaders, "resp_headers"); err != nil {
		a.writeError(w, r, http.StatusBadRequest, codeBadRequest, err.Error())
		return
	}

	if req.Name == nil && req.Domain == nil && req.RespStatus == nil && params.RespHeaders == nil && req.RespBody == nil {
		a.writeError(w, r, http.StatusBadRequest, codeBadRequest,
			"provide at least one of name, domain, resp_status, resp_headers or resp_body")
		return
	}

	if req.Name != nil {
		name, nameErr := validateName(*req.Name, minWebhookNameLength, maxWebhookNameLength)
		if nameErr != nil {
			a.writeError(w, r, http.StatusBadRequest, codeBadRequest, nameErr.Error())
			return
		}
		params.Name = &name
	}

	if req.Domain != nil {
		domain, domainErr := a.validateWebhookDomain(req.Domain)
		if domainErr != nil {
			a.writeError(w, r, http.StatusBadRequest, codeBadRequest, domainErr.Error())
			return
		}
		params.Domain = &domain
	}

	if req.RespStatus != nil && (*req.RespStatus < minRespStatus || *req.RespStatus > maxRespStatus) {
		a.writeError(w, r, http.StatusBadRequest, codeBadRequest,
			fmt.Sprintf("resp_status must be between %d and %d", minRespStatus, maxRespStatus))
		return
	}

	if req.RespBody != nil && utf8.RuneCountInString(*req.RespBody) > maxRespBodyLength {
		a.writeError(w, r, http.StatusBadRequest, codeBadRequest,
			fmt.Sprintf("resp_body must be at most %d characters", maxRespBodyLength))
		return
	}

	updated, err := a.store.UpdateWebhookEndpoint(r.Context(), params)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			a.writeError(w, r, http.StatusNotFound, codeNotFound, messageWebhookNotFound)
			return
		}
		a.serverError(w, r, err)
		return
	}

	a.writeJSON(w, r, http.StatusOK, newWebhookResponse(updated))
}

// handleDeleteWebhook deletes an endpoint and, by cascade, its captures. Its
// slug stops capturing at once.
func (a *api) handleDeleteWebhook(w http.ResponseWriter, r *http.Request) {
	endpoint, ok := a.webhookFromPath(w, r)
	if !ok {
		return
	}

	if err := a.store.DeleteWebhookEndpoint(r.Context(), endpoint.ID); err != nil {
		a.serverError(w, r, err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// handleListCaptures is the polling cursor: captures with id > after, oldest
// first, summary fields only. The client appends and polls again with the
// last id it has.
func (a *api) handleListCaptures(w http.ResponseWriter, r *http.Request) {
	endpoint, ok := a.webhookFromPath(w, r)
	if !ok {
		return
	}

	after, err := queryInt(r, "after", 0, 0, -1)
	if err != nil {
		a.writeError(w, r, http.StatusBadRequest, codeBadRequest, err.Error())
		return
	}
	limit, err := queryInt(r, "limit", defaultCapturePage, 1, maxCapturePage)
	if err != nil {
		a.writeError(w, r, http.StatusBadRequest, codeBadRequest, err.Error())
		return
	}

	rows, err := a.store.ListWebhookCapturesAfter(r.Context(), db.ListWebhookCapturesAfterParams{
		EndpointID: endpoint.ID,
		After:      after,
		MaxRows:    int32(limit), //nolint:gosec // G115: bounded by maxCapturePage
	})
	if err != nil {
		a.serverError(w, r, err)
		return
	}

	captures := make([]captureSummaryResponse, 0, len(rows))
	for _, row := range rows {
		captures = append(captures, captureSummaryResponse{
			ID:            row.ID,
			Method:        row.Method,
			Path:          row.Path,
			Query:         row.Query,
			ContentType:   row.ContentType,
			BodySize:      row.BodySize,
			BodyTruncated: row.BodyTruncated,
			SourceIP:      row.SourceIp,
			ReceivedAt:    row.ReceivedAt,
		})
	}

	a.writeJSON(w, r, http.StatusOK, captures)
}

// handleGetCapture returns one capture in full, headers and body included.
func (a *api) handleGetCapture(w http.ResponseWriter, r *http.Request) {
	endpoint, ok := a.webhookFromPath(w, r)
	if !ok {
		return
	}

	captureID, err := strconv.ParseInt(r.PathValue("capture_id"), 10, 64)
	if err != nil || captureID <= 0 {
		a.writeError(w, r, http.StatusBadRequest, codeBadRequest, "capture_id must be a positive integer")
		return
	}

	capture, err := a.store.GetWebhookCapture(r.Context(), db.GetWebhookCaptureParams{
		ID:         captureID,
		EndpointID: endpoint.ID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			a.writeError(w, r, http.StatusNotFound, codeNotFound, messageCaptureNotFound)
			return
		}
		a.serverError(w, r, err)
		return
	}

	a.writeJSON(w, r, http.StatusOK, captureResponse{
		captureSummaryResponse: captureSummaryResponse{
			ID:            capture.ID,
			Method:        capture.Method,
			Path:          capture.Path,
			Query:         capture.Query,
			ContentType:   capture.ContentType,
			BodySize:      capture.BodySize,
			BodyTruncated: capture.BodyTruncated,
			SourceIP:      capture.SourceIp,
			ReceivedAt:    capture.ReceivedAt,
		},
		Headers: capture.Headers,
		Body:    capture.Body,
	})
}

// handleClearCaptures deletes every capture of an endpoint.
func (a *api) handleClearCaptures(w http.ResponseWriter, r *http.Request) {
	endpoint, ok := a.webhookFromPath(w, r)
	if !ok {
		return
	}

	if err := a.store.DeleteWebhookCaptures(r.Context(), endpoint.ID); err != nil {
		a.serverError(w, r, err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// webhookFromPath authenticates, parses {id} and checks access. On failure
// it has already written the response.
func (a *api) webhookFromPath(w http.ResponseWriter, r *http.Request) (db.WebhookEndpoint, bool) {
	user, ok := a.authenticatedUser(w, r)
	if !ok {
		return db.WebhookEndpoint{}, false
	}

	endpointID, ok := a.pathUUID(w, r, "id")
	if !ok {
		return db.WebhookEndpoint{}, false
	}

	endpoint, err := a.authz.RequireWebhookAccess(r.Context(), endpointID, user.ID)
	if err != nil {
		a.writeAuthzError(w, r, err)
		return db.WebhookEndpoint{}, false
	}

	return endpoint, true
}

// validateWebhookDomain normalises an endpoint's preferred domain: absent or
// empty is "" (the server default); anything else must be a configured
// capture domain.
func (a *api) validateWebhookDomain(raw *string) (string, error) {
	if raw == nil {
		return "", nil
	}

	domain := strings.ToLower(strings.TrimSpace(*raw))
	if domain == "" || slices.Contains(a.hooksDomains, domain) {
		return domain, nil
	}

	if len(a.hooksDomains) == 0 {
		return "", errors.New("domain must be empty: " + messageHooksNotConfigured)
	}
	return "", fmt.Errorf("domain must be one of %s, or empty for the default (%s)",
		strings.Join(a.hooksDomains, ", "), a.hooksDomains[0])
}

// newWebhookSlug returns slugLength random characters of slugAlphabet from
// crypto/rand, without modulo bias.
func newWebhookSlug() (string, error) {
	const unbiased = 256 - 256%len(slugAlphabet) // 252: largest multiple of 36

	slug := make([]byte, 0, slugLength)
	buf := make([]byte, slugLength*2)

	for len(slug) < slugLength {
		if _, err := rand.Read(buf); err != nil {
			return "", fmt.Errorf("generating webhook slug: %w", err)
		}
		for _, b := range buf {
			if int(b) < unbiased && len(slug) < slugLength {
				slug = append(slug, slugAlphabet[int(b)%len(slugAlphabet)])
			}
		}
	}

	return string(slug), nil
}

// queryInt reads an optional integer query parameter with bounds; maxValue
// below zero means unbounded.
func queryInt(r *http.Request, name string, fallback, minValue, maxValue int64) (int64, error) {
	raw := r.URL.Query().Get(name)
	if raw == "" {
		return fallback, nil
	}

	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || value < minValue || (maxValue >= 0 && value > maxValue) {
		if maxValue >= 0 {
			return 0, fmt.Errorf("%s must be an integer between %d and %d", name, minValue, maxValue)
		}
		return 0, fmt.Errorf("%s must be an integer of at least %d", name, minValue)
	}

	return value, nil
}
