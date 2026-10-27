package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/komiljonov/ghostman/internal/db"
)

// Authorization is a cascading per-node setting on folders and requests, like
// follow_redirects: the server stores each node's values and the client
// resolves the chain. auth.type "inherit" defers to the parent (the end of the
// chain is no auth); "none" is explicitly no auth and stops the chain.
//
// The value fields are opaque and may hold {{variables}}. A real credential is
// meant to live in a secret variable referenced as {{name}}, whose value never
// reaches the server; a literal typed in here is stored and synced like any
// other field. Fields not matching the type are kept, not cleared, so that
// switching the type back restores them.
//
// "jwt" is a token the CLIENT builds and signs at send time from the stored
// template (algorithm, secret, payload, placement). The server never signs and
// never sees a resolved secret or payload.

// maxAuthValueLength bounds every auth string field, in characters. For the
// JWT payload it matches the <table>_auth_jwt_payload_length CHECK.
const maxAuthValueLength = 8192

// nodeAuthResponse is the full stored auth configuration of a node (not to be
// confused with authResponse, the login reply).
type nodeAuthResponse struct {
	Type          string `json:"type"`
	BearerToken   string `json:"bearer_token"`
	BasicUsername string `json:"basic_username"`
	BasicPassword string `json:"basic_password"`
	APIKeyName    string `json:"api_key_name"`
	APIKeyValue   string `json:"api_key_value"`
	APIKeyIn      string `json:"api_key_in"`

	JWTAlgorithm    string `json:"jwt_algorithm"`
	JWTSecret       string `json:"jwt_secret"`
	JWTSecretBase64 bool   `json:"jwt_secret_base64"`
	JWTPayload      string `json:"jwt_payload"`
	JWTAddTo        string `json:"jwt_add_to"`
	JWTName         string `json:"jwt_name"`
	JWTPrefix       string `json:"jwt_prefix"`
}

// authFromFolder, authFromRequest and authFromRequestRow read a node's auth_*
// columns into the response shape.
func authFromFolder(f db.Folder) nodeAuthResponse {
	return nodeAuthResponse{
		Type: f.AuthType, BearerToken: f.AuthBearerToken,
		BasicUsername: f.AuthBasicUsername, BasicPassword: f.AuthBasicPassword,
		APIKeyName: f.AuthApiKeyName, APIKeyValue: f.AuthApiKeyValue, APIKeyIn: f.AuthApiKeyIn,
		JWTAlgorithm: f.AuthJwtAlgorithm, JWTSecret: f.AuthJwtSecret, JWTSecretBase64: f.AuthJwtSecretBase64,
		JWTPayload: f.AuthJwtPayload, JWTAddTo: f.AuthJwtAddTo, JWTName: f.AuthJwtName, JWTPrefix: f.AuthJwtPrefix,
	}
}

func authFromRequest(r db.Request) nodeAuthResponse {
	return nodeAuthResponse{
		Type: r.AuthType, BearerToken: r.AuthBearerToken,
		BasicUsername: r.AuthBasicUsername, BasicPassword: r.AuthBasicPassword,
		APIKeyName: r.AuthApiKeyName, APIKeyValue: r.AuthApiKeyValue, APIKeyIn: r.AuthApiKeyIn,
		JWTAlgorithm: r.AuthJwtAlgorithm, JWTSecret: r.AuthJwtSecret, JWTSecretBase64: r.AuthJwtSecretBase64,
		JWTPayload: r.AuthJwtPayload, JWTAddTo: r.AuthJwtAddTo, JWTName: r.AuthJwtName, JWTPrefix: r.AuthJwtPrefix,
	}
}

func authFromRequestRow(r db.ListRequestsByProjectRow) nodeAuthResponse {
	return nodeAuthResponse{
		Type: r.AuthType, BearerToken: r.AuthBearerToken,
		BasicUsername: r.AuthBasicUsername, BasicPassword: r.AuthBasicPassword,
		APIKeyName: r.AuthApiKeyName, APIKeyValue: r.AuthApiKeyValue, APIKeyIn: r.AuthApiKeyIn,
		JWTAlgorithm: r.AuthJwtAlgorithm, JWTSecret: r.AuthJwtSecret, JWTSecretBase64: r.AuthJwtSecretBase64,
		JWTPayload: r.AuthJwtPayload, JWTAddTo: r.AuthJwtAddTo, JWTName: r.AuthJwtName, JWTPrefix: r.AuthJwtPrefix,
	}
}

// authPatch is a validated auth update. Type is always set; every other field
// is nil unless the request sent it, and nil keeps the stored value.
type authPatch struct {
	Type          *string
	BearerToken   *string
	BasicUsername *string
	BasicPassword *string
	APIKeyName    *string
	APIKeyValue   *string
	APIKeyIn      *string

	JWTAlgorithm    *string
	JWTSecret       *string
	JWTSecretBase64 *bool
	JWTPayload      *string
	JWTAddTo        *string
	JWTName         *string
	JWTPrefix       *string
}

// applyToRequest and applyToFolder copy the patch into a partial update's
// Auth* parameters. A nil patch leaves them nil: nothing changes.
func (p *authPatch) applyToRequest(params *db.UpdateRequestParams) {
	if p == nil {
		return
	}
	params.AuthType, params.AuthBearerToken = p.Type, p.BearerToken
	params.AuthBasicUsername, params.AuthBasicPassword = p.BasicUsername, p.BasicPassword
	params.AuthApiKeyName, params.AuthApiKeyValue, params.AuthApiKeyIn = p.APIKeyName, p.APIKeyValue, p.APIKeyIn
	params.AuthJwtAlgorithm, params.AuthJwtSecret, params.AuthJwtSecretBase64 = p.JWTAlgorithm, p.JWTSecret, p.JWTSecretBase64
	params.AuthJwtPayload, params.AuthJwtAddTo = p.JWTPayload, p.JWTAddTo
	params.AuthJwtName, params.AuthJwtPrefix = p.JWTName, p.JWTPrefix
}

func (p *authPatch) applyToFolder(params *db.UpdateFolderParams) {
	if p == nil {
		return
	}
	params.AuthType, params.AuthBearerToken = p.Type, p.BearerToken
	params.AuthBasicUsername, params.AuthBasicPassword = p.BasicUsername, p.BasicPassword
	params.AuthApiKeyName, params.AuthApiKeyValue, params.AuthApiKeyIn = p.APIKeyName, p.APIKeyValue, p.APIKeyIn
	params.AuthJwtAlgorithm, params.AuthJwtSecret, params.AuthJwtSecretBase64 = p.JWTAlgorithm, p.JWTSecret, p.JWTSecretBase64
	params.AuthJwtPayload, params.AuthJwtAddTo = p.JWTPayload, p.JWTAddTo
	params.AuthJwtName, params.AuthJwtPrefix = p.JWTName, p.JWTPrefix
}

// parseAuthPatch validates a PATCH body's auth object. Absent or null returns
// nil: the stored auth is unchanged. Errors name the path, e.g.
// "auth.type is required".
func parseAuthPatch(raw json.RawMessage) (*authPatch, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}

	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
		return nil, errors.New("auth must be an object with a type")
	}

	var patch authPatch
	stringFields := map[string]**string{
		"type":           &patch.Type,
		"bearer_token":   &patch.BearerToken,
		"basic_username": &patch.BasicUsername,
		"basic_password": &patch.BasicPassword,
		"api_key_name":   &patch.APIKeyName,
		"api_key_value":  &patch.APIKeyValue,
		"api_key_in":     &patch.APIKeyIn,
		"jwt_algorithm":  &patch.JWTAlgorithm,
		"jwt_secret":     &patch.JWTSecret,
		"jwt_payload":    &patch.JWTPayload,
		"jwt_add_to":     &patch.JWTAddTo,
		"jwt_name":       &patch.JWTName,
		"jwt_prefix":     &patch.JWTPrefix,
	}
	boolFields := map[string]**bool{
		"jwt_secret_base64": &patch.JWTSecretBase64,
	}

	// Sorted, so the same bad object always yields the same message.
	names := make([]string, 0, len(fields))
	for name := range fields {
		names = append(names, name)
	}
	slices.Sort(names)

	for _, name := range names {
		raw := fields[name]

		if target, isBool := boolFields[name]; isBool {
			var value bool
			// null would otherwise decode silently as false.
			if string(raw) == "null" || json.Unmarshal(raw, &value) != nil {
				return nil, fmt.Errorf("auth.%s must be a boolean", name)
			}
			*target = &value
			continue
		}

		target, known := stringFields[name]
		if !known {
			return nil, fmt.Errorf("auth.%s is not a known field", name)
		}

		var value string
		// null would otherwise decode silently as "".
		if string(raw) == "null" || json.Unmarshal(raw, &value) != nil {
			return nil, fmt.Errorf("auth.%s must be a string", name)
		}
		if utf8.RuneCountInString(value) > maxAuthValueLength {
			return nil, fmt.Errorf("auth.%s must be at most %d characters", name, maxAuthValueLength)
		}
		*target = &value
	}

	if patch.Type == nil {
		return nil, fmt.Errorf("auth.type is required and must be one of %s", joinValues(db.AuthTypes))
	}

	for _, enum := range []struct {
		name    string
		value   *string
		allowed []string
	}{
		{"type", patch.Type, db.AuthTypes},
		{"api_key_in", patch.APIKeyIn, db.APIKeyPlacements},
		{"jwt_algorithm", patch.JWTAlgorithm, db.JWTAlgorithms},
		{"jwt_add_to", patch.JWTAddTo, db.JWTPlacements},
	} {
		if enum.value != nil && !slices.Contains(enum.allowed, *enum.value) {
			return nil, fmt.Errorf("auth.%s must be one of %s", enum.name, joinValues(enum.allowed))
		}
	}

	return &patch, nil
}

// joinValues lists allowed values for an error message.
func joinValues(values []string) string {
	return strings.Join(values, ", ")
}
