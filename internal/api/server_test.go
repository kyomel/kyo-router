package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"kyo-router/internal/catalog"
	"kyo-router/internal/connector"
	"kyo-router/internal/models"
	"kyo-router/internal/router"
)

func newHandler(t *testing.T) http.Handler {
	t.Helper()
	return newHandlerWithBackends(t, nil)
}

// fakeConnector is an in-memory Connector: the R seam swapped without mocks.
type fakeConnector struct {
	events []models.StreamEvent
	err    error
	gotUp  string
	gotReq models.ChatRequest
}

func (f *fakeConnector) Chat(_ context.Context, upstream string, req models.ChatRequest, emit connector.Emit) error {
	f.gotUp = upstream
	f.gotReq = req
	if f.err != nil {
		return f.err
	}
	for _, event := range f.events {
		if err := emit(event); err != nil {
			return err
		}
	}
	return nil
}

func newHandlerWithBackends(t *testing.T, backends map[string]connector.Connector) http.Handler {
	t.Helper()
	cat, err := catalog.New([]catalog.Model{
		{ID: "devin/claude-haiku-5-5-low", Connector: "devin", Upstream: "claude-haiku-5-5-low", Protocol: catalog.ProtocolDevin, Aliases: []string{"devin/haiku"}},
		{ID: "opencode/grok-4.7", Connector: "opencode", Upstream: "grok-4.7", Protocol: catalog.ProtocolResponses},
		{ID: "opencode/glm-5.3", Connector: "opencode", Upstream: "glm-5.3", Protocol: catalog.ProtocolChat},
		{ID: "deepseek/deepseek-flash", Connector: "deepseek", Upstream: "deepseek-flash", Protocol: catalog.ProtocolChat},
	}, 1700000000)
	if err != nil {
		t.Fatalf("catalog.New: %v", err)
	}
	return New(cat, router.New(cat, backends, nil), "", nil)
}

func do(t *testing.T, h http.Handler, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(method, path, reader))
	return rec
}

func TestHealthz(t *testing.T) {
	rec := do(t, newHandler(t), http.MethodGet, "/healthz", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got := rec.Body.String(); got != "ok" {
		t.Errorf("body = %q, want ok", got)
	}
}

func TestListModels(t *testing.T) {
	rec := do(t, newHandler(t), http.MethodGet, "/v1/models", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}

	var got struct {
		Object string `json:"object"`
		Data   []struct {
			ID      string `json:"id"`
			Object  string `json:"object"`
			Created int64  `json:"created"`
			OwnedBy string `json:"owned_by"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Object != "list" {
		t.Errorf("object = %q, want list", got.Object)
	}

	wantIDs := []string{"devin/claude-haiku-5-5-low", "devin/haiku", "opencode/grok-4.7", "opencode/glm-5.3", "deepseek/deepseek-flash"}
	if len(got.Data) != len(wantIDs) {
		t.Fatalf("models = %d, want %d", len(got.Data), len(wantIDs))
	}
	for i, want := range wantIDs {
		m := got.Data[i]
		if m.ID != want {
			t.Errorf("data[%d].id = %q, want %q", i, m.ID, want)
		}
		if m.Object != "model" || m.Created != 1700000000 || m.OwnedBy == "" {
			t.Errorf("data[%d] = %+v, want object=model, created=1700000000, owned_by set", i, m)
		}
	}
}

func TestChatCompletions(t *testing.T) {
	cases := []struct {
		name     string
		body     string
		want     int
		wantCode string
		wantKind Kind
	}{
		{
			name:     "known model whose connector is not built",
			body:     `{"model":"opencode/glm-5.3","messages":[{"role":"user","content":"hi"}]}`,
			want:     http.StatusNotImplemented,
			wantCode: "connector_not_ready",
			wantKind: KindUnsupported,
		},
		{
			name:     "alias resolves to its model",
			body:     `{"model":"devin/haiku","messages":[{"role":"user","content":"hi"}]}`,
			want:     http.StatusNotImplemented,
			wantCode: "connector_not_ready",
			wantKind: KindUnsupported,
		},
		{
			name:     "unknown model",
			body:     `{"model":"opencode/nope","messages":[{"role":"user","content":"hi"}]}`,
			want:     http.StatusNotFound,
			wantCode: "model_not_found",
			wantKind: KindBadRequest,
		},
		{
			name:     "missing model",
			body:     `{"messages":[{"role":"user","content":"hi"}]}`,
			want:     http.StatusBadRequest,
			wantCode: "model_required",
			wantKind: KindBadRequest,
		},
		{
			name:     "malformed json",
			body:     `{"model":`,
			want:     http.StatusBadRequest,
			wantCode: "invalid_json",
			wantKind: KindBadRequest,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := do(t, newHandler(t), http.MethodPost, "/v1/chat/completions", tc.body)
			if rec.Code != tc.want {
				t.Fatalf("status = %d, want %d", rec.Code, tc.want)
			}

			var got struct {
				Error struct {
					Message string `json:"message"`
					Type    string `json:"type"`
					Code    string `json:"code"`
				} `json:"error"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
				t.Fatalf("decode: %v", err)
			}
			if got.Error.Code != tc.wantCode {
				t.Errorf("code = %q, want %q", got.Error.Code, tc.wantCode)
			}
			if got.Error.Type != string(tc.wantKind) {
				t.Errorf("type = %q, want %q", got.Error.Type, tc.wantKind)
			}
			if got.Error.Message == "" {
				t.Error("message is empty")
			}
		})
	}
}

func TestChatCompletionsDispatch(t *testing.T) {
	effort := "low"
	fake := &fakeConnector{events: []models.StreamEvent{
		{Type: models.EventDelta, ReasoningContent: "thinking..."},
		{Type: models.EventDelta, Delta: "hi"},
		{Type: models.EventDone, FinishReason: "stop", Usage: &models.Usage{InputTokens: 3, OutputTokens: 5}},
	}}
	h := newHandlerWithBackends(t, map[string]connector.Connector{"deepseek": fake})

	rec := do(t, h, http.MethodPost, "/v1/chat/completions",
		`{"model":"deepseek/deepseek-flash","messages":[{"role":"user","content":"hi"}],"reasoning_effort":"low"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	var got struct {
		Choices []struct {
			Message struct {
				Content          string `json:"content"`
				ReasoningContent string `json:"reasoning_content"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		Usage struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
			TotalTokens      int `json:"total_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Choices[0].Message.Content != "hi" || got.Choices[0].Message.ReasoningContent != "thinking..." {
		t.Errorf("message = %+v", got.Choices[0].Message)
	}
	if got.Usage.PromptTokens != 3 || got.Usage.CompletionTokens != 5 || got.Usage.TotalTokens != 8 {
		t.Errorf("usage = %+v", got.Usage)
	}
	if fake.gotUp != "deepseek-flash" {
		t.Errorf("upstream = %q, want deepseek-flash", fake.gotUp)
	}
	if fake.gotReq.ReasoningEffort == nil || *fake.gotReq.ReasoningEffort != effort {
		t.Errorf("reasoning_effort = %v, want %q", fake.gotReq.ReasoningEffort, effort)
	}
}

func TestChatCompletionsStream(t *testing.T) {
	fake := &fakeConnector{events: []models.StreamEvent{
		{Type: models.EventDelta, ReasoningContent: "thinking..."},
		{Type: models.EventDelta, Delta: "hi"},
		{Type: models.EventDone, FinishReason: "stop", Usage: &models.Usage{InputTokens: 3, OutputTokens: 5}},
	}}
	h := newHandlerWithBackends(t, map[string]connector.Connector{"deepseek": fake})

	rec := do(t, h, http.MethodPost, "/v1/chat/completions",
		`{"model":"deepseek/deepseek-flash","messages":[{"role":"user","content":"hi"}],"stream":true}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "text/event-stream" {
		t.Errorf("content-type = %q, want text/event-stream", ct)
	}
	body := rec.Body.String()
	for _, want := range []string{`"reasoning_content":"thinking..."`, `"content":"hi"`, `"finish_reason":"stop"`, "[DONE]"} {
		if !strings.Contains(body, want) {
			t.Errorf("stream missing %s:\n%s", want, body)
		}
	}
}

func TestChatCompletionsConnectorError(t *testing.T) {
	fake := &fakeConnector{err: &connector.Error{Kind: connector.KindUpstreamBad, Code: "http_400", Status: 400, Message: "bad effort"}}
	h := newHandlerWithBackends(t, map[string]connector.Connector{"deepseek": fake})

	rec := do(t, h, http.MethodPost, "/v1/chat/completions",
		`{"model":"deepseek/deepseek-flash","messages":[{"role":"user","content":"hi"}]}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (UpstreamBad passes its status)", rec.Code)
	}
}

func TestListModelsShape(t *testing.T) {
	cases := []struct {
		name      string
		model     catalog.Model
		wantOwned string
	}{
		{
			name:      "devin entry",
			model:     catalog.Model{ID: "devin/claude-haiku-5-5-low", Connector: "devin", Upstream: "claude-haiku-5-5-low", Protocol: catalog.ProtocolDevin},
			wantOwned: "devin",
		},
		{
			name:      "opencode chat entry",
			model:     catalog.Model{ID: "opencode/glm-5.3", Connector: "opencode", Upstream: "glm-5.3", Protocol: catalog.ProtocolChat},
			wantOwned: "opencode",
		},
		{
			name:      "opencode responses entry",
			model:     catalog.Model{ID: "opencode/grok-4.7", Connector: "opencode", Upstream: "grok-4.7", Protocol: catalog.ProtocolResponses},
			wantOwned: "opencode",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cat, err := catalog.New([]catalog.Model{tc.model}, 1700000000)
			if err != nil {
				t.Fatalf("catalog.New: %v", err)
			}

			rec := do(t, New(cat, router.New(cat, nil, nil), "", nil), http.MethodGet, "/v1/models", "")
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200", rec.Code)
			}

			var got struct {
				Object string `json:"object"`
				Data   []struct {
					ID      string `json:"id"`
					Object  string `json:"object"`
					Created int64  `json:"created"`
					OwnedBy string `json:"owned_by"`
				} `json:"data"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
				t.Fatalf("decode: %v", err)
			}
			if got.Object != "list" {
				t.Errorf("object = %q, want list", got.Object)
			}
			if len(got.Data) != 1 {
				t.Fatalf("data = %d entries, want 1", len(got.Data))
			}
			m := got.Data[0]
			if m.ID != tc.model.ID {
				t.Errorf("id = %q, want %q", m.ID, tc.model.ID)
			}
			if m.Object != "model" {
				t.Errorf("object = %q, want model", m.Object)
			}
			if m.Created != 1700000000 {
				t.Errorf("created = %d, want 1700000000", m.Created)
			}
			if m.OwnedBy != tc.wantOwned {
				t.Errorf("owned_by = %q, want %q (the connector)", m.OwnedBy, tc.wantOwned)
			}
		})
	}
}

func TestChatCompletionsContentParts(t *testing.T) {
	fake := &fakeConnector{events: []models.StreamEvent{{Type: models.EventDone, FinishReason: "stop"}}}
	h := newHandlerWithBackends(t, map[string]connector.Connector{"deepseek": fake})

	// Real clients (pi, prime) send content as an array of parts.
	rec := do(t, h, http.MethodPost, "/v1/chat/completions",
		`{"model":"deepseek/deepseek-flash","messages":[{"role":"user","content":[{"type":"text","text":"hi"},{"type":"text","text":" there"}]}]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if fake.gotReq.Messages[0].Content != "hi there" {
		t.Errorf("content = %q, want joined parts", fake.gotReq.Messages[0].Content)
	}
}

func TestChatCompletionsDeveloperAndTools(t *testing.T) {
	fake := &fakeConnector{events: []models.StreamEvent{{Type: models.EventDone, FinishReason: "stop"}}}
	h := newHandlerWithBackends(t, map[string]connector.Connector{"deepseek": fake})

	rec := do(t, h, http.MethodPost, "/v1/chat/completions",
		`{"model":"deepseek/deepseek-flash","messages":[{"role":"developer","content":"rules"},{"role":"user","content":"hi"}],`+
			`"tools":[{"type":"function","function":{"name":"lookup","description":"d","parameters":{"type":"object"}}}],`+
			`"tool_choice":"auto"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if fake.gotReq.Messages[0].Role != models.RoleDeveloper {
		t.Errorf("role = %q, want developer preserved canonically", fake.gotReq.Messages[0].Role)
	}
	if len(fake.gotReq.Tools) != 1 || fake.gotReq.Tools[0].Function.Name != "lookup" {
		t.Errorf("tools = %+v, want one forwarded tool", fake.gotReq.Tools)
	}
	if string(fake.gotReq.ToolChoice) != `"auto"` {
		t.Errorf("tool_choice = %s, want \"auto\"", fake.gotReq.ToolChoice)
	}
}

func TestChatCompletionsToolCallHistory(t *testing.T) {
	fake := &fakeConnector{events: []models.StreamEvent{{Type: models.EventDone, FinishReason: "stop"}}}
	h := newHandlerWithBackends(t, map[string]connector.Connector{"deepseek": fake})

	// Real clients replay assistant tool calls in the nested OpenAI shape.
	rec := do(t, h, http.MethodPost, "/v1/chat/completions",
		`{"model":"deepseek/deepseek-flash","messages":[`+
			`{"role":"user","content":"hi"},`+
			`{"role":"assistant","content":"","tool_calls":[{"id":"call_1","type":"function","function":{"name":"lookup","arguments":"{\"q\":\"x\"}"}}]},`+
			`{"role":"tool","tool_call_id":"call_1","content":"result"}]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	msgs := fake.gotReq.Messages
	if len(msgs) != 3 {
		t.Fatalf("messages = %d, want 3", len(msgs))
	}
	calls := msgs[1].ToolCalls
	if len(calls) != 1 || calls[0].ID != "call_1" || calls[0].Name != "lookup" || calls[0].Arguments != `{"q":"x"}` {
		t.Errorf("assistant tool calls = %+v, want nested function decoded", calls)
	}
	if msgs[2].ToolCallID != "call_1" || msgs[2].Content != "result" {
		t.Errorf("tool result = %+v", msgs[2])
	}
}
