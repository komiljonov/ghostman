package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/komiljonov/ghostman/internal/db"
)

// Environments are named sets of variables inside a project. Like folders they
// are working material: anyone who can reach the project may change them.
//
// Secret variables are stored by key only. The server never accepts, stores or
// returns a secret's value — those live solely in the desktop client — and the
// environment_variables_secret_no_value CHECK backs that up in the database.

// Bounds matching the environments_name_length and
// environment_variables_key_length CHECK constraints.
const (
	minEnvironmentNameLength = 1
	maxEnvironmentNameLength = 100
	minVariableKeyLength     = 1
	maxVariableKeyLength     = 200
)

// checkViolation is the PostgreSQL SQLSTATE for a CHECK-constraint breach.
const checkViolation = "23514"

// secretNoValueConstraint is the CHECK that keeps secret values off the server.
const secretNoValueConstraint = "environment_variables_secret_no_value"

type environmentRequest struct {
	Name string `json:"name"`
}

type environmentOrderRequest struct {
	ProjectID      string   `json:"project_id"`
	EnvironmentIDs []string `json:"environment_ids"`
}

type variableCreateRequest struct {
	Key   string  `json:"key"`
	Type  *string `json:"type"`
	Value *string `json:"value"`
}

// variableUpdateRequest is a partial update. value is decoded raw because
// "absent" (keep it) and null (clear it) mean different things.
type variableUpdateRequest struct {
	Key   *string         `json:"key"`
	Type  *string         `json:"type"`
	Value json.RawMessage `json:"value"`
}

type variableOrderRequest struct {
	EnvironmentID string   `json:"environment_id"`
	VariableIDs   []string `json:"variable_ids"`
}

// environmentResponse is the full shape, used wherever a single environment is
// the subject of the request.
type environmentResponse struct {
	ID        string    `json:"id"`
	ProjectID string    `json:"project_id"`
	Name      string    `json:"name"`
	SortOrder int32     `json:"sort_order"`
	CreatedAt time.Time `json:"created_at"`
}

// environmentSummaryResponse is one entry of a project's environment list. The
// project is implied by the request path, so it is not repeated.
type environmentSummaryResponse struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	SortOrder int32     `json:"sort_order"`
	CreatedAt time.Time `json:"created_at"`
}

// variableResponse carries value: null for every secret, whatever is stored.
type variableResponse struct {
	ID        string  `json:"id"`
	Key       string  `json:"key"`
	Type      string  `json:"type"`
	Value     *string `json:"value"`
	SortOrder int32   `json:"sort_order"`
}

func newEnvironmentResponse(environment db.Environment) environmentResponse {
	return environmentResponse{
		ID:        environment.ID.String(),
		ProjectID: environment.ProjectID.String(),
		Name:      environment.Name,
		SortOrder: environment.SortOrder,
		CreatedAt: environment.CreatedAt,
	}
}

func newVariableResponse(variable db.EnvironmentVariable) variableResponse {
	value := variable.Value
	// The database already guarantees this; the response does not rely on it.
	if variable.Type == db.VariableSecret {
		value = nil
	}

	return variableResponse{
		ID:        variable.ID.String(),
		Key:       variable.Key,
		Type:      variable.Type,
		Value:     value,
		SortOrder: variable.SortOrder,
	}
}

// handleCreateEnvironment creates an environment at the end of the project's
// list. Names are unique per project, ignoring case.
func (a *api) handleCreateEnvironment(w http.ResponseWriter, r *http.Request) {
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

	name, ok := a.readEnvironmentName(w, r)
	if !ok {
		return
	}

	environment, err := a.store.CreateEnvironment(r.Context(), db.CreateEnvironmentParams{
		ProjectID: projectID,
		Name:      name,
	})
	if err != nil {
		a.writeEnvironmentError(w, r, err)
		return
	}

	a.writeJSON(w, r, http.StatusCreated, newEnvironmentResponse(environment))
}

// handleListEnvironments returns the project's environments in order.
func (a *api) handleListEnvironments(w http.ResponseWriter, r *http.Request) {
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

	rows, err := a.store.ListEnvironmentsByProject(r.Context(), projectID)
	if err != nil {
		a.serverError(w, r, err)
		return
	}

	environments := make([]environmentSummaryResponse, 0, len(rows))
	for _, environment := range rows {
		environments = append(environments, environmentSummaryResponse{
			ID:        environment.ID.String(),
			Name:      environment.Name,
			SortOrder: environment.SortOrder,
			CreatedAt: environment.CreatedAt,
		})
	}

	a.writeJSON(w, r, http.StatusOK, environments)
}

// handleUpdateEnvironment renames an environment.
func (a *api) handleUpdateEnvironment(w http.ResponseWriter, r *http.Request) {
	user, ok := a.authenticatedUser(w, r)
	if !ok {
		return
	}

	environmentID, ok := a.pathUUID(w, r, "id")
	if !ok {
		return
	}

	if _, err := a.authz.RequireEnvironmentAccess(r.Context(), environmentID, user.ID); err != nil {
		a.writeAuthzError(w, r, err)
		return
	}

	name, ok := a.readEnvironmentName(w, r)
	if !ok {
		return
	}

	environment, err := a.store.UpdateEnvironmentName(r.Context(), db.UpdateEnvironmentNameParams{
		ID:   environmentID,
		Name: name,
	})
	if err != nil {
		a.writeEnvironmentError(w, r, err)
		return
	}

	a.writeJSON(w, r, http.StatusOK, newEnvironmentResponse(environment))
}

// handleDeleteEnvironment deletes an environment and, by cascade, its
// variables.
func (a *api) handleDeleteEnvironment(w http.ResponseWriter, r *http.Request) {
	user, ok := a.authenticatedUser(w, r)
	if !ok {
		return
	}

	environmentID, ok := a.pathUUID(w, r, "id")
	if !ok {
		return
	}

	if _, err := a.authz.RequireEnvironmentAccess(r.Context(), environmentID, user.ID); err != nil {
		a.writeAuthzError(w, r, err)
		return
	}

	if err := a.store.DeleteEnvironment(r.Context(), environmentID); err != nil {
		a.serverError(w, r, err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// handleReorderEnvironments rewrites the project's environment order. The
// list must be exactly the project's environments.
func (a *api) handleReorderEnvironments(w http.ResponseWriter, r *http.Request) {
	user, ok := a.authenticatedUser(w, r)
	if !ok {
		return
	}

	// The scope is in the body, so it has to be read before the access check.
	var req environmentOrderRequest
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

	environmentIDs, err := parseUUIDs(req.EnvironmentIDs, "environment_ids")
	if err != nil {
		a.writeError(w, r, http.StatusBadRequest, codeBadRequest, err.Error())
		return
	}

	err = a.store.ReorderEnvironments(r.Context(), projectID, environmentIDs, func(current []uuid.UUID) error {
		if setErr := checkSameIDSet(environmentIDs, current,
			"environment_ids", "an environment of this project", "environments of this project"); setErr != nil {
			return badRequestError{err: setErr}
		}
		return nil
	})
	if err != nil {
		a.writeEnvironmentError(w, r, err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// handleCreateVariable adds a variable at the end of the environment's list.
// A secret may be created, but never with a value.
func (a *api) handleCreateVariable(w http.ResponseWriter, r *http.Request) {
	user, ok := a.authenticatedUser(w, r)
	if !ok {
		return
	}

	environmentID, ok := a.pathUUID(w, r, "env_id")
	if !ok {
		return
	}

	if _, err := a.authz.RequireEnvironmentAccess(r.Context(), environmentID, user.ID); err != nil {
		a.writeAuthzError(w, r, err)
		return
	}

	var req variableCreateRequest
	if err := readJSON(w, r, &req); err != nil {
		a.writeError(w, r, http.StatusBadRequest, codeBadRequest, err.Error())
		return
	}

	key, err := validateTrimmedLength("key", req.Key, minVariableKeyLength, maxVariableKeyLength)
	if err != nil {
		a.writeError(w, r, http.StatusBadRequest, codeBadRequest, err.Error())
		return
	}

	variableType := db.VariableRegular
	if req.Type != nil {
		if !isVariableType(*req.Type) {
			a.writeError(w, r, http.StatusBadRequest, codeBadRequest, messageInvalidVariableType)
			return
		}
		variableType = *req.Type
	}

	value := req.Value
	if variableType == db.VariableSecret {
		if value != nil && *value != "" {
			a.writeError(w, r, http.StatusBadRequest, codeBadRequest, messageSecretValue)
			return
		}
		// An empty string is "no value", which for a secret is the only option.
		value = nil
	}

	variable, err := a.store.CreateVariable(r.Context(), db.CreateVariableParams{
		EnvironmentID: environmentID,
		Key:           key,
		Type:          variableType,
		Value:         value,
	})
	if err != nil {
		a.writeVariableError(w, r, err)
		return
	}

	a.writeJSON(w, r, http.StatusCreated, newVariableResponse(variable))
}

// handleListVariables returns the environment's variables in order, secrets
// with value: null.
func (a *api) handleListVariables(w http.ResponseWriter, r *http.Request) {
	user, ok := a.authenticatedUser(w, r)
	if !ok {
		return
	}

	environmentID, ok := a.pathUUID(w, r, "env_id")
	if !ok {
		return
	}

	if _, err := a.authz.RequireEnvironmentAccess(r.Context(), environmentID, user.ID); err != nil {
		a.writeAuthzError(w, r, err)
		return
	}

	rows, err := a.store.ListVariablesByEnvironment(r.Context(), environmentID)
	if err != nil {
		a.serverError(w, r, err)
		return
	}

	variables := make([]variableResponse, 0, len(rows))
	for _, variable := range rows {
		variables = append(variables, newVariableResponse(variable))
	}

	a.writeJSON(w, r, http.StatusOK, variables)
}

// handleUpdateVariable applies a partial update to a variable. Turning a
// regular variable secret discards its stored value; giving a secret a value
// is refused.
func (a *api) handleUpdateVariable(w http.ResponseWriter, r *http.Request) {
	user, ok := a.authenticatedUser(w, r)
	if !ok {
		return
	}

	variableID, ok := a.pathUUID(w, r, "id")
	if !ok {
		return
	}

	current, err := a.authz.RequireVariableAccess(r.Context(), variableID, user.ID)
	if err != nil {
		a.writeAuthzError(w, r, err)
		return
	}

	var req variableUpdateRequest
	if err = readJSON(w, r, &req); err != nil {
		a.writeError(w, r, http.StatusBadRequest, codeBadRequest, err.Error())
		return
	}

	if req.Key == nil && req.Type == nil && len(req.Value) == 0 {
		a.writeError(w, r, http.StatusBadRequest, codeBadRequest, "provide at least one of key, type or value")
		return
	}

	params := db.UpdateVariableParams{ID: variableID, Type: req.Type}

	if req.Key != nil {
		key, keyErr := validateTrimmedLength("key", *req.Key, minVariableKeyLength, maxVariableKeyLength)
		if keyErr != nil {
			a.writeError(w, r, http.StatusBadRequest, codeBadRequest, keyErr.Error())
			return
		}
		params.Key = &key
	}

	if req.Type != nil && !isVariableType(*req.Type) {
		a.writeError(w, r, http.StatusBadRequest, codeBadRequest, messageInvalidVariableType)
		return
	}

	if len(req.Value) > 0 {
		value, valueErr := parseNullableString(req.Value, "value")
		if valueErr != nil {
			a.writeError(w, r, http.StatusBadRequest, codeBadRequest, valueErr.Error())
			return
		}
		params.SetValue = true
		params.Value = value
	}

	// Judged against the type the variable will have after this update. If a
	// concurrent update makes it secret in the meantime, the query itself
	// discards the value, so the race can only lose a value, never store one.
	finalType := current.Type
	if req.Type != nil {
		finalType = *req.Type
	}
	if finalType == db.VariableSecret && params.Value != nil && *params.Value != "" {
		a.writeError(w, r, http.StatusBadRequest, codeBadRequest, messageSecretValue)
		return
	}

	variable, err := a.store.UpdateVariable(r.Context(), params)
	if err != nil {
		a.writeVariableError(w, r, err)
		return
	}

	a.writeJSON(w, r, http.StatusOK, newVariableResponse(variable))
}

// handleDeleteVariable deletes one variable.
func (a *api) handleDeleteVariable(w http.ResponseWriter, r *http.Request) {
	user, ok := a.authenticatedUser(w, r)
	if !ok {
		return
	}

	variableID, ok := a.pathUUID(w, r, "id")
	if !ok {
		return
	}

	if _, err := a.authz.RequireVariableAccess(r.Context(), variableID, user.ID); err != nil {
		a.writeAuthzError(w, r, err)
		return
	}

	if err := a.store.DeleteVariable(r.Context(), variableID); err != nil {
		a.serverError(w, r, err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// handleReorderVariables rewrites an environment's variable order. The list
// must be exactly the environment's variables.
func (a *api) handleReorderVariables(w http.ResponseWriter, r *http.Request) {
	user, ok := a.authenticatedUser(w, r)
	if !ok {
		return
	}

	var req variableOrderRequest
	if err := readJSON(w, r, &req); err != nil {
		a.writeError(w, r, http.StatusBadRequest, codeBadRequest, err.Error())
		return
	}

	environmentID, err := uuid.Parse(req.EnvironmentID)
	if err != nil {
		a.writeError(w, r, http.StatusBadRequest, codeBadRequest, "environment_id must be a valid uuid")
		return
	}

	if _, err = a.authz.RequireEnvironmentAccess(r.Context(), environmentID, user.ID); err != nil {
		a.writeAuthzError(w, r, err)
		return
	}

	variableIDs, err := parseUUIDs(req.VariableIDs, "variable_ids")
	if err != nil {
		a.writeError(w, r, http.StatusBadRequest, codeBadRequest, err.Error())
		return
	}

	err = a.store.ReorderVariables(r.Context(), environmentID, variableIDs, func(current []uuid.UUID) error {
		if setErr := checkSameIDSet(variableIDs, current,
			"variable_ids", "a variable of this environment", "variables of this environment"); setErr != nil {
			return badRequestError{err: setErr}
		}
		return nil
	})
	if err != nil {
		a.writeVariableError(w, r, err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// readEnvironmentName decodes and validates a {name} body.
func (a *api) readEnvironmentName(w http.ResponseWriter, r *http.Request) (string, bool) {
	var req environmentRequest
	if err := readJSON(w, r, &req); err != nil {
		a.writeError(w, r, http.StatusBadRequest, codeBadRequest, err.Error())
		return "", false
	}

	name, err := validateName(req.Name, minEnvironmentNameLength, maxEnvironmentNameLength)
	if err != nil {
		a.writeError(w, r, http.StatusBadRequest, codeBadRequest, err.Error())
		return "", false
	}

	return name, true
}

// writeEnvironmentError maps the failures an environment write can report
// after the access check has passed.
func (a *api) writeEnvironmentError(w http.ResponseWriter, r *http.Request, err error) {
	var badRequest badRequestError

	switch {
	case errors.As(err, &badRequest):
		a.writeError(w, r, http.StatusBadRequest, codeBadRequest, badRequest.Error())

	case isUniqueViolation(err):
		// The only unique constraint besides the key is the per-project name.
		a.writeError(w, r, http.StatusConflict, codeConflict, messageEnvironmentNameTaken)

	case errors.Is(err, pgx.ErrNoRows):
		// Deleted between the access check and the write.
		a.writeError(w, r, http.StatusNotFound, codeNotFound, messageEnvironmentNotFound)

	default:
		a.serverError(w, r, err)
	}
}

// writeVariableError maps the failures a variable write can report after the
// access check has passed.
func (a *api) writeVariableError(w http.ResponseWriter, r *http.Request, err error) {
	var badRequest badRequestError
	var pgErr *pgconn.PgError

	switch {
	case errors.As(err, &badRequest):
		a.writeError(w, r, http.StatusBadRequest, codeBadRequest, badRequest.Error())

	case isUniqueViolation(err):
		// The only unique constraint besides the key is (environment_id, key).
		a.writeError(w, r, http.StatusConflict, codeConflict, messageVariableKeyTaken)

	case errors.As(err, &pgErr) && pgErr.Code == checkViolation && pgErr.ConstraintName == secretNoValueConstraint:
		// Unreachable through the handlers, which refuse first; kept so the
		// database's refusal still reads as the rule rather than a 500.
		a.writeError(w, r, http.StatusBadRequest, codeBadRequest, messageSecretValue)

	case errors.Is(err, pgx.ErrNoRows):
		// Deleted between the access check and the write.
		a.writeError(w, r, http.StatusNotFound, codeNotFound, messageVariableNotFound)

	default:
		a.serverError(w, r, err)
	}
}

func isVariableType(value string) bool {
	return value == db.VariableRegular || value == db.VariableSecret
}

// parseNullableString reads a field that may be a string or null.
func parseNullableString(raw json.RawMessage, field string) (*string, error) {
	if string(raw) == "null" {
		return nil, nil
	}

	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, errors.New(field + " must be a string or null")
	}

	return &value, nil
}
