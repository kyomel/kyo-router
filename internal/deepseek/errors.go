package deepseek

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"kyo-router/internal/connector"
	"kyo-router/internal/oai"
)

// upstreamHTTPError maps a non-2xx status to a Kind. The body is read for the
// message only: classification never looks at text.
func upstreamHTTPError(resp *http.Response) error {
	kind := connector.KindUpstreamBad
	switch {
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		kind = connector.KindAuth
	case resp.StatusCode == http.StatusTooManyRequests:
		kind = connector.KindRateLimit
	case resp.StatusCode >= 500:
		kind = connector.KindUpstream
	}

	connErr := &connector.Error{
		Kind:   kind,
		Code:   "http_" + strconv.Itoa(resp.StatusCode),
		Status: resp.StatusCode,
	}
	if body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10)); err == nil {
		if detail := oai.ParseError(body); detail.Message != "" {
			connErr.Message = detail.Message
		}
	}
	if connErr.Message == "" {
		connErr.Message = http.StatusText(resp.StatusCode)
	}
	if kind == connector.KindRateLimit {
		connErr.RetryAfter = parseRetryAfter(resp.Header.Get("Retry-After"))
	}
	return connErr
}

// transportError tags a local transport failure as Network so the router can
// retry it. A context error keeps its identity through Unwrap.
func transportError(err error) error {
	if err == nil {
		return nil
	}
	code := "transport"
	switch {
	case errors.Is(err, context.Canceled):
		code = "context_canceled"
	case errors.Is(err, context.DeadlineExceeded):
		code = "context_deadline"
	case errors.Is(err, io.ErrUnexpectedEOF):
		code = "unexpected_eof"
	}
	return &connector.Error{
		Kind:    connector.KindNetwork,
		Code:    code,
		Message: err.Error(),
		Err:     err,
	}
}

func badRequest(code, message string) *connector.Error {
	return &connector.Error{Kind: connector.KindBadRequest, Code: code, Message: message}
}

func upstreamBad(code, message string, err error) *connector.Error {
	return &connector.Error{Kind: connector.KindUpstreamBad, Code: code, Message: message, Err: err}
}

// parseRetryAfter reads the delay-seconds form of Retry-After. HTTP dates are
// rare enough upstream that they fall back to no hint.
func parseRetryAfter(value string) time.Duration {
	value = strings.TrimSpace(value)
	if seconds, err := strconv.ParseInt(value, 10, 64); err == nil && seconds > 0 {
		return time.Duration(seconds) * time.Second
	}
	return 0
}
