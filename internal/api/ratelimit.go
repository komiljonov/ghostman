package api

import (
	"fmt"
	"math"
	"net/http"
	"strconv"
	"time"

	"github.com/komiljonov/ghostman/internal/ratelimit"
)

// Limits on the unauthenticated auth endpoints, per client IP: brute-forcing
// a password or mass-registering is throttled to 5 attempts a minute after an
// initial burst of 10. Each route has its own limiter, so registering does not
// spend login attempts.
const (
	authAttemptsPerMinute = 5
	authAttemptsBurst     = 10
)

// rateLimitByIP refuses requests from a client IP that has run out of tokens,
// with 429, the standard envelope and Retry-After. A nil limiter disables
// limiting, which the handler tests rely on.
func (a *api) rateLimitByIP(limiter *ratelimit.Limiter, next http.HandlerFunc) http.HandlerFunc {
	if limiter == nil {
		return next
	}

	return func(w http.ResponseWriter, r *http.Request) {
		allowed, retryAfter := limiter.Allow(clientIP(r))
		if !allowed {
			seconds := retryAfterSeconds(retryAfter)
			w.Header().Set("Retry-After", strconv.Itoa(seconds))
			a.writeError(w, r, http.StatusTooManyRequests, codeTooManyRequests,
				fmt.Sprintf("too many attempts; try again in %d seconds", seconds))
			return
		}

		next(w, r)
	}
}

// retryAfterSeconds rounds a wait up to whole seconds, at least one, as the
// Retry-After header wants.
func retryAfterSeconds(wait time.Duration) int {
	return max(1, int(math.Ceil(wait.Seconds())))
}
