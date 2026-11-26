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
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}

	var items []json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil, fmt.Errorf("%s must be an array of {key, value, enabled} rows", field)
	}

	if len(items) > maxKeyValueRows {
		return nil, fmt.Errorf("%s must have at most %d rows, got %d", field, maxKeyValueRows, len(items))
	}

	rows := make([]keyValueRow, 0, len(items))
	for i, item := range items {
		row, err := parseKeyValueRow(item, fmt.Sprintf("%s[%d]", field, i))
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

// parseKeyValueRow validates one row, reporting errors under path.
func parseKeyValueRow(raw json.RawMessage, path string) (keyValueRow, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
		return keyValueRow{}, fmt.Errorf("%s must be an object with key, value and enabled", path)
	}

	// Sorted, so the same bad row always yields the same message.
	names := make([]string, 0, len(fields))
	for name := range fields {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		if !slices.Contains(keyValueRowFields, name) {
			return keyValueRow{}, fmt.Errorf("%s.%s is not a known field; a row has only key, value and enabled", path, name)
		}
	}

	var row keyValueRow
	var err error

	if row.Key, err = rowString(fields, path, "key", maxKeyValueKeyLength); err != nil {
		return keyValueRow{}, err
	}
	// Checked trimmed, but stored as given: keys are opaque.
	if strings.TrimSpace(row.Key) == "" {
		return keyValueRow{}, fmt.Errorf("%s.key is required", path)
	}

	if row.Value, err = rowString(fields, path, "value", maxKeyValueValueLength); err != nil {
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
