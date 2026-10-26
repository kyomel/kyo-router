// Package models holds the canonical chat types. Provider wire shapes stop at
// the connector boundary; everything inside the router passes these types.
package models

// Role is the speaker of one chat message.
type Role string

const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

// ToolCall is one assistant tool call in the transcript.
type ToolCall struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// Message is one chat turn. Content is text only; image parts are not part of
// the canonical type yet.
type Message struct {
	Role      Role       `json:"role"`
	Content   string     `json:"content"`
	Name      string     `json:"name,omitempty"`
	ToolCalls []ToolCall `json:"tool_calls,omitempty"`
	// ToolCallID links a tool result to the assistant call it answers.
	ToolCallID string `json:"tool_call_id,omitempty"`
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
