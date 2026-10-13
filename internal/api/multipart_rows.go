package api

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
)

// A multipart body's fields are rows like a form's, but each row is either
// text or a file:
//
//	{"key": "note",   "kind": "text", "value": "{{msg}}",   "enabled": true}
//	{"key": "upload", "kind": "file", "file_name": "a.png", "enabled": true}
//
// A file row carries a DISPLAY name only. The file's bytes and its path on
// disk are client-local and never reach the server, the same principle as
// secret variable values: nothing here can hold file content.

// Multipart row kinds.
const (
	multipartKindText = "text"
	multipartKindFile = "file"
)

// maxMultipartFileNameLength bounds a file row's display name.
const maxMultipartFileNameLength = 255

// multipartKinds lists the kinds in the order error messages name them.
var multipartKinds = []string{multipartKindText, multipartKindFile}

// multipartRowFields is the exact field set of each kind.
var multipartRowFields = map[string][]string{
	multipartKindText: {"key", "kind", "value", "enabled"},
	multipartKindFile: {"key", "kind", "file_name", "enabled"},
}

// The stored shapes, one per kind. Validated rows are re-encoded from these,
// so a file row cannot carry a value and a text row cannot carry a file name.
type multipartTextRow struct {
	Key     string `json:"key"`
	Kind    string `json:"kind"`
	Value   string `json:"value"`
	Enabled bool   `json:"enabled"`
}

type multipartFileRow struct {
	Key      string `json:"key"`
	Kind     string `json:"kind"`
	FileName string `json:"file_name"`
	Enabled  bool   `json:"enabled"`
}

// parseMultipartRows validates a multipart body's fields array: at most
// maxKeyValueRows rows, in order, duplicate keys allowed. Errors name the
// row, e.g. "body.fields[2].value is not a known field for a \"file\" row".
func parseMultipartRows(raw json.RawMessage, field string) (json.RawMessage, error) {
	var items []json.RawMessage
	if string(raw) == "null" || json.Unmarshal(raw, &items) != nil {
		return nil, fmt.Errorf("%s must be an array of {key, kind, value | file_name, enabled} rows", field)
	}

	if len(items) > maxKeyValueRows {
		return nil, fmt.Errorf("%s must have at most %d rows, got %d", field, maxKeyValueRows, len(items))
	}

	rows := make([]any, 0, len(items))
	for i, item := range items {
		row, err := parseMultipartRow(item, fmt.Sprintf("%s[%d]", field, i))
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

// parseMultipartRow validates one row against the field set of its kind.
func parseMultipartRow(raw json.RawMessage, path string) (any, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
		return nil, fmt.Errorf("%s must be an object with key, kind and enabled", path)
	}

	kind, err := multipartRowKind(fields, path)
	if err != nil {
		return nil, err
	}

	// Sorted, so the same bad row always yields the same message.
	names := make([]string, 0, len(fields))
	for name := range fields {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		if !slices.Contains(multipartRowFields[kind], name) {
			return nil, fmt.Errorf("%s.%s is not a known field for a %q row", path, name, kind)
		}
	}

	key, err := rowString(fields, path, "key", maxKeyValueKeyLength)
	if err != nil {
		return nil, err
	}
	// Checked trimmed, stored as given, like form and header keys.
	if strings.TrimSpace(key) == "" {
		return nil, fmt.Errorf("%s.key is required", path)
	}

	enabled, err := rowBool(fields, path, "enabled")
	if err != nil {
		return nil, err
	}

	if kind == multipartKindFile {
		// A display name only; may be empty when no file is picked yet.
		fileName, nameErr := rowString(fields, path, "file_name", maxMultipartFileNameLength)
		if nameErr != nil {
			return nil, nameErr
		}
		return multipartFileRow{Key: key, Kind: kind, FileName: fileName, Enabled: enabled}, nil
	}

	value, err := rowString(fields, path, "value", maxKeyValueValueLength)
	if err != nil {
		return nil, err
	}
	return multipartTextRow{Key: key, Kind: kind, Value: value, Enabled: enabled}, nil
}

// multipartRowKind reads a row's required kind.
func multipartRowKind(fields map[string]json.RawMessage, path string) (string, error) {
	raw, ok := fields["kind"]
	if !ok {
		return "", fmt.Errorf("%s.kind is required and must be one of %s", path, strings.Join(multipartKinds, ", "))
	}

	var kind string
	if string(raw) == "null" || json.Unmarshal(raw, &kind) != nil {
		return "", fmt.Errorf("%s.kind must be a string, one of %s", path, strings.Join(multipartKinds, ", "))
	}
	if !slices.Contains(multipartKinds, kind) {
		return "", fmt.Errorf("%s.kind must be one of %s", path, strings.Join(multipartKinds, ", "))
	}

	return kind, nil
}

// rowBool reads a required boolean field of a row.
func rowBool(fields map[string]json.RawMessage, path, name string) (bool, error) {
	raw, ok := fields[name]
	if !ok {
		return false, fmt.Errorf("%s.%s is required", path, name)
	}

	var value bool
	// null would otherwise decode silently as false.
	if string(raw) == "null" || json.Unmarshal(raw, &value) != nil {
		return false, fmt.Errorf("%s.%s must be a boolean", path, name)
	}

	return value, nil
}
