package api

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"

	"kyo-router/internal/connector"
	"kyo-router/internal/httpx"
	"kyo-router/internal/models"
)

// chunk is the OpenAI chat.completion.chunk shape sent per stream event.
type chunk struct {
	ID      string         `json:"id"`
	Object  string         `json:"object"`
	Created int64          `json:"created"`
	Model   string         `json:"model"`
	Choices []chunkChoice  `json:"choices"`
	Usage   *responseUsage `json:"usage,omitempty"`
}

type chunkChoice struct {
	Index        int        `json:"index"`
	Delta        chunkDelta `json:"delta"`
	FinishReason *string    `json:"finish_reason"`
}

type chunkDelta struct {
	Role             string          `json:"role,omitempty"`
	Content          string          `json:"content,omitempty"`
	ReasoningContent string          `json:"reasoning_content,omitempty"`
	ToolCalls        []chunkToolCall `json:"tool_calls,omitempty"`
}

type chunkToolCall struct {
	Index    int    `json:"index"`
	ID       string `json:"id,omitempty"`
	Type     string `json:"type,omitempty"`
	Function struct {
		Name      string `json:"name,omitempty"`
		Arguments string `json:"arguments,omitempty"`
	} `json:"function"`
}

type responseUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

func usageOf(u *models.Usage) *responseUsage {
	if u == nil {
		return nil
	}
	return &responseUsage{
		PromptTokens:     u.InputTokens,
		CompletionTokens: u.OutputTokens,
		TotalTokens:      u.InputTokens + u.OutputTokens,
	}
}

// streamEncoder writes one OpenAI SSE response for the canonical event flow.
// Headers leave on the first event, not at construction: a connector that
// fails before producing anything still gets a real error status. A mid-stream
// failure degrades to an in-band error object.
type streamEncoder struct {
	w       http.ResponseWriter
	flusher *http.ResponseController
	id      string
	created int64
	model   string
	sent    bool
	started bool
}

func newStreamEncoder(w http.ResponseWriter, model string, created int64) *streamEncoder {
	return &streamEncoder{
		w:       w,
		flusher: http.NewResponseController(w),
		id:      "chatcmpl-" + randHex(12),
		created: created,
		model:   model,
	}
}

func (e *streamEncoder) start() {
	if e.started {
		return
	}
	h := e.w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("Connection", "keep-alive")
	e.w.WriteHeader(http.StatusOK)
	e.started = true
}

func (e *streamEncoder) emit(event models.StreamEvent) error {
	e.start()
	c := chunk{ID: e.id, Object: "chat.completion.chunk", Created: e.created, Model: e.model}
	switch event.Type {
	case models.EventDelta:
		delta := chunkDelta{Content: event.Delta, ReasoningContent: event.ReasoningContent}
		if !e.sent {
			delta.Role = "assistant"
		}
		for _, call := range event.ToolCalls {
			out := chunkToolCall{Index: call.Index}
			if call.ID != "" {
				out.ID = call.ID
				out.Type = "function"
			}
			out.Function.Name = call.Name
			out.Function.Arguments = call.Arguments
			delta.ToolCalls = append(delta.ToolCalls, out)
		}
		c.Choices = []chunkChoice{{Index: 0, Delta: delta}}
	case models.EventDone:
		reason := event.FinishReason
		c.Choices = []chunkChoice{{Index: 0, Delta: chunkDelta{}, FinishReason: &reason}}
		c.Usage = usageOf(event.Usage)
	}
	if err := e.write(c); err != nil {
		return err
	}
	e.sent = true
	return nil
}

func (e *streamEncoder) write(v any) error {
	payload, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if _, err := e.w.Write([]byte("data: ")); err != nil {
		return err
	}
	if _, err := e.w.Write(payload); err != nil {
		return err
	}
	if _, err := e.w.Write([]byte("\n\n")); err != nil {
		return err
	}
	return e.flusher.Flush()
}

// finish writes the [DONE] sentinel once the stream started.
func (e *streamEncoder) finish() {
	if !e.started {
		return
	}
	_, _ = e.w.Write([]byte("data: " + httpx.Done + "\n\n"))
	_ = e.flusher.Flush()
}

// fail writes an in-band error once headers already left.
func (e *streamEncoder) fail(kind connector.Kind, code, message string) {
	_ = e.write(errorEnvelope{Error: errorDetail{Message: message, Type: string(kind), Code: code}})
}

// completionBuilder folds canonical events into one non-streamed response.
type completionBuilder struct {
	content   strings.Builder
	reasoning strings.Builder
	calls     []completionToolCall
	finish    string
	usage     *models.Usage
}

type completionToolCall struct {
	index     int
	id        string
	name      string
	arguments strings.Builder
}

func (b *completionBuilder) add(event models.StreamEvent) {
	switch event.Type {
	case models.EventDelta:
		b.content.WriteString(event.Delta)
		b.reasoning.WriteString(event.ReasoningContent)
		for _, call := range event.ToolCalls {
			for len(b.calls) <= call.Index {
				b.calls = append(b.calls, completionToolCall{index: len(b.calls)})
			}
			target := &b.calls[call.Index]
			if call.ID != "" {
				target.id = call.ID
			}
			if call.Name != "" {
				target.name = call.Name
			}
			target.arguments.WriteString(call.Arguments)
		}
	case models.EventDone:
		b.finish = event.FinishReason
		b.usage = event.Usage
	}
}

// completion is the OpenAI chat.completion shape for non-stream replies.
type completion struct {
	ID      string             `json:"id"`
	Object  string             `json:"object"`
	Created int64              `json:"created"`
	Model   string             `json:"model"`
	Choices []completionChoice `json:"choices"`
	Usage   *responseUsage     `json:"usage,omitempty"`
}

type completionChoice struct {
	Index        int               `json:"index"`
	Message      completionMessage `json:"message"`
	FinishReason string            `json:"finish_reason"`
}

type completionMessage struct {
	Role             string          `json:"role"`
	Content          string          `json:"content"`
	ReasoningContent string          `json:"reasoning_content,omitempty"`
	ToolCalls        []chunkToolCall `json:"tool_calls,omitempty"`
}

func (b *completionBuilder) build(model string, created int64) completion {
	message := completionMessage{
		Role:             "assistant",
		Content:          b.content.String(),
		ReasoningContent: b.reasoning.String(),
	}
	for _, call := range b.calls {
		out := chunkToolCall{Index: call.index, ID: call.id, Type: "function"}
		out.Function.Name = call.name
		out.Function.Arguments = call.arguments.String()
		message.ToolCalls = append(message.ToolCalls, out)
	}
	finish := b.finish
	if finish == "" {
		finish = "stop"
	}
	return completion{
		ID:      "chatcmpl-" + randHex(12),
		Object:  "chat.completion",
		Created: created,
		Model:   model,
		Choices: []completionChoice{{Index: 0, Message: message, FinishReason: finish}},
		Usage:   usageOf(b.usage),
	}
}

func randHex(bytes int) string {
	raw := make([]byte, bytes)
	if _, err := rand.Read(raw); err != nil {
		return "kyorouter"
	}
	return hex.EncodeToString(raw)
}
