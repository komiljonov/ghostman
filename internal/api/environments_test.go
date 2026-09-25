package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
)

func (f teamsFixture) postEnvironment(t *testing.T, token, projectID, name string) *httptest.ResponseRecorder {
	t.Helper()

	return do(t, f.handler, http.MethodPost, "/api/v1/projects/"+projectID+"/environments",
		folderBody(t, map[string]any{"name": name}), token)
}

// createEnvironment creates an environment and fails unless it succeeds.
func (f teamsFixture) createEnvironment(t *testing.T, token, projectID, name string) environmentResponse {
	t.Helper()

	rec := f.postEnvironment(t, token, projectID, name)
	if rec.Code != http.StatusCreated {
		t.Fatalf("creating environment %q: status = %d, want %d (body: %s)", name, rec.Code, http.StatusCreated, rec.Body)
	}

	var environment environmentResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &environment); err != nil {
		t.Fatalf("decoding environment response: %v", err)
	}

	return environment
}

func (f teamsFixture) listEnvironments(t *testing.T, token, projectID string) []environmentSummaryResponse {
	t.Helper()

	rec := do(t, f.handler, http.MethodGet, "/api/v1/projects/"+projectID+"/environments", "", token)
	if rec.Code != http.StatusOK {
		t.Fatalf("listing environments: status = %d, want %d (body: %s)", rec.Code, http.StatusOK, rec.Body)
	}

	var environments []environmentSummaryResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &environments); err != nil {
		t.Fatalf("decoding environment list: %v", err)
	}

	return environments
}

func (f teamsFixture) postVariable(t *testing.T, token, environmentID string, fields map[string]any) *httptest.ResponseRecorder {
	t.Helper()

	return do(t, f.handler, http.MethodPost, "/api/v1/environments/"+environmentID+"/variables",
		folderBody(t, fields), token)
}

// createVariable creates a variable and fails unless it succeeds.
func (f teamsFixture) createVariable(t *testing.T, token, environmentID string, fields map[string]any) variableResponse {
	t.Helper()

	rec := f.postVariable(t, token, environmentID, fields)
	if rec.Code != http.StatusCreated {
		t.Fatalf("creating variable %v: status = %d, want %d (body: %s)", fields, rec.Code, http.StatusCreated, rec.Body)
	}

	var variable variableResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &variable); err != nil {
		t.Fatalf("decoding variable response: %v", err)
	}

	return variable
}

func (f teamsFixture) listVariables(t *testing.T, token, environmentID string) []variableResponse {
	t.Helper()

	rec := do(t, f.handler, http.MethodGet, "/api/v1/environments/"+environmentID+"/variables", "", token)
	if rec.Code != http.StatusOK {
		t.Fatalf("listing variables: status = %d, want %d (body: %s)", rec.Code, http.StatusOK, rec.Body)
	}

	var variables []variableResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &variables); err != nil {
		t.Fatalf("decoding variable list: %v", err)
	}

	return variables
}

func (f teamsFixture) patchVariable(t *testing.T, token, variableID, body string) *httptest.ResponseRecorder {
	t.Helper()

	return do(t, f.handler, http.MethodPatch, "/api/v1/variables/"+variableID, body, token)
}

// storedVariable reads a variable's type and value straight from the table:
// what the security rule is about is what the database holds, not what the
// API chooses to show.
func (f teamsFixture) storedVariable(t *testing.T, variableID string) (string, *string) {
	t.Helper()

	var variableType string
	var value *string
	if err := f.pool.QueryRow(t.Context(),
		"SELECT type, value FROM environment_variables WHERE id = $1", variableID).Scan(&variableType, &value); err != nil {
		t.Fatalf("reading stored variable: %v", err)
	}

	return variableType, value
}

func (f teamsFixture) countRows(t *testing.T, query string, args ...any) int {
	t.Helper()

	var count int
	if err := f.pool.QueryRow(t.Context(), query, args...).Scan(&count); err != nil {
		t.Fatalf("counting rows: %v", err)
	}

	return count
}

func sortOrders[T any](items []T, sortOrder func(T) int32) []int32 {
	orders := make([]int32, 0, len(items))
	for _, item := range items {
		orders = append(orders, sortOrder(item))
	}
	return orders
}

// newEnvironmentProject sets up an owner, a team, a project and one
// environment in it.
func newEnvironmentProject(t *testing.T, emailPrefix string) (teamsFixture, authResponse, projectResponse, environmentResponse) {
	t.Helper()

	f, owner, _, project := newFolderProject(t, emailPrefix)
	environment := f.createEnvironment(t, owner.Token, project.ID, "dev")

	return f, owner, project, environment
}

func TestSecretVariablesNeverStoreValues(t *testing.T) {
	f, owner, _, env := newEnvironmentProject(t, "secret-owner")

	t.Run("create secret with a value is refused", func(t *testing.T) {
		rec := f.postVariable(t, owner.Token, env.ID, map[string]any{"key": "API_TOKEN", "type": "secret", "value": "hunter2"})
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want %d (body: %s)", rec.Code, http.StatusBadRequest, rec.Body)
		}
		if msg := decodeEnvelope(t, rec).Error.Message; msg != messageSecretValue {
			t.Errorf("message = %q, want %q", msg, messageSecretValue)
		}
		if count := f.countRows(t, "SELECT count(*) FROM environment_variables"); count != 0 {
			t.Errorf("refused create left %d rows", count)
		}
	})

	secret := f.createVariable(t, owner.Token, env.ID, map[string]any{"key": "API_TOKEN", "type": "secret"})
	t.Run("create secret without a value", func(t *testing.T) {
		if secret.Type != "secret" || secret.Value != nil {
			t.Errorf("response = %+v, want type secret with null value", secret)
		}
		if _, value := f.storedVariable(t, secret.ID); value != nil {
			t.Errorf("stored value = %q, want NULL", *value)
		}
	})

	t.Run("an empty value on a secret is no value", func(t *testing.T) {
		empty := f.createVariable(t, owner.Token, env.ID, map[string]any{"key": "EMPTY_SECRET", "type": "secret", "value": ""})
		if _, value := f.storedVariable(t, empty.ID); value != nil {
			t.Errorf("stored value = %q, want NULL", *value)
		}
	})

	regular := f.createVariable(t, owner.Token, env.ID, map[string]any{"key": "BASE_URL", "value": "https://api.example.com"})
	if regular.Type != "regular" || regular.Value == nil || *regular.Value != "https://api.example.com" {
		t.Fatalf("regular variable = %+v, want type regular (the default) with its value", regular)
	}

	t.Run("PATCH secret with a value is refused", func(t *testing.T) {
		rec := f.patchVariable(t, owner.Token, secret.ID, `{"value":"hunter2"}`)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want %d (body: %s)", rec.Code, http.StatusBadRequest, rec.Body)
		}
		if _, value := f.storedVariable(t, secret.ID); value != nil {
			t.Errorf("stored value = %q, want NULL", *value)
		}
	})

	t.Run("PATCH to secret together with a value is refused", func(t *testing.T) {
		rec := f.patchVariable(t, owner.Token, regular.ID, `{"type":"secret","value":"hunter2"}`)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want %d (body: %s)", rec.Code, http.StatusBadRequest, rec.Body)
		}
		if variableType, value := f.storedVariable(t, regular.ID); variableType != "regular" || value == nil {
			t.Errorf("stored = %s/%v, want the regular variable untouched", variableType, value)
		}
	})

	t.Run("PATCH regular to secret discards the stored value", func(t *testing.T) {
		rec := f.patchVariable(t, owner.Token, regular.ID, `{"type":"secret"}`)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d (body: %s)", rec.Code, http.StatusOK, rec.Body)
		}

		var updated variableResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &updated); err != nil {
			t.Fatalf("decoding response: %v", err)
		}
		if updated.Type != "secret" || updated.Value != nil {
			t.Errorf("response = %+v, want secret with null value", updated)
		}

		variableType, value := f.storedVariable(t, regular.ID)
		if variableType != "secret" {
			t.Errorf("stored type = %s, want secret", variableType)
		}
		if value != nil {
			t.Errorf("stored value = %q, want NULL", *value)
		}
	})

	t.Run("PATCH secret back to regular may set a value", func(t *testing.T) {
		rec := f.patchVariable(t, owner.Token, regular.ID, `{"type":"regular","value":"https://staging.example.com"}`)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d (body: %s)", rec.Code, http.StatusOK, rec.Body)
		}
		if _, value := f.storedVariable(t, regular.ID); value == nil || *value != "https://staging.example.com" {
			t.Errorf("stored value = %v, want the new value", value)
		}
	})

	t.Run("list shows secrets with an explicit null value", func(t *testing.T) {
		rec := do(t, f.handler, http.MethodGet, "/api/v1/environments/"+env.ID+"/variables", "", owner.Token)
		if !strings.Contains(rec.Body.String(), `"key":"API_TOKEN","type":"secret","value":null`) {
			t.Errorf("secret not serialised with value null: %s", rec.Body)
		}
	})

	t.Run("the database refuses a secret with a value on its own", func(t *testing.T) {
		assertSecretCheck := func(t *testing.T, err error) {
			t.Helper()

			var pgErr *pgconn.PgError
			if !errors.As(err, &pgErr) || pgErr.Code != checkViolation || pgErr.ConstraintName != secretNoValueConstraint {
				t.Fatalf("error = %v, want a %s violation of %s", err, checkViolation, secretNoValueConstraint)
			}
		}

		_, err := f.pool.Exec(t.Context(), `
			INSERT INTO environment_variables (environment_id, key, type, value)
			VALUES ($1, 'DIRECT', 'secret', 'hunter2')`, env.ID)
		assertSecretCheck(t, err)

		_, err = f.pool.Exec(t.Context(),
			"UPDATE environment_variables SET value = 'hunter2' WHERE id = $1", secret.ID)
		assertSecretCheck(t, err)
	})
}

func TestEnvironmentNamesUniquePerProject(t *testing.T) {
	f, owner, project, dev := newEnvironmentProject(t, "env-unique-owner")
	staging := f.createEnvironment(t, owner.Token, project.ID, "staging")

	for _, name := range []string{"dev", "Dev", "  DEV  "} {
		t.Run("create "+name, func(t *testing.T) {
			rec := f.postEnvironment(t, owner.Token, project.ID, name)
			if rec.Code != http.StatusConflict {
				t.Fatalf("status = %d, want %d (body: %s)", rec.Code, http.StatusConflict, rec.Body)
			}
			if msg := decodeEnvelope(t, rec).Error.Message; msg != messageEnvironmentNameTaken {
				t.Errorf("message = %q, want %q", msg, messageEnvironmentNameTaken)
			}
		})
	}

	t.Run("rename onto another environment's name", func(t *testing.T) {
		rec := do(t, f.handler, http.MethodPatch, "/api/v1/environments/"+staging.ID, `{"name":"DEV"}`, owner.Token)
		if rec.Code != http.StatusConflict {
			t.Fatalf("status = %d, want %d (body: %s)", rec.Code, http.StatusConflict, rec.Body)
		}
	})

	t.Run("recasing its own name is fine", func(t *testing.T) {
		rec := do(t, f.handler, http.MethodPatch, "/api/v1/environments/"+dev.ID, `{"name":"Dev"}`, owner.Token)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d (body: %s)", rec.Code, http.StatusOK, rec.Body)
		}
	})

	t.Run("the same name in another project", func(t *testing.T) {
		team := f.createTeam(t, owner.Token, "Elsewhere")
		other := f.createProject(t, owner.Token, team.ID, "Other")
		f.createEnvironment(t, owner.Token, other.ID, "dev")
	})
}

func TestVariableKeysUniquePerEnvironment(t *testing.T) {
	f, owner, project, dev := newEnvironmentProject(t, "var-unique-owner")
	staging := f.createEnvironment(t, owner.Token, project.ID, "staging")

	baseURL := f.createVariable(t, owner.Token, dev.ID, map[string]any{"key": "BASE_URL", "value": "a"})
	other := f.createVariable(t, owner.Token, dev.ID, map[string]any{"key": "TIMEOUT", "value": "30"})

	// Keys collide regardless of case: base_url next to BASE_URL would make a
	// {{base_url}} reference ambiguous. A secret collides too, since the key is
	// the identity whatever the type.
	for _, key := range []string{"BASE_URL", "base_url", "Base_Url", "  base_url  "} {
		t.Run("create "+key, func(t *testing.T) {
			rec := f.postVariable(t, owner.Token, dev.ID, map[string]any{"key": key, "type": "secret"})
			if rec.Code != http.StatusConflict {
				t.Fatalf("status = %d, want %d (body: %s)", rec.Code, http.StatusConflict, rec.Body)
			}
			if msg := decodeEnvelope(t, rec).Error.Message; msg != messageVariableKeyTaken {
				t.Errorf("message = %q, want %q", msg, messageVariableKeyTaken)
			}
		})
	}

	t.Run("rename onto an existing key in another case", func(t *testing.T) {
		rec := f.patchVariable(t, owner.Token, other.ID, `{"key":"base_url"}`)
		if rec.Code != http.StatusConflict {
			t.Fatalf("status = %d, want %d (body: %s)", rec.Code, http.StatusConflict, rec.Body)
		}
	})

	t.Run("recasing its own key is fine and keeps the new case", func(t *testing.T) {
		rec := f.patchVariable(t, owner.Token, baseURL.ID, `{"key":"Base_Url"}`)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d (body: %s)", rec.Code, http.StatusOK, rec.Body)
		}

		var updated variableResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &updated); err != nil {
			t.Fatalf("decoding response: %v", err)
		}
		if updated.Key != "Base_Url" {
			t.Errorf("key = %q, want the case as written, %q", updated.Key, "Base_Url")
		}
	})

	t.Run("the same key in another environment", func(t *testing.T) {
		f.createVariable(t, owner.Token, staging.ID, map[string]any{"key": "BASE_URL", "value": "c"})
	})
}

func TestEnvironmentAndVariableOrder(t *testing.T) {
	f, owner, project, dev := newEnvironmentProject(t, "env-order-owner")
	staging := f.createEnvironment(t, owner.Token, project.ID, "staging")
	prod := f.createEnvironment(t, owner.Token, project.ID, "prod")

	team := f.createTeam(t, owner.Token, "Elsewhere")
	otherProject := f.createProject(t, owner.Token, team.ID, "Other")
	foreignEnv := f.createEnvironment(t, owner.Token, otherProject.ID, "dev")

	envOrders := func(t *testing.T) ([]string, []int32) {
		t.Helper()
		environments := f.listEnvironments(t, owner.Token, project.ID)
		names := make([]string, 0, len(environments))
		for _, environment := range environments {
			names = append(names, environment.Name)
		}
		return names, sortOrders(environments, func(e environmentSummaryResponse) int32 { return e.SortOrder })
	}

	if names, orders := envOrders(t); strings.Join(names, ",") != "dev,staging,prod" || !equalOrders(orders, 0, 1, 2) {
		t.Fatalf("initial environments = %v %v, want dev,staging,prod at 0,1,2", names, orders)
	}

	putEnvOrder := func(ids ...string) *httptest.ResponseRecorder {
		return do(t, f.handler, http.MethodPut, "/api/v1/environments/order",
			folderBody(t, map[string]any{"project_id": project.ID, "environment_ids": ids}), owner.Token)
	}

	t.Run("environment order: wrong sets", func(t *testing.T) {
		for name, ids := range map[string][]string{
			"missing one":       {dev.ID, staging.ID},
			"another project's": {dev.ID, staging.ID, prod.ID, foreignEnv.ID},
			"duplicate":         {dev.ID, staging.ID, staging.ID},
			"empty":             {},
			"not a uuid":        {"nope"},
		} {
			t.Run(name, func(t *testing.T) {
				if rec := putEnvOrder(ids...); rec.Code != http.StatusBadRequest {
					t.Fatalf("status = %d, want %d (body: %s)", rec.Code, http.StatusBadRequest, rec.Body)
				}
			})
		}
	})

	t.Run("environment order: 0-based and contiguous", func(t *testing.T) {
		if rec := putEnvOrder(prod.ID, dev.ID, staging.ID); rec.Code != http.StatusNoContent {
			t.Fatalf("status = %d, want %d (body: %s)", rec.Code, http.StatusNoContent, rec.Body)
		}
		if names, orders := envOrders(t); strings.Join(names, ",") != "prod,dev,staging" || !equalOrders(orders, 0, 1, 2) {
			t.Errorf("environments = %v %v, want prod,dev,staging at 0,1,2", names, orders)
		}

		// A new environment lands right after them.
		if added := f.createEnvironment(t, owner.Token, project.ID, "qa"); added.SortOrder != 3 {
			t.Errorf("new environment sort_order = %d, want 3", added.SortOrder)
		}
	})

	a := f.createVariable(t, owner.Token, dev.ID, map[string]any{"key": "A", "value": "1"})
	b := f.createVariable(t, owner.Token, dev.ID, map[string]any{"key": "B", "type": "secret"})
	c := f.createVariable(t, owner.Token, dev.ID, map[string]any{"key": "C", "value": "3"})
	foreignVar := f.createVariable(t, owner.Token, staging.ID, map[string]any{"key": "X", "value": "x"})

	varOrders := func(t *testing.T) ([]string, []int32) {
		t.Helper()
		variables := f.listVariables(t, owner.Token, dev.ID)
		keys := make([]string, 0, len(variables))
		for _, variable := range variables {
			keys = append(keys, variable.Key)
		}
		return keys, sortOrders(variables, func(v variableResponse) int32 { return v.SortOrder })
	}

	if keys, orders := varOrders(t); strings.Join(keys, ",") != "A,B,C" || !equalOrders(orders, 0, 1, 2) {
		t.Fatalf("initial variables = %v %v, want A,B,C at 0,1,2", keys, orders)
	}

	putVarOrder := func(ids ...string) *httptest.ResponseRecorder {
		return do(t, f.handler, http.MethodPut, "/api/v1/variables/order",
			folderBody(t, map[string]any{"environment_id": dev.ID, "variable_ids": ids}), owner.Token)
	}

	t.Run("variable order: wrong sets", func(t *testing.T) {
		for name, ids := range map[string][]string{
			"missing one":           {a.ID, b.ID},
			"another environment's": {a.ID, b.ID, c.ID, foreignVar.ID},
			"duplicate":             {a.ID, a.ID, b.ID},
		} {
			t.Run(name, func(t *testing.T) {
				if rec := putVarOrder(ids...); rec.Code != http.StatusBadRequest {
					t.Fatalf("status = %d, want %d (body: %s)", rec.Code, http.StatusBadRequest, rec.Body)
				}
			})
		}
	})

	t.Run("variable order: 0-based and contiguous", func(t *testing.T) {
		if rec := putVarOrder(c.ID, a.ID, b.ID); rec.Code != http.StatusNoContent {
			t.Fatalf("status = %d, want %d (body: %s)", rec.Code, http.StatusNoContent, rec.Body)
		}
		if keys, orders := varOrders(t); strings.Join(keys, ",") != "C,A,B" || !equalOrders(orders, 0, 1, 2) {
			t.Errorf("variables = %v %v, want C,A,B at 0,1,2", keys, orders)
		}
	})
}

func equalOrders(got []int32, want ...int32) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

func TestEnvironmentCascades(t *testing.T) {
	f, owner, project, dev := newEnvironmentProject(t, "env-cascade-owner")
	staging := f.createEnvironment(t, owner.Token, project.ID, "staging")

	f.createVariable(t, owner.Token, dev.ID, map[string]any{"key": "A", "value": "1"})
	f.createVariable(t, owner.Token, dev.ID, map[string]any{"key": "B", "type": "secret"})
	f.createVariable(t, owner.Token, staging.ID, map[string]any{"key": "C", "value": "3"})

	t.Run("deleting an environment removes its variables only", func(t *testing.T) {
		if rec := do(t, f.handler, http.MethodDelete, "/api/v1/environments/"+dev.ID, "", owner.Token); rec.Code != http.StatusNoContent {
			t.Fatalf("status = %d, want %d (body: %s)", rec.Code, http.StatusNoContent, rec.Body)
		}
		if count := f.countRows(t, "SELECT count(*) FROM environment_variables WHERE environment_id = $1", dev.ID); count != 0 {
			t.Errorf("variables of the deleted environment = %d, want 0", count)
		}
		if count := f.countRows(t, "SELECT count(*) FROM environment_variables"); count != 1 {
			t.Errorf("variables left = %d, want 1 (staging's)", count)
		}
	})

	t.Run("deleting the project removes its environments and variables", func(t *testing.T) {
		if rec := do(t, f.handler, http.MethodDelete, "/api/v1/projects/"+project.ID, "", owner.Token); rec.Code != http.StatusNoContent {
			t.Fatalf("status = %d, want %d (body: %s)", rec.Code, http.StatusNoContent, rec.Body)
		}
		if count := f.countRows(t, "SELECT count(*) FROM environments"); count != 0 {
			t.Errorf("environments after project delete = %d, want 0", count)
		}
		if count := f.countRows(t, "SELECT count(*) FROM environment_variables"); count != 0 {
			t.Errorf("variables after project delete = %d, want 0", count)
		}
	})
}

// TestEnvironmentAccessMatrix reuses the project access fixture: every endpoint
// answers 404 to anyone who cannot reach the project, with the same message a
// nonexistent id gets, and works for anyone who can.
func TestEnvironmentAccessMatrix(t *testing.T) {
	f := newAccessMatrixFixture(t)

	hiddenEnv := f.createEnvironment(t, f.owner.Token, f.ownerProject.ID, "hidden")
	hiddenVar := f.createVariable(t, f.owner.Token, hiddenEnv.ID, map[string]any{"key": "K", "value": "v"})

	type request struct {
		name, method, path, body, message string
	}

	denied := func(projectID, envID, varID string) []request {
		return []request{
			{"create environment", http.MethodPost, "/api/v1/projects/" + projectID + "/environments", `{"name":"x"}`, messageProjectNotFound},
			{"list environments", http.MethodGet, "/api/v1/projects/" + projectID + "/environments", "", messageProjectNotFound},
			{"order environments", http.MethodPut, "/api/v1/environments/order",
				`{"project_id":"` + projectID + `","environment_ids":["` + envID + `"]}`, messageProjectNotFound},
			{"rename environment", http.MethodPatch, "/api/v1/environments/" + envID, `{"name":"x"}`, messageEnvironmentNotFound},
			{"delete environment", http.MethodDelete, "/api/v1/environments/" + envID, "", messageEnvironmentNotFound},
			{"create variable", http.MethodPost, "/api/v1/environments/" + envID + "/variables", `{"key":"x"}`, messageEnvironmentNotFound},
			{"list variables", http.MethodGet, "/api/v1/environments/" + envID + "/variables", "", messageEnvironmentNotFound},
			{"order variables", http.MethodPut, "/api/v1/variables/order",
				`{"environment_id":"` + envID + `","variable_ids":["` + varID + `"]}`, messageEnvironmentNotFound},
			{"update variable", http.MethodPatch, "/api/v1/variables/" + varID, `{"value":"x"}`, messageVariableNotFound},
			{"delete variable", http.MethodDelete, "/api/v1/variables/" + varID, "", messageVariableNotFound},
		}
	}

	callers := []struct {
		name  string
		token string
	}{
		{name: "restricted member without a grant", token: f.limited.Token},
		{name: "outsider", token: f.outsider.Token},
	}

	for _, caller := range callers {
		t.Run(caller.name, func(t *testing.T) {
			for _, req := range denied(f.ownerProject.ID, hiddenEnv.ID, hiddenVar.ID) {
				t.Run(req.name, func(t *testing.T) {
					rec := do(t, f.handler, req.method, req.path, req.body, caller.token)
					if rec.Code != http.StatusNotFound {
						t.Fatalf("status = %d, want %d (body: %s)", rec.Code, http.StatusNotFound, rec.Body)
					}
					if msg := decodeEnvelope(t, rec).Error.Message; msg != req.message {
						t.Errorf("message = %q, want %q", msg, req.message)
					}
				})
			}
		})
	}

	// Nonexistent ids get exactly the same answers, even for the team owner.
	t.Run("nonexistent ids look the same", func(t *testing.T) {
		for _, req := range denied(uuid.New().String(), uuid.New().String(), uuid.New().String()) {
			t.Run(req.name, func(t *testing.T) {
				rec := do(t, f.handler, req.method, req.path, req.body, f.owner.Token)
				if rec.Code != http.StatusNotFound {
					t.Fatalf("status = %d, want %d (body: %s)", rec.Code, http.StatusNotFound, rec.Body)
				}
				if msg := decodeEnvelope(t, rec).Error.Message; msg != req.message {
					t.Errorf("message = %q, want %q", msg, req.message)
				}
			})
		}
	})

	// None of the denied calls changed anything.
	if variables := f.listVariables(t, f.owner.Token, hiddenEnv.ID); len(variables) != 1 || *variables[0].Value != "v" {
		t.Errorf("hidden variables = %+v, want the untouched K=v", variables)
	}

	t.Run("any member with access has full rights", func(t *testing.T) {
		// Restricted, holding a grant, managing neither project nor team.
		token := f.limited.Token
		projectID := f.grantedProject.ID

		env := f.createEnvironment(t, token, projectID, "mine")
		if rec := do(t, f.handler, http.MethodPatch, "/api/v1/environments/"+env.ID, `{"name":"renamed"}`, token); rec.Code != http.StatusOK {
			t.Errorf("rename environment: status = %d (body: %s)", rec.Code, rec.Body)
		}
		if got := len(f.listEnvironments(t, token, projectID)); got != 1 {
			t.Errorf("list environments: %d, want 1", got)
		}
		if rec := do(t, f.handler, http.MethodPut, "/api/v1/environments/order",
			folderBody(t, map[string]any{"project_id": projectID, "environment_ids": []string{env.ID}}), token); rec.Code != http.StatusNoContent {
			t.Errorf("order environments: status = %d (body: %s)", rec.Code, rec.Body)
		}

		variable := f.createVariable(t, token, env.ID, map[string]any{"key": "K", "value": "v"})
		if rec := f.patchVariable(t, token, variable.ID, `{"value":"w"}`); rec.Code != http.StatusOK {
			t.Errorf("update variable: status = %d (body: %s)", rec.Code, rec.Body)
		}
		if got := len(f.listVariables(t, token, env.ID)); got != 1 {
			t.Errorf("list variables: %d, want 1", got)
		}
		if rec := do(t, f.handler, http.MethodPut, "/api/v1/variables/order",
			folderBody(t, map[string]any{"environment_id": env.ID, "variable_ids": []string{variable.ID}}), token); rec.Code != http.StatusNoContent {
			t.Errorf("order variables: status = %d (body: %s)", rec.Code, rec.Body)
		}
		if rec := do(t, f.handler, http.MethodDelete, "/api/v1/variables/"+variable.ID, "", token); rec.Code != http.StatusNoContent {
			t.Errorf("delete variable: status = %d (body: %s)", rec.Code, rec.Body)
		}
		if rec := do(t, f.handler, http.MethodDelete, "/api/v1/environments/"+env.ID, "", token); rec.Code != http.StatusNoContent {
			t.Errorf("delete environment: status = %d (body: %s)", rec.Code, rec.Body)
		}
	})
}

func TestEnvironmentAndVariableValidation(t *testing.T) {
	f, owner, project, env := newEnvironmentProject(t, "env-validation-owner")
	variable := f.createVariable(t, owner.Token, env.ID, map[string]any{"key": "K", "value": "v"})

	tests := []struct {
		name   string
		method string
		path   string
		body   string
	}{
		{name: "environment name empty", method: http.MethodPost, path: "/api/v1/projects/" + project.ID + "/environments", body: `{"name":"  "}`},
		{name: "environment name too long", method: http.MethodPost, path: "/api/v1/projects/" + project.ID + "/environments",
			body: `{"name":"` + strings.Repeat("x", 101) + `"}`},
		{name: "rename to empty", method: http.MethodPatch, path: "/api/v1/environments/" + env.ID, body: `{"name":""}`},
		{name: "variable key empty", method: http.MethodPost, path: "/api/v1/environments/" + env.ID + "/variables", body: `{"key":" "}`},
		{name: "variable key too long", method: http.MethodPost, path: "/api/v1/environments/" + env.ID + "/variables",
			body: `{"key":"` + strings.Repeat("k", 201) + `"}`},
		{name: "unknown variable type", method: http.MethodPost, path: "/api/v1/environments/" + env.ID + "/variables", body: `{"key":"X","type":"password"}`},
		{name: "patch with nothing to change", method: http.MethodPatch, path: "/api/v1/variables/" + variable.ID, body: `{}`},
		{name: "patch value not a string", method: http.MethodPatch, path: "/api/v1/variables/" + variable.ID, body: `{"value":42}`},
		{name: "patch unknown type", method: http.MethodPatch, path: "/api/v1/variables/" + variable.ID, body: `{"type":"password"}`},
		{name: "patch key empty", method: http.MethodPatch, path: "/api/v1/variables/" + variable.ID, body: `{"key":""}`},
		{name: "create environment bad project id", method: http.MethodPost, path: "/api/v1/projects/not-a-uuid/environments", body: `{"name":"x"}`},
		{name: "list environments bad project id", method: http.MethodGet, path: "/api/v1/projects/not-a-uuid/environments"},
		{name: "rename bad id", method: http.MethodPatch, path: "/api/v1/environments/not-a-uuid", body: `{"name":"x"}`},
		{name: "delete environment bad id", method: http.MethodDelete, path: "/api/v1/environments/not-a-uuid"},
		{name: "create variable bad env id", method: http.MethodPost, path: "/api/v1/environments/not-a-uuid/variables", body: `{"key":"x"}`},
		{name: "list variables bad env id", method: http.MethodGet, path: "/api/v1/environments/not-a-uuid/variables"},
		{name: "patch variable bad id", method: http.MethodPatch, path: "/api/v1/variables/not-a-uuid", body: `{"value":"x"}`},
		{name: "delete variable bad id", method: http.MethodDelete, path: "/api/v1/variables/not-a-uuid"},
		{name: "environment order bad project id", method: http.MethodPut, path: "/api/v1/environments/order", body: `{"project_id":"nope","environment_ids":[]}`},
		{name: "variable order bad env id", method: http.MethodPut, path: "/api/v1/variables/order", body: `{"environment_id":"nope","variable_ids":[]}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := do(t, f.handler, tt.method, tt.path, tt.body, owner.Token)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want %d (body: %s)", rec.Code, http.StatusBadRequest, rec.Body)
			}
		})
	}

	t.Run("patch value to null clears a regular variable", func(t *testing.T) {
		if rec := f.patchVariable(t, owner.Token, variable.ID, `{"value":null}`); rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d (body: %s)", rec.Code, http.StatusOK, rec.Body)
		}
		if _, value := f.storedVariable(t, variable.ID); value != nil {
			t.Errorf("stored value = %q, want NULL", *value)
		}
	})
}

func TestEnvironmentRoutesRequireAuth(t *testing.T) {
	f := newTeamsFixture(t)

	id := uuid.New().String()
	tests := []struct {
		method string
		path   string
	}{
		{http.MethodPost, "/api/v1/projects/" + id + "/environments"},
		{http.MethodGet, "/api/v1/projects/" + id + "/environments"},
		{http.MethodPut, "/api/v1/environments/order"},
		{http.MethodPatch, "/api/v1/environments/" + id},
		{http.MethodDelete, "/api/v1/environments/" + id},
		{http.MethodPost, "/api/v1/environments/" + id + "/variables"},
		{http.MethodGet, "/api/v1/environments/" + id + "/variables"},
		{http.MethodPut, "/api/v1/variables/order"},
		{http.MethodPatch, "/api/v1/variables/" + id},
		{http.MethodDelete, "/api/v1/variables/" + id},
	}

	for _, tt := range tests {
		t.Run(tt.method+" "+tt.path, func(t *testing.T) {
			rec := do(t, f.handler, tt.method, tt.path, "", "")
			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want %d (body: %s)", rec.Code, http.StatusUnauthorized, rec.Body)
			}
		})
	}
}
