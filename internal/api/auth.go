package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/mail"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/komiljonov/ghostman/internal/auth"
	"github.com/komiljonov/ghostman/internal/db"
)

// Input limits. The minimum password length is the only one the product cares
// about; the maxima exist to keep absurd input out of the database.
const (
	minPasswordLength = 8
	maxPasswordLength = 512
	maxEmailLength    = 254
	maxNameLength     = 200
)

// uniqueViolation is the PostgreSQL SQLSTATE for a unique-constraint breach.
const uniqueViolation = "23505"

// Nothing in this file logs a request body, an Authorization header or a
// password: credentials must never reach the logs.

type registerRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
	Name     string `json:"name"`
}

type loginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

// userResponse is the only user shape ever serialised. It has no field for
// password_hash, so a hash cannot leak into a response by accident.
type userResponse struct {
	ID    string `json:"id"`
	Email string `json:"email"`
	Name  string `json:"name"`
}

type authResponse struct {
	User  userResponse `json:"user"`
	Token string       `json:"token"`
}

func newUserResponse(user db.User) userResponse {
	return userResponse{
		ID:    user.ID.String(),
		Email: user.Email,
		Name:  user.Name,
	}
}

// handleRegister creates a user and an initial session.
func (a *api) handleRegister(w http.ResponseWriter, r *http.Request) {
	var req registerRequest
	if err := readJSON(w, r, &req); err != nil {
		a.writeBodyError(w, r, err)
		return
	}

	email := normalizeEmail(req.Email)
	name := strings.TrimSpace(req.Name)

	if err := validateEmail(email); err != nil {
		a.writeError(w, r, http.StatusBadRequest, codeBadRequest, err.Error())
		return
	}
	if err := validatePassword(req.Password); err != nil {
		a.writeError(w, r, http.StatusBadRequest, codeBadRequest, err.Error())
		return
	}
	if utf8.RuneCountInString(name) > maxNameLength {
		a.writeError(w, r, http.StatusBadRequest, codeBadRequest,
			fmt.Sprintf("name must be at most %d characters", maxNameLength))
		return
	}

	passwordHash, err := auth.HashPassword(req.Password)
	if err != nil {
		a.serverError(w, r, err)
		return
	}

	user, err := a.store.CreateUser(r.Context(), db.CreateUserParams{
		Email:        email,
		PasswordHash: passwordHash,
		Name:         name,
	})
	if err != nil {
		// The unique index is the source of truth for duplicates; pre-checking
		// with a SELECT would race with a concurrent registration.
		if isUniqueViolation(err) {
			a.writeError(w, r, http.StatusConflict, codeConflict, messageEmailTaken)
			return
		}
		a.serverError(w, r, err)
		return
	}

	// A failure here leaves the account created but without a session; the
	// client can simply log in. Not worth a transaction.
	token, err := a.issueSession(r.Context(), user.ID)
	if err != nil {
		a.serverError(w, r, err)
		return
	}

	a.writeJSON(w, r, http.StatusCreated, authResponse{
		User:  newUserResponse(user),
		Token: token,
	})
}

// handleLogin exchanges credentials for a session token. Unknown email and
// wrong password are answered identically, in both message and timing.
func (a *api) handleLogin(w http.ResponseWriter, r *http.Request) {
	var req loginRequest
	if err := readJSON(w, r, &req); err != nil {
		a.writeBodyError(w, r, err)
		return
	}

	email := normalizeEmail(req.Email)

	user, err := a.store.GetUserByEmail(r.Context(), email)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// Spend the same time as a real verification would.
			auth.DummyVerify(req.Password)
			a.writeError(w, r, http.StatusUnauthorized, codeUnauthorized, messageBadCredentials)
			return
		}
		a.serverError(w, r, err)
		return
	}

	match, err := auth.VerifyPassword(req.Password, user.PasswordHash)
	if err != nil {
		// A stored hash we cannot parse is a server-side problem, not a client
		// one. Log the user id only; never the hash or the password.
		a.serverError(w, r, fmt.Errorf("verifying password for user %s: %w", user.ID, err))
		return
	}
	if !match {
		a.writeError(w, r, http.StatusUnauthorized, codeUnauthorized, messageBadCredentials)
		return
	}

	token, err := a.issueSession(r.Context(), user.ID)
	if err != nil {
		a.serverError(w, r, err)
		return
	}

	a.writeJSON(w, r, http.StatusOK, authResponse{
		User:  newUserResponse(user),
		Token: token,
	})
}

// handleLogout deletes the session that authenticated this request.
func (a *api) handleLogout(w http.ResponseWriter, r *http.Request) {
	tokenHash, ok := sessionTokenHashFromContext(r.Context())
	if !ok {
		// Unreachable: the route is wrapped in requireAuth.
		a.serverError(w, r, errors.New("logout: no session in request context"))
		return
	}

	if err := a.store.DeleteSession(r.Context(), tokenHash); err != nil {
		a.serverError(w, r, err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// handleMe returns the authenticated user.
func (a *api) handleMe(w http.ResponseWriter, r *http.Request) {
	user, ok := UserFromContext(r.Context())
	if !ok {
		// Unreachable: the route is wrapped in requireAuth.
		a.serverError(w, r, errors.New("me: no user in request context"))
		return
	}

	a.writeJSON(w, r, http.StatusOK, userResponse{
		ID:    user.ID.String(),
		Email: user.Email,
		Name:  user.Name,
	})
}

// issueSession mints a token and stores its hash. Only the token is returned;
// the raw value is never persisted.
func (a *api) issueSession(ctx context.Context, userID uuid.UUID) (string, error) {
	token, tokenHash, err := auth.NewSessionToken()
	if err != nil {
		return "", err
	}

	if _, err := a.store.CreateSession(ctx, db.CreateSessionParams{
		TokenHash: tokenHash,
		UserID:    userID,
		ExpiresAt: time.Now().Add(auth.SessionTTL),
	}); err != nil {
		return "", fmt.Errorf("creating session: %w", err)
	}

	return token, nil
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == uniqueViolation
}

// normalizeEmail lowercases and trims, matching the CHECK constraint on the
// users table.
func normalizeEmail(raw string) string {
	return strings.ToLower(strings.TrimSpace(raw))
}

func validateEmail(email string) error {
	if email == "" {
		return errors.New("email is required")
	}

	if len(email) > maxEmailLength {
		return fmt.Errorf("email must be at most %d characters", maxEmailLength)
	}

	// ParseAddress also accepts `Display Name <a@b>`; requiring the parsed
	// address to equal the input rejects those.
	addr, err := mail.ParseAddress(email)
	if err != nil || addr.Address != email {
		return errors.New("email is not a valid address")
	}

	return nil
}

func validatePassword(password string) error {
	if utf8.RuneCountInString(password) < minPasswordLength {
		return fmt.Errorf("password must be at least %d characters", minPasswordLength)
	}

	if len(password) > maxPasswordLength {
		return fmt.Errorf("password must be at most %d bytes", maxPasswordLength)
	}

	return nil
}
