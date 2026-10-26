// Package router owns route matching and the pre-first-byte fallback chain.
// It is the functional core between api and connectors: given a request it
// picks the model leg order, retries per the Kind table, and stops the moment
// a byte reaches the client.
package router

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"kyo-router/internal/catalog"
	"kyo-router/internal/connector"
	"kyo-router/internal/models"
)

// Rule maps a match pattern to fallback model ids. Match is an exact id or a
// trailing-"*" prefix over the canonical id namespace (devin/*, opencode/*).
type Rule struct {
	Match     string
	Fallbacks []string
}

// Result reports which leg served the request; api logs it.
type Result struct {
	Model     catalog.Model
	Attempts  int // total connector tries across all legs
	Fallbacks int // legs skipped before the serving leg
}

// RetryPolicy bounds waiting so a stuck upstream cannot hold a request
// forever. Values come from 01-architecture.md's Kind table.
const (
	networkRetries = 2
	rateLimitRetry = 1
	authRetry      = 1
	maxRetryWait   = 30 * time.Second
	networkBackoff = 200 * time.Millisecond
)

// Router resolves model ids into connector calls with retry and fallback.
type Router struct {
	catalog  *catalog.Catalog
	backends map[string]connector.Connector
	rules    []Rule
	sleep    func(ctx context.Context, d time.Duration) error
}

// New builds a router. backends maps connector names to live connectors.
func New(cat *catalog.Catalog, backends map[string]connector.Connector, rules []Rule) *Router {
	return &Router{
		catalog:  cat,
		backends: backends,
		rules:    rules,
		sleep:    realSleep,
	}
}

// WithSleep swaps the wait seam (tests inject a no-op).
func (r *Router) WithSleep(sleep func(ctx context.Context, d time.Duration) error) *Router {
	if sleep != nil {
		r.sleep = sleep
	}
	return r
}

// Chain returns the leg order for a canonical model id: the model itself
// then the first matching rule's fallbacks. No match means no fallbacks.
func (r *Router) Chain(id string) []string {
	for _, rule := range r.rules {
		if matchRule(rule.Match, id) {
			return append([]string{id}, rule.Fallbacks...)
		}
	}
	return []string{id}
}

// Execute resolves the request model, walks the chain, and emits events.
// Failover and retry happen only before the first emitted event; after that
// the error surfaces and the stream owner reports it in-band.
func (r *Router) Execute(ctx context.Context, req models.ChatRequest, emit connector.Emit) (Result, error) {
	var result Result
	primary, err := r.catalog.Resolve(req.Model)
	if err != nil {
		return result, err
	}
	started := false
	guarded := func(event models.StreamEvent) error {
		started = true
		return emit(event)
	}

	var lastErr error
	for i, legID := range r.Chain(primary.ID) {
		leg, err := r.catalog.Resolve(legID)
		if err != nil {
			// A fallback id that vanished from the catalog is a config bug;
			// surface it rather than silently skipping legs.
			return result, fmt.Errorf("route fallback %q: %w", legID, err)
		}
		backend, ok := r.backends[leg.Connector]
		if !ok {
			lastErr = &connector.Error{
				Kind:    connector.KindUnsupported,
				Code:    "connector_not_ready",
				Message: "connector " + leg.Connector + " is not built yet",
			}
			if r.advance(lastErr) {
				continue
			}
			return result, lastErr
		}
		result.Fallbacks = i
		err = r.tryLeg(ctx, backend, leg, req, guarded, &result)
		if err == nil {
			result.Model = leg
			return result, nil
		}
		lastErr = err
		if started || !r.advance(err) {
			return result, err
		}
	}
	return result, lastErr
}

// tryLeg runs one leg with the Kind-table retries. emit writes through
// `guarded` so a partial stream stops the whole chain.
func (r *Router) tryLeg(ctx context.Context, backend connector.Connector, leg catalog.Model, req models.ChatRequest, emit connector.Emit, result *Result) error {
	networkLeft := networkRetries
	rateLeft := rateLimitRetry
	authLeft := authRetry
	for {
		result.Attempts++
		err := backend.Chat(ctx, leg.Upstream, req, emit)
		if err == nil {
			return nil
		}
		var connErr *connector.Error
		if !errors.As(err, &connErr) {
			return err
		}
		switch connErr.Kind {
		case connector.KindNetwork:
			if networkLeft > 0 {
				networkLeft--
				if werr := r.sleep(ctx, networkBackoff*time.Duration(networkRetries-networkLeft)); werr != nil {
					return werr
				}
				continue
			}
		case connector.KindRateLimit:
			if rateLeft > 0 {
				rateLeft--
				wait := connErr.RetryAfter
				if wait <= 0 || wait > maxRetryWait {
					wait = networkBackoff
				}
				if werr := r.sleep(ctx, wait); werr != nil {
					return werr
				}
				continue
			}
		case connector.KindAuth:
			if authLeft > 0 {
				authLeft--
				continue
			}
		}
		return err
	}
}

// advance reports whether a failed leg may hand off to the next fallback:
// retryable/upstream kinds advance; validation and unsupported kinds
// surface immediately.
func (r *Router) advance(err error) bool {
	var connErr *connector.Error
	if !errors.As(err, &connErr) {
		return false
	}
	switch connErr.Kind {
	case connector.KindAuth, connector.KindRateLimit, connector.KindUpstream, connector.KindNetwork:
		return true
	}
	return false
}

func matchRule(pattern, id string) bool {
	if strings.HasSuffix(pattern, "*") {
		return strings.HasPrefix(id, strings.TrimSuffix(pattern, "*"))
	}
	return pattern == id
}

func realSleep(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
