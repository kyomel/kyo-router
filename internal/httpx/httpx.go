// Package httpx holds the shared HTTP pieces every connector uses: the
// Transport seam, the status -> Kind table, upstream error scaffolding, and
// the SSE scanner.
package httpx

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"kyo-router/internal/connector"
)

// Transport is the HTTP seam every connector shares. *http.Client satisfies
// it; tests substitute an httptest.RoundTripper or a recording stub.
type Transport interface {
	Do(*http.Request) (*http.Response, error)
}

// StatusKind is the shared status -> Kind table from docs/01-architecture.md.
// A provider that gives one status extra meaning (opencode maps 400 to
// BadRequest) keeps its own row and delegates the rest here.
func StatusKind(status int) connector.Kind {
	switch {
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return connector.KindAuth
	case status == http.StatusTooManyRequests:
		return connector.KindRateLimit
	case status >= 500:
		return connector.KindUpstream
	}
	return connector.KindUpstreamBad
}

// UpstreamError builds the http_<status> connector.Error scaffolding: Kind,
// code, status, message fallback, and the Retry-After hint on rate limits.
// Callers that read provider detail from the body override Code and Message.
func UpstreamError(resp *http.Response, kind connector.Kind, message string) *connector.Error {
	if message == "" {
		message = http.StatusText(resp.StatusCode)
	}
	connErr := &connector.Error{
		Kind:    kind,
		Code:    "http_" + strconv.Itoa(resp.StatusCode),
		Message: message,
		Status:  resp.StatusCode,
	}
	if kind == connector.KindRateLimit {
		connErr.RetryAfter = ParseRetryAfter(resp.Header.Get("Retry-After"))
	}
	return connErr
}

// ParseRetryAfter reads the delay-seconds form of Retry-After.
func ParseRetryAfter(value string) time.Duration {
	value = strings.TrimSpace(value)
	if seconds, err := strconv.ParseInt(value, 10, 64); err == nil && seconds > 0 {
		return time.Duration(seconds) * time.Second
	}
	return 0
}
