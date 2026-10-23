package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/komiljonov/ghostman/internal/db"
)

// The proxy setting is a cascading per-node setting on folders and requests,
// like auth: one object in the API, {mode, proxy_id}, over the proxy_mode and
// proxy_id columns. The server stores each node's values; the client resolves
// the chain.
//
//	inherit - the parent's setting, up to the client's global default
//	global  - skip ancestors, use the client's global default
//	none    - direct, no proxy; stops the chain
//	proxy   - use proxy_id, a proxy of the node's own project
//
// When that proxy is deleted, proxy_id becomes null and the mode stays
// "proxy"; the client resolves such a node as "inherit".

// errProxyNotInProject is a 400: proxy_id names no proxy of this project.
var errProxyNotInProject = errors.New("proxy.proxy_id is not a proxy of this project")

// nodeProxyFields is the exact field set of the proxy object.
var nodeProxyFields = []string{"mode", "proxy_id"}

// nodeProxyResponse is a node's stored proxy setting, unresolved.
type nodeProxyResponse struct {
	Mode    string  `json:"mode"`
	ProxyID *string `json:"proxy_id"`
}

func newNodeProxyResponse(mode string, proxyID *uuid.UUID) nodeProxyResponse {
	return nodeProxyResponse{Mode: mode, ProxyID: optionalUUIDString(proxyID)}
}

// proxyPatch is a validated proxy update: a mode, and the proxy id to store
// with it (always nil unless the mode is "proxy").
type proxyPatch struct {
	Mode    string
	ProxyID *uuid.UUID
}

// values returns the patch in update-parameter form: mode, whether to write
// proxy_id at all, and the id to write. A nil patch changes nothing.
func (p *proxyPatch) values() (mode *string, setProxy bool, proxyID *uuid.UUID) {
	if p == nil {
		return nil, false, nil
	}
	return &p.Mode, true, p.ProxyID
}

// parseProxyPatch validates a PATCH body's proxy object. Absent or null
// returns nil: the stored setting is unchanged. It checks the shape only;
// that proxy_id belongs to the node's project is checked by
// requireProxyInProject.
func parseProxyPatch(raw json.RawMessage) (*proxyPatch, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}

	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
		return nil, errors.New("proxy must be an object with a mode")
	}

	names := make([]string, 0, len(fields))
	for name := range fields {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		if !slices.Contains(nodeProxyFields, name) {
			return nil, fmt.Errorf("proxy.%s is not a known field; the object has only mode and proxy_id", name)
		}
	}

	modes := strings.Join(db.ProxyModes, ", ")

	rawMode, ok := fields["mode"]
	if !ok {
		return nil, fmt.Errorf("proxy.mode is required and must be one of %s", modes)
	}
	var mode string
	if string(rawMode) == "null" || json.Unmarshal(rawMode, &mode) != nil {
		return nil, fmt.Errorf("proxy.mode must be a string, one of %s", modes)
	}
	if !slices.Contains(db.ProxyModes, mode) {
		return nil, fmt.Errorf("proxy.mode must be one of %s", modes)
	}

	proxyID, err := parseNullableUUID(fields["proxy_id"], "proxy.proxy_id")
	if err != nil {
		return nil, err
	}

	if mode == db.ProxyModeProxy && proxyID == nil {
		return nil, errors.New(`proxy.proxy_id is required when mode is "proxy"`)
	}
	// Other modes store no proxy. A non-null id there is refused rather than
	// silently dropped, so a client bug does not go unnoticed.
	if mode != db.ProxyModeProxy && proxyID != nil {
		return nil, fmt.Errorf(`proxy.proxy_id must be null or absent when mode is %q`, mode)
	}

	return &proxyPatch{Mode: mode, ProxyID: proxyID}, nil
}

// requireProxyInProject checks that a patch's proxy, if any, is a proxy of
// projectID. A proxy deleted after this check is caught by the foreign key at
// write time (db.IsProxyGone).
func (a *api) requireProxyInProject(ctx context.Context, patch *proxyPatch, projectID uuid.UUID) error {
	if patch == nil || patch.ProxyID == nil {
		return nil
	}

	proxy, err := a.store.GetProxyByID(ctx, *patch.ProxyID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return errProxyNotInProject
		}
		return err
	}
	if proxy.ProjectID != projectID {
		return errProxyNotInProject
	}

	return nil
}
