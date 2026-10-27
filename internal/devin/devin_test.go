package devin

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"kyo-router/internal/connector"
	"kyo-router/internal/devin/devinpb"
	"kyo-router/internal/models"
)

func TestChatStreamsFrames(t *testing.T) {
	requests := make(chan *devinpb.GetChatMessageRequest, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != chatPath {
			t.Errorf("path = %q, want %q", r.URL.Path, chatPath)
		}
		if got := r.Header.Get("Content-Type"); got != "application/connect+proto" {
			t.Errorf("content-type = %q", got)
		}
		if got := r.Header.Get("Connect-Protocol-Version"); got != "1" {
			t.Errorf("protocol version = %q", got)
		}
		if got := r.Header.Get("Connect-Content-Encoding"); got != "gzip" {
			t.Errorf("content encoding = %q, want gzip", got)
		}
		var request devinpb.GetChatMessageRequest
		readConnectRequest(t, r, &request)
		requests <- &request

		w.Header().Set("Content-Type", "application/connect+proto")
		w.Header().Set("Connect-Content-Encoding", "gzip")
		writeConnectMessage(t, w, &devinpb.GetChatMessageResponse{DeltaThinking: "think "}, true)
		writeConnectMessage(t, w, &devinpb.GetChatMessageResponse{DeltaText: "hello"}, true)
		writeConnectMessage(t, w, &devinpb.GetChatMessageResponse{DeltaText: " world"}, false)
		writeConnectMessage(t, w, &devinpb.GetChatMessageResponse{
			StopReason: devinpb.StopReason_STOP_REASON_STOP_PATTERN,
			Usage: &devinpb.ModelUsageStats{
				InputTokens:      10,
				OutputTokens:     5,
				CacheReadTokens:  2,
				CacheWriteTokens: 1,
			},
		}, false)
		writeEndStream(t, w, `{}`)
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
	}))
	defer server.Close()

	dev := newTestConnector(t, server, Auth{UserJWT: "test-jwt", BaseURL: server.URL}, "test-key")
	maxTokens := 123
	temperature := 0.2
	request := models.ChatRequest{
		Messages: []models.Message{
			{Role: models.RoleSystem, Content: "system prompt"},
			{Role: models.RoleUser, Content: "hi"},
			{Role: models.RoleAssistant, Content: "hello", ToolCalls: []models.ToolCall{
				{ID: "call_1", Name: "lookup", Arguments: `{"q":"x"}`},
			}},
			{Role: models.RoleTool, Content: "result", ToolCallID: "call_1"},
		},
		Temperature: &temperature,
		MaxTokens:   &maxTokens,
		Stop:        []string{"END"},
	}

	var events []models.StreamEvent
	if err := dev.Chat(context.Background(), "claude-haiku-5-5-low", request, func(event models.StreamEvent) error {
		events = append(events, event)
		return nil
	}); err != nil {
		t.Fatalf("Chat: %v", err)
	}

	if len(events) != 4 {
		t.Fatalf("events = %d, want 4: %+v", len(events), events)
	}
	if events[0].ReasoningContent != "think " || events[0].Delta != "" {
		t.Errorf("thinking event = %+v", events[0])
	}
	if events[1].Delta != "hello" || events[1].ReasoningContent != "" {
		t.Errorf("text event 1 = %+v", events[1])
	}
	if events[2].Delta != " world" {
		t.Errorf("text event 2 = %+v", events[2])
	}
	done := events[3]
	if done.Type != models.EventDone || done.FinishReason != "stop" {
		t.Errorf("done event = %+v", done)
	}
	if done.Usage == nil {
		t.Fatal("done usage is nil")
	}
	if *done.Usage != (models.Usage{InputTokens: 10, OutputTokens: 5, CacheReadTokens: 2, CacheWriteTokens: 1}) {
		t.Errorf("usage = %+v", *done.Usage)
	}

	got := <-requests
	if got.GetChatModelUid() != "claude-haiku-5-5-low" {
		t.Errorf("chat model uid = %q", got.GetChatModelUid())
	}
	if got.GetRequestType() != devinpb.ChatMessageRequestType_CHAT_MESSAGE_REQUEST_TYPE_CASCADE {
		t.Errorf("request type = %v, want CASCADE", got.GetRequestType())
	}
	if got.GetPlannerMode() != devinpb.ConversationalPlannerMode_CONVERSATIONAL_PLANNER_MODE_DEFAULT {
		t.Errorf("planner mode = %v, want DEFAULT", got.GetPlannerMode())
	}
	if got.GetCascadeId() == "" || got.GetExecutionId() == "" {
		t.Error("cascade id and execution id must be set")
	}
	if got.GetCascadeId() == got.GetExecutionId() {
		t.Error("cascade id and execution id must differ")
	}
	if got.GetPrompt() != "system prompt" {
		t.Errorf("prompt = %q", got.GetPrompt())
	}
	metadata := got.GetMetadata()
	if metadata.GetApiKey() != SessionTokenPrefix+"test-key" {
		t.Errorf("api key = %q, want exactly one prefix", metadata.GetApiKey())
	}
	if metadata.GetUserJwt() != "test-jwt" {
		t.Errorf("user jwt = %q", metadata.GetUserJwt())
	}
	if metadata.GetIdeName() != "windsurf" || metadata.GetExtensionName() != "windsurf" {
		t.Errorf("ide identity = %q/%q", metadata.GetIdeName(), metadata.GetExtensionName())
	}
	if metadata.GetIdeVersion() != DefaultIDEVersion || metadata.GetExtensionVersion() != DefaultExtensionVersion {
		t.Errorf("versions = %q/%q", metadata.GetIdeVersion(), metadata.GetExtensionVersion())
	}
	if metadata.GetLocale() != "en" {
		t.Errorf("locale = %q", metadata.GetLocale())
	}

	prompts := got.GetChatMessagePrompts()
	if len(prompts) != 3 {
		t.Fatalf("prompts = %d, want 3", len(prompts))
	}
	if prompts[0].GetSource() != devinpb.ChatMessageSource_CHAT_MESSAGE_SOURCE_USER || prompts[0].GetPrompt() != "hi" {
		t.Errorf("user prompt = %+v", prompts[0])
	}
	if prompts[1].GetSource() != devinpb.ChatMessageSource_CHAT_MESSAGE_SOURCE_SYSTEM || prompts[1].GetPrompt() != "hello" {
		t.Errorf("assistant prompt = %+v", prompts[1])
	}
	if len(prompts[1].GetToolCalls()) != 1 || prompts[1].GetToolCalls()[0].GetId() != "call_1" {
		t.Errorf("assistant tool calls = %+v", prompts[1].GetToolCalls())
	}
	if prompts[2].GetSource() != devinpb.ChatMessageSource_CHAT_MESSAGE_SOURCE_TOOL ||
		prompts[2].GetToolCallId() != "call_1" || prompts[2].GetPrompt() != "result" {
		t.Errorf("tool prompt = %+v", prompts[2])
	}

	configuration := got.GetConfiguration()
	if configuration.GetMaxTokens() != 123 || configuration.GetTemperature() != 0.2 {
		t.Errorf("configuration = %+v", configuration)
	}
	if configuration.GetFirstTemperature() != 0.2 || configuration.GetTopK() != 50 || configuration.GetTopP() != 1 {
		t.Errorf("sampling = %+v", configuration)
	}
	if configuration.GetNumCompletions() != 1 || configuration.GetMaxNewlines() != 200 {
		t.Errorf("limits = %+v", configuration)
	}
	stopPatterns := configuration.GetStopPatterns()
	if len(stopPatterns) != 6 || stopPatterns[len(stopPatterns)-1] != "END" {
		t.Errorf("stop patterns = %v, want 5 defaults plus END", stopPatterns)
	}
	if got.GetToolChoice().GetOptionName() != "auto" {
		t.Errorf("tool choice = %v", got.GetToolChoice())
	}
	if got.GetSystemPromptCacheOptions().GetType() != devinpb.CacheControlType_CACHE_CONTROL_TYPE_EPHEMERAL {
		t.Errorf("cache options = %+v", got.GetSystemPromptCacheOptions())
	}
	if !got.GetDisableParallelToolCalls() {
		t.Error("disable parallel tool calls = false")
	}
}

func TestChatToolCallDeltas(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/connect+proto")
		w.Header().Set("Connect-Content-Encoding", "gzip")
		writeConnectMessage(t, w, &devinpb.GetChatMessageResponse{
			DeltaToolCalls: []*devinpb.ChatToolCall{{Id: "call_1", Name: "lookup", ArgumentsJson: `{"q":`}},
		}, true)
		writeConnectMessage(t, w, &devinpb.GetChatMessageResponse{
			DeltaToolCalls: []*devinpb.ChatToolCall{{Id: "call_1", ArgumentsJson: `{"q":"x"}`}},
		}, true)
		writeConnectMessage(t, w, &devinpb.GetChatMessageResponse{
			StopReason: devinpb.StopReason_STOP_REASON_FUNCTION_CALL,
		}, false)
		writeEndStream(t, w, `{}`)
	}))
	defer server.Close()

	dev := newTestConnector(t, server, Auth{UserJWT: "test-jwt", BaseURL: server.URL}, "test-key")
	var events []models.StreamEvent
	if err := dev.Chat(context.Background(), "claude-haiku-5-5-low", models.ChatRequest{
		Messages: []models.Message{{Role: models.RoleUser, Content: "hi"}},
	}, func(event models.StreamEvent) error {
		events = append(events, event)
		return nil
	}); err != nil {
		t.Fatalf("Chat: %v", err)
	}

	if len(events) != 3 {
		t.Fatalf("events = %d, want 3: %+v", len(events), events)
	}
	first := events[0].ToolCalls
	if len(first) != 1 || first[0].ID != "call_1" || first[0].Name != "lookup" || first[0].Arguments != `{"q":` {
		t.Errorf("first tool call = %+v", first)
	}
	second := events[1].ToolCalls
	if len(second) != 1 || second[0].Index != 0 || second[0].Arguments != `"x"}` {
		t.Errorf("second tool call = %+v", second)
	}
	if events[2].FinishReason != "tool_calls" {
		t.Errorf("finish reason = %q, want tool_calls", events[2].FinishReason)
	}
}

func TestChatTrailerErrors(t *testing.T) {
	cases := []struct {
		name    string
		trailer string
		want    connector.Kind
	}{
		{"unauthenticated is auth", `{"error":{"code":"unauthenticated","message":"nope"}}`, connector.KindAuth},
		{"permission denied is auth", `{"error":{"code":"permission_denied","message":"nope"}}`, connector.KindAuth},
		{"resource exhausted is rate limit", `{"error":{"code":"resource_exhausted","message":"nope"}}`, connector.KindRateLimit},
		{"unavailable is upstream", `{"error":{"code":"unavailable","message":"nope"}}`, connector.KindUpstream},
		{"internal is upstream", `{"error":{"code":"internal","message":"nope"}}`, connector.KindUpstream},
		{"failed precondition is upstream bad", `{"error":{"code":"failed_precondition","message":"nope"}}`, connector.KindUpstreamBad},
		{"invalid argument is upstream bad", `{"error":{"code":"invalid_argument","message":"nope"}}`, connector.KindUpstreamBad},
		{"message text does not classify", `{"error":{"code":"unauthenticated","message":"rate limit exceeded, retry later"}}`, connector.KindAuth},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/connect+proto")
				writeEndStream(t, w, tc.trailer)
			}))
			defer server.Close()

			dev := newTestConnector(t, server, Auth{UserJWT: "test-jwt", BaseURL: server.URL}, "test-key")
			err := dev.Chat(context.Background(), "claude-haiku-5-5-low", models.ChatRequest{
				Messages: []models.Message{{Role: models.RoleUser, Content: "hi"}},
			}, func(models.StreamEvent) error { return nil })
			if kind := connector.KindOf(err); kind != tc.want {
				t.Errorf("kind = %q, want %q (err %v)", kind, tc.want, err)
			}
		})
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
		{http.StatusServiceUnavailable, connector.KindUpstream},
		{http.StatusBadRequest, connector.KindUpstreamBad},
		{http.StatusNotFound, connector.KindUpstreamBad},
	}
	for _, tc := range cases {
		t.Run(http.StatusText(tc.status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(`{"error":"unauthorized rate limit upstream"}`))
			}))
			defer server.Close()

			dev := newTestConnector(t, server, Auth{UserJWT: "test-jwt", BaseURL: server.URL}, "test-key")
			err := dev.Chat(context.Background(), "claude-haiku-5-5-low", models.ChatRequest{
				Messages: []models.Message{{Role: models.RoleUser, Content: "hi"}},
			}, func(models.StreamEvent) error { return nil })
			if kind := connector.KindOf(err); kind != tc.want {
				t.Errorf("kind = %q, want %q (err %v)", kind, tc.want, err)
			}
		})
	}
}

func TestChatRateLimitKeepsRetryAfter(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Retry-After", "3")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer server.Close()

	dev := newTestConnector(t, server, Auth{UserJWT: "test-jwt", BaseURL: server.URL}, "test-key")
	err := dev.Chat(context.Background(), "claude-haiku-5-5-low", models.ChatRequest{
		Messages: []models.Message{{Role: models.RoleUser, Content: "hi"}},
	}, func(models.StreamEvent) error { return nil })
	var connErr *connector.Error
	if !errors.As(err, &connErr) {
		t.Fatalf("err = %v, want *connector.Error", err)
	}
	if connErr.Kind != connector.KindRateLimit {
		t.Fatalf("kind = %q, want RateLimit", connErr.Kind)
	}
	if connErr.RetryAfter != 3*time.Second {
		t.Errorf("retry after = %v, want 3s", connErr.RetryAfter)
	}
}

func TestChatNetworkErrorIsNetwork(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	baseURL := server.URL
	server.Close()

	dev, err := New(Options{
		Credentials: Credentials{APIKey: NormalizeSessionToken("test-key")},
		TokenSource: staticTokenSource{auth: Auth{UserJWT: "test-jwt", BaseURL: baseURL}},
		Transport:   http.DefaultClient,
		Clock:       &fakeClock{now: fakeNow},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	err = dev.Chat(context.Background(), "claude-haiku-5-5-low", models.ChatRequest{
		Messages: []models.Message{{Role: models.RoleUser, Content: "hi"}},
	}, func(models.StreamEvent) error { return nil })
	if kind := connector.KindOf(err); kind != connector.KindNetwork {
		t.Fatalf("kind = %q, want Network (err %v)", kind, err)
	}
}

func TestChatCanceledContextIsNetwork(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer server.Close()

	dev := newTestConnector(t, server, Auth{UserJWT: "test-jwt", BaseURL: server.URL}, "test-key")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := dev.Chat(ctx, "claude-haiku-5-5-low", models.ChatRequest{
		Messages: []models.Message{{Role: models.RoleUser, Content: "hi"}},
	}, func(models.StreamEvent) error { return nil })
	if kind := connector.KindOf(err); kind != connector.KindNetwork {
		t.Fatalf("kind = %q, want Network (err %v)", kind, err)
	}
}

func TestChatTruncatedStreamIsAnError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/connect+proto")
		w.Header().Set("Connect-Content-Encoding", "gzip")
		writeConnectMessage(t, w, &devinpb.GetChatMessageResponse{DeltaText: "hello"}, true)
	}))
	defer server.Close()

	dev := newTestConnector(t, server, Auth{UserJWT: "test-jwt", BaseURL: server.URL}, "test-key")
	var events []models.StreamEvent
	err := dev.Chat(context.Background(), "claude-haiku-5-5-low", models.ChatRequest{
		Messages: []models.Message{{Role: models.RoleUser, Content: "hi"}},
	}, func(event models.StreamEvent) error {
		events = append(events, event)
		return nil
	})
	if err == nil {
		t.Fatal("Chat returned nil for a stream with no end-stream trailer")
	}
	if kind := connector.KindOf(err); kind != connector.KindUpstream {
		t.Errorf("kind = %q, want Upstream (err %v)", kind, err)
	}
	if len(events) != 1 || events[0].Delta != "hello" {
		t.Errorf("events = %+v, want the text delta before the failure", events)
	}
}

func TestChatEmitErrorStopsStream(t *testing.T) {
	stop := errors.New("client went away")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/connect+proto")
		w.Header().Set("Connect-Content-Encoding", "gzip")
		writeConnectMessage(t, w, &devinpb.GetChatMessageResponse{DeltaText: "hello"}, true)
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		<-r.Context().Done()
	}))
	defer server.Close()

	dev := newTestConnector(t, server, Auth{UserJWT: "test-jwt", BaseURL: server.URL}, "test-key")
	err := dev.Chat(context.Background(), "claude-haiku-5-5-low", models.ChatRequest{
		Messages: []models.Message{{Role: models.RoleUser, Content: "hi"}},
	}, func(models.StreamEvent) error { return stop })
	if !errors.Is(err, stop) {
		t.Fatalf("err = %v, want the emit error", err)
	}
}

func TestChatRejectsBadRequests(t *testing.T) {
	cases := []struct {
		name     string
		upstream string
		request  models.ChatRequest
		code     string
	}{
		{
			name:     "empty upstream",
			upstream: "",
			request:  models.ChatRequest{Messages: []models.Message{{Role: models.RoleUser, Content: "hi"}}},
			code:     "model_required",
		},
		{
			name:     "no messages",
			upstream: "claude-haiku-5-5-low",
			request:  models.ChatRequest{},
			code:     "messages_required",
		},
		{
			name:     "tool without id",
			upstream: "claude-haiku-5-5-low",
			request:  models.ChatRequest{Messages: []models.Message{{Role: models.RoleTool, Content: "x"}}},
			code:     "tool_call_id_required",
		},
		{
			name:     "unknown role",
			upstream: "claude-haiku-5-5-low",
			request:  models.ChatRequest{Messages: []models.Message{{Role: "narrator", Content: "x"}}},
			code:     "unknown_role",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.NotFoundHandler())
			defer server.Close()
			dev := newTestConnector(t, server, Auth{UserJWT: "jwt", BaseURL: server.URL}, "test-key")
			err := dev.Chat(context.Background(), tc.upstream, tc.request, func(models.StreamEvent) error { return nil })
			var connErr *connector.Error
			if !errors.As(err, &connErr) {
				t.Fatalf("err = %v, want *connector.Error", err)
			}
			if connErr.Kind != connector.KindBadRequest || connErr.Code != tc.code {
				t.Errorf("error = %+v, want BadRequest %s", connErr, tc.code)
			}
		})
	}
}

func TestChatTokenErrorIsReturned(t *testing.T) {
	authErr := &connector.Error{Kind: connector.KindAuth, Code: "auth_failed", Message: "nope"}
	dev, err := New(Options{
		Credentials: Credentials{APIKey: NormalizeSessionToken("test-key")},
		TokenSource: staticTokenSource{err: authErr},
		Transport:   http.DefaultClient,
		Clock:       &fakeClock{now: fakeNow},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	err = dev.Chat(context.Background(), "claude-haiku-5-5-low", models.ChatRequest{
		Messages: []models.Message{{Role: models.RoleUser, Content: "hi"}},
	}, func(models.StreamEvent) error { return nil })
	if !errors.Is(err, authErr) {
		t.Fatalf("err = %v, want the token error", err)
	}
}

func TestBuildPromptsDeveloperRole(t *testing.T) {
	system, prompts, err := buildPrompts([]models.Message{
		{Role: models.RoleDeveloper, Content: "dev rules"},
		{Role: models.RoleSystem, Content: "sys rules"},
		{Role: models.RoleUser, Content: "hi"},
	}, "cascade-1")
	if err != nil {
		t.Fatalf("buildPrompts: %v", err)
	}
	if system != "dev rules\n\nsys rules" {
		t.Errorf("system = %q, want both parts joined", system)
	}
	if len(prompts) != 1 || prompts[0].GetPrompt() != "hi" {
		t.Errorf("prompts = %+v, want one user turn", prompts)
	}
}
