package api

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/komiljonov/ghostman/internal/auth"
	"github.com/komiljonov/ghostman/internal/db"
)

// fakeStore is an in-memory Store. The scaffold has no test database, so
// handler tests run against this instead; the SQL itself is covered by the
// manual end-to-end checks documented in the README.
type fakeStore struct {
	unimplementedTeamStore

	mu           sync.Mutex
	usersByEmail map[string]db.User
	usersByID    map[uuid.UUID]db.User
	sessions     map[string]db.Session // keyed by hex(token_hash)
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		usersByEmail: make(map[string]db.User),
		usersByID:    make(map[uuid.UUID]db.User),
		sessions:     make(map[string]db.Session),
	}
}

func (f *fakeStore) CreateUser(_ context.Context, arg db.CreateUserParams) (db.User, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if _, exists := f.usersByEmail[arg.Email]; exists {
		// What PostgreSQL returns when the unique index on email is violated.
		return db.User{}, &pgconn.PgError{
			Code:           uniqueViolation,
			Message:        `duplicate key value violates unique constraint "users_email_key"`,
			ConstraintName: "users_email_key",
		}
	}

	user := db.User{
		ID:           uuid.New(),
		Email:        arg.Email,
		PasswordHash: arg.PasswordHash,
		Name:         arg.Name,
		CreatedAt:    time.Now(),
	}

	f.usersByEmail[user.Email] = user
	f.usersByID[user.ID] = user

	return user, nil
}

func (f *fakeStore) GetUserByEmail(_ context.Context, email string) (db.User, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	user, ok := f.usersByEmail[email]
	if !ok {
		return db.User{}, pgx.ErrNoRows
	}
	return user, nil
}

func (f *fakeStore) GetUserByID(_ context.Context, id uuid.UUID) (db.User, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	user, ok := f.usersByID[id]
	if !ok {
		return db.User{}, pgx.ErrNoRows
	}
	return user, nil
}

func (f *fakeStore) CreateSession(_ context.Context, arg db.CreateSessionParams) (db.Session, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	session := db.Session{
		TokenHash: arg.TokenHash,
		UserID:    arg.UserID,
		CreatedAt: time.Now(),
		ExpiresAt: arg.ExpiresAt,
	}
	f.sessions[hex.EncodeToString(arg.TokenHash)] = session

	return session, nil
}

func (f *fakeStore) GetSessionByTokenHash(_ context.Context, tokenHash []byte) (db.GetSessionByTokenHashRow, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	session, ok := f.sessions[hex.EncodeToString(tokenHash)]
	// Mirrors the "expires_at > now()" filter in the real query: an expired
	// session is indistinguishable from a missing one.
	if !ok || !session.ExpiresAt.After(time.Now()) {
		return db.GetSessionByTokenHashRow{}, pgx.ErrNoRows
	}

	user, ok := f.usersByID[session.UserID]
	if !ok {
		return db.GetSessionByTokenHashRow{}, pgx.ErrNoRows
	}

	return db.GetSessionByTokenHashRow{Session: session, User: user}, nil
}

func (f *fakeStore) DeleteSession(_ context.Context, tokenHash []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	delete(f.sessions, hex.EncodeToString(tokenHash))
	return nil
}

// expireSession backdates a session so tests can exercise the expiry path.
func (f *fakeStore) expireSession(tokenHash []byte) {
	f.mu.Lock()
	defer f.mu.Unlock()

	key := hex.EncodeToString(tokenHash)
	session := f.sessions[key]
	session.ExpiresAt = time.Now().Add(-time.Minute)
	f.sessions[key] = session
}

func newAuthTestServer(t *testing.T) (http.Handler, *fakeStore) {
	t.Helper()

	store := newFakeStore()
	a := &api{
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		store:  store,
	}

	return a.routes(), store
}

// do issues a request against the router, optionally with a bearer token.
func do(t *testing.T, h http.Handler, method, path, body, token string) *httptest.ResponseRecorder {
	t.Helper()

	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}

	req := httptest.NewRequestWithContext(t.Context(), method, path, reader)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	return rec
}

// registerUser registers a user and returns the issued token.
func registerUser(t *testing.T, h http.Handler, email, password, name string) (authResponse, *httptest.ResponseRecorder) {
	t.Helper()

	body, err := json.Marshal(registerRequest{Email: email, Password: password, Name: name})
	if err != nil {
		t.Fatalf("marshalling register request: %v", err)
	}

	rec := do(t, h, http.MethodPost, "/api/v1/auth/register", string(body), "")

	var got authResponse
	if rec.Code == http.StatusCreated {
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatalf("decoding register response: %v", err)
		}
	}

	return got, rec
}

func decodeEnvelope(t *testing.T, rec *httptest.ResponseRecorder) errorEnvelope {
	t.Helper()

	var env errorEnvelope
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("decoding error envelope from %q: %v", rec.Body.String(), err)
	}

	return env
}

func TestRegister(t *testing.T) {
	h, store := newAuthTestServer(t)

	got, rec := registerUser(t, h, "Ghost@Example.com ", "supersecret", "  Ghost  ")

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d (body: %s)", rec.Code, http.StatusCreated, rec.Body)
	}

	if got.Token == "" {
		t.Error("response contains no token")
	}

	// Email is normalised, name is trimmed.
	if got.User.Email != "ghost@example.com" {
		t.Errorf("email = %q, want %q", got.User.Email, "ghost@example.com")
	}
	if got.User.Name != "Ghost" {
		t.Errorf("name = %q, want %q", got.User.Name, "Ghost")
	}
	if _, err := uuid.Parse(got.User.ID); err != nil {
		t.Errorf("id %q is not a uuid: %v", got.User.ID, err)
	}

	// The response must never carry the password or its hash.
	body := rec.Body.String()
	if strings.Contains(body, "password") {
		t.Errorf("response mentions a password field: %s", body)
	}

	stored := store.usersByEmail["ghost@example.com"]
	if strings.Contains(body, stored.PasswordHash) {
		t.Error("response contains the stored password hash")
	}
	if stored.PasswordHash == "supersecret" {
		t.Error("password was stored in plain text")
	}
}

func TestRegisterDuplicateEmail(t *testing.T) {
	h, _ := newAuthTestServer(t)

	if _, rec := registerUser(t, h, "dup@example.com", "supersecret", ""); rec.Code != http.StatusCreated {
		t.Fatalf("first register: status = %d, want %d", rec.Code, http.StatusCreated)
	}

	// Different casing must still collide, since emails are normalised.
	_, rec := registerUser(t, h, "DUP@example.com", "anotherpassword", "")
	if rec.Code != http.StatusConflict {
		t.Fatalf("second register: status = %d, want %d (body: %s)", rec.Code, http.StatusConflict, rec.Body)
	}

	if env := decodeEnvelope(t, rec); env.Error.Code != codeConflict {
		t.Errorf("code = %q, want %q", env.Error.Code, codeConflict)
	}
}

func TestRegisterValidation(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{name: "short password", body: `{"email":"a@example.com","password":"short","name":""}`},
		{name: "empty password", body: `{"email":"a@example.com","password":"","name":""}`},
		{name: "missing email", body: `{"email":"","password":"supersecret","name":""}`},
		{name: "malformed email", body: `{"email":"not-an-email","password":"supersecret","name":""}`},
		{name: "email with display name", body: `{"email":"Ghost <ghost@example.com>","password":"supersecret","name":""}`},
		{name: "malformed json", body: `{"email":`},
		{name: "empty body", body: ``},
		{name: "unknown field", body: `{"email":"a@example.com","password":"supersecret","admin":true}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, _ := newAuthTestServer(t)

			rec := do(t, h, http.MethodPost, "/api/v1/auth/register", tt.body, "")
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want %d (body: %s)", rec.Code, http.StatusBadRequest, rec.Body)
			}

			if env := decodeEnvelope(t, rec); env.Error.Code != codeBadRequest {
				t.Errorf("code = %q, want %q", env.Error.Code, codeBadRequest)
			}
		})
	}
}

func TestLogin(t *testing.T) {
	h, _ := newAuthTestServer(t)

	if _, rec := registerUser(t, h, "login@example.com", "supersecret", "Login"); rec.Code != http.StatusCreated {
		t.Fatalf("register: status = %d, want %d", rec.Code, http.StatusCreated)
	}

	rec := do(t, h, http.MethodPost, "/api/v1/auth/login",
		`{"email":"LOGIN@example.com","password":"supersecret"}`, "")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d (body: %s)", rec.Code, http.StatusOK, rec.Body)
	}

	var got authResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decoding login response: %v", err)
	}

	if got.Token == "" {
		t.Error("login returned no token")
	}
	if got.User.Email != "login@example.com" {
		t.Errorf("email = %q, want %q", got.User.Email, "login@example.com")
	}
}

func TestLoginFailuresAreIndistinguishable(t *testing.T) {
	h, _ := newAuthTestServer(t)

	if _, rec := registerUser(t, h, "known@example.com", "supersecret", ""); rec.Code != http.StatusCreated {
		t.Fatalf("register: status = %d, want %d", rec.Code, http.StatusCreated)
	}

	wrongPassword := do(t, h, http.MethodPost, "/api/v1/auth/login",
		`{"email":"known@example.com","password":"wrongpassword"}`, "")

	unknownEmail := do(t, h, http.MethodPost, "/api/v1/auth/login",
		`{"email":"nobody@example.com","password":"supersecret"}`, "")

	for name, rec := range map[string]*httptest.ResponseRecorder{
		"wrong password": wrongPassword,
		"unknown email":  unknownEmail,
	} {
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("%s: status = %d, want %d (body: %s)", name, rec.Code, http.StatusUnauthorized, rec.Body)
		}
	}

	// Identical bodies: the response must not reveal whether the account exists.
	if wrongPassword.Body.String() != unknownEmail.Body.String() {
		t.Errorf("responses differ:\n wrong password: %s unknown email:  %s",
			wrongPassword.Body, unknownEmail.Body)
	}

	if env := decodeEnvelope(t, wrongPassword); env.Error.Message != messageBadCredentials {
		t.Errorf("message = %q, want %q", env.Error.Message, messageBadCredentials)
	}
}

func TestMe(t *testing.T) {
	h, _ := newAuthTestServer(t)

	registered, rec := registerUser(t, h, "me@example.com", "supersecret", "Me")
	if rec.Code != http.StatusCreated {
		t.Fatalf("register: status = %d, want %d", rec.Code, http.StatusCreated)
	}

	meRec := do(t, h, http.MethodGet, "/api/v1/me", "", registered.Token)
	if meRec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d (body: %s)", meRec.Code, http.StatusOK, meRec.Body)
	}

	var got userResponse
	if err := json.Unmarshal(meRec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decoding /me response: %v", err)
	}

	if got != registered.User {
		t.Errorf("/me returned %+v, want %+v", got, registered.User)
	}
}

func TestMeUnauthorized(t *testing.T) {
	h, store := newAuthTestServer(t)

	registered, rec := registerUser(t, h, "expiry@example.com", "supersecret", "")
	if rec.Code != http.StatusCreated {
		t.Fatalf("register: status = %d, want %d", rec.Code, http.StatusCreated)
	}

	tests := []struct {
		name   string
		header string // full Authorization header value; empty means none
	}{
		{name: "no header"},
		{name: "garbage token", header: "Bearer not-a-real-token"},
		{name: "empty token", header: "Bearer "},
		{name: "wrong scheme", header: "Basic " + registered.Token},
		{name: "token without scheme", header: registered.Token},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/me", nil)
			if tt.header != "" {
				req.Header.Set("Authorization", tt.header)
			}

			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)

			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want %d (body: %s)", rec.Code, http.StatusUnauthorized, rec.Body)
			}

			if env := decodeEnvelope(t, rec); env.Error.Code != codeUnauthorized {
				t.Errorf("code = %q, want %q", env.Error.Code, codeUnauthorized)
			}
		})
	}

	// An expired session is rejected exactly like an unknown one.
	t.Run("expired session", func(t *testing.T) {
		store.expireSession(auth.HashToken(registered.Token))

		rec := do(t, h, http.MethodGet, "/api/v1/me", "", registered.Token)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
		}
	})
}

func TestLogout(t *testing.T) {
	h, store := newAuthTestServer(t)

	registered, rec := registerUser(t, h, "logout@example.com", "supersecret", "")
	if rec.Code != http.StatusCreated {
		t.Fatalf("register: status = %d, want %d", rec.Code, http.StatusCreated)
	}

	// A second session must survive logging out of the first.
	loginRec := do(t, h, http.MethodPost, "/api/v1/auth/login",
		`{"email":"logout@example.com","password":"supersecret"}`, "")
	if loginRec.Code != http.StatusOK {
		t.Fatalf("login: status = %d, want %d", loginRec.Code, http.StatusOK)
	}

	var second authResponse
	if err := json.Unmarshal(loginRec.Body.Bytes(), &second); err != nil {
		t.Fatalf("decoding login response: %v", err)
	}

	logoutRec := do(t, h, http.MethodPost, "/api/v1/auth/logout", "", registered.Token)
	if logoutRec.Code != http.StatusNoContent {
		t.Fatalf("logout: status = %d, want %d (body: %s)", logoutRec.Code, http.StatusNoContent, logoutRec.Body)
	}
	if logoutRec.Body.Len() != 0 {
		t.Errorf("logout body = %q, want empty", logoutRec.Body)
	}

	if meRec := do(t, h, http.MethodGet, "/api/v1/me", "", registered.Token); meRec.Code != http.StatusUnauthorized {
		t.Errorf("/me after logout: status = %d, want %d", meRec.Code, http.StatusUnauthorized)
	}

	if meRec := do(t, h, http.MethodGet, "/api/v1/me", "", second.Token); meRec.Code != http.StatusOK {
		t.Errorf("/me on the other session: status = %d, want %d", meRec.Code, http.StatusOK)
	}

	store.mu.Lock()
	remaining := len(store.sessions)
	store.mu.Unlock()

	if remaining != 1 {
		t.Errorf("stored sessions = %d, want 1", remaining)
	}
}

func TestPublicRoutesStayPublic(t *testing.T) {
	h, _ := newAuthTestServer(t)

	if rec := do(t, h, http.MethodGet, "/api/v1/hello", "", ""); rec.Code != http.StatusOK {
		t.Errorf("/api/v1/hello without a token: status = %d, want %d", rec.Code, http.StatusOK)
	}
}
