package api

import (
	"net/http"
	"strings"
	"testing"
)

func TestMultipartBodyRoundTrip(t *testing.T) {
	f, owner, _, project := newFolderProject(t, "multipart-roundtrip")
	request := f.createRequest(t, owner.Token, project.ID, map[string]any{"name": "Upload", "method": "POST"})

	// Both kinds, duplicate keys, {{vars}}, a disabled row, an empty file
	// name (no file picked yet), padded key: stored exactly as sent, in order.
	body := `{"type":"multipart","fields":[
		{"key":"note","kind":"text","value":"hello {{user}}","enabled":true},
		{"key":"avatar","kind":"file","file_name":"me.png","enabled":true},
		{"key":"tag","kind":"text","value":"a","enabled":true},
		{"key":"tag","kind":"text","value":"","enabled":false},
		{"key":"later","kind":"file","file_name":"","enabled":false},
		{"key":" padded ","kind":"file","file_name":"report final (2).pdf","enabled":true}
	]}`

	rec := f.patchBody(t, owner.Token, request.ID, body)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", rec.Code, rec.Body)
	}
	assertSameJSON(t, "PATCH response body", decodeRequestDetail(t, rec.Body.Bytes()).Body, body)
	assertSameJSON(t, "GET body", f.getRequestDetail(t, owner.Token, request.ID).Body, body)

	t.Run("switching to form and back replaces cleanly", func(t *testing.T) {
		form := `{"type":"form","fields":[{"key":"a","value":"1","enabled":true}]}`
		f.patchBody(t, owner.Token, request.ID, form)
		assertSameJSON(t, "form body", f.getRequestDetail(t, owner.Token, request.ID).Body, form)

		empty := `{"type":"multipart","fields":[]}`
		f.patchBody(t, owner.Token, request.ID, empty)
		assertSameJSON(t, "empty multipart body", f.getRequestDetail(t, owner.Token, request.ID).Body, empty)
	})

	t.Run("form rows still reject a kind", func(t *testing.T) {
		rec := f.patchBody(t, owner.Token, request.ID, `{"type":"form","fields":[{"key":"a","kind":"text","value":"1","enabled":true}]}`)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("form row with kind: status = %d, want 400", rec.Code)
		}
	})
}

func TestMultipartBodyValidation(t *testing.T) {
	f, owner, _, project := newFolderProject(t, "multipart-validation")
	request := f.createRequest(t, owner.Token, project.ID, map[string]any{"name": "Upload"})

	text := `{"key":"k","kind":"text","value":"v","enabled":true}`
	file := `{"key":"f","kind":"file","file_name":"a.png","enabled":true}`
	multipart := func(rows ...string) string {
		return `{"type":"multipart","fields":[` + strings.Join(rows, ",") + `]}`
	}

	tests := []struct {
		name    string
		body    string
		message string
	}{
		// The point of the file kind: no content, ever.
		{"file with a value", multipart(text, `{"key":"f","kind":"file","file_name":"a.png","value":"BYTES","enabled":true}`),
			`body.fields[1].value is not a known field for a "file" row`},
		{"file with content", multipart(`{"key":"f","kind":"file","file_name":"a.png","content":"BYTES","enabled":true}`),
			`body.fields[0].content is not a known field for a "file" row`},
		{"file with a path", multipart(`{"key":"f","kind":"file","file_name":"a.png","path":"C:\\x\\a.png","enabled":true}`),
			`body.fields[0].path is not a known field for a "file" row`},
		{"text with a file name", multipart(`{"key":"k","kind":"text","value":"v","file_name":"a.png","enabled":true}`),
			`body.fields[0].file_name is not a known field for a "text" row`},
		{"file missing its file name", multipart(`{"key":"f","kind":"file","enabled":true}`), "body.fields[0].file_name is required"},
		{"text missing its value", multipart(file, `{"key":"k","kind":"text","enabled":true}`), "body.fields[1].value is required"},
		{"kind missing", multipart(`{"key":"k","value":"v","enabled":true}`), "body.fields[0].kind is required"},
		{"kind unknown", multipart(`{"key":"k","kind":"binary","value":"v","enabled":true}`), "body.fields[0].kind must be one of text, file"},
		{"kind not a string", multipart(`{"key":"k","kind":1,"value":"v","enabled":true}`), "body.fields[0].kind must be a string"},
		{"empty key", multipart(text, text, `{"key":"  ","kind":"file","file_name":"","enabled":true}`), "body.fields[2].key is required"},
		{"key too long", multipart(`{"key":"` + strings.Repeat("k", 201) + `","kind":"text","value":"","enabled":true}`),
			"body.fields[0].key must be at most 200 characters"},
		{"value too long", multipart(`{"key":"k","kind":"text","value":"` + strings.Repeat("v", 8193) + `","enabled":true}`),
			"body.fields[0].value must be at most 8192 characters"},
		{"file name too long", multipart(`{"key":"f","kind":"file","file_name":"` + strings.Repeat("n", 256) + `","enabled":true}`),
			"body.fields[0].file_name must be at most 255 characters"},
		{"enabled missing", multipart(`{"key":"f","kind":"file","file_name":"a"}`), "body.fields[0].enabled is required"},
		{"enabled not a boolean", multipart(`{"key":"f","kind":"file","file_name":"a","enabled":"yes"}`), "body.fields[0].enabled must be a boolean"},
		{"row not an object", multipart(text, `"note"`), "body.fields[1] must be an object"},
		{"fields not an array", `{"type":"multipart","fields":{}}`, "body.fields must be an array"},
		{"fields null", `{"type":"multipart","fields":null}`, "body.fields must be an array"},
		{"fields missing", `{"type":"multipart"}`, "body.fields is required"},
		{"multipart with content", `{"type":"multipart","fields":[],"content":"x"}`, `body.content is not a known field for a "multipart" body`},
		{"101 rows", multipart(strings.Split(strings.TrimSuffix(strings.Repeat(text+"|", 101), "|"), "|")...), "body.fields must have at most 100 rows"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := f.patchBody(t, owner.Token, request.ID, tt.body)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400 (body: %.200s)", rec.Code, rec.Body)
			}
			if msg := decodeEnvelope(t, rec).Error.Message; !strings.HasPrefix(msg, tt.message) {
				t.Errorf("message = %q, want prefix %q", msg, tt.message)
			}
		})
	}

	// Nothing refused above was written.
	assertSameJSON(t, "body after refused patches", f.getRequestDetail(t, owner.Token, request.ID).Body, `{"type":"none"}`)

	t.Run("the limits themselves are accepted", func(t *testing.T) {
		rows := strings.Split(strings.TrimSuffix(strings.Repeat(file+"|", 100), "|"), "|")
		rows[0] = `{"key":"` + strings.Repeat("k", 200) + `","kind":"file","file_name":"` + strings.Repeat("é", 255) + `","enabled":false}`
		if rec := f.patchBody(t, owner.Token, request.ID, multipart(rows...)); rec.Code != http.StatusOK {
			t.Errorf("status = %d, want 200 (body: %.200s)", rec.Code, rec.Body)
		}
	})
}
