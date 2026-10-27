// Package connector is the boundary between routing and provider wire code.
// A connector owns one upstream protocol and translates that provider's
// failures into Kind before they cross this boundary.
package connector

import (
	"context"
	"errors"
	"fmt"
	"time"

	"kyo-router/internal/models"
)

// Kind is the E-channel tag from docs/01-architecture.md. Callers branch on
// Kind, never on an error message.
type Kind string

const (
	KindAuth        Kind = "Auth"
	KindRateLimit   Kind = "RateLimit"
	KindUpstream    Kind = "Upstream"
	KindNetwork     Kind = "Network"
	KindBadRequest  Kind = "BadRequest"
	KindUnsupported Kind = "Unsupported"
	KindUpstreamBad Kind = "UpstreamBad"
	KindInternal    Kind = "Internal"
)

// Error is a connector failure tagged with a Kind. Code is the provider status
// or code for logs; it never carries a key or token. Status carries the
// upstream HTTP status so the API layer can pass it through on UpstreamBad.
type Error struct {
	Kind       Kind
	Code       string
	Message    string
	Status     int
	RetryAfter time.Duration
	Err        error
}

func (e *Error) Error() string {
	if e.Code == "" {
		return fmt.Sprintf("%s: %s", e.Kind, e.Message)
	}
	return fmt.Sprintf("%s %s: %s", e.Kind, e.Code, e.Message)
}

func (e *Error) Unwrap() error { return e.Err }

// KindOf returns the Kind tagged on err, or "" when err is not a connector
// error. It unwraps, so a wrapped connector error still classifies.
func KindOf(err error) Kind {
	var target *Error
	if errors.As(err, &target) {
		return target.Kind
	}
	return ""
}

// Emit receives one canonical stream event. A non-nil return stops the stream.
type Emit func(models.StreamEvent) error

// Connector is one upstream. Chat streams canonical events through emit and
// returns nil only after the upstream stream ended cleanly. upstream is the
// provider id from the catalog, passed verbatim.
type Connector interface {
	Chat(ctx context.Context, upstream string, req models.ChatRequest, emit Emit) error
}
