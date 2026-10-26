package router

import (
	"context"
	"errors"
	"testing"
	"time"

	"kyo-router/internal/catalog"
	"kyo-router/internal/connector"
	"kyo-router/internal/models"
)

type fakeConnector struct {
	calls  int
	errs   []error // consumed in order; when empty, the scripted events go out
	events []models.StreamEvent
}

func (f *fakeConnector) Chat(_ context.Context, _ string, _ models.ChatRequest, emit connector.Emit) error {
	f.calls++
	if len(f.errs) > 0 {
		err := f.errs[0]
		f.errs = f.errs[1:]
		return err
	}
	for _, event := range f.events {
		if err := emit(event); err != nil {
			return err
		}
	}
	return nil
}

func kindErr(kind connector.Kind) *connector.Error {
	return &connector.Error{Kind: kind, Code: string(kind), Message: string(kind)}
}

func request() models.ChatRequest {
	return models.ChatRequest{
		Model:    "devin/a-low",
		Messages: []models.Message{{Role: models.RoleUser, Content: "hi"}},
	}
}

func newRouter(t *testing.T, backends map[string]connector.Connector, rules []Rule) *Router {
	t.Helper()
	cat, err := catalog.New([]catalog.Model{
		{ID: "devin/a-low", Connector: "devin", Upstream: "a-low", Protocol: catalog.ProtocolDevin},
		{ID: "opencode/b", Connector: "opencode", Upstream: "b", Protocol: catalog.ProtocolChat},
		{ID: "deepseek/c", Connector: "deepseek", Upstream: "c", Protocol: catalog.ProtocolChat},
	}, 1)
	if err != nil {
		t.Fatalf("catalog.New: %v", err)
	}
	return New(cat, backends, rules).WithSleep(func(context.Context, time.Duration) error { return nil })
}

func emitAll() (connector.Emit, *[]models.StreamEvent) {
	events := new([]models.StreamEvent)
	return func(event models.StreamEvent) error {
		*events = append(*events, event)
		return nil
	}, events
}

func TestExecutePrimary(t *testing.T) {
	primary := &fakeConnector{events: []models.StreamEvent{{Type: models.EventDone, FinishReason: "stop"}}}
	rt := newRouter(t, map[string]connector.Connector{"devin": primary}, nil)

	emit, events := emitAll()
	result, err := rt.Execute(context.Background(), request(), emit)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result.Model.ID != "devin/a-low" || result.Attempts != 1 || result.Fallbacks != 0 {
		t.Errorf("result = %+v", result)
	}
	if len(*events) != 1 {
		t.Errorf("events = %d", len(*events))
	}
}

func TestExecuteFallbackOnUpstream(t *testing.T) {
	primary := &fakeConnector{errs: []error{kindErr(connector.KindUpstream)}}
	fallback := &fakeConnector{events: []models.StreamEvent{{Type: models.EventDone, FinishReason: "stop"}}}
	rt := newRouter(t,
		map[string]connector.Connector{"devin": primary, "opencode": fallback},
		[]Rule{{Match: "devin/*", Fallbacks: []string{"opencode/b"}}})

	emit, _ := emitAll()
	result, err := rt.Execute(context.Background(), request(), emit)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result.Model.ID != "opencode/b" || result.Fallbacks != 1 {
		t.Errorf("result = %+v, want opencode/b at fallback 1", result)
	}
}

func TestExecuteRateLimitRetriesOnce(t *testing.T) {
	primary := &fakeConnector{
		errs: []error{
			&connector.Error{Kind: connector.KindRateLimit, Code: "rate_limited", RetryAfter: time.Second},
		},
		events: []models.StreamEvent{{Type: models.EventDone}},
	}
	rt := newRouter(t, map[string]connector.Connector{"devin": primary}, nil)

	emit, _ := emitAll()
	if _, err := rt.Execute(context.Background(), request(), emit); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if primary.calls != 2 {
		t.Errorf("calls = %d, want 2 (one RateLimit retry)", primary.calls)
	}
}

func TestExecuteAuthRetriesThenFallsBack(t *testing.T) {
	primary := &fakeConnector{errs: []error{
		kindErr(connector.KindAuth), kindErr(connector.KindAuth),
	}}
	fallback := &fakeConnector{events: []models.StreamEvent{{Type: models.EventDone}}}
	rt := newRouter(t,
		map[string]connector.Connector{"devin": primary, "opencode": fallback},
		[]Rule{{Match: "devin/*", Fallbacks: []string{"opencode/b"}}})

	emit, _ := emitAll()
	result, err := rt.Execute(context.Background(), request(), emit)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if primary.calls != 2 {
		t.Errorf("primary calls = %d, want 2 (one auth retry)", primary.calls)
	}
	if result.Model.ID != "opencode/b" {
		t.Errorf("served = %q, want opencode/b", result.Model.ID)
	}
}

func TestExecuteNoFailoverAfterFirstByte(t *testing.T) {
	// Primary emits an event then dies; the chain must not restart on the
	// fallback - the client already has bytes.
	primary := &fakeConnector{events: []models.StreamEvent{
		{Type: models.EventDelta, Delta: "partial"},
		{Type: models.EventDelta, Delta: "more"},
	}}
	leaky := leakyConnector{inner: primary, failAfter: 1, fail: kindErr(connector.KindUpstream)}
	fallback := &fakeConnector{events: []models.StreamEvent{{Type: models.EventDone}}}
	rt := newRouter(t,
		map[string]connector.Connector{"devin": leaky, "opencode": fallback},
		[]Rule{{Match: "devin/*", Fallbacks: []string{"opencode/b"}}})

	emit, events := emitAll()
	_, err := rt.Execute(context.Background(), request(), emit)
	if err == nil {
		t.Fatal("Execute succeeded; want the mid-stream error surfaced")
	}
	if fallback.calls != 0 {
		t.Errorf("fallback calls = %d, want 0 (bytes already sent)", fallback.calls)
	}
	if len(*events) != 1 {
		t.Errorf("events = %d, want only the partial delta", len(*events))
	}
}

// leakyConnector fails the emit after N events, mimicking a mid-stream kill.
type leakyConnector struct {
	inner     *fakeConnector
	failAfter int
	fail      error
	sent      int
}

func (l leakyConnector) Chat(ctx context.Context, upstream string, req models.ChatRequest, emit connector.Emit) error {
	return l.inner.Chat(ctx, upstream, req, func(event models.StreamEvent) error {
		l.sent++
		if l.sent > l.failAfter {
			return l.fail
		}
		return emit(event)
	})
}

func TestExecuteUnsupportedSurfaces(t *testing.T) {
	primary := &fakeConnector{errs: []error{kindErr(connector.KindUnsupported)}}
	fallback := &fakeConnector{events: []models.StreamEvent{{Type: models.EventDone}}}
	rt := newRouter(t,
		map[string]connector.Connector{"devin": primary, "opencode": fallback},
		[]Rule{{Match: "devin/*", Fallbacks: []string{"opencode/b"}}})

	emit, _ := emitAll()
	_, err := rt.Execute(context.Background(), request(), emit)
	var connErr *connector.Error
	if !errors.As(err, &connErr) || connErr.Kind != connector.KindUnsupported {
		t.Fatalf("err = %v, want Unsupported", err)
	}
	if fallback.calls != 0 {
		t.Errorf("fallback calls = %d, want 0 (Unsupported never falls back)", fallback.calls)
	}
}

func TestExecuteMissingBackendIsConnectorNotReady(t *testing.T) {
	rt := newRouter(t, map[string]connector.Connector{}, nil)
	emit, _ := emitAll()
	_, err := rt.Execute(context.Background(), request(), emit)
	var connErr *connector.Error
	if !errors.As(err, &connErr) || connErr.Code != "connector_not_ready" {
		t.Fatalf("err = %v, want connector_not_ready", err)
	}
}

func TestExecuteUnknownModel(t *testing.T) {
	rt := newRouter(t, nil, nil)
	emit, _ := emitAll()
	_, err := rt.Execute(context.Background(), models.ChatRequest{
		Model:    "nope/x",
		Messages: request().Messages,
	}, emit)
	if !errors.Is(err, catalog.ErrModelNotFound) {
		t.Fatalf("err = %v, want ErrModelNotFound", err)
	}
}

func TestChainMatchesPrefix(t *testing.T) {
	rt := newRouter(t, nil, []Rule{
		{Match: "devin/*", Fallbacks: []string{"opencode/b"}},
		{Match: "opencode/*", Fallbacks: []string{"deepseek/c"}},
	})
	if got := rt.Chain("devin/a-low"); len(got) != 2 || got[1] != "opencode/b" {
		t.Errorf("chain = %v", got)
	}
	if got := rt.Chain("opencode/b"); len(got) != 2 || got[1] != "deepseek/c" {
		t.Errorf("chain = %v", got)
	}
	if got := rt.Chain("deepseek/c"); len(got) != 1 {
		t.Errorf("chain = %v, want no fallbacks", got)
	}
}
