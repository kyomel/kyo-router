// Package deepseek implements the DeepSeek connector: a plain
// OpenAI-compatible upstream. Chat passes messages and reasoning_effort
// through verbatim and reads reasoning_content back from the stream. The
// wire codec is shared in internal/oai; this package holds auth, endpoint,
// and error classification.
package deepseek

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"slices"
	"strings"

	"kyo-router/internal/connector"
	"kyo-router/internal/httpx"
	"kyo-router/internal/models"
	"kyo-router/internal/oai"
)

const (
	chatPath   = "/chat/completions"
	modelsPath = "/models"
)

// Options builds one DeepSeek connector. Transport is the R seam.
type Options struct {
	APIKey    string
	BaseURL   string
	Transport httpx.Transport
}

// Connector speaks DeepSeek's OpenAI-compatible API.
type Connector struct {
	apiKey    string
	baseURL   string
	transport httpx.Transport
}

// New builds a connector; the key is required because every request carries it.
func New(opts Options) (*Connector, error) {
	if strings.TrimSpace(opts.APIKey) == "" {
		return nil, errors.New("deepseek: connector needs an api key")
	}
	if strings.TrimSpace(opts.BaseURL) == "" {
		return nil, errors.New("deepseek: connector needs a base url")
	}
	if opts.Transport == nil {
		opts.Transport = &http.Client{}
	}
	return &Connector{
		apiKey:    opts.APIKey,
		baseURL:   strings.TrimRight(opts.BaseURL, "/"),
		transport: opts.Transport,
	}, nil
}

// Chat runs one completion. req.Stream picks the wire shape; reasoning_effort
// passes through verbatim so the client's level reaches the model.
func (c *Connector) Chat(ctx context.Context, upstream string, req models.ChatRequest, emit connector.Emit) error {
	if err := connector.ValidateChat(upstream, req); err != nil {
		return err
	}
	emit = connector.EnsureEmit(emit)

	payload, err := oai.MarshalChatRequest(upstream, req, oai.RequestOptions{
		StreamOptions:     true,
		ReasoningEffort:   true,
		ReasoningEchoBack: true,
	})
	if err != nil {
		return connector.BadRequest("encode_request", "cannot encode chat request")
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+chatPath, bytes.NewReader(payload))
	if err != nil {
		return connector.BadRequest("build_request", "cannot build chat request")
	}
	httpReq.Header.Set("Authorization", "Bearer "+c.apiKey)
	httpReq.Header.Set("Content-Type", "application/json")
	if req.Stream {
		httpReq.Header.Set("Accept", "text/event-stream")
	}

	resp, err := c.transport.Do(httpReq)
	if err != nil {
		return connector.NetworkError(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return upstreamHTTPError(resp)
	}
	if req.Stream {
		return connector.NormalizeStreamError(oai.TranslateStream(resp.Body, emit))
	}
	return connector.NormalizeStreamError(oai.TranslateCompletion(resp.Body, emit))
}

// DiscoveredModel holds the capabilities DeepSeek reports on GET /models.
type DiscoveredModel struct {
	ID             string
	SupportsImages bool
	SupportsEffort bool
}

// Models fetches the live DeepSeek model list from GET /models.
func (c *Connector) Models(ctx context.Context) ([]DiscoveredModel, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+modelsPath, nil)
	if err != nil {
		return nil, connector.BadRequest("build_request", "cannot build models request")
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)

	resp, err := c.transport.Do(req)
	if err != nil {
		return nil, connector.NetworkError(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return nil, upstreamHTTPError(resp)
	}
	var list struct {
		Data []struct {
			ID              string   `json:"id"`
			InputModalities []string `json:"input_modalities"`
			Effort          *struct {
				SupportedLevels []string `json:"supported_levels"`
			} `json:"effort"`
		} `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&list); err != nil {
		return nil, connector.UpstreamBad("decode_models", "response is not a model list", err)
	}
	out := make([]DiscoveredModel, 0, len(list.Data))
	for _, item := range list.Data {
		id := strings.TrimSpace(item.ID)
		if id == "" {
			continue
		}
		out = append(out, DiscoveredModel{
			ID:             id,
			SupportsImages: slices.Contains(item.InputModalities, "image"),
			SupportsEffort: item.Effort != nil && len(item.Effort.SupportedLevels) > 0,
		})
	}
	return out, nil
}
