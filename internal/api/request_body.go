package api

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
)

// A request body is a tagged union on "type":
//
//	{"type": "none"}
//	{"type": "raw", "content_type": "...", "content": "..."}
//	{"type": "form", "fields": [{"key", "value", "enabled"}, ...]}
//
// Each variant has an exact set of fields, like a key/value row. The server
// does not check content against content_type: a raw body labelled JSON that
// does not parse may simply be mid-edit, and warning about it is the client's
// job. File and multipart bodies are deliberately not synced.

// Request body variants.
const (
	bodyTypeNone = "none"
	bodyTypeRaw  = "raw"
	bodyTypeForm = "form"
)

// Bounds for a raw body.
const (
	maxRawContentTypeLength = 200
	maxRawContentBytes      = 1 << 20 // 1 MiB, counted in bytes
)

// bodyTypes lists the variants in the order error messages name them.
var bodyTypes = []string{bodyTypeNone, bodyTypeRaw, bodyTypeForm}

// bodyFields is the exact field set of each variant.
var bodyFields = map[string][]string{
	bodyTypeNone: {"type"},
	bodyTypeRaw:  {"type", "content_type", "content"},
	bodyTypeForm: {"type", "fields"},
}

// The stored shapes. Validated bodies are re-encoded from these, so nothing
// outside a variant's field set can reach the database.
type noneBody struct {
	Type string `json:"type"`
}

type rawBody struct {
	Type        string `json:"type"`
	ContentType string `json:"content_type"`
	Content     string `json:"content"`
}

type formBody struct {
	Type   string          `json:"type"`
	Fields json.RawMessage `json:"fields"`
}

// parseRequestBody validates a request body. Absent or null returns nil,
// which the partial update reads as "keep the current body". Otherwise the
// whole body is replaced: switching type leaves nothing of the old variant
// behind. Errors name the path, e.g. "body.content_type is required".
func parseRequestBody(raw json.RawMessage) (json.RawMessage, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}

	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
		return nil, fmt.Errorf("body must be an object with a type of %s", strings.Join(bodyTypes, ", "))
	}

	bodyType, err := parseBodyType(fields)
	if err != nil {
		return nil, err
	}

	// Sorted, so the same bad body always yields the same message.
	names := make([]string, 0, len(fields))
	for name := range fields {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		if !slices.Contains(bodyFields[bodyType], name) {
			return nil, fmt.Errorf("body.%s is not a known field for a %q body", name, bodyType)
		}
	}

	var body any
	switch bodyType {
	case bodyTypeNone:
		body = noneBody{Type: bodyTypeNone}

	case bodyTypeRaw:
		body, err = parseRawBody(fields)

	case bodyTypeForm:
		body, err = parseFormBody(fields)
	}
	if err != nil {
		return nil, err
	}

	encoded, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("encoding body: %w", err)
	}

	return encoded, nil
}

func parseBodyType(fields map[string]json.RawMessage) (string, error) {
	raw, ok := fields["type"]
	if !ok {
		return "", fmt.Errorf("body.type is required and must be one of %s", strings.Join(bodyTypes, ", "))
	}

	var bodyType string
	if string(raw) == "null" || json.Unmarshal(raw, &bodyType) != nil {
		return "", fmt.Errorf("body.type must be a string, one of %s", strings.Join(bodyTypes, ", "))
	}

	if !slices.Contains(bodyTypes, bodyType) {
		return "", fmt.Errorf("body.type must be one of %s", strings.Join(bodyTypes, ", "))
	}

	return bodyType, nil
}

func parseRawBody(fields map[string]json.RawMessage) (rawBody, error) {
	contentType, err := rowString(fields, "body", "content_type", maxRawContentTypeLength)
	if err != nil {
		return rawBody{}, err
	}
	// Checked trimmed, stored as given; otherwise opaque.
	if strings.TrimSpace(contentType) == "" {
		return rawBody{}, fmt.Errorf("body.content_type is required")
	}

	rawContent, ok := fields["content"]
	if !ok {
		return rawBody{}, fmt.Errorf("body.content is required")
	}

	var content string
	// null would otherwise decode silently as "".
	if string(rawContent) == "null" || json.Unmarshal(rawContent, &content) != nil {
		return rawBody{}, fmt.Errorf("body.content must be a string")
	}

	// Bytes, not characters: this bound is about payload size.
	if len(content) > maxRawContentBytes {
		return rawBody{}, fmt.Errorf("body.content must be at most %d bytes", maxRawContentBytes)
	}

	return rawBody{Type: bodyTypeRaw, ContentType: contentType, Content: content}, nil
}

func parseFormBody(fields map[string]json.RawMessage) (formBody, error) {
	rawFields, ok := fields["fields"]
	if !ok {
		return formBody{}, fmt.Errorf("body.fields is required")
	}

	// parseKeyValueRows reads null as "keep"; inside a body it is just wrong.
	if string(rawFields) == "null" {
		return formBody{}, fmt.Errorf("body.fields must be an array of {key, value, enabled} rows")
	}

	rows, err := parseKeyValueRows(rawFields, "body.fields")
	if err != nil {
		return formBody{}, err
	}

	return formBody{Type: bodyTypeForm, Fields: rows}, nil
}
