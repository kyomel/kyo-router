package connector

import (
	"context"
	"errors"
	"io"
	"strings"

	"kyo-router/internal/models"
)

// BadRequest is a caller-side failure the router must not retry.
func BadRequest(code, message string) *Error {
	return &Error{Kind: KindBadRequest, Code: code, Message: message}
}

// UpstreamBad is a provider failure the router must not retry.
func UpstreamBad(code, message string, err error) *Error {
	return &Error{Kind: KindUpstreamBad, Code: code, Message: message, Err: err}
}

// Internal is a local failure that is neither the caller's nor the provider's
// fault. The router must not retry or fall back from it.
func Internal(code, message string, err error) *Error {
	return &Error{Kind: KindInternal, Code: code, Message: message, Err: err}
}

// NetworkError tags a local transport failure as Network so the router can
// retry it. A context error keeps its identity through Unwrap.
func NetworkError(err error) error {
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
	return &Error{Kind: KindNetwork, Code: code, Message: err.Error(), Err: err}
}

// ValidateChat is the shared boundary check every connector runs before
// touching the wire: a non-empty upstream uid and at least one message.
func ValidateChat(upstream string, req models.ChatRequest) error {
	if strings.TrimSpace(upstream) == "" {
		return BadRequest("model_required", "upstream model id is required")
	}
	if len(req.Messages) == 0 {
		return BadRequest("messages_required", "at least one message is required")
	}
	return nil
}

// EnsureEmit guards a nil emit with a discard sink so connectors never branch
// on it mid-stream.
func EnsureEmit(emit Emit) Emit {
	if emit == nil {
		return func(models.StreamEvent) error { return nil }
	}
	return emit
}

// NormalizeStreamError passes connector errors through untouched; anything
// else is a transport failure.
func NormalizeStreamError(err error) error {
	var connErr *Error
	if errors.As(err, &connErr) {
		return err
	}
	return NetworkError(err)
}
