package api

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"runtime/debug"
	"slices"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/komiljonov/ghostman/internal/db"
)

// Webhook capture is a separate code path from the API. Requests to a capture
// domain never reach the API router: no auth middleware, no session lookup, no
// CORS, and none of the API's request limits. Requests to any other host
// (the API host included) never reach capture.
//
// For each configured domain D:
//
//	Host D,           path /h/<slug>[/rest] -> capture; /rest is the recorded path
//	Host D,           any other path        -> plain 404
//	Host <slug>.D,    any path              -> capture, path recorded as-is
//	Host <x>.<y>.D or a malformed slug      -> plain 404
//
// A slug is captured on EVERY configured domain; an endpoint's stored domain
// is a display preference only, so changing it never breaks a URL already
// handed out.

const (
	// slugLength and slugAlphabet define a webhook slug: DNS-label-safe, so
	// it works as a subdomain.
	slugLength   = 10
	slugAlphabet = "abcdefghijklmnopqrstuvwxyz0123456789"

	// maxStoredCaptureBody is how much of a captured body is kept; the rest
	// is read and discarded up to maxReadCaptureBody, so that the sender
	// still gets the configured response (never a 413).
	maxStoredCaptureBody = 1 << 20
	maxReadCaptureBody   = 10 << 20

	// maxCapturesPerEndpoint is how many captures are kept per endpoint; older
	// ones are pruned as new ones arrive.
	maxCapturesPerEndpoint = 500

	// Per-endpoint capture rate: 10 requests a second, bursts of 30. A refused
	// request is answered 429 and not stored.
	captureRequestsPerSecond = 10
	captureRequestsBurst     = 30

	// captureIDHeader is added to every captured response.
	captureIDHeader = "X-Ghostman-Capture-Id"
)

// framingHeaders are never sent from an endpoint's configured headers: they
// describe the response body, which the server writes itself, and a wrong
// value would corrupt the response.
var framingHeaders = []string{"Content-Length", "Transfer-Encoding", "Connection"}

// withCapture puts the host switch in front of the API. Without configured
// capture domains it returns the API unchanged: no capture routing exists.
func (a *api) withCapture(apiHandler http.Handler) http.Handler {
	if len(a.hooksDomains) == 0 {
		return apiHandler
	}

	capture := a.logRequests(http.HandlerFunc(a.handleCapture))

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if a.isCaptureHost(r.Host) {
			capture.ServeHTTP(w, r)
			return
		}
		apiHandler.ServeHTTP(w, r)
	})
}

// isCaptureHost reports whether host is a capture domain or a subdomain of one.
func (a *api) isCaptureHost(host string) bool {
	host = normalizeHost(host)
	for _, domain := range a.hooksDomains {
		if host == domain || strings.HasSuffix(host, "."+domain) {
			return true
		}
	}
	return false
}

// captureTarget extracts the slug and the path to record from a request to a
// capture host. ok is false when the request names no well-formed slug.
func (a *api) captureTarget(r *http.Request) (slug, path string, ok bool) {
	host := normalizeHost(r.Host)
	requestPath := r.URL.EscapedPath()

	for _, domain := range a.hooksDomains {
		if host == domain {
			rest, found := strings.CutPrefix(requestPath, "/h/")
			if !found {
				return "", "", false
			}
			slug, tail, _ := strings.Cut(rest, "/")
			return slug, "/" + tail, isValidSlug(slug)
		}

		if label, found := strings.CutSuffix(host, "."+domain); found {
			if requestPath == "" {
				requestPath = "/"
			}
			// A label with a dot in it is a deeper subdomain, not a slug.
			return label, requestPath, isValidSlug(label)
		}
	}

	return "", "", false
}

// handleCapture records one request to a webhook endpoint and answers with
// the endpoint's configured response. It accepts any method and any path.
func (a *api) handleCapture(w http.ResponseWriter, r *http.Request) {
	slug, path, ok := a.captureTarget(r)

	defer func() {
		rec := recover()
		if rec == nil {
			return
		}
		if err, isErr := rec.(error); isErr && errors.Is(err, http.ErrAbortHandler) {
			panic(rec)
		}
		a.logger.ErrorContext(r.Context(), "webhook capture panicked",
			slog.String("slug", slug),
			slog.Any("panic", rec),
			slog.String("stack", string(debug.Stack())),
		)
		http.Error(w, "internal error", http.StatusInternalServerError)
	}()

	if !ok {
		http.NotFound(w, r)
		return
	}

	endpoint, err := a.store.GetWebhookEndpointBySlug(r.Context(), slug)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			http.NotFound(w, r)
			return
		}
		a.captureError(w, r, slug, "looking up webhook endpoint", err)
		return
	}

	// After the lookup, so an unknown slug is always a 404. A refused
	// request is not stored.
	if a.captureLimiter != nil {
		if allowed, retryAfter := a.captureLimiter.Allow(slug); !allowed {
			w.Header().Set("Retry-After", strconv.Itoa(retryAfterSeconds(retryAfter)))
			http.Error(w, "too many requests", http.StatusTooManyRequests)
			return
		}
	}

	body, size, truncated, err := readCaptureBody(r.Body)
	if err != nil {
		a.logger.WarnContext(r.Context(), "webhook capture: reading body failed",
			slog.String("slug", slug), errorAttr(err))
		http.Error(w, "could not read request body", http.StatusBadRequest)
		return
	}

	headers, err := captureHeaders(r)
	if err != nil {
		a.captureError(w, r, slug, "encoding captured headers", err)
		return
	}

	captureID, err := a.store.CaptureWebhook(r.Context(), db.InsertWebhookCaptureParams{
		EndpointID:    endpoint.ID,
		Method:        r.Method,
		Path:          path,
		Query:         r.URL.RawQuery,
		Headers:       headers,
		ContentType:   r.Header.Get("Content-Type"),
		Body:          body,
		BodySize:      int32(size), //nolint:gosec // G115: bounded by maxReadCaptureBody (10 MiB)
		BodyTruncated: truncated,
		SourceIp:      clientIP(r),
	}, maxCapturesPerEndpoint)
	if err != nil {
		a.captureError(w, r, slug, "storing webhook capture", err)
		return
	}

	writeConfiguredResponse(w, endpoint, captureID)
}

// readCaptureBody keeps up to maxStoredCaptureBody bytes, then reads and
// discards the rest up to maxReadCaptureBody. It returns the stored bytes (nil
// for an empty body), the number of bytes received, and whether the stored
// body is cut short.
func readCaptureBody(body io.Reader) ([]byte, int64, bool, error) {
	stored, err := io.ReadAll(io.LimitReader(body, maxStoredCaptureBody+1))
	if err != nil {
		return nil, 0, false, err
	}

	size := int64(len(stored))
	truncated := false

	if size > maxStoredCaptureBody {
		stored = stored[:maxStoredCaptureBody]
		truncated = true

		discarded, err := io.Copy(io.Discard, io.LimitReader(body, maxReadCaptureBody-size))
		if err != nil {
			return nil, 0, false, err
		}
		size += discarded
	}

	if len(stored) == 0 {
		return nil, 0, false, nil
	}

	return stored, size, truncated, nil
}

// capturedHeader is one header of a captured request.
type capturedHeader struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

// captureHeaders lists the request's headers as [{key, value}], Host first
// (Go keeps it out of r.Header), the rest sorted by name with each value kept
// in order, so repeated headers survive.
func captureHeaders(r *http.Request) (json.RawMessage, error) {
	headers := []capturedHeader{{Key: "Host", Value: r.Host}}

	names := make([]string, 0, len(r.Header))
	for name := range r.Header {
		names = append(names, name)
	}
	slices.Sort(names)

	for _, name := range names {
		for _, value := range r.Header[name] {
			headers = append(headers, capturedHeader{Key: name, Value: value})
		}
	}

	return json.Marshal(headers)
}

// writeConfiguredResponse answers the sender with the endpoint's status,
// enabled headers and body, plus the capture id.
func writeConfiguredResponse(w http.ResponseWriter, endpoint db.WebhookEndpoint, captureID int64) {
	var rows []keyValueRow
	// Stored rows were validated on the way in; a decode failure only means
	// no custom headers are sent.
	_ = json.Unmarshal(endpoint.RespHeaders, &rows)

	for _, row := range rows {
		if !row.Enabled || slices.Contains(framingHeaders, http.CanonicalHeaderKey(row.Key)) {
			continue
		}
		w.Header().Add(row.Key, row.Value)
	}
	w.Header().Set(captureIDHeader, strconv.FormatInt(captureID, 10))

	status := int(endpoint.RespStatus)
	w.WriteHeader(status)

	// 1xx, 204 and 304 responses cannot carry a body.
	if status >= 200 && status != http.StatusNoContent && status != http.StatusNotModified {
		_, _ = io.WriteString(w, endpoint.RespBody)
	}
}

// captureError logs a capture failure with its slug and answers 500 in plain
// text.
func (a *api) captureError(w http.ResponseWriter, r *http.Request, slug, action string, err error) {
	a.logger.ErrorContext(r.Context(), "webhook capture failed",
		slog.String("slug", slug),
		slog.String("action", action),
		errorAttr(err),
	)
	http.Error(w, "internal error", http.StatusInternalServerError)
}

// normalizeHost lowercases a Host header and strips its port and any
// trailing dot.
func normalizeHost(host string) string {
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	return strings.TrimSuffix(strings.ToLower(host), ".")
}

// isValidSlug reports a well-formed slug: exactly slugLength characters of
// slugAlphabet.
func isValidSlug(slug string) bool {
	if len(slug) != slugLength {
		return false
	}
	for _, c := range slug {
		if !strings.ContainsRune(slugAlphabet, c) {
			return false
		}
	}
	return true
}
