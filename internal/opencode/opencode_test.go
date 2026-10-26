package opencode

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"kyo-router/internal/connector"
	"kyo-router/internal/models"
)

func newTestConnector(t *testing.T, server *httptest.Server, opts Options) *Connector {
	t.Helper()
	opts.APIKey = "oc_sk_test"
	opts.BaseURL = server.URL
	conn, err := New(opts)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return conn
}

func chatMessages() []models.Message {
	return []models.Message{{Role: models.RoleUser, Content: "hi"}}
}

func collect(t *testing.T, conn *Connector, upstream string, req models.ChatRequest) []models.StreamEvent {
	t.Helper()
	var events []models.StreamEvent
	err := conn.Chat(context.Background(), upstream, req, func(event models.StreamEvent) error {
		events = append(events, event)
		return nil
	})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	return events
}

func TestChatPassthrough(t *testing.T) {
	var captured map[string]any
	var sessions []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != chatPath {
			t.Errorf("path = %q, want %q", r.URL.Path, chatPath)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer oc_sk_test" {
			t.Errorf("authorization = %q", got)
		}
		session := r.Header.Get("x-opencode-session")
		if session == "" {
			t.Error("x-opencode-session missing")
		}
		sessions = append(sessions, session)
		_ = json.NewDecoder(r.Body).Decode(&captured)
		_, _ = w.Write([]byte(`{"choices":[{"index":0,"message":{"content":"hi"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":2}}`))
	}))
	defer server.Close()

	conn := newTestConnector(t, server, Options{})
	req := models.ChatRequest{Messages: chatMessages()}
	events := collect(t, conn, "glm-5.3", req)
	// Second call gets a fresh session in request mode.
	_ = collect(t, conn, "glm-5.3", req)

	if captured["model"] != "glm-5.3" {
		t.Errorf("model = %v", captured["model"])
	}
	if _, present := captured["reasoning_effort"]; present {
		t.Error("reasoning_effort leaked onto a chat-model request")
	}
	if len(events) != 2 || events[0].Delta != "hi" || events[1].FinishReason != "stop" {
		t.Errorf("events = %+v", events)
	}
	if sessions[0] == sessions[1] {
		t.Error("request mode reused the session id; want a fresh one per call")
	}
}

func TestChatFixedSession(t *testing.T) {
	var sessions []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sessions = append(sessions, r.Header.Get("x-opencode-session"))
		_, _ = w.Write([]byte(`{"choices":[{"index":0,"message":{"content":"hi"},"finish_reason":"stop"}]}`))
	}))
	defer server.Close()

	conn := newTestConnector(t, server, Options{SessionMode: SessionFixed})
	req := models.ChatRequest{Messages: chatMessages()}
	_ = collect(t, conn, "glm-5.3", req)
	_ = collect(t, conn, "glm-5.3", req)
	if sessions[0] != sessions[1] || sessions[0] == "" {
		t.Errorf("fixed mode sessions = %v, want one stable value", sessions)
	}
}

func TestResponsesStream(t *testing.T) {
	var captured map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != responsesPath {
			t.Errorf("path = %q, want %q", r.URL.Path, responsesPath)
		}
		_ = json.NewDecoder(r.Body).Decode(&captured)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"ye\"}\n\n"))
		_, _ = w.Write([]byte("event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"s\"}\n\n"))
		_, _ = w.Write([]byte("event: response.output_item.done\ndata: {\"type\":\"response.output_item.done\",\"item\":{\"type\":\"reasoning\",\"summary\":[{\"text\":\"ponder\"}]}}\n\n"))
		_, _ = w.Write([]byte("event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output\":[],\"usage\":{\"input_tokens\":5,\"output_tokens\":3}}}\n\n"))
	}))
	defer server.Close()

	effort := "low"
	conn := newTestConnector(t, server, Options{ResponsesModels: []string{"grok-4.7"}})
	events := collect(t, conn, "grok-4.7", models.ChatRequest{
		Messages:        chatMessages(),
		Stream:          true,
		ReasoningEffort: &effort,
	})

	reasoning, _ := captured["reasoning"].(map[string]any)
	if reasoning["effort"] != "low" {
		t.Errorf("reasoning = %v, want effort low", captured["reasoning"])
	}
	if captured["stream"] != true {
		t.Errorf("stream = %v", captured["stream"])
	}
	input := captured["input"].([]any)
	first := input[0].(map[string]any)
	if first["type"] != "message" || first["role"] != "user" {
		t.Errorf("input[0] = %+v", first)
	}

	if len(events) != 4 {
		t.Fatalf("events = %d, want 4: %+v", len(events), events)
	}
	if events[0].Delta != "ye" || events[1].Delta != "s" {
		t.Errorf("deltas = %+v", events[:2])
	}
	if events[2].ReasoningContent != "ponder" {
		t.Errorf("reasoning = %+v", events[2])
	}
	done := events[3]
	if done.Type != models.EventDone || done.Usage == nil || done.Usage.InputTokens != 5 {
		t.Errorf("done = %+v", done)
	}
}

func TestResponsesNonStream(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"status":"completed","output":[{"type":"reasoning","summary":[{"text":"ponder"}]},{"type":"message","content":[{"text":"yes"}]}],"usage":{"input_tokens":9,"output_tokens":4}}`))
	}))
	defer server.Close()

	conn := newTestConnector(t, server, Options{ResponsesModels: []string{"grok-4.7"}})
	events := collect(t, conn, "grok-4.7", models.ChatRequest{Messages: chatMessages()})

	if len(events) != 3 {
		t.Fatalf("events = %d, want 3: %+v", len(events), events)
	}
	if events[0].ReasoningContent != "ponder" || events[1].Delta != "yes" {
		t.Errorf("events = %+v", events)
	}
	if events[2].Usage == nil || events[2].Usage.OutputTokens != 4 {
		t.Errorf("done = %+v", events[2])
	}
}

func TestErrorClassification(t *testing.T) {
	cases := []struct {
		name     string
		status   int
		body     string
		wantKind connector.Kind
	}{
		{"missing session", http.StatusBadRequest,
			`{"error":{"message":"session required","type":"invalid_request_error","code":"MissingSessionID"}}`, connector.KindBadRequest},
		{"wrong protocol", http.StatusBadRequest,
			`{"error":{"message":"not supported","type":"invalid_request_error","code":"ModelProtocolUnsupported"}}`, connector.KindUnsupported},
		{"free tier", http.StatusForbidden,
			`{"error":{"message":"no funds","code":"FreeTierError"}}`, connector.KindUnsupported},
		{"unavailable", http.StatusBadRequest,
			`{"error":{"message":"Model is unavailable"}}`, connector.KindUnsupported},
		{"funds", http.StatusBadRequest,
			`{"error":{"message":"Insufficient account funds"}}`, connector.KindAuth},
		{"rate limit", http.StatusTooManyRequests,
			`{"error":{"message":"slow down"}}`, connector.KindRateLimit},
		{"server", http.StatusBadGateway,
			`{"error":{"message":"oops"}}`, connector.KindUpstream},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()

			conn := newTestConnector(t, server, Options{})
			err := conn.Chat(context.Background(), "glm-5.3", models.ChatRequest{Messages: chatMessages()}, nil)
			if kind := connector.KindOf(err); kind != tc.wantKind {
				t.Errorf("kind = %q, want %q (err %v)", kind, tc.wantKind, err)
			}
		})
	}
}

func TestNewRejectsBadOptions(t *testing.T) {
	if _, err := New(Options{}); err == nil {
		t.Fatal("New accepted empty options")
	}
	if _, err := New(Options{APIKey: "k", BaseURL: "u", SessionMode: "weird"}); err == nil {
		t.Fatal("New accepted unknown session mode")
	}
}

var _ = errors.As
