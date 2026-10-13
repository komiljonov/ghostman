package api

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/komiljonov/ghostman/internal/ratelimit"
)

// newRateLimitedAuthServer is the auth router over the in-memory store with
// the production login/register limits switched on.
func newRateLimitedAuthServer(t *testing.T) http.Handler {
	t.Helper()

	a := &api{
		logger:          slog.New(slog.NewTextHandler(io.Discard, nil)),
		store:           newFakeStore(),
		loginLimiter:    ratelimit.PerMinute(authAttemptsPerMinute, authAttemptsBurst),
		registerLimiter: ratelimit.PerMinute(authAttemptsPerMinute, authAttemptsBurst),
	}
	return a.routes()
}

// postFrom POSTs body to path as if from remoteAddr, optionally through the
// trusted proxy with an X-Forwarded-For value.
func postFrom(t *testing.T, h http.Handler, path, body, remoteAddr, forwardedFor string) *httptest.ResponseRecorder {
	t.Helper()

	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = remoteAddr
	if forwardedFor != "" {
		req.Header.Set("X-Forwarded-For", forwardedFor)
	}

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func assertRateLimited(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()

	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429 (body: %s)", rec.Code, rec.Body)
	}
	retry, err := strconv.Atoi(rec.Header().Get("Retry-After"))
	// 5 a minute: the next token is at most 12 seconds away.
	if err != nil || retry < 1 || retry > 12 {
		t.Errorf("Retry-After = %q, want 1..12 seconds", rec.Header().Get("Retry-After"))
	}
	if env := decodeEnvelope(t, rec); env.Error.Code != codeTooManyRequests {
		t.Errorf("envelope code = %q, want %q", env.Error.Code, codeTooManyRequests)
	}
}

func TestLoginRateLimit(t *testing.T) {
	h := newRateLimitedAuthServer(t)
	wrong := `{"email":"nobody@example.com","password":"wrong-password"}`

	for i := range authAttemptsBurst {
		if rec := postFrom(t, h, "/api/v1/auth/login", wrong, "203.0.113.1:4000", ""); rec.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d: status = %d, want 401 inside the burst", i+1, rec.Code)
		}
	}
	assertRateLimited(t, postFrom(t, h, "/api/v1/auth/login", wrong, "203.0.113.1:4000", ""))

	t.Run("buckets are per IP", func(t *testing.T) {
		if rec := postFrom(t, h, "/api/v1/auth/login", wrong, "203.0.113.2:4000", ""); rec.Code != http.StatusUnauthorized {
			t.Errorf("another IP: status = %d, want 401", rec.Code)
		}
	})

	t.Run("register has its own bucket", func(t *testing.T) {
		rec := postFrom(t, h, "/api/v1/auth/register",
			`{"email":"new@example.com","password":"long-enough-1","name":"N"}`, "203.0.113.1:4000", "")
		if rec.Code != http.StatusCreated {
			t.Errorf("register from the login-limited IP: status = %d, want 201", rec.Code)
		}
	})

	t.Run("behind the trusted proxy, the forwarded IP is the key", func(t *testing.T) {
		for range authAttemptsBurst {
			postFrom(t, h, "/api/v1/auth/login", wrong, "127.0.0.1:9000", "198.51.100.1")
		}
		assertRateLimited(t, postFrom(t, h, "/api/v1/auth/login", wrong, "127.0.0.1:9000", "198.51.100.1"))

		// Same proxy, different client: not limited.
		if rec := postFrom(t, h, "/api/v1/auth/login", wrong, "127.0.0.1:9000", "198.51.100.2"); rec.Code != http.StatusUnauthorized {
			t.Errorf("other client behind the proxy: status = %d, want 401", rec.Code)
		}
	})

	t.Run("a spoofed header from outside does not escape the limit", func(t *testing.T) {
		rec := postFrom(t, h, "/api/v1/auth/login", wrong, "203.0.113.1:4000", "10.9.8.7")
		assertRateLimited(t, rec)
	})
}

func TestRegisterRateLimit(t *testing.T) {
	h := newRateLimitedAuthServer(t)

	for i := range authAttemptsBurst {
		body := `{"email":"user` + strconv.Itoa(i) + `@example.com","password":"long-enough-1","name":"U"}`
		if rec := postFrom(t, h, "/api/v1/auth/register", body, "203.0.113.5:4000", ""); rec.Code != http.StatusCreated {
			t.Fatalf("register %d: status = %d, want 201", i+1, rec.Code)
		}
	}
	assertRateLimited(t, postFrom(t, h, "/api/v1/auth/register",
		`{"email":"one-more@example.com","password":"long-enough-1","name":"U"}`, "203.0.113.5:4000", ""))
}

func TestOnlyAuthRoutesAreRateLimited(t *testing.T) {
	h := newRateLimitedAuthServer(t)

	for range 3 * authAttemptsBurst {
		if rec := postFrom(t, h, "/api/v1/auth/logout", "", "203.0.113.9:4000", ""); rec.Code == http.StatusTooManyRequests {
			t.Fatal("logout was rate-limited; only login and register should be")
		}
	}
}

func TestClientIP(t *testing.T) {
	tests := []struct {
		name       string
		remoteAddr string
		forwarded  []string
		want       string
	}{
		{"direct client", "203.0.113.9:5555", nil, "203.0.113.9"},
		{"direct client with a spoofed header", "203.0.113.9:5555", []string{"1.2.3.4"}, "203.0.113.9"},
		{"trusted proxy", "127.0.0.1:5555", []string{"198.51.100.7"}, "198.51.100.7"},
		{"trusted proxy, client-supplied entries to the left", "127.0.0.1:5555", []string{"6.6.6.6, 7.7.7.7, 198.51.100.7"}, "198.51.100.7"},
		{"trusted proxy, repeated headers", "127.0.0.1:5555", []string{"6.6.6.6", "198.51.100.9"}, "198.51.100.9"},
		{"trusted IPv6 proxy", "[::1]:5555", []string{"2001:db8::1"}, "2001:db8::1"},
		{"trusted proxy, garbage entry", "127.0.0.1:5555", []string{"evil"}, "127.0.0.1"},
		{"trusted proxy, no header", "127.0.0.1:5555", nil, "127.0.0.1"},
		{"loopback range", "127.0.0.2:5555", []string{"198.51.100.3"}, "198.51.100.3"},
		{"private network is not trusted", "10.0.0.5:5555", []string{"198.51.100.4"}, "10.0.0.5"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil)
			req.RemoteAddr = tt.remoteAddr
			for _, value := range tt.forwarded {
				req.Header.Add("X-Forwarded-For", value)
			}
			if got := clientIP(req); got != tt.want {
				t.Errorf("clientIP = %q, want %q", got, tt.want)
			}
		})
	}
}
