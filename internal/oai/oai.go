// Package oai is the shared OpenAI-compatible chat codec. Connectors whose
// upstream speaks chat/completions (deepseek, opencode chat models) encode
// requests and decode streams through it so the wire format lives in one
// place; provider quirks stay in each connector's request options.
package oai

import (
	"bufio"
	"encoding/json"
	"io"

	"kyo-router/internal/connector"
	"kyo-router/internal/httpx"
	"kyo-router/internal/models"
)

// RequestOptions covers the optional fields an OpenAI-compatible upstream
// accepts. Each connector sets only what its provider honors.
type RequestOptions struct {
	StreamOptions   bool // send stream_options{include_usage} when Stream
	ReasoningEffort bool // forward models.ChatRequest.ReasoningEffort
	// ReasoningEchoBack synthesizes a placeholder reasoning_content on
	// assistant history messages that lost it. DeepSeek thinking mode
	// rejects requests whose replayed tool-call turns omit the field.
	ReasoningEchoBack bool
}

type streamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

type wireMessage struct {
	Role       string         `json:"role"`
	Content    string         `json:"content"`
	Name       string         `json:"name,omitempty"`
	ToolCallID string         `json:"tool_call_id,omitempty"`
	ToolCalls  []wireToolCall `json:"tool_calls,omitempty"`
	Reasoning  string         `json:"reasoning_content,omitempty"`
}

type wireToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type wireRequest struct {
	Model           string          `json:"model"`
	Messages        []wireMessage   `json:"messages"`
	Stream          bool            `json:"stream"`
	StreamOptions   *streamOptions  `json:"stream_options,omitempty"`
	Temperature     *float64        `json:"temperature,omitempty"`
	MaxTokens       *int            `json:"max_tokens,omitempty"`
	Stop            []string        `json:"stop,omitempty"`
	Tools           []models.Tool   `json:"tools,omitempty"`
	ToolChoice      json.RawMessage `json:"tool_choice,omitempty"`
	ReasoningEffort *string         `json:"reasoning_effort,omitempty"`
}

// MarshalChatRequest encodes the canonical request to the chat/completions
// wire shape. stream_options goes out only when Stream is set - several
// upstreams 400 on stream_options without it.
func MarshalChatRequest(upstream string, req models.ChatRequest, opts RequestOptions) ([]byte, error) {
	messages := make([]wireMessage, 0, len(req.Messages))
	for _, m := range req.Messages {
		role := string(m.Role)
		if m.Role == models.RoleDeveloper {
			role = "system" // chat/completions wire predates the rename
		}
		reasoning := m.ReasoningContent
		if opts.ReasoningEchoBack && m.Role == models.RoleAssistant && len(m.ToolCalls) > 0 && reasoning == "" {
			reasoning = "reasoning not retained"
		}
		wire := wireMessage{
			Role:       role,
			Content:    m.Content,
			Name:       m.Name,
			ToolCallID: m.ToolCallID,
			Reasoning:  reasoning,
		}
		for _, call := range m.ToolCalls {
			tc := wireToolCall{ID: call.ID, Type: "function"}
			tc.Function.Name = call.Name
			tc.Function.Arguments = call.Arguments
			wire.ToolCalls = append(wire.ToolCalls, tc)
		}
		messages = append(messages, wire)
	}
	wire := wireRequest{
		Model:       upstream,
		Messages:    messages,
		Stream:      req.Stream,
		Temperature: req.Temperature,
		MaxTokens:   req.MaxTokens,
		Stop:        req.Stop,
		Tools:       req.Tools,
		ToolChoice:  req.ToolChoice,
	}
	if req.Stream && opts.StreamOptions {
		wire.StreamOptions = &streamOptions{IncludeUsage: true}
	}
	if opts.ReasoningEffort {
		wire.ReasoningEffort = req.ReasoningEffort
	}
	return json.Marshal(wire)
}

// streamChunk is the OpenAI chat.completion.chunk shape.
type streamChunk struct {
	Choices []struct {
		Delta struct {
			Content          *string `json:"content"`
			ReasoningContent *string `json:"reasoning_content"`
			ToolCalls        []struct {
				Index    int    `json:"index"`
				ID       string `json:"id"`
				Type     string `json:"type"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"delta"`
		FinishReason *string `json:"finish_reason"`
	} `json:"choices"`
	Usage *WireUsage `json:"usage"`
}

// WireUsage is the OpenAI usage object.
type WireUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
}

// TranslateStream reads a chat/completions SSE body into canonical events.
func TranslateStream(body io.Reader, emit connector.Emit) error {
	scanner := httpx.NewSSEScanner(body)
	finishReason := ""
	var usage *models.Usage
	for {
		data, ok := scanner.Next()
		if !ok {
			break
		}
		if data == httpx.Done {
			break
		}
		var chunk streamChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			return &connector.Error{Kind: connector.KindUpstreamBad, Code: "decode_chunk", Message: "stream chunk is not a completion chunk", Err: err}
		}
		for _, choice := range chunk.Choices {
			delta := choice.Delta
			if delta.ReasoningContent != nil && *delta.ReasoningContent != "" {
				if err := emit(models.StreamEvent{Type: models.EventDelta, ReasoningContent: *delta.ReasoningContent}); err != nil {
					return err
				}
			}
			if delta.Content != nil && *delta.Content != "" {
				if err := emit(models.StreamEvent{Type: models.EventDelta, Delta: *delta.Content}); err != nil {
					return err
				}
			}
			for _, call := range delta.ToolCalls {
				if err := emit(models.StreamEvent{Type: models.EventDelta, ToolCalls: []models.ToolCallDelta{{
					Index:     call.Index,
					ID:        call.ID,
					Name:      call.Function.Name,
					Arguments: call.Function.Arguments,
				}}}); err != nil {
					return err
				}
			}
			if choice.FinishReason != nil && *choice.FinishReason != "" {
				finishReason = *choice.FinishReason
			}
		}
		if chunk.Usage != nil {
			usage = &models.Usage{InputTokens: chunk.Usage.PromptTokens, OutputTokens: chunk.Usage.CompletionTokens}
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	return emit(models.StreamEvent{Type: models.EventDone, FinishReason: finishReason, Usage: usage})
}

// wireCompletion is the non-streamed chat.completion shape.
type wireCompletion struct {
	Choices []struct {
		Message struct {
			Content          *string `json:"content"`
			ReasoningContent *string `json:"reasoning_content"`
			ToolCalls        []struct {
				ID       string `json:"id"`
				Type     string `json:"type"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage *WireUsage `json:"usage"`
}

// TranslateCompletion reads a non-streamed chat.completion into events:
// reasoning, content, tool calls, then Done.
func TranslateCompletion(body io.Reader, emit connector.Emit) error {
	var completion wireCompletion
	if err := json.NewDecoder(bufio.NewReader(body)).Decode(&completion); err != nil {
		return &connector.Error{Kind: connector.KindUpstreamBad, Code: "decode_completion", Message: "response is not a chat completion", Err: err}
	}
	if len(completion.Choices) == 0 {
		return &connector.Error{Kind: connector.KindUpstreamBad, Code: "empty_choices", Message: "completion returned no choices"}
	}
	choice := completion.Choices[0]
	if reasoning := choice.Message.ReasoningContent; reasoning != nil && *reasoning != "" {
		if err := emit(models.StreamEvent{Type: models.EventDelta, ReasoningContent: *reasoning}); err != nil {
			return err
		}
	}
	if content := choice.Message.Content; content != nil && *content != "" {
		if err := emit(models.StreamEvent{Type: models.EventDelta, Delta: *content}); err != nil {
			return err
		}
	}
	for i, call := range choice.Message.ToolCalls {
		if err := emit(models.StreamEvent{Type: models.EventDelta, ToolCalls: []models.ToolCallDelta{{
			Index:     i,
			ID:        call.ID,
			Name:      call.Function.Name,
			Arguments: call.Function.Arguments,
		}}}); err != nil {
			return err
		}
	}
	var usage *models.Usage
	if completion.Usage != nil {
		usage = &models.Usage{InputTokens: completion.Usage.PromptTokens, OutputTokens: completion.Usage.CompletionTokens}
	}
	return emit(models.StreamEvent{Type: models.EventDone, FinishReason: choice.FinishReason, Usage: usage})
}

// ErrorDetail is the OpenAI error envelope's inner object.
type ErrorDetail struct {
	Message string `json:"message"`
	Type    string `json:"type"`
	Code    string `json:"code"`
}

// ParseError extracts the OpenAI error envelope. Zero values mean the body
// was not an error object.
func ParseError(body []byte) ErrorDetail {
	var envelope struct {
		Error ErrorDetail `json:"error"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return ErrorDetail{}
	}
	return envelope.Error
}
