package api

import (
	"fmt"
	"log/slog"
	"net/http"
	"runtime/debug"
	"strings"
	"time"
)

// corsMaxAge is how long a browser may cache a preflight response.
const corsMaxAge = "300"

// chain applies middleware so that the first argument is the outermost wrapper:
// chain(h, logRequests, recoverPanics) runs logRequests, then recoverPanics,
// then h.
func chain(h http.Handler, middleware ...func(http.Handler) http.Handler) http.Handler {
	for i := len(middleware) - 1; i >= 0; i-- {
		h = middleware[i](h)
	}
	return h
}

// statusRecorder captures the status code and response size for logging.
type statusRecorder struct {
	http.ResponseWriter
	status int
	bytes  int
}

func (s *statusRecorder) WriteHeader(code int) {
	if s.status == 0 {
		s.status = code
		s.ResponseWriter.WriteHeader(code)
	}
}

func (s *statusRecorder) Write(b []byte) (int, error) {
	if s.status == 0 {
		s.status = http.StatusOK
	}
	n, err := s.ResponseWriter.Write(b)
	s.bytes += n
	return n, err
}

// limitRequestBody caps every request body at maxRequestBody. A request that
// declares a larger Content-Length is refused with 413 before any handler
// runs; one that does not declare its length is cut off at the limit, and
// readJSON turns that into the same 413.
func (a *api) limitRequestBody(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.ContentLength > maxRequestBody {
			a.writeError(w, r, http.StatusRequestEntityTooLarge, codeTooLarge,
				bodyTooLargeError{limit: maxRequestBody}.Error())
			return
		}

		r.Body = http.MaxBytesReader(w, r.Body, maxRequestBody)
		next.ServeHTTP(w, r)
	})
}

// logRequests emits one structured log line per request, at debug level for
// successful requests and warn/error for 4xx/5xx.
func (a *api) logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w}

		next.ServeHTTP(rec, r)

		if rec.status == 0 {
			rec.status = http.StatusOK
		}

		level := slog.LevelDebug
		switch {
		case rec.status >= http.StatusInternalServerError:
			level = slog.LevelError
		case rec.status >= http.StatusBadRequest:
			level = slog.LevelWarn
		}

		a.logger.LogAttrs(r.Context(), level, "http request",
			slog.String("method", r.Method),
			slog.String("path", r.URL.Path),
			slog.Int("status", rec.status),
			slog.Int("bytes", rec.bytes),
			slog.Duration("duration", time.Since(start)),
			slog.String("remote_addr", r.RemoteAddr),
		)
	})
}

// recoverPanics turns a panicking handler into a logged 500 instead of a
// dropped connection.
func (a *api) recoverPanics(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			rec := recover()
			if rec == nil {
				return
			}

			// http.ErrAbortHandler is the documented way to abort a response;
			// it is not an error and must keep propagating.
			if err, ok := rec.(error); ok && err == http.ErrAbortHandler { //nolint:errorlint // sentinel is panicked directly
				panic(rec)
			}

			// Signal that the connection is unusable for keep-alive.
			w.Header().Set("Connection", "close")

			a.logger.ErrorContext(r.Context(), "recovered from panic",
				slog.Any("panic", rec),
				slog.String("stack", string(debug.Stack())),
			)
			a.serverError(w, r, fmt.Errorf("panic: %v", rec))
		}()

		next.ServeHTTP(w, r)
	})
}

// withCORS applies a permissive CORS policy and short-circuits preflight
// requests. The scaffold allows any origin because the desktop client has no
// fixed one; tighten this before exposing the server publicly.
func (a *api) withCORS(next http.Handler) http.Handler {
	allowedMethods := strings.Join([]string{
		http.MethodGet,
		http.MethodPost,
		http.MethodPut,
		http.MethodPatch,
		http.MethodDelete,
		http.MethodOptions,
	}, ", ")

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Add("Vary", "Origin")

		if r.Method == http.MethodOptions && r.Header.Get("Access-Control-Request-Method") != "" {
			w.Header().Set("Access-Control-Allow-Methods", allowedMethods)
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
			w.Header().Set("Access-Control-Max-Age", corsMaxAge)
			w.Header().Add("Vary", "Access-Control-Request-Method")
			w.Header().Add("Vary", "Access-Control-Request-Headers")
			w.WriteHeader(http.StatusNoContent)
			return
		}

		next.ServeHTTP(w, r)
	})
}
