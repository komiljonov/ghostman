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

// maxAuthValueLength bounds every auth value field, in characters.
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
}

func newNodeAuthResponse(authType, bearerToken, basicUsername, basicPassword, apiKeyName, apiKeyValue, apiKeyIn string) nodeAuthResponse {
	return nodeAuthResponse{
		Type:          authType,
		BearerToken:   bearerToken,
		BasicUsername: basicUsername,
		BasicPassword: basicPassword,
		APIKeyName:    apiKeyName,
		APIKeyValue:   apiKeyValue,
		APIKeyIn:      apiKeyIn,
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
}

// values returns the patch's fields in column order, ready for a partial
// update's Auth* parameters. A nil patch returns all nils: nothing changes.
func (p *authPatch) values() (authType, bearerToken, basicUsername, basicPassword, apiKeyName, apiKeyValue, apiKeyIn *string) {
	if p == nil {
		return nil, nil, nil, nil, nil, nil, nil
	}
	return p.Type, p.BearerToken, p.BasicUsername, p.BasicPassword, p.APIKeyName, p.APIKeyValue, p.APIKeyIn
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
	targets := map[string]**string{
		"type":           &patch.Type,
		"bearer_token":   &patch.BearerToken,
		"basic_username": &patch.BasicUsername,
		"basic_password": &patch.BasicPassword,
		"api_key_name":   &patch.APIKeyName,
		"api_key_value":  &patch.APIKeyValue,
		"api_key_in":     &patch.APIKeyIn,
	}

	// Sorted, so the same bad object always yields the same message.
	names := make([]string, 0, len(fields))
	for name := range fields {
		names = append(names, name)
	}
	slices.Sort(names)

	for _, name := range names {
		target, known := targets[name]
		if !known {
			return nil, fmt.Errorf("auth.%s is not a known field", name)
		}

		var value string
		// null would otherwise decode silently as "".
		if string(fields[name]) == "null" || json.Unmarshal(fields[name], &value) != nil {
			return nil, fmt.Errorf("auth.%s must be a string", name)
		}
		if utf8.RuneCountInString(value) > maxAuthValueLength {
			return nil, fmt.Errorf("auth.%s must be at most %d characters", name, maxAuthValueLength)
		}
		*target = &value
	}

	if patch.Type == nil {
		return nil, fmt.Errorf("auth.type is required and must be one of %s", strings.Join(db.AuthTypes, ", "))
	}
	if !slices.Contains(db.AuthTypes, *patch.Type) {
		return nil, fmt.Errorf("auth.type must be one of %s", strings.Join(db.AuthTypes, ", "))
	}
	if patch.APIKeyIn != nil && !slices.Contains(db.APIKeyPlacements, *patch.APIKeyIn) {
		return nil, fmt.Errorf("auth.api_key_in must be one of %s", strings.Join(db.APIKeyPlacements, ", "))
	}

	return &patch, nil
}
