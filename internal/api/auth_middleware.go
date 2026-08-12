package api

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/komiljonov/ghostman/internal/auth"
)

const bearerPrefix = "Bearer "

// AuthUser is the authenticated caller, as handlers see it.
type AuthUser struct {
	ID    uuid.UUID
	Email string
	Name  string
}

// authContext is everything RequireAuth resolves from the bearer token. It is
// stored under a single context key.
type authContext struct {
	user             AuthUser
	sessionTokenHash []byte
}

// ctxKey is unexported so no other package can collide with or overwrite the
// values RequireAuth stores.
type ctxKey struct{ name string }

var authContextKey = &ctxKey{"auth"}

// UserFromContext returns the authenticated user attached by RequireAuth. The
// second result is false on requests that did not go through RequireAuth.
func UserFromContext(ctx context.Context) (AuthUser, bool) {
	authCtx, ok := ctx.Value(authContextKey).(authContext)
	if !ok {
		return AuthUser{}, false
	}
	return authCtx.user, true
}

// sessionTokenHashFromContext returns the hash of the token that authenticated
// the request, which logout needs in order to delete exactly this session.
func sessionTokenHashFromContext(ctx context.Context) ([]byte, bool) {
	authCtx, ok := ctx.Value(authContextKey).(authContext)
	if !ok {
		return nil, false
	}
	return authCtx.sessionTokenHash, true
}

// requireAuth rejects requests without a valid, unexpired bearer token and
// attaches the caller to the request context.
func (a *api) requireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token, ok := bearerToken(r)
		if !ok {
			a.writeError(w, r, http.StatusUnauthorized, codeUnauthorized, messageAuthRequired)
			return
		}

		// The query filters on expires_at, so an expired session looks exactly
		// like an unknown one here.
		row, err := a.store.GetSessionByTokenHash(r.Context(), auth.HashToken(token))
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				a.writeError(w, r, http.StatusUnauthorized, codeUnauthorized, messageInvalidToken)
				return
			}
			a.serverError(w, r, err)
			return
		}

		ctx := context.WithValue(r.Context(), authContextKey, authContext{
			user: AuthUser{
				ID:    row.User.ID,
				Email: row.User.Email,
				Name:  row.User.Name,
			},
			sessionTokenHash: row.Session.TokenHash,
		})

		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// bearerToken extracts the token from an Authorization header. The scheme is
// matched case-insensitively, as RFC 7235 requires.
func bearerToken(r *http.Request) (string, bool) {
	header := r.Header.Get("Authorization")
	if len(header) <= len(bearerPrefix) {
		return "", false
	}

	if !strings.EqualFold(header[:len(bearerPrefix)], bearerPrefix) {
		return "", false
	}

	token := strings.TrimSpace(header[len(bearerPrefix):])
	if token == "" {
		return "", false
	}

	return token, true
}
