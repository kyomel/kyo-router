package opencode

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

// upstreamHTTPError maps a non-2xx status plus the provider error envelope to
// a Kind (02-connectors.md error table). Typed code/type fields classify
// first; the two message-only signals match exactly - never substring.
func upstreamHTTPError(resp *http.Response) error {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	detail := oai.ParseError(body)

	kind := kindFromProviderSignal(detail, kindFromStatus(resp.StatusCode))

	connErr := &connector.Error{
		Kind:   kind,
		Code:   "http_" + strconv.Itoa(resp.StatusCode),
		Status: resp.StatusCode,
	}
	if detail.Code != "" {
		connErr.Code = detail.Code
	}
	if detail.Message != "" {
		connErr.Message = detail.Message
	} else {
		connErr.Message = http.StatusText(resp.StatusCode)
	}
	if kind == connector.KindRateLimit {
		connErr.RetryAfter = parseRetryAfter(resp.Header.Get("Retry-After"))
	}
	return connErr
}

func kindFromStatus(status int) connector.Kind {
	switch {
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return connector.KindAuth
	case status == http.StatusTooManyRequests:
		return connector.KindRateLimit
	case status >= 500:
		return connector.KindUpstream
	case status == http.StatusBadRequest:
		return connector.KindBadRequest
	}
	return connector.KindUpstreamBad
}

// kindFromProviderSignal applies the doc's provider-signal rows. Typed
// code/type fields outrank the generic status: FreeTierError can arrive on
// a 403 yet means Unsupported, not Auth. "Model is unavailable" and
// "Insufficient account funds" arrive as bare messages and match exactly.
func kindFromProviderSignal(detail oai.ErrorDetail, fallback connector.Kind) connector.Kind {
	signal := detail.Code
	if signal == "" {
		signal = detail.Type
	}
	switch signal {
	case "MissingSessionID":
		return connector.KindBadRequest
	case "ModelProtocolUnsupported", "FreeTierError":
		return connector.KindUnsupported
	}
	switch detail.Message {
	case "Model is unavailable":
		return connector.KindUnsupported
	case "Insufficient account funds":
		return connector.KindAuth
	}
	return fallback
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

// parseRetryAfter reads the delay-seconds form of Retry-After.
func parseRetryAfter(value string) time.Duration {
	value = strings.TrimSpace(value)
	if seconds, err := strconv.ParseInt(value, 10, 64); err == nil && seconds > 0 {
		return time.Duration(seconds) * time.Second
	}
	return 0
}
