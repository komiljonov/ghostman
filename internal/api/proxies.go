package api

import (
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/komiljonov/ghostman/internal/db"
)

// Proxies are per-project objects like environments: anyone who can reach the
// project may manage them. A proxy's password is client-local and never part
// of the API — there is no field for it, and sending one is an unknown-field
// 400.

// Bounds matching the proxies CHECK constraints.
const (
	minProxyNameLength = 1
	maxProxyNameLength = 100
	minProxyHostLength = 1
	maxProxyHostLength = 255
	minProxyPort       = 1
	maxProxyPort       = 65535
)

type proxyCreateRequest struct {
	Name      string  `json:"name"`
	ProxyType *string `json:"proxy_type"`
	Host      string  `json:"host"`
	Port      *int32  `json:"port"`
	Username  *string `json:"username"`
	Bypass    *string `json:"bypass"`
}

// proxyUpdateRequest is a partial update: an absent (or null) field keeps its
// current value.
type proxyUpdateRequest struct {
	Name      *string `json:"name"`
	ProxyType *string `json:"proxy_type"`
	Host      *string `json:"host"`
	Port      *int32  `json:"port"`
	Username  *string `json:"username"`
	Bypass    *string `json:"bypass"`
}

type proxyOrderRequest struct {
	ProjectID string   `json:"project_id"`
	ProxyIDs  []string `json:"proxy_ids"`
}

// proxyResponse is a whole proxy; there is nothing secret in it to hold back
// from the list.
type proxyResponse struct {
	ID        string    `json:"id"`
	ProjectID string    `json:"project_id"`
	Name      string    `json:"name"`
	ProxyType string    `json:"proxy_type"`
	Host      string    `json:"host"`
	Port      int32     `json:"port"`
	Username  string    `json:"username"`
	Bypass    string    `json:"bypass"`
	SortOrder int32     `json:"sort_order"`
	CreatedAt time.Time `json:"created_at"`
}

func newProxyResponse(proxy db.Proxy) proxyResponse {
	return proxyResponse{
		ID:        proxy.ID.String(),
		ProjectID: proxy.ProjectID.String(),
		Name:      proxy.Name,
		ProxyType: proxy.ProxyType,
		Host:      proxy.Host,
		Port:      proxy.Port,
		Username:  proxy.Username,
		Bypass:    proxy.Bypass,
		SortOrder: proxy.SortOrder,
		CreatedAt: proxy.CreatedAt,
	}
}

// handleCreateProxy adds a proxy at the end of the project's list.
func (a *api) handleCreateProxy(w http.ResponseWriter, r *http.Request) {
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

	var req proxyCreateRequest
	if err := readJSON(w, r, &req); err != nil {
		a.writeBodyError(w, r, err)
		return
	}

	params := db.CreateProxyParams{ProjectID: projectID, ProxyType: db.ProxyTypeHTTP}

	var err error
	if params.Name, err = validateName(req.Name, minProxyNameLength, maxProxyNameLength); err != nil {
		a.writeError(w, r, http.StatusBadRequest, codeBadRequest, err.Error())
		return
	}
	if params.Host, err = validateTrimmedLength("host", req.Host, minProxyHostLength, maxProxyHostLength); err != nil {
		a.writeError(w, r, http.StatusBadRequest, codeBadRequest, err.Error())
		return
	}
	if req.Port == nil {
		a.writeError(w, r, http.StatusBadRequest, codeBadRequest, "port is required")
		return
	}
	if err = validateProxyFields(req.ProxyType, req.Port); err != nil {
		a.writeError(w, r, http.StatusBadRequest, codeBadRequest, err.Error())
		return
	}
	params.Port = *req.Port
	if req.ProxyType != nil {
		params.ProxyType = *req.ProxyType
	}
	if req.Username != nil {
		params.Username = *req.Username
	}
	if req.Bypass != nil {
		params.Bypass = *req.Bypass
	}

	proxy, err := a.store.CreateProxy(r.Context(), params)
	if err != nil {
		a.writeProxyError(w, r, err)
		return
	}

	a.writeJSON(w, r, http.StatusCreated, newProxyResponse(proxy))
}

// handleListProxies returns the project's proxies in order.
func (a *api) handleListProxies(w http.ResponseWriter, r *http.Request) {
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

	rows, err := a.store.ListProxiesByProject(r.Context(), projectID)
	if err != nil {
		a.serverError(w, r, err)
		return
	}

	proxies := make([]proxyResponse, 0, len(rows))
	for _, proxy := range rows {
		proxies = append(proxies, newProxyResponse(proxy))
	}

	a.writeJSON(w, r, http.StatusOK, proxies)
}

// handleGetProxy returns one proxy.
func (a *api) handleGetProxy(w http.ResponseWriter, r *http.Request) {
	proxy, ok := a.proxyFromPath(w, r)
	if !ok {
		return
	}

	a.writeJSON(w, r, http.StatusOK, newProxyResponse(proxy))
}

// handleUpdateProxy applies a partial update to a proxy.
func (a *api) handleUpdateProxy(w http.ResponseWriter, r *http.Request) {
	proxy, ok := a.proxyFromPath(w, r)
	if !ok {
		return
	}

	var req proxyUpdateRequest
	if err := readJSON(w, r, &req); err != nil {
		a.writeBodyError(w, r, err)
		return
	}

	if req.Name == nil && req.ProxyType == nil && req.Host == nil && req.Port == nil && req.Username == nil && req.Bypass == nil {
		a.writeError(w, r, http.StatusBadRequest, codeBadRequest,
			"provide at least one of name, proxy_type, host, port, username or bypass")
		return
	}

	params := db.UpdateProxyParams{
		ID:        proxy.ID,
		ProxyType: req.ProxyType,
		Port:      req.Port,
		Username:  req.Username,
		Bypass:    req.Bypass,
	}

	if req.Name != nil {
		name, err := validateName(*req.Name, minProxyNameLength, maxProxyNameLength)
		if err != nil {
			a.writeError(w, r, http.StatusBadRequest, codeBadRequest, err.Error())
			return
		}
		params.Name = &name
	}
	if req.Host != nil {
		host, err := validateTrimmedLength("host", *req.Host, minProxyHostLength, maxProxyHostLength)
		if err != nil {
			a.writeError(w, r, http.StatusBadRequest, codeBadRequest, err.Error())
			return
		}
		params.Host = &host
	}
	if err := validateProxyFields(req.ProxyType, req.Port); err != nil {
		a.writeError(w, r, http.StatusBadRequest, codeBadRequest, err.Error())
		return
	}

	updated, err := a.store.UpdateProxy(r.Context(), params)
	if err != nil {
		a.writeProxyError(w, r, err)
		return
	}

	a.writeJSON(w, r, http.StatusOK, newProxyResponse(updated))
}

// handleDeleteProxy deletes a proxy. Folders and requests using it keep mode
// "proxy" with proxy_id null (ON DELETE SET NULL); the client resolves them as
// "inherit".
func (a *api) handleDeleteProxy(w http.ResponseWriter, r *http.Request) {
	proxy, ok := a.proxyFromPath(w, r)
	if !ok {
		return
	}

	if err := a.store.DeleteProxy(r.Context(), proxy.ID); err != nil {
		a.serverError(w, r, err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// handleReorderProxies rewrites the project's proxy order. The list must be
// exactly the project's proxies.
func (a *api) handleReorderProxies(w http.ResponseWriter, r *http.Request) {
	user, ok := a.authenticatedUser(w, r)
	if !ok {
		return
	}

	// The scope is in the body, so it has to be read before the access check.
	var req proxyOrderRequest
	if err := readJSON(w, r, &req); err != nil {
		a.writeBodyError(w, r, err)
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

	proxyIDs, err := parseUUIDs(req.ProxyIDs, "proxy_ids")
	if err != nil {
		a.writeError(w, r, http.StatusBadRequest, codeBadRequest, err.Error())
		return
	}

	err = a.store.ReorderProxies(r.Context(), projectID, proxyIDs, func(current []uuid.UUID) error {
		if setErr := checkSameIDSet(proxyIDs, current,
			"proxy_ids", "a proxy of this project", "proxies of this project"); setErr != nil {
			return badRequestError{err: setErr}
		}
		return nil
	})
	if err != nil {
		a.writeProxyError(w, r, err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// proxyFromPath authenticates, parses {id} and checks access. On failure it
// has already written the response.
func (a *api) proxyFromPath(w http.ResponseWriter, r *http.Request) (db.Proxy, bool) {
	user, ok := a.authenticatedUser(w, r)
	if !ok {
		return db.Proxy{}, false
	}

	proxyID, ok := a.pathUUID(w, r, "id")
	if !ok {
		return db.Proxy{}, false
	}

	proxy, err := a.authz.RequireProxyAccess(r.Context(), proxyID, user.ID)
	if err != nil {
		a.writeAuthzError(w, r, err)
		return db.Proxy{}, false
	}

	return proxy, true
}

// validateProxyFields checks the optional proxy_type and port.
func validateProxyFields(proxyType *string, port *int32) error {
	if proxyType != nil && !slices.Contains(db.ProxyTypes, *proxyType) {
		return fmt.Errorf("proxy_type must be one of %s", strings.Join(db.ProxyTypes, ", "))
	}
	if port != nil && (*port < minProxyPort || *port > maxProxyPort) {
		return fmt.Errorf("port must be between %d and %d", minProxyPort, maxProxyPort)
	}
	return nil
}

// writeProxyError maps the failures a proxy write can report after the access
// check has passed.
func (a *api) writeProxyError(w http.ResponseWriter, r *http.Request, err error) {
	var badRequest badRequestError

	switch {
	case errors.As(err, &badRequest):
		a.writeError(w, r, http.StatusBadRequest, codeBadRequest, badRequest.Error())

	case db.IsProxyNameTaken(err):
		a.writeError(w, r, http.StatusConflict, codeConflict, messageProxyNameTaken)

	case errors.Is(err, pgx.ErrNoRows):
		// Deleted between the access check and the write.
		a.writeError(w, r, http.StatusNotFound, codeNotFound, messageProxyNotFound)

	default:
		a.serverError(w, r, err)
	}
}
