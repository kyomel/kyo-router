package oai

import (
	"encoding/json"
	"testing"

	"kyo-router/internal/models"
)

func TestMarshalChatRequestWire(t *testing.T) {
	temperature := 0.5
	maxTokens := 100
	effort := "low"
	tool := models.Tool{Type: "function"}
	tool.Function.Name = "lookup"
	tool.Function.Description = "find things"
	tool.Function.Parameters = json.RawMessage(`{"type":"object"}`)

	req := models.ChatRequest{
		Model: "client-model",
		Messages: []models.Message{
			{Role: models.RoleDeveloper, Content: "rules"},
			{Role: models.RoleUser, Content: "hi"},
			{Role: models.RoleAssistant, ToolCalls: []models.ToolCall{{ID: "c1", Name: "lookup", Arguments: `{}`}}},
			{Role: models.RoleAssistant, Content: "plain reply"},
			{Role: models.RoleAssistant, Content: "kept", ReasoningContent: "real reasoning"},
			{Role: models.RoleTool, Content: "out", ToolCallID: "c1"},
		},
		Stream:          true,
		Temperature:     &temperature,
		MaxTokens:       &maxTokens,
		Stop:            []string{"END"},
		Tools:           []models.Tool{tool},
		ToolChoice:      json.RawMessage(`"auto"`),
		ReasoningEffort: &effort,
	}

	body, err := MarshalChatRequest("upstream-model", req, RequestOptions{
		StreamOptions:     true,
		ReasoningEffort:   true,
		ReasoningEchoBack: true,
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var wire wireRequest
	if err := json.Unmarshal(body, &wire); err != nil {
		t.Fatalf("decode wire: %v", err)
	}
	if wire.Model != "upstream-model" {
		t.Errorf("model = %q, want upstream-model", wire.Model)
	}
	if len(wire.Messages) != 6 {
		t.Fatalf("messages = %d, want 6", len(wire.Messages))
	}
	if wire.Messages[0].Role != "system" || wire.Messages[0].Content != "rules" {
		t.Errorf("developer message = %+v, want system role", wire.Messages[0])
	}
	if wire.Messages[2].Reasoning != "reasoning not retained" {
		t.Errorf("tool-call assistant reasoning = %q, want placeholder", wire.Messages[2].Reasoning)
	}
	if wire.Messages[2].ToolCalls[0].Function.Name != "lookup" {
		t.Errorf("tool call = %+v", wire.Messages[2].ToolCalls[0])
	}
	if wire.Messages[3].Reasoning != "" {
		t.Errorf("plain assistant reasoning = %q, want empty", wire.Messages[3].Reasoning)
	}
	if wire.Messages[4].Reasoning != "real reasoning" {
		t.Errorf("retained reasoning = %q, want real reasoning", wire.Messages[4].Reasoning)
	}
	if wire.Messages[5].Role != "tool" || wire.Messages[5].ToolCallID != "c1" {
		t.Errorf("tool result = %+v", wire.Messages[5])
	}
	if len(wire.Tools) != 1 || wire.Tools[0].Function.Name != "lookup" {
		t.Errorf("tools = %+v", wire.Tools)
	}
	if string(wire.ToolChoice) != `"auto"` {
		t.Errorf("tool_choice = %s, want \"auto\"", wire.ToolChoice)
	}
	if wire.StreamOptions == nil || !wire.StreamOptions.IncludeUsage {
		t.Errorf("stream_options = %+v, want include_usage", wire.StreamOptions)
	}
	if wire.ReasoningEffort == nil || *wire.ReasoningEffort != "low" {
		t.Errorf("reasoning_effort = %v, want low", wire.ReasoningEffort)
	}
}

func TestMarshalChatRequestEchoOff(t *testing.T) {
	req := models.ChatRequest{
		Messages: []models.Message{
			{Role: models.RoleAssistant, ToolCalls: []models.ToolCall{{ID: "c1", Name: "f", Arguments: "{}"}}},
		},
	}
	body, err := MarshalChatRequest("m", req, RequestOptions{})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var wire wireRequest
	if err := json.Unmarshal(body, &wire); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if wire.Messages[0].Reasoning != "" {
		t.Errorf("reasoning = %q, want empty without echo option", wire.Messages[0].Reasoning)
	}
	if wire.StreamOptions != nil {
		t.Errorf("stream_options = %+v, want nil on non-stream request", wire.StreamOptions)
	}
}
