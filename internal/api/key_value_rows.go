package api

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"
)

// A request's headers and query parameters share one shape: an ordered array
// of {key, value, enabled} rows. Keys and values are opaque strings — they may
// hold {{variable}} references, which only the client resolves — and duplicate
// keys are legal, since HTTP allows repeated headers and query parameters.

// Bounds for a key/value array.
const (
	maxKeyValueRows        = 100
	maxKeyValueKeyLength   = 200
	maxKeyValueValueLength = 8192
)

// keyValueRow is one validated row, re-encoded in this fixed shape before it
// is stored.
type keyValueRow struct {
	Key     string `json:"key"`
	Value   string `json:"value"`
	Enabled bool   `json:"enabled"`
}

// keyValueRowFields lists the only fields a row may carry.
var keyValueRowFields = []string{"key", "value", "enabled"}

// parseKeyValueRows validates a headers or query_params array. Absent or null
// returns nil, which the partial update reads as "keep the current value".
// Otherwise it returns the array re-encoded from the validated rows, in the
// order given. Errors name the field and the row index, for example
// "headers[3].key is required".
func parseKeyValueRows(raw json.RawMessage, field string) (json.RawMessage, error) {
	return parseRowArray(raw, field, "{key, value, enabled}", parseKeyValueRow)
}

// parsePathParamRows validates a path_params array of {key, value} rows, with
// the same bounds, null handling and error paths as parseKeyValueRows. The
// keys come from :segments in the url, but only the client derives them; the
// server stores the rows as given.
func parsePathParamRows(raw json.RawMessage, field string) (json.RawMessage, error) {
	return parseRowArray(raw, field, "{key, value}", parsePathParamRow)
}

// parseRowArray is the array half of the row validators: absent or null is
// nil, otherwise at most maxKeyValueRows rows, each checked by parseRow under
// "field[i]" and re-encoded in order.
func parseRowArray[T any](
	raw json.RawMessage,
	field, shape string,
	parseRow func(raw json.RawMessage, path string) (T, error),
) (json.RawMessage, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}

	var items []json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil, fmt.Errorf("%s must be an array of %s rows", field, shape)
	}

	if len(items) > maxKeyValueRows {
		return nil, fmt.Errorf("%s must have at most %d rows, got %d", field, maxKeyValueRows, len(items))
	}

	rows := make([]T, 0, len(items))
	for i, item := range items {
		row, err := parseRow(item, fmt.Sprintf("%s[%d]", field, i))
		if err != nil {
			return nil, err
		}
		rows = append(rows, row)
	}

	encoded, err := json.Marshal(rows)
	if err != nil {
		return nil, fmt.Errorf("encoding %s: %w", field, err)
	}

	return encoded, nil
}

// pathParamRow is one validated path parameter row.
type pathParamRow struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

// pathParamRowFields lists the only fields a path parameter row may carry.
var pathParamRowFields = []string{"key", "value"}

// parsePathParamRow validates one {key, value} row, reporting errors under
// path.
func parsePathParamRow(raw json.RawMessage, path string) (pathParamRow, error) {
	fields, err := rowFields(raw, path, pathParamRowFields, "key and value")
	if err != nil {
		return pathParamRow{}, err
	}

	var row pathParamRow
	if row.Key, row.Value, err = rowKeyValue(fields, path); err != nil {
		return pathParamRow{}, err
	}

	return row, nil
}

// parseKeyValueRow validates one row, reporting errors under path.
func parseKeyValueRow(raw json.RawMessage, path string) (keyValueRow, error) {
	fields, err := rowFields(raw, path, keyValueRowFields, "key, value and enabled")
	if err != nil {
		return keyValueRow{}, err
	}

	var row keyValueRow
	if row.Key, row.Value, err = rowKeyValue(fields, path); err != nil {
		return keyValueRow{}, err
	}

	enabled, ok := fields["enabled"]
	if !ok {
		return keyValueRow{}, fmt.Errorf("%s.enabled is required", path)
	}
	// null would otherwise decode silently as false.
	if string(enabled) == "null" || json.Unmarshal(enabled, &row.Enabled) != nil {
		return keyValueRow{}, fmt.Errorf("%s.enabled must be a boolean", path)
	}

	return row, nil
}

// rowFields decodes a row object and refuses any field not in allowed;
// describe names the allowed fields in messages.
func rowFields(raw json.RawMessage, path string, allowed []string, describe string) (map[string]json.RawMessage, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
		return nil, fmt.Errorf("%s must be an object with %s", path, describe)
	}

	// Sorted, so the same bad row always yields the same message.
	names := make([]string, 0, len(fields))
	for name := range fields {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		if !slices.Contains(allowed, name) {
			return nil, fmt.Errorf("%s.%s is not a known field; a row has only %s", path, name, describe)
		}
	}

	return fields, nil
}

// rowKeyValue reads a row's required key (non-blank) and value (may be empty).
func rowKeyValue(fields map[string]json.RawMessage, path string) (key, value string, err error) {
	if key, err = rowString(fields, path, "key", maxKeyValueKeyLength); err != nil {
		return "", "", err
	}
	// Checked trimmed, but stored as given: keys are opaque.
	if strings.TrimSpace(key) == "" {
		return "", "", fmt.Errorf("%s.key is required", path)
	}

	if value, err = rowString(fields, path, "value", maxKeyValueValueLength); err != nil {
		return "", "", err
	}

	return key, value, nil
}

// rowString reads a required string field of a row, bounded in characters.
func rowString(fields map[string]json.RawMessage, path, name string, maxLength int) (string, error) {
	raw, ok := fields[name]
	if !ok {
		return "", fmt.Errorf("%s.%s is required", path, name)
	}

	var value string
	// null would otherwise decode silently as "".
	if string(raw) == "null" || json.Unmarshal(raw, &value) != nil {
		return "", fmt.Errorf("%s.%s must be a string", path, name)
	}

	if utf8.RuneCountInString(value) > maxLength {
		return "", fmt.Errorf("%s.%s must be at most %d characters", path, name, maxLength)
	}

	return value, nil
}
