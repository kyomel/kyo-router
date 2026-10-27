// Package models holds the canonical chat types. Provider wire shapes stop at
// the connector boundary; everything inside the router passes these types.
package models

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Role is the speaker of one chat message.
type Role string

const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
	// RoleDeveloper is OpenAI's renamed system role. Providers that only take
	// "system" map it at their wire boundary.
	RoleDeveloper Role = "developer"
)

// ToolCall is one assistant tool call in the transcript.
type ToolCall struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// UnmarshalJSON accepts both the OpenAI wire shape (nested function object)
// and the flat canonical shape. Inbound client history arrives nested.
func (t *ToolCall) UnmarshalJSON(data []byte) error {
	var wire struct {
		ID       string `json:"id"`
		Function struct {
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
		} `json:"function"`
	}
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}
	if wire.Function.Name != "" || wire.Function.Arguments != "" {
		t.ID, t.Name, t.Arguments = wire.ID, wire.Function.Name, wire.Function.Arguments
		return nil
	}
	var flat struct {
		ID        string `json:"id"`
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	}
	if err := json.Unmarshal(data, &flat); err != nil {
		return err
	}
	t.ID, t.Name, t.Arguments = flat.ID, flat.Name, flat.Arguments
	return nil
}

// Message is one chat turn. Content is text only; image parts are not part of
// the canonical type yet. The wire accepts content as a string or an array of
// parts (real clients send parts); non-text parts are dropped at the boundary.
type Message struct {
	Role      Role       `json:"role"`
	Content   string     `json:"content"`
	Name      string     `json:"name,omitempty"`
	ToolCalls []ToolCall `json:"tool_calls,omitempty"`
	// ToolCallID links a tool result to the assistant call it answers.
	ToolCallID string `json:"tool_call_id,omitempty"`
	// ReasoningContent echoes prior assistant reasoning back in multi-turn
	// history. DeepSeek thinking mode rejects requests that omit it.
	ReasoningContent string `json:"reasoning_content,omitempty"`
}

// UnmarshalJSON accepts content in both OpenAI forms: "text" and
// [{type:"text",text:"..."}]. Image and other non-text parts drop here; the
// canonical image channel lands with a dedicated type.
func (m *Message) UnmarshalJSON(data []byte) error {
	var raw struct {
		Role       Role            `json:"role"`
		Content    json.RawMessage `json:"content"`
		Name       string          `json:"name"`
		ToolCalls  []ToolCall      `json:"tool_calls"`
		ToolCallID string          `json:"tool_call_id"`
		Reasoning  string          `json:"reasoning_content"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	content, err := decodeContent(raw.Content)
	if err != nil {
		return err
	}
	m.Role, m.Content, m.Name = raw.Role, content, raw.Name
	m.ToolCalls, m.ToolCallID = raw.ToolCalls, raw.ToolCallID
	m.ReasoningContent = raw.Reasoning
	return nil
}

func decodeContent(raw json.RawMessage) (string, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return "", nil
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s, nil
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &parts); err != nil {
		return "", fmt.Errorf("content must be a string or an array of parts")
	}
	var b strings.Builder
	for _, part := range parts {
		if part.Type == "text" || part.Type == "input_text" {
			b.WriteString(part.Text)
		}
	}
	return b.String(), nil
}

// Tool is one OpenAI function tool definition, forwarded verbatim.
type Tool struct {
	Type     string `json:"type"`
	Function struct {
		Name        string          `json:"name"`
		Description string          `json:"description,omitempty"`
		Parameters  json.RawMessage `json:"parameters,omitempty"`
	} `json:"function"`
}

// ChatRequest is the canonical inbound request. It is decoded once at the HTTP
// boundary and passed down without re-parsing.
type ChatRequest struct {
	Model       string    `json:"model"`
	Messages    []Message `json:"messages"`
	Stream      bool      `json:"stream,omitempty"`
	Temperature *float64  `json:"temperature,omitempty"`
	MaxTokens   *int      `json:"max_tokens,omitempty"`
	Stop        []string  `json:"stop,omitempty"`
	Tools       []Tool    `json:"tools,omitempty"`
	// ToolChoice passes through untouched - "auto"/"none"/"required" or a
	// function selector object.
	ToolChoice json.RawMessage `json:"tool_choice,omitempty"`
	// ReasoningEffort is the OpenAI reasoning_effort value, passed verbatim.
	// Each connector maps it to its wire format; Devin ignores it because the
	// effort lives in the model uid suffix instead.
	ReasoningEffort *string `json:"reasoning_effort,omitempty"`
}

// EventType is the kind of a StreamEvent.
type EventType string

const (
	EventDelta EventType = "delta"
	EventDone  EventType = "done"
)

// Usage is the token accounting of one completed reply.
type Usage struct {
	InputTokens      int
	OutputTokens     int
	CacheReadTokens  int
	CacheWriteTokens int
}

// ToolCallDelta is one fragment of a streamed tool call. ID and Name are set
// on the first fragment only; Arguments carries only the new text.
type ToolCallDelta struct {
	Index     int
	ID        string
	Name      string
	Arguments string
}

// StreamEvent is the canonical unit of a streaming reply. Connectors emit it;
// the API encodes it as OpenAI SSE. ReasoningContent maps to the DeepSeek-style
// delta.reasoning_content field (docs/02-connectors.md, pick B).
type StreamEvent struct {
	Type             EventType
	Delta            string
	ReasoningContent string
	ToolCalls        []ToolCallDelta
	Usage            *Usage
	FinishReason     string
}
