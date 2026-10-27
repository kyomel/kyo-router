package devin

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strings"

	"google.golang.org/protobuf/proto"

	"kyo-router/internal/connector"
	"kyo-router/internal/devin/devinpb"
)

const catalogPath = "/exa.api_server_pb.ApiServerService/GetCliModelConfigs"

// ThinkingLevel is one effort suffix Devin bakes into a model uid.
type ThinkingLevel string

const (
	ThinkingMinimal ThinkingLevel = "minimal"
	ThinkingLow     ThinkingLevel = "low"
	ThinkingMedium  ThinkingLevel = "medium"
	ThinkingHigh    ThinkingLevel = "high"
	ThinkingXHigh   ThinkingLevel = "xhigh"
	ThinkingMax     ThinkingLevel = "max"
	ThinkingNone    ThinkingLevel = "none"
)

// Thinking describes how one uid reasons. Level is the explicit suffix;
// Defaulted marks a bare "thinking" uid, which runs at medium; Plain marks a
// uid with no reasoning.
type Thinking struct {
	Level     ThinkingLevel
	Defaulted bool
	Plain     bool
}

// Effective returns the level a request runs when the uid has no explicit one.
func (t Thinking) Effective() (ThinkingLevel, bool) {
	if t.Level != "" {
		return t.Level, true
	}
	if t.Defaulted {
		return ThinkingMedium, true
	}
	return "", false
}

// Model is one usable Devin uid, verbatim.
type Model struct {
	UID            string
	SupportsImages bool
	Thinking       Thinking
}

// Catalog is the live model list discovery returns.
type Catalog struct {
	Models []Model
}

// Catalog fetches GetCliModelConfigs with the same key the chat calls use.
// Pseudo models (adaptive, auto) and the fusion family are excluded; uids
// stay verbatim.
func (c *Connector) Catalog(ctx context.Context) (*Catalog, error) {
	auth, err := c.tokens.Token(ctx)
	if err != nil {
		return nil, err
	}
	payload, err := proto.Marshal(&devinpb.GetCliModelConfigsRequest{
		Metadata: newMetadata(c.credentials.APIKey, auth.UserJWT, c.ideVersion, c.extensionVersion),
	})
	if err != nil {
		return nil, connector.UpstreamBad("encode_catalog", "cannot encode GetCliModelConfigs request", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		strings.TrimRight(auth.BaseURL, "/")+catalogPath, bytes.NewReader(payload))
	if err != nil {
		return nil, connector.BadRequest("catalog_request", "cannot build GetCliModelConfigs request")
	}
	req.Header.Set("Content-Type", "application/proto")
	req.Header.Set("Connect-Protocol-Version", "1")
	req.Header.Set("Accept", "*/*")

	resp, err := c.transport.Do(req)
	if err != nil {
		return nil, connector.NetworkError(err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, connector.NetworkError(err)
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return nil, httpError(resp.StatusCode, resp.Header.Get("Retry-After"), c.clock.Now())
	}

	var decoded devinpb.GetCliModelConfigsResponse
	if err := proto.Unmarshal(body, &decoded); err != nil {
		return nil, connector.UpstreamBad("decode_catalog", "GetCliModelConfigs response is not protobuf", err)
	}
	catalog := catalogFromResponse(&decoded)
	if len(catalog.Models) == 0 {
		return nil, connector.UpstreamBad("empty_catalog", "Devin returned an empty model catalog for this account", nil)
	}
	return catalog, nil
}

// catalogFromResponse is the pure mapping, so tests need no HTTP.
func catalogFromResponse(resp *devinpb.GetCliModelConfigsResponse) *Catalog {
	models := make([]Model, 0, len(resp.GetClientModelConfigs()))
	for _, config := range resp.GetClientModelConfigs() {
		uid := strings.TrimSpace(config.GetModelUid())
		if uid == "" || config.GetDisabled() || isExcludedUID(uid) {
			continue
		}
		family := strings.TrimSpace(config.GetModelFamilyMetadata().GetModelFamilyLabel())
		if family == "" {
			family = strings.TrimSpace(config.GetModelInfo().GetModelFamilyUid())
		}
		if isExcludedName(family) {
			continue
		}
		models = append(models, Model{
			UID:            uid,
			SupportsImages: config.GetSupportsImages(),
			Thinking:       thinkingOf(uid),
		})
	}
	return &Catalog{Models: models}
}

// isExcludedUID drops Devin's pseudo models: adaptive and auto are picker
// entries, never served as a uid, and fusion is a separate product family.
func isExcludedUID(uid string) bool {
	return uid == "adaptive" || uid == "auto" || uid == "fusion" || strings.HasPrefix(uid, "fusion-")
}

func isExcludedName(name string) bool {
	return isExcludedUID(strings.ToLower(strings.TrimSpace(name)))
}

// Variant modifiers change speed or cache scope, not thinking effort.
var variantModifiers = []string{"-fast", "-1m", "-priority", "-lightning"}

var levelSuffixes = []struct {
	suffix string
	level  ThinkingLevel
}{
	{"-minimal", ThinkingMinimal},
	{"-xhigh", ThinkingXHigh},
	{"-medium", ThinkingMedium},
	{"-high", ThinkingHigh},
	{"-low", ThinkingLow},
	{"-max", ThinkingMax},
	{"-none", ThinkingNone},
}

// thinkingOf reads the effort from the uid suffix. A bare "thinking" suffix
// carries no effort word, so it defaults to medium (docs/05-model-list-proposed).
func thinkingOf(uid string) Thinking {
	base := stripVariantModifiers(uid)
	for _, candidate := range levelSuffixes {
		if strings.HasSuffix(base, candidate.suffix) {
			return Thinking{Level: candidate.level}
		}
	}
	if strings.HasSuffix(base, "-thinking") {
		return Thinking{Defaulted: true}
	}
	return Thinking{Plain: true}
}

func stripVariantModifiers(uid string) string {
	for {
		stripped := false
		for _, modifier := range variantModifiers {
			if strings.HasSuffix(uid, modifier) {
				uid = strings.TrimSuffix(uid, modifier)
				stripped = true
				break
			}
		}
		if !stripped {
			return uid
		}
	}
}
