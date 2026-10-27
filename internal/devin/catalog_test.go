package devin

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"google.golang.org/protobuf/proto"

	"kyo-router/internal/connector"
	"kyo-router/internal/devin/devinpb"
)

func catalogConfig(uid, label, family string, maxTokens, maxOutput int32, images, disabled bool) *devinpb.ClientModelConfig {
	return &devinpb.ClientModelConfig{
		ModelUid:       uid,
		Label:          label,
		Disabled:       disabled,
		SupportsImages: images,
		MaxTokens:      maxTokens,
		ModelInfo: &devinpb.ModelInfo{
			MaxOutputTokens: maxOutput,
			ModelFamilyUid:  family,
		},
		ModelFamilyMetadata: &devinpb.ModelFamilyMetadata{ModelFamilyLabel: family},
	}
}

func TestCatalogExcludesFusionAdaptiveAndAuto(t *testing.T) {
	response := &devinpb.GetCliModelConfigsResponse{
		ClientModelConfigs: []*devinpb.ClientModelConfig{
			catalogConfig("claude-haiku-5-5-low", "Claude Haiku 5.5 Low", "Claude Haiku 5.5", 200_000, 64_000, true, false),
			catalogConfig("fusion-high", "Fusion High", "Fusion", 1_000_000, 64_000, false, false),
			catalogConfig("claude-opus-5-high", "Claude Opus 5 High", "fusion", 1_000_000, 64_000, false, false),
			catalogConfig("adaptive", "Adaptive", "Adaptive", 200_000, 64_000, false, false),
			catalogConfig("auto", "Auto", "Auto", 200_000, 64_000, false, false),
			catalogConfig("gpt-6-sol-high", "GPT 6 Sol High", "auto", 200_000, 64_000, false, false),
			catalogConfig("glm-5-3-high", "GLM 5.3 High", "GLM 5.3", 200_000, 64_000, false, true),
			catalogConfig("", "No UID", "No UID", 1, 1, false, false),
		},
	}

	catalog := catalogFromResponse(response)
	if len(catalog.Models) != 1 {
		t.Fatalf("models = %d, want 1: %+v", len(catalog.Models), catalog.Models)
	}
	model := catalog.Models[0]
	if model.UID != "claude-haiku-5-5-low" {
		t.Errorf("uid = %q", model.UID)
	}
	if !model.SupportsImages {
		t.Error("supports images = false, want true")
	}
}

func TestThinkingOf(t *testing.T) {
	cases := []struct {
		uid       string
		level     ThinkingLevel
		defaulted bool
		plain     bool
	}{
		{"claude-haiku-5-5-low", ThinkingLow, false, false},
		{"claude-opus-5-5-high-fast", ThinkingHigh, false, false},
		{"claude-opus-5-5-xhigh", ThinkingXHigh, false, false},
		{"gemini-3-5-flash-minimal", ThinkingMinimal, false, false},
		{"gpt-6-sol-none-priority", ThinkingNone, false, false},
		{"swe-2-max-lightning", ThinkingMax, false, false},
		{"glm-5-2", "", false, true},
		{"glm-5-2-1m", "", false, true},
		{"swe-1-7", "", false, true},
		{"kimi-k2-6", "", false, true},
		{"claude-opus-4-6-thinking", "", true, false},
		{"claude-opus-4-6-thinking-1m", "", true, false},
	}
	for _, tc := range cases {
		t.Run(tc.uid, func(t *testing.T) {
			got := thinkingOf(tc.uid)
			if got.Level != tc.level || got.Defaulted != tc.defaulted || got.Plain != tc.plain {
				t.Errorf("thinkingOf(%q) = %+v, want level=%q defaulted=%v plain=%v",
					tc.uid, got, tc.level, tc.defaulted, tc.plain)
			}
		})
	}
}

func TestThinkingLevelsReachable(t *testing.T) {
	uids := map[ThinkingLevel]string{
		ThinkingMinimal: "gemini-3-5-flash-minimal",
		ThinkingLow:     "claude-haiku-5-5-low",
		ThinkingMedium:  "claude-haiku-5-5-medium",
		ThinkingHigh:    "claude-haiku-5-5-high",
		ThinkingXHigh:   "claude-haiku-5-5-xhigh",
		ThinkingMax:     "claude-haiku-5-5-max",
		ThinkingNone:    "gpt-6-sol-none",
	}
	for want, uid := range uids {
		got := thinkingOf(uid)
		if got.Level != want {
			t.Errorf("thinkingOf(%q) = %q, want %q", uid, got.Level, want)
		}
	}
	if len(uids) != 7 {
		t.Errorf("levels = %d, want 7", len(uids))
	}
}

func TestThinkingCoverage(t *testing.T) {
	defaultedUIDs := []string{
		"claude-opus-4-6-thinking",
		"claude-opus-4-6-thinking-1m",
		"claude-sonnet-4-6-thinking",
		"claude-sonnet-4-6-thinking-1m",
	}
	plainUIDs := []string{
		"glm-5-2", "glm-5-2-1m",
		"swe-1-6", "swe-1-7",
		"claude-opus-4-6", "claude-opus-4-6-1m",
		"claude-sonnet-4-6", "claude-sonnet-4-6-1m",
		"kimi-k2-6", "kimi-k2-7",
	}

	var configs []*devinpb.ClientModelConfig
	for _, uid := range defaultedUIDs {
		configs = append(configs, catalogConfig(uid, uid, "Defaulted", 1, 1, false, false))
	}
	for _, uid := range plainUIDs {
		configs = append(configs, catalogConfig(uid, uid, "Plain", 1, 1, false, false))
	}
	for _, uid := range []string{
		"claude-haiku-5-5-low",
		"claude-haiku-5-5-medium",
		"claude-haiku-5-5-high",
		"claude-haiku-5-5-xhigh",
		"claude-haiku-5-5-max",
	} {
		configs = append(configs, catalogConfig(uid, uid, "Claude Haiku 5.5", 1, 1, false, false))
	}

	catalog := catalogFromResponse(&devinpb.GetCliModelConfigsResponse{ClientModelConfigs: configs})

	var defaulted, plain, explicit int
	for _, model := range catalog.Models {
		switch {
		case model.Thinking.Defaulted:
			defaulted++
			level, ok := model.Thinking.Effective()
			if !ok || level != ThinkingMedium {
				t.Errorf("%s effective level = %q/%v, want medium", model.UID, level, ok)
			}
		case model.Thinking.Plain:
			plain++
		default:
			explicit++
		}
	}
	if defaulted != 4 {
		t.Errorf("defaulted = %d, want 4", defaulted)
	}
	if plain != 10 {
		t.Errorf("plain = %d, want 10", plain)
	}
	if explicit != 5 {
		t.Errorf("explicit = %d, want 5", explicit)
	}
}

func TestCatalogFetch(t *testing.T) {
	var sawKey, sawJWT string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != catalogPath {
			t.Errorf("path = %q, want %q", r.URL.Path, catalogPath)
		}
		if got := r.Header.Get("Content-Type"); got != "application/proto" {
			t.Errorf("content-type = %q", got)
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read body: %v", err)
		}
		var request devinpb.GetCliModelConfigsRequest
		if err := proto.Unmarshal(body, &request); err != nil {
			t.Errorf("decode request: %v", err)
		}
		sawKey = request.GetMetadata().GetApiKey()
		sawJWT = request.GetMetadata().GetUserJwt()
		writeProto(t, w, &devinpb.GetCliModelConfigsResponse{
			ClientModelConfigs: []*devinpb.ClientModelConfig{
				catalogConfig("claude-haiku-5-5-low", "Claude Haiku 5.5 Low", "Claude Haiku 5.5", 200_000, 64_000, true, false),
			},
		})
	}))
	defer server.Close()

	dev := newTestConnector(t, server, Auth{UserJWT: "test-jwt", BaseURL: server.URL}, "test-key")
	catalog, err := dev.Catalog(context.Background())
	if err != nil {
		t.Fatalf("Catalog: %v", err)
	}
	if len(catalog.Models) != 1 || catalog.Models[0].UID != "claude-haiku-5-5-low" {
		t.Fatalf("models = %+v", catalog.Models)
	}
	if sawKey != SessionTokenPrefix+"test-key" {
		t.Errorf("api key = %q", sawKey)
	}
	if sawJWT != "test-jwt" {
		t.Errorf("user jwt = %q", sawJWT)
	}
}

func TestCatalogEmptyIsUpstreamBad(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeProto(t, w, &devinpb.GetCliModelConfigsResponse{})
	}))
	defer server.Close()

	dev := newTestConnector(t, server, Auth{UserJWT: "test-jwt", BaseURL: server.URL}, "test-key")
	_, err := dev.Catalog(context.Background())
	if kind := connector.KindOf(err); kind != connector.KindUpstreamBad {
		t.Fatalf("kind = %q, want UpstreamBad (err %v)", kind, err)
	}
}

func TestCatalogHTTPErrors(t *testing.T) {
	cases := []struct {
		status int
		want   connector.Kind
	}{
		{http.StatusUnauthorized, connector.KindAuth},
		{http.StatusForbidden, connector.KindAuth},
		{http.StatusTooManyRequests, connector.KindRateLimit},
		{http.StatusInternalServerError, connector.KindUpstream},
		{http.StatusBadGateway, connector.KindUpstream},
		{http.StatusBadRequest, connector.KindUpstreamBad},
		{http.StatusNotFound, connector.KindUpstreamBad},
	}
	for _, tc := range cases {
		t.Run(http.StatusText(tc.status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
			}))
			defer server.Close()

			dev := newTestConnector(t, server, Auth{UserJWT: "test-jwt", BaseURL: server.URL}, "test-key")
			_, err := dev.Catalog(context.Background())
			if kind := connector.KindOf(err); kind != tc.want {
				t.Errorf("kind = %q, want %q (err %v)", kind, tc.want, err)
			}
		})
	}
}

func TestCatalogRetryAfter(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Retry-After", "7")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer server.Close()

	dev := newTestConnector(t, server, Auth{UserJWT: "test-jwt", BaseURL: server.URL}, "test-key")
	_, err := dev.Catalog(context.Background())
	var connErr *connector.Error
	if !errors.As(err, &connErr) {
		t.Fatalf("err = %v, want *connector.Error", err)
	}
	if connErr.RetryAfter.Seconds() != 7 {
		t.Errorf("retry after = %v, want 7s", connErr.RetryAfter)
	}
}
