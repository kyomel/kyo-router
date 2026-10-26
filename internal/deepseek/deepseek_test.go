package deepseek

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"kyo-router/internal/connector"
	"kyo-router/internal/models"
)

func newTestConnector(t *testing.T, server *httptest.Server) *Connector {
	t.Helper()
	conn, err := New(Options{APIKey: "sk_test", BaseURL: server.URL})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return conn
}

func chatMessages() []models.Message {
	return []models.Message{{Role: models.RoleUser, Content: "hi"}}
}

func collect(t *testing.T, conn *Connector, req models.ChatRequest) []models.StreamEvent {
	t.Helper()
	var events []models.StreamEvent
	err := conn.Chat(context.Background(), "deepseek-flash", req, func(event models.StreamEvent) error {
		events = append(events, event)
		return nil
	})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	return events
}

func TestChatStream(t *testing.T) {
	var captured map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != chatPath {
			t.Errorf("path = %q, want %q", r.URL.Path, chatPath)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer sk_test" {
			t.Errorf("authorization = %q", got)
		}
		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
			t.Errorf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"reasoning_content\":\"ponder\"}}]}\n\n"))
		_, _ = w.Write([]byte("data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hi\"}}]}\n\n"))
		_, _ = w.Write([]byte("data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":3,\"completion_tokens\":2}}\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer server.Close()

	effort := "low"
	events := collect(t, newTestConnector(t, server), models.ChatRequest{
		Messages:        chatMessages(),
		Stream:          true,
		ReasoningEffort: &effort,
	})

	if captured["model"] != "deepseek-flash" {
		t.Errorf("model = %v", captured["model"])
	}
	if captured["reasoning_effort"] != "low" {
		t.Errorf("reasoning_effort = %v, want low", captured["reasoning_effort"])
	}
	if captured["stream"] != true {
		t.Errorf("stream = %v, want true", captured["stream"])
	}

	if len(events) != 3 {
		t.Fatalf("events = %d, want 3: %+v", len(events), events)
	}
	if events[0].ReasoningContent != "ponder" {
		t.Errorf("event[0] = %+v, want reasoning", events[0])
	}
	if events[1].Delta != "hi" {
		t.Errorf("event[1] = %+v, want content delta", events[1])
	}
	done := events[2]
	if done.Type != models.EventDone || done.FinishReason != "stop" {
		t.Errorf("done = %+v", done)
	}
	if done.Usage == nil || done.Usage.InputTokens != 3 || done.Usage.OutputTokens != 2 {
		t.Errorf("usage = %+v", done.Usage)
	}
}

func TestChatNonStream(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"choices":[{"index":0,"message":{"role":"assistant","content":"hi","reasoning_content":"ponder","tool_calls":[{"id":"call_1","type":"function","function":{"name":"lookup","arguments":"{}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":7,"completion_tokens":4}}`))
	}))
	defer server.Close()

	events := collect(t, newTestConnector(t, server), models.ChatRequest{Messages: chatMessages()})

	if len(events) != 4 {
		t.Fatalf("events = %d, want 4: %+v", len(events), events)
	}
	if events[0].ReasoningContent != "ponder" {
		t.Errorf("event[0] = %+v", events[0])
	}
	if events[1].Delta != "hi" {
		t.Errorf("event[1] = %+v", events[1])
	}
	call := events[2].ToolCalls
	if len(call) != 1 || call[0].ID != "call_1" || call[0].Name != "lookup" {
		t.Errorf("tool calls = %+v", events[2])
	}
	if events[3].Type != models.EventDone || events[3].FinishReason != "tool_calls" {
		t.Errorf("done = %+v", events[3])
	}
}

func TestChatToolCallPassthrough(t *testing.T) {
	var captured map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&captured)
		_, _ = w.Write([]byte(`{"choices":[{"index":0,"message":{"content":"done"},"finish_reason":"stop"}]}`))
	}))
	defer server.Close()

	conn := newTestConnector(t, server)
	req := models.ChatRequest{Messages: []models.Message{
		{Role: models.RoleUser, Content: "hi"},
		{Role: models.RoleAssistant, Content: "", ToolCalls: []models.ToolCall{{ID: "call_1", Name: "lookup", Arguments: `{"q":"x"}`}}},
		{Role: models.RoleTool, ToolCallID: "call_1", Content: "result"},
	}}
	_ = collect(t, conn, req)

	messages := captured["messages"].([]any)
	assistant := messages[1].(map[string]any)
	toolCalls := assistant["tool_calls"].([]any)
	call := toolCalls[0].(map[string]any)
	if call["type"] != "function" || call["id"] != "call_1" {
		t.Errorf("tool call = %+v", call)
	}
	fn := call["function"].(map[string]any)
	if fn["name"] != "lookup" || fn["arguments"] != `{"q":"x"}` {
		t.Errorf("function = %+v", fn)
	}
	tool := messages[2].(map[string]any)
	if tool["tool_call_id"] != "call_1" {
		t.Errorf("tool message = %+v", tool)
	}
}

func TestChatHTTPErrors(t *testing.T) {
	cases := []struct {
		status int
		want   connector.Kind
	}{
		{http.StatusUnauthorized, connector.KindAuth},
		{http.StatusForbidden, connector.KindAuth},
		{http.StatusTooManyRequests, connector.KindRateLimit},
		{http.StatusInternalServerError, connector.KindUpstream},
		{http.StatusBadGateway, connector.KindUpstream},
		{http.StatusBadRequest, connector.KindUpstreamBad},
		{http.StatusNotFound, connector.KindUpstreamBad},
	}
	for _, tc := range cases {
		t.Run(http.StatusText(tc.status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
			}))
			defer server.Close()

			conn := newTestConnector(t, server)
			err := conn.Chat(context.Background(), "deepseek-flash", models.ChatRequest{Messages: chatMessages()}, nil)
			if kind := connector.KindOf(err); kind != tc.want {
				t.Errorf("kind = %q, want %q (err %v)", kind, tc.want, err)
			}
		})
	}
}

func TestChatErrorMessagePassthrough(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"message":"Invalid reasoning_effort","type":"invalid_request_error"}}`))
	}))
	defer server.Close()

	conn := newTestConnector(t, server)
	err := conn.Chat(context.Background(), "deepseek-flash", models.ChatRequest{Messages: chatMessages()}, nil)
	if !strings.Contains(err.Error(), "Invalid reasoning_effort") {
		t.Errorf("message = %q, want the upstream message", err)
	}
	var connErr *connector.Error
	if !errors.As(err, &connErr) {
		t.Fatalf("err = %v, want *connector.Error", err)
	}
	if connErr.Status != 400 {
		t.Errorf("status = %d, want 400", connErr.Status)
	}
}

func TestNewRejectsEmptyOptions(t *testing.T) {
	if _, err := New(Options{}); err == nil {
		t.Fatal("New accepted empty options")
	}
	if _, err := New(Options{APIKey: "sk"}); err == nil {
		t.Fatal("New accepted empty base url")
	}
}
