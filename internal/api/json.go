package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
)

// maxRequestBody caps how much JSON the server will read from a single request.
const maxRequestBody = 1 << 20 // 1 MiB

// errorEnvelope is the single shape every error response takes:
//
//	{"error": {"code": "not_found", "message": "..."}}
type errorEnvelope struct {
	Error errorBody `json:"error"`
}

type errorBody struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// Error codes returned by the scaffold. Handlers should reuse these rather than
// inventing strings at the call site.
const (
	codeBadRequest   = "bad_request"
	codeUnauthorized = "unauthorized"
	codeForbidden    = "forbidden"
	codeNotFound     = "not_found"
	codeConflict     = "conflict"
	codeInternal     = "internal_error"
	codeUnavailable  = "service_unavailable"

	messageInternal  = "the server encountered an unexpected problem"
	messageDBUnready = "database is not reachable"

	messageAuthRequired = "authentication required"
	messageEmailTaken   = "an account with that email already exists"

	// messageTeamNotFound is also what a non-member sees, so that team
	// existence is not observable from outside the team.
	messageTeamNotFound = "team not found"
	messageNotTeamOwner = "only the team owner can do that"

	// messageInvitationNotFound covers a missing invitation, one addressed to
	// somebody else, and one belonging to a team the caller does not own.
	messageInvitationNotFound = "invitation not found"
	messageInvitationAnswered = "this invitation has already been answered"
	messageInvitationPending  = "an invitation for that email is already pending"
	messageAlreadyMember      = "that email is already a member of this team"
	messageCannotInviteSelf   = "you cannot invite yourself"

	messageMemberNotFound    = "team member not found"
	messageCannotRemoveOwner = "the team owner cannot be removed; transfer ownership first"

	// messageProjectNotFound is also what a member without access to this
	// particular project sees, so project existence is not observable.
	messageProjectNotFound  = "project not found"
	messageNotProjectOwner  = "only the team owner or the project owner can do that"
	messageOwnerAccessFixed = "the team owner always has access to every project"

	// messageFolderNotFound also covers a folder in a project the caller
	// cannot reach, so folder ids are not observable either.
	messageFolderNotFound = "folder not found"

	// As with folders, these also cover an environment or variable in a
	// project the caller cannot reach.
	messageEnvironmentNotFound  = "environment not found"
	messageVariableNotFound     = "variable not found"
	messageEnvironmentNameTaken = "an environment with that name already exists in this project"
	messageVariableKeyTaken     = "a variable with that key already exists in this environment"
	messageInvalidVariableType  = `type must be "regular" or "secret"`
	messageRequestNotFound      = "request not found"
	messageSecretValue          = "secret values are never stored on the server; keep them in the desktop client"

	//nolint:gosec // G101: a message shown to clients, not a credential.
	messageInvalidToken = "invalid or expired token"

	// messageBadCredentials is returned for both an unknown email and a wrong
	// password, so responses never reveal whether an account exists.
	messageBadCredentials = "invalid email or password"
)

// writeJSON serialises v as JSON with the given status code. A failure to
// encode is logged rather than returned: by the time encoding starts the status
// line is already on the wire.
func (a *api) writeJSON(w http.ResponseWriter, r *http.Request, status int, v any) {
	buf, err := json.Marshal(v)
	if err != nil {
		a.logger.ErrorContext(r.Context(), "encoding response body",
			slog.Any("error", err),
			slog.String("path", r.URL.Path),
		)
		http.Error(w, `{"error":{"code":"internal_error","message":"response encoding failed"}}`, http.StatusInternalServerError)
		return
	}

	buf = append(buf, '\n')

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)

	if _, err := w.Write(buf); err != nil {
		a.logger.WarnContext(r.Context(), "writing response body",
			slog.Any("error", err),
			slog.String("path", r.URL.Path),
		)
	}
}

// writeError sends an error envelope with the given status and code.
func (a *api) writeError(w http.ResponseWriter, r *http.Request, status int, code, message string) {
	a.writeJSON(w, r, status, errorEnvelope{Error: errorBody{Code: code, Message: message}})
}

// serverError logs the underlying cause and returns an opaque 500 to the
// client, so internal details never leak into responses.
func (a *api) serverError(w http.ResponseWriter, r *http.Request, err error) {
	a.logger.ErrorContext(r.Context(), "unhandled server error",
		slog.Any("error", err),
		slog.String("method", r.Method),
		slog.String("path", r.URL.Path),
	)
	a.writeError(w, r, http.StatusInternalServerError, codeInternal, messageInternal)
}

// readJSON decodes a single JSON object from the request body into dst. It
// rejects oversized bodies, unknown fields and trailing data, and turns decoder
// errors into messages that are safe to show a client.
func readJSON(w http.ResponseWriter, r *http.Request, dst any) error {
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBody)

	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()

	if err := dec.Decode(dst); err != nil {
		return decodeError(err)
	}

	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("body must contain a single JSON value")
	}

	return nil
}

func decodeError(err error) error {
	var syntaxErr *json.SyntaxError
	var typeErr *json.UnmarshalTypeError
	var maxBytesErr *http.MaxBytesError

	switch {
	case errors.As(err, &syntaxErr):
		return fmt.Errorf("body contains malformed JSON at character %d", syntaxErr.Offset)

	case errors.Is(err, io.ErrUnexpectedEOF):
		return errors.New("body contains malformed JSON")

	case errors.As(err, &typeErr):
		if typeErr.Field != "" {
			return fmt.Errorf("body contains the wrong type for field %q", typeErr.Field)
		}
		return fmt.Errorf("body contains the wrong type at character %d", typeErr.Offset)

	case errors.Is(err, io.EOF):
		return errors.New("body must not be empty")

	case errors.As(err, &maxBytesErr):
		return fmt.Errorf("body must not be larger than %d bytes", maxBytesErr.Limit)

	case strings.HasPrefix(err.Error(), "json: unknown field "):
		field := strings.TrimPrefix(err.Error(), "json: unknown field ")
		return fmt.Errorf("body contains unknown field %s", field)

	default:
		return err
	}
}
