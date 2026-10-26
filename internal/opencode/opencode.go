// Package opencode implements the OpenCode Go connector. Chat-protocol
// models share the OpenAI codec (internal/oai); responses-protocol models
// translate to the Responses API wire. Every call carries
// x-opencode-session - a fresh UUID per request by default, a fixed value
// when configured (02-connectors.md pair note).
package opencode

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/http"
	"strings"

	"kyo-router/internal/connector"
	"kyo-router/internal/models"
	"kyo-router/internal/oai"
)

const (
	chatPath      = "/chat/completions"
	responsesPath = "/responses"
)

// Transport is the HTTP seam. *http.Client satisfies it.
type Transport interface {
	Do(*http.Request) (*http.Response, error)
}

// SessionMode selects how x-opencode-session is filled.
type SessionMode string

const (
	// SessionPerRequest mints a fresh UUID per call (default).
	SessionPerRequest SessionMode = "request"
	// SessionFixed reuses one UUID for the connector's life.
	SessionFixed SessionMode = "fixed"
)

// Options builds one OpenCode connector. ResponsesModels lists upstream ids
// that speak the Responses API; everything else uses chat/completions.
type Options struct {
	APIKey          string
	BaseURL         string
	SessionMode     SessionMode
	ResponsesModels []string
	Transport       Transport
}

// Connector speaks OpenCode Go's chat and responses surfaces.
type Connector struct {
	apiKey        string
	baseURL       string
	sessionMode   SessionMode
	fixedSession  string
	responsesOnly map[string]bool
	transport     Transport
}

// New builds a connector; the key is required because every request carries it.
func New(opts Options) (*Connector, error) {
	if strings.TrimSpace(opts.APIKey) == "" {
		return nil, errors.New("opencode: connector needs an api key")
	}
	if strings.TrimSpace(opts.BaseURL) == "" {
		return nil, errors.New("opencode: connector needs a base url")
	}
	if opts.SessionMode == "" {
		opts.SessionMode = SessionPerRequest
	}
	if opts.SessionMode != SessionPerRequest && opts.SessionMode != SessionFixed {
		return nil, errors.New("opencode: session mode must be request or fixed")
	}
	if opts.Transport == nil {
		opts.Transport = &http.Client{}
	}
	responses := make(map[string]bool, len(opts.ResponsesModels))
	for _, id := range opts.ResponsesModels {
		responses[id] = true
	}
	conn := &Connector{
		apiKey:        opts.APIKey,
		baseURL:       strings.TrimRight(opts.BaseURL, "/"),
		sessionMode:   opts.SessionMode,
		responsesOnly: responses,
		transport:     opts.Transport,
	}
	if opts.SessionMode == SessionFixed {
		conn.fixedSession = newUUID()
	}
	return conn, nil
}

// Chat runs one completion, picking the wire protocol from the upstream id.
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

	var payload []byte
	var path string
	var err error
	if c.responsesOnly[upstream] {
		payload, err = marshalResponsesRequest(upstream, req)
		path = responsesPath
	} else {
		payload, err = oai.MarshalChatRequest(upstream, req, oai.RequestOptions{StreamOptions: true})
		path = chatPath
	}
	if err != nil {
		return badRequest("encode_request", "cannot encode chat request")
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(payload))
	if err != nil {
		return badRequest("build_request", "cannot build chat request")
	}
	httpReq.Header.Set("Authorization", "Bearer "+c.apiKey)
	httpReq.Header.Set("Content-Type", "application/json")
	// x-opencode-session is mandatory on chat/completions (MissingSessionID
	// otherwise) and accepted on /responses.
	httpReq.Header.Set("x-opencode-session", c.session())
	if req.Stream {
		httpReq.Header.Set("Accept", "text/event-stream")
	}

	resp, err := c.transport.Do(httpReq)
	if err != nil {
		return transportError(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return upstreamHTTPError(resp)
	}
	var translateErr error
	if c.responsesOnly[upstream] {
		if req.Stream {
			translateErr = translateResponsesStream(resp.Body, emit)
		} else {
			translateErr = translateResponses(resp.Body, emit)
		}
	} else if req.Stream {
		translateErr = oai.TranslateStream(resp.Body, emit)
	} else {
		translateErr = oai.TranslateCompletion(resp.Body, emit)
	}
	if translateErr != nil {
		var connErr *connector.Error
		if errors.As(translateErr, &connErr) {
			return translateErr
		}
		return transportError(translateErr)
	}
	return nil
}

// session returns the header value per the configured mode.
func (c *Connector) session() string {
	if c.sessionMode == SessionFixed {
		return c.fixedSession
	}
	return newUUID()
}

func newUUID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand failure is a system fault; fall back to a fixed string
		// rather than panic. Upstream accepts any non-empty session id.
		return "kyo-router"
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	var dst [36]byte
	hex.Encode(dst[0:8], b[0:4])
	dst[8] = '-'
	hex.Encode(dst[9:13], b[4:6])
	dst[13] = '-'
	hex.Encode(dst[14:18], b[6:8])
	dst[18] = '-'
	hex.Encode(dst[19:23], b[8:10])
	dst[23] = '-'
	hex.Encode(dst[24:36], b[10:16])
	return string(dst[:])
}
