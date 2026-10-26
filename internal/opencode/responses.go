package opencode

// Responses API wire codec (verified live 2026-10-26 against grok-4.7).
// Request input[] holds message / function_call / function_call_output items;
// reasoning.effort carries the canonical reasoning_effort. Streaming events
// map: response.output_text.delta -> Delta, response.reasoning_summary_text.
// delta -> ReasoningContent, response.output_item.done(function_call) ->
// ToolCalls, response.completed -> Done with usage.

import (
	"encoding/json"
	"io"

	"kyo-router/internal/connector"
	"kyo-router/internal/httpx"
	"kyo-router/internal/models"
)

type responsesInput struct {
	Type      string          `json:"type"`
	Role      string          `json:"role,omitempty"`
	Content   []responsesPart `json:"content,omitempty"`
	CallID    string          `json:"call_id,omitempty"`
	Name      string          `json:"name,omitempty"`
	Arguments string          `json:"arguments,omitempty"`
	Output    string          `json:"output,omitempty"`
}

type responsesPart struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type responsesRequest struct {
	Model           string           `json:"model"`
	Input           []responsesInput `json:"input"`
	Stream          bool             `json:"stream"`
	Temperature     *float64         `json:"temperature,omitempty"`
	MaxOutputTokens *int             `json:"max_output_tokens,omitempty"`
	Reasoning       *responsesReason `json:"reasoning,omitempty"`
}

type responsesReason struct {
	Effort  string `json:"effort"`
	Summary string `json:"summary,omitempty"`
}

func marshalResponsesRequest(upstream string, req models.ChatRequest) ([]byte, error) {
	input := make([]responsesInput, 0, len(req.Messages))
	for _, m := range req.Messages {
		for _, call := range m.ToolCalls {
			input = append(input, responsesInput{
				Type:      "function_call",
				CallID:    call.ID,
				Name:      call.Name,
				Arguments: call.Arguments,
			})
		}
		switch m.Role {
		case models.RoleTool:
			input = append(input, responsesInput{
				Type:   "function_call_output",
				CallID: m.ToolCallID,
				Output: m.Content,
			})
		default:
			partType := "input_text"
			if m.Role == models.RoleAssistant {
				partType = "output_text"
			}
			input = append(input, responsesInput{
				Type:    "message",
				Role:    string(m.Role),
				Content: []responsesPart{{Type: partType, Text: m.Content}},
			})
		}
	}
	wire := responsesRequest{
		Model:           upstream,
		Input:           input,
		Stream:          req.Stream,
		Temperature:     req.Temperature,
		MaxOutputTokens: req.MaxTokens,
	}
	if req.ReasoningEffort != nil && *req.ReasoningEffort != "" {
		wire.Reasoning = &responsesReason{Effort: *req.ReasoningEffort, Summary: "auto"}
	}
	return json.Marshal(wire)
}

// responseObject is the full response resource returned non-stream and in
// response.completed.
type responseObject struct {
	Status string `json:"status"`
	Error  *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
	Output []struct {
		Type    string `json:"type"`
		Summary []struct {
			Text string `json:"text"`
		} `json:"summary"`
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
		CallID    string `json:"call_id"`
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"output"`
	Usage *struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
}

// emitOutput replays one response's output[] as canonical events.
func emitOutput(resp responseObject, emit connector.Emit) error {
	toolIndex := 0
	for _, item := range resp.Output {
		switch item.Type {
		case "reasoning":
			for _, s := range item.Summary {
				if s.Text == "" {
					continue
				}
				if err := emit(models.StreamEvent{Type: models.EventDelta, ReasoningContent: s.Text}); err != nil {
					return err
				}
			}
		case "message":
			for _, part := range item.Content {
				if part.Text == "" {
					continue
				}
				if err := emit(models.StreamEvent{Type: models.EventDelta, Delta: part.Text}); err != nil {
					return err
				}
			}
		case "function_call":
			if err := emit(models.StreamEvent{Type: models.EventDelta, ToolCalls: []models.ToolCallDelta{{
				Index:     toolIndex,
				ID:        item.CallID,
				Name:      item.Name,
				Arguments: item.Arguments,
			}}}); err != nil {
				return err
			}
			toolIndex++
		}
	}
	return nil
}

func usageOf(resp responseObject) *models.Usage {
	if resp.Usage == nil {
		return nil
	}
	return &models.Usage{InputTokens: resp.Usage.InputTokens, OutputTokens: resp.Usage.OutputTokens}
}

// streamEvent is the data payload of a Responses SSE frame.
type streamEvent struct {
	Type     string          `json:"type"`
	Delta    string          `json:"delta"`
	Item     json.RawMessage `json:"item"`
	Response responseObject  `json:"response"`
}

func translateResponsesStream(body io.Reader, emit connector.Emit) error {
	scanner := httpx.NewSSEScanner(body)
	for {
		data, ok := scanner.Next()
		if !ok {
			break
		}
		if data == httpx.Done {
			break
		}
		var event streamEvent
		if err := json.Unmarshal([]byte(data), &event); err != nil {
			return &connector.Error{Kind: connector.KindUpstreamBad, Code: "decode_event", Message: "stream event is not a responses event", Err: err}
		}
		switch event.Type {
		case "response.output_text.delta":
			if err := emit(models.StreamEvent{Type: models.EventDelta, Delta: event.Delta}); err != nil {
				return err
			}
		case "response.reasoning_summary_text.delta", "response.reasoning_text.delta":
			if err := emit(models.StreamEvent{Type: models.EventDelta, ReasoningContent: event.Delta}); err != nil {
				return err
			}
		case "response.output_item.done":
			var item struct {
				Type      string `json:"type"`
				CallID    string `json:"call_id"`
				Name      string `json:"name"`
				Arguments string `json:"arguments"`
				Summary   []struct {
					Text string `json:"text"`
				} `json:"summary"`
			}
			if err := json.Unmarshal(event.Item, &item); err != nil {
				continue
			}
			if item.Type == "function_call" {
				if err := emit(models.StreamEvent{Type: models.EventDelta, ToolCalls: []models.ToolCallDelta{{
					ID:        item.CallID,
					Name:      item.Name,
					Arguments: item.Arguments,
				}}}); err != nil {
					return err
				}
			}
			// reasoning summary arrives complete in item.done for providers
			// that do not stream reasoning_summary_text deltas.
			for _, s := range item.Summary {
				if s.Text == "" {
					continue
				}
				if err := emit(models.StreamEvent{Type: models.EventDelta, ReasoningContent: s.Text}); err != nil {
					return err
				}
			}
		case "response.completed":
			reason := "stop"
			if event.Response.Status == "incomplete" {
				reason = "length"
			}
			return emit(models.StreamEvent{Type: models.EventDone, FinishReason: reason, Usage: usageOf(event.Response)})
		case "response.failed", "response.incomplete":
			reason := "stop"
			if event.Response.Error != nil {
				return &connector.Error{Kind: connector.KindUpstreamBad, Code: event.Response.Error.Code, Message: event.Response.Error.Message}
			}
			return emit(models.StreamEvent{Type: models.EventDone, FinishReason: reason, Usage: usageOf(event.Response)})
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	return emit(models.StreamEvent{Type: models.EventDone, FinishReason: "stop"})
}

func translateResponses(body io.Reader, emit connector.Emit) error {
	var resp responseObject
	if err := json.NewDecoder(body).Decode(&resp); err != nil {
		return &connector.Error{Kind: connector.KindUpstreamBad, Code: "decode_response", Message: "body is not a responses object", Err: err}
	}
	if resp.Error != nil {
		return &connector.Error{Kind: connector.KindUpstreamBad, Code: resp.Error.Code, Message: resp.Error.Message}
	}
	if err := emitOutput(resp, emit); err != nil {
		return err
	}
	reason := "stop"
	if resp.Status == "incomplete" {
		reason = "length"
	}
	return emit(models.StreamEvent{Type: models.EventDone, FinishReason: reason, Usage: usageOf(resp)})
}
