package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/komiljonov/ghostman/internal/db"
)

// GET /projects/{id}/variable-keys lists the distinct variable keys the
// project's requests and folders refer to, so the client can offer them (and
// spot missing ones) without fetching every request.
//
// The token grammar mirrors the client's resolver: "{{" + key + "}}", where the
// key is one or more characters with no "{" or "}", matched exactly (no
// trimming). Keys starting with "$" are generated per send ({{$uuid}} …) and
// are never environment keys, so they are left out.
//
// Scanned: url, header and query-param keys and values, a raw body's content,
// form field keys and values, multipart keys and text values (a file row's
// file_name is a display name, never resolved), and every auth field whatever
// the active type, since inactive fields are kept for switching back.
// Disabled rows count too: they still refer to the key. Scripts are not
// scanned for tokens; only the string literal passed to
// gm.environment.get/set/unset counts there.

// variableToken matches one {{key}}; the key is group 1.
var variableToken = regexp.MustCompile(`\{\{([^{}]+)\}\}`)

// scriptEnvKey matches gm.environment.get/set/unset whose first argument is a
// plain string literal: double-quoted (group 1), single-quoted (group 2) or a
// template literal without escapes or interpolation (group 3). Anything else
// (a variable, a concatenation) is not a known key and is skipped.
var scriptEnvKey = regexp.MustCompile(
	`\bgm\s*\.\s*environment\s*\.\s*(?:get|set|unset)\s*\(\s*` +
		`(?:"((?:[^"\\\n]|\\.)*)"|'((?:[^'\\\n]|\\.)*)'|` + "`([^`\\\\$]*)`" + `)`)

type variableKeysResponse struct {
	Keys []string `json:"keys"`
}

// storedBodyText is the part of a stored request body that may hold tokens.
// The body was validated on write, so the shape is known: raw has content,
// form and multipart have fields (a multipart file row has no value).
type storedBodyText struct {
	Content string `json:"content"`
	Fields  []struct {
		Key   string `json:"key"`
		Value string `json:"value"`
	} `json:"fields"`
}

// handleListVariableKeys returns the sorted, distinct variable keys used
// across the project.
func (a *api) handleListVariableKeys(w http.ResponseWriter, r *http.Request) {
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

	rows, err := a.store.ListProjectVariableSources(r.Context(), projectID)
	if err != nil {
		a.serverError(w, r, err)
		return
	}

	keys, err := collectVariableKeys(rows)
	if err != nil {
		a.serverError(w, r, err)
		return
	}

	a.writeJSON(w, r, http.StatusOK, variableKeysResponse{Keys: keys})
}

// collectVariableKeys extracts the keys from every source row, deduplicated
// and sorted. It never returns nil, so an unused project serializes as [].
func collectVariableKeys(rows []db.ListProjectVariableSourcesRow) ([]string, error) {
	seen := map[string]struct{}{}

	scanText := func(text string) {
		for _, match := range variableToken.FindAllStringSubmatch(text, -1) {
			if key := match[1]; !strings.HasPrefix(key, "$") {
				seen[key] = struct{}{}
			}
		}
	}

	for _, row := range rows {
		for _, text := range []string{
			row.Url,
			row.AuthBearerToken, row.AuthBasicUsername, row.AuthBasicPassword,
			row.AuthApiKeyName, row.AuthApiKeyValue,
			row.AuthJwtSecret, row.AuthJwtPayload, row.AuthJwtName, row.AuthJwtPrefix,
		} {
			scanText(text)
		}

		for _, list := range []struct {
			name string
			raw  json.RawMessage
		}{{"headers", row.Headers}, {"query_params", row.QueryParams}} {
			var kv []keyValueRow
			if err := json.Unmarshal(list.raw, &kv); err != nil {
				return nil, fmt.Errorf("decoding stored %s: %w", list.name, err)
			}
			for _, item := range kv {
				scanText(item.Key)
				scanText(item.Value)
			}
		}

		var body storedBodyText
		if err := json.Unmarshal(row.Body, &body); err != nil {
			return nil, fmt.Errorf("decoding stored body: %w", err)
		}
		scanText(body.Content)
		for _, field := range body.Fields {
			scanText(field.Key)
			scanText(field.Value)
		}

		for _, script := range []string{row.PreScript, row.PostScript} {
			for _, key := range scriptEnvKeys(script) {
				seen[key] = struct{}{}
			}
		}
	}

	keys := make([]string, 0, len(seen))
	for key := range seen {
		keys = append(keys, key)
	}
	slices.Sort(keys)

	return keys, nil
}

// scriptEnvKeys returns the non-empty string literals a script passes to
// gm.environment.get/set/unset. A literal whose escapes cannot be decoded is
// skipped rather than guessed at.
func scriptEnvKeys(script string) []string {
	var keys []string

	for _, match := range scriptEnvKey.FindAllStringSubmatch(script, -1) {
		// At most one group is non-empty; an empty literal yields "" from all
		// three and is skipped below.
		key, ok := match[3], true
		switch {
		case match[1] != "":
			key, ok = unquoteJS(match[1])
		case match[2] != "":
			key, ok = unquoteJS(match[2])
		}

		if ok && key != "" {
			keys = append(keys, key)
		}
	}

	return keys
}

// unquoteJS decodes the escapes of a JavaScript string literal body (without
// its quotes). It covers what a key could plausibly contain; it reports false
// for an escape it does not understand.
func unquoteJS(body string) (string, bool) {
	if !strings.Contains(body, `\`) {
		return body, true
	}

	var out strings.Builder
	for i := 0; i < len(body); i++ {
		c := body[i]
		if c != '\\' {
			out.WriteByte(c)
			continue
		}

		i++
		if i >= len(body) {
			return "", false
		}

		switch e := body[i]; e {
		case 'n':
			out.WriteByte('\n')
		case 't':
			out.WriteByte('\t')
		case 'r':
			out.WriteByte('\r')
		case '\\', '\'', '"', '`', '$', '{', '}':
			out.WriteByte(e)
		case 'u':
			if i+4 >= len(body) {
				return "", false
			}
			code, err := strconv.ParseUint(body[i+1:i+5], 16, 16)
			if err != nil {
				return "", false
			}
			out.WriteRune(rune(code))
			i += 4
		default:
			return "", false
		}
	}

	return out.String(), true
}
