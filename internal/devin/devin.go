// Package devin implements the Devin model-API connector: GetUserJwt auth with
// a cached single-flight token, GetCliModelConfigs catalog discovery, and
// GetChatMessage streaming over Connect-RPC.
package devin

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"

	"kyo-router/internal/connector"
	"kyo-router/internal/devin/devinpb"
	"kyo-router/internal/models"
)

const chatPath = "/exa.api_server_pb.ApiServerService/GetChatMessage"

const (
	defaultMaxOutputTokens = 64_000
	defaultTemperature     = 0.4
)

// Options builds one Devin connector. Transport, TokenSource, and Clock are the
// R seams; a test swaps any of them without changing the call graph.
type Options struct {
	Credentials      Credentials
	TokenSource      TokenSource
	Transport        Transport
	Clock            Clock
	IDEVersion       string
	ExtensionVersion string
	JWTRefreshMargin time.Duration
	MaxOutputTokens  int
	Temperature      float64
}

// Connector speaks Devin's model API: GetUserJwt for auth, GetChatMessage for
// chat, GetCliModelConfigs for the catalog.
type Connector struct {
	credentials      Credentials
	tokens           TokenSource
	transport        Transport
	clock            Clock
	ideVersion       string
	extensionVersion string
	maxOutputTokens  int
	temperature      float64
}

// New builds a connector. Model-api credentials are required because every
// request carries the key in its metadata; a TokenSource is optional and
// defaults to the cached JWT source built from those credentials.
func New(opts Options) (*Connector, error) {
	if strings.TrimSpace(opts.Credentials.APIKey) == "" {
		return nil, errors.New("devin: connector needs model-api credentials")
	}
	opts.Credentials.APIKey = NormalizeSessionToken(opts.Credentials.APIKey)
	if ClassifyKey(opts.Credentials.APIKey) != KeyKindModelAPI {
		return nil, errors.New("devin: connector needs a model-api key, not an agent-api key")
	}
	if strings.TrimSpace(opts.Credentials.BaseURL) == "" {
		opts.Credentials.BaseURL = DefaultAPIURL
	}
	if opts.Transport == nil {
		opts.Transport = &http.Client{}
	}
	if opts.Clock == nil {
		opts.Clock = systemClock{}
	}
	if opts.IDEVersion == "" {
		opts.IDEVersion = DefaultIDEVersion
	}
	if opts.ExtensionVersion == "" {
		opts.ExtensionVersion = DefaultExtensionVersion
	}
	if opts.MaxOutputTokens <= 0 {
		opts.MaxOutputTokens = defaultMaxOutputTokens
	}
	if opts.Temperature <= 0 {
		opts.Temperature = defaultTemperature
	}

	tokens := opts.TokenSource
	if tokens == nil {
		source, err := NewTokenSource(TokenSourceOptions{
			Credentials:      opts.Credentials,
			Transport:        opts.Transport,
			Clock:            opts.Clock,
			RefreshMargin:    opts.JWTRefreshMargin,
			IDEVersion:       opts.IDEVersion,
			ExtensionVersion: opts.ExtensionVersion,
		})
		if err != nil {
			return nil, err
		}
		tokens = source
	}

	return &Connector{
		credentials:      opts.Credentials,
		tokens:           tokens,
		transport:        opts.Transport,
		clock:            opts.Clock,
		ideVersion:       opts.IDEVersion,
		extensionVersion: opts.ExtensionVersion,
		maxOutputTokens:  opts.MaxOutputTokens,
		temperature:      opts.Temperature,
	}, nil
}

// Chat streams one completion. The upstream uid is passed verbatim, so the
// effort suffix in the catalog id is the effort Devin runs.
func (c *Connector) Chat(ctx context.Context, upstream string, req models.ChatRequest, emit connector.Emit) error {
	if strings.TrimSpace(upstream) == "" {
		return badRequest("model_required", "upstream model id is required")
	}
	if len(req.Messages) == 0 {
		return badRequest("messages_required", "at least one message is required")
	}
	if emit == nil {
		emit = func(models.StreamEvent) error { return nil }
	}

	auth, err := c.tokens.Token(ctx)
	if err != nil {
		return err
	}
	message, err := c.buildChatRequest(upstream, req, auth.UserJWT)
	if err != nil {
		return err
	}

	recorder := &responseRecorder{transport: c.transport}
	transport := connecthttp.NewTransport(recorder, strings.TrimRight(auth.BaseURL, "/"), connecthttp.WithSendGzip())
	client := connect.NewClient(transport)
	rpcCtx, callInfo := connect.NewClientContext(ctx)
	stream, err := client.CallServerStream(rpcCtx, connect.Spec{
		StreamType: connect.StreamTypeServer,
		Procedure:  chatPath,
	}, message)
	if err != nil {
		return c.fail(connectError(rpcCtx, callInfo, recorder, err, c.clock.Now()))
	}
	defer stream.Close()

	translator := &streamTranslator{emit: emit, toolCalls: make(map[string]*toolCallState)}
	for {
		var response devinpb.GetChatMessageResponse
		err := stream.Receive(&response)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return c.fail(connectError(rpcCtx, callInfo, recorder, err, c.clock.Now()))
		}
		if err := translator.add(&response); err != nil {
			return err
		}
	}
	return translator.finish()
}

// fail drops the cached JWT when the upstream rejected it, so the router's
// refresh-once retry mints a fresh token.
func (c *Connector) fail(err error) error {
	var connErr *connector.Error
	if errors.As(err, &connErr) && connErr.Kind == connector.KindAuth {
		c.tokens.Invalidate()
	}
	return err
}

func (c *Connector) buildChatRequest(upstream string, req models.ChatRequest, userJWT string) (*devinpb.GetChatMessageRequest, error) {
	cascadeID, err := newUUID()
	if err != nil {
		return nil, fmt.Errorf("devin: generate cascade id: %w", err)
	}
	executionID, err := newUUID()
	if err != nil {
		return nil, fmt.Errorf("devin: generate execution id: %w", err)
	}
	prompt, prompts, err := buildPrompts(req.Messages, cascadeID)
	if err != nil {
		return nil, err
	}

	temperature := c.temperature
	if req.Temperature != nil {
		temperature = *req.Temperature
	}
	maxTokens := c.maxOutputTokens
	if req.MaxTokens != nil && *req.MaxTokens > 0 {
		maxTokens = *req.MaxTokens
	}
	stopPatterns := append(defaultStopPatterns(), req.Stop...)

	return &devinpb.GetChatMessageRequest{
		Metadata:                 newMetadata(c.credentials.APIKey, userJWT, c.ideVersion, c.extensionVersion),
		Prompt:                   prompt,
		ChatMessagePrompts:       prompts,
		ChatModelUid:             upstream,
		RequestType:              devinpb.ChatMessageRequestType_CHAT_MESSAGE_REQUEST_TYPE_CASCADE,
		PlannerMode:              devinpb.ConversationalPlannerMode_CONVERSATIONAL_PLANNER_MODE_DEFAULT,
		ToolChoice:               &devinpb.ChatToolChoice{OptionName: "auto"},
		SystemPromptCacheOptions: &devinpb.PromptCacheOptions{Type: devinpb.CacheControlType_CACHE_CONTROL_TYPE_EPHEMERAL},
		DisableParallelToolCalls: true,
		CascadeId:                cascadeID,
		ExecutionId:              executionID,
		Configuration: &devinpb.CompletionConfiguration{
			NumCompletions:      1,
			MaxTokens:           uint64(maxTokens),
			MaxNewlines:         200,
			Temperature:         temperature,
			FirstTemperature:    temperature,
			TopK:                50,
			TopP:                1,
			StopPatterns:        stopPatterns,
			FimEotProbThreshold: 1,
		},
	}, nil
}

func buildPrompts(messages []models.Message, cascadeID string) (string, []*devinpb.ChatMessagePrompt, error) {
	systemParts := make([]string, 0, 1)
	prompts := make([]*devinpb.ChatMessagePrompt, 0, len(messages))
	for index, message := range messages {
		switch message.Role {
		case models.RoleSystem:
			if strings.TrimSpace(message.Content) != "" {
				systemParts = append(systemParts, message.Content)
			}
		case models.RoleUser:
			prompts = append(prompts, &devinpb.ChatMessagePrompt{
				MessageId: deterministicUUID(fmt.Sprintf("%s %d %s", cascadeID, index, message.Role)),
				Source:    devinpb.ChatMessageSource_CHAT_MESSAGE_SOURCE_USER,
				Prompt:    message.Content,
			})
		case models.RoleAssistant:
			prompt := &devinpb.ChatMessagePrompt{
				MessageId: "bot-" + deterministicUUID(fmt.Sprintf("%s %d %s", cascadeID, index, message.Role)),
				Source:    devinpb.ChatMessageSource_CHAT_MESSAGE_SOURCE_SYSTEM,
				Prompt:    message.Content,
			}
			for _, call := range message.ToolCalls {
				prompt.ToolCalls = append(prompt.ToolCalls, &devinpb.ChatToolCall{
					Id:            call.ID,
					Name:          call.Name,
					ArgumentsJson: call.Arguments,
				})
			}
			prompts = append(prompts, prompt)
		case models.RoleTool:
			if strings.TrimSpace(message.ToolCallID) == "" {
				return "", nil, badRequest("tool_call_id_required", "tool message needs tool_call_id")
			}
			prompts = append(prompts, &devinpb.ChatMessagePrompt{
				MessageId:  deterministicUUID(fmt.Sprintf("%s %d %s %s", cascadeID, index, message.Role, message.ToolCallID)),
				Source:     devinpb.ChatMessageSource_CHAT_MESSAGE_SOURCE_TOOL,
				Prompt:     message.Content,
				ToolCallId: message.ToolCallID,
			})
		default:
			return "", nil, badRequest("unknown_role", "unknown message role "+string(message.Role))
		}
	}
	return strings.Join(systemParts, "\n\n"), prompts, nil
}

// defaultStopPatterns are Devin's own turn markers.
func defaultStopPatterns() []string {
	names := []string{"user", "bot", "context_request", "endoftext", "end_of_turn"}
	patterns := make([]string, 0, len(names))
	for _, name := range names {
		patterns = append(patterns, "<|"+name+"|>")
	}
	return patterns
}

type toolCallState struct {
	index     int
	id        string
	name      string
	arguments string
}

type streamTranslator struct {
	emit             connector.Emit
	toolCalls        map[string]*toolCallState
	activeToolCallID string
	sawToolCalls     bool
	stopReason       devinpb.StopReason
	usage            *models.Usage
}

func (t *streamTranslator) add(response *devinpb.GetChatMessageResponse) error {
	if thinking := response.GetDeltaThinking(); thinking != "" {
		if err := t.emit(models.StreamEvent{Type: models.EventDelta, ReasoningContent: thinking}); err != nil {
			return err
		}
	}
	if text := response.GetDeltaText(); text != "" {
		if err := t.emit(models.StreamEvent{Type: models.EventDelta, Delta: text}); err != nil {
			return err
		}
	}
	if calls := response.GetDeltaToolCalls(); len(calls) > 0 {
		if err := t.addToolCalls(calls); err != nil {
			return err
		}
	}
	if reason := response.GetStopReason(); reason != devinpb.StopReason_STOP_REASON_UNSPECIFIED {
		t.stopReason = reason
	}
	if usage := response.GetUsage(); usage != nil {
		t.usage = &models.Usage{
			InputTokens:      int(usage.GetInputTokens()),
			OutputTokens:     int(usage.GetOutputTokens()),
			CacheReadTokens:  int(usage.GetCacheReadTokens()),
			CacheWriteTokens: int(usage.GetCacheWriteTokens()),
		}
	}
	return nil
}

func (t *streamTranslator) addToolCalls(calls []*devinpb.ChatToolCall) error {
	for _, call := range calls {
		id := call.GetId()
		if id == "" {
			id = t.activeToolCallID
		}
		if id == "" {
			continue
		}
		state, exists := t.toolCalls[id]
		if !exists {
			state = &toolCallState{index: len(t.toolCalls), id: id, name: call.GetName()}
			t.toolCalls[id] = state
			t.activeToolCallID = id
			t.sawToolCalls = true
			state.arguments = call.GetArgumentsJson()
			if err := t.emit(models.StreamEvent{Type: models.EventDelta, ToolCalls: []models.ToolCallDelta{{
				Index:     state.index,
				ID:        state.id,
				Name:      state.name,
				Arguments: state.arguments,
			}}}); err != nil {
				return err
			}
			continue
		}

		t.activeToolCallID = id
		if name := call.GetName(); name != "" && name != state.name {
			state.name = name
			if err := t.emit(models.StreamEvent{Type: models.EventDelta, ToolCalls: []models.ToolCallDelta{{
				Index: state.index,
				Name:  state.name,
			}}}); err != nil {
				return err
			}
		}
		raw := call.GetArgumentsJson()
		if raw == "" {
			continue
		}
		delta := raw
		if strings.HasPrefix(raw, state.arguments) {
			delta = raw[len(state.arguments):]
			state.arguments = raw
		} else {
			state.arguments += raw
		}
		if delta == "" {
			continue
		}
		if err := t.emit(models.StreamEvent{Type: models.EventDelta, ToolCalls: []models.ToolCallDelta{{
			Index:     state.index,
			Arguments: delta,
		}}}); err != nil {
			return err
		}
	}
	return nil
}

func (t *streamTranslator) finish() error {
	return t.emit(models.StreamEvent{
		Type:         models.EventDone,
		FinishReason: t.finishReason(),
		Usage:        t.usage,
	})
}

func (t *streamTranslator) finishReason() string {
	if t.sawToolCalls {
		return "tool_calls"
	}
	switch t.stopReason {
	case devinpb.StopReason_STOP_REASON_MAX_TOKENS:
		return "length"
	case devinpb.StopReason_STOP_REASON_CONTENT_FILTER:
		return "content_filter"
	case devinpb.StopReason_STOP_REASON_FUNCTION_CALL:
		return "tool_calls"
	default:
		return "stop"
	}
}

func newUUID() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	raw[6] = (raw[6] & 0x0f) | 0x40
	raw[8] = (raw[8] & 0x3f) | 0x80
	return formatUUID(raw[:]), nil
}

// deterministicUUID keeps message ids stable across resends of the same
// cascade, which keeps Devin's server-side prompt cache warm.
func deterministicUUID(seed string) string {
	sum := sha256.Sum256([]byte(seed))
	raw := sum[:16]
	raw[6] = (raw[6] & 0x0f) | 0x50
	raw[8] = (raw[8] & 0x3f) | 0x80
	return formatUUID(raw)
}

func formatUUID(raw []byte) string {
	encoded := hex.EncodeToString(raw)
	return encoded[0:8] + "-" + encoded[8:12] + "-" + encoded[12:16] + "-" + encoded[16:20] + "-" + encoded[20:32]
}
