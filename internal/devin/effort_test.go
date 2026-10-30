package devin

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"kyo-router/internal/devin/devinpb"
	"kyo-router/internal/models"
)

func TestNormalizeEffortLevel(t *testing.T) {
	cases := map[string]ThinkingLevel{
		"low":        ThinkingLow,
		" LOW ":      ThinkingLow,
		"medium":     ThinkingMedium,
		"high":       ThinkingHigh,
		"xhigh":      ThinkingXHigh,
		"x-high":     ThinkingXHigh,
		"max":        ThinkingMax,
		"minimal":    ThinkingMinimal,
		"none":       ThinkingNone,
		"MAX":        ThinkingMax,
		"extra-high": ThinkingXHigh,
	}
	for in, want := range cases {
		if got, ok := NormalizeEffortLevel(in); !ok || got != want {
			t.Errorf("NormalizeEffortLevel(%q) = %q,%v want %q,true", in, got, ok, want)
		}
	}
	for _, in := range []string{"", "off", "auto", "default", "ultra", "high-priority"} {
		if _, ok := NormalizeEffortLevel(in); ok {
			t.Errorf("NormalizeEffortLevel(%q) ok, want false", in)
		}
	}
}

func TestRewriteUpstream(t *testing.T) {
	cases := []struct {
		upstream string
		level    ThinkingLevel
		want     string
		changed  bool
	}{
		{"claude-haiku-5-5-low", ThinkingHigh, "claude-haiku-5-5-high", true},
		{"claude-haiku-5-5-low", ThinkingLow, "claude-haiku-5-5-low", false},
		{"gpt-5-4-high-priority", ThinkingLow, "gpt-5-4-low-priority", true},
		{"glm-5-2-max-1m", ThinkingNone, "glm-5-2-none-1m", true},
		{"swe-2-medium-lightning", ThinkingMax, "swe-2-max-lightning", true},
		{"claude-opus-4-6-thinking", ThinkingHigh, "claude-opus-4-6-high", true},
		{"claude-opus-4-6", ThinkingHigh, "claude-opus-4-6", false},
		{"glm-5-2", ThinkingMax, "glm-5-2", false},
		{"gpt-5-4-mini-xhigh", ThinkingLow, "gpt-5-4-mini-low", true},
	}
	for _, tc := range cases {
		got, changed := RewriteUpstream(tc.upstream, tc.level)
		if got != tc.want || changed != tc.changed {
			t.Errorf("Rewrite(%q,%q) = %q,%v want %q,%v", tc.upstream, tc.level, got, changed, tc.want, tc.changed)
		}
	}
}

func TestEffectiveUpstream(t *testing.T) {
	known := map[string]bool{"claude-haiku-5-5-high": true}
	high := "high"
	low := "low"
	off := "off"
	bogus := "ultra"

	if got := EffectiveUpstream("claude-haiku-5-5-low", &high, known); got != "claude-haiku-5-5-high" {
		t.Errorf("known rewrite = %q", got)
	}
	if got := EffectiveUpstream("claude-haiku-5-5-low", &high, map[string]bool{}); got != "claude-haiku-5-5-high" {
		t.Errorf("empty known allows rewrite = %q", got)
	}
	if got := EffectiveUpstream("claude-haiku-5-5-low", &high, map[string]bool{"other": true}); got != "claude-haiku-5-5-low" {
		t.Errorf("unknown target keeps original = %q", got)
	}
	if got := EffectiveUpstream("claude-opus-4-6", &high, known); got != "claude-opus-4-6" {
		t.Errorf("plain keeps original = %q", got)
	}
	if got := EffectiveUpstream("claude-haiku-5-5-low", &low, known); got != "claude-haiku-5-5-low" {
		t.Errorf("same level = %q", got)
	}
	if got := EffectiveUpstream("claude-haiku-5-5-low", &off, known); got != "claude-haiku-5-5-low" {
		t.Errorf("off keeps original = %q", got)
	}
	if got := EffectiveUpstream("claude-haiku-5-5-low", &bogus, known); got != "claude-haiku-5-5-low" {
		t.Errorf("bogus keeps original = %q", got)
	}
	if got := EffectiveUpstream("claude-haiku-5-5-low", nil, known); got != "claude-haiku-5-5-low" {
		t.Errorf("nil keeps original = %q", got)
	}
}

func TestChatReasoningEffortRewritesUID(t *testing.T) {
	uids := make(chan string, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request devinpb.GetChatMessageRequest
		readConnectRequest(t, r, &request)
		uids <- request.GetChatModelUid()
		w.Header().Set("Content-Type", "application/connect+proto")
		writeEndStream(t, w, `{}`)
	}))
	defer server.Close()

	dev, err := New(Options{
		Credentials: Credentials{
			APIKey:  NormalizeSessionToken("test-key"),
			BaseURL: server.URL,
			Source:  SourceEnv,
			Kind:    KeyKindModelAPI,
		},
		TokenSource:    staticTokenSource{auth: Auth{UserJWT: "jwt", BaseURL: server.URL}},
		Transport:      server.Client(),
		Clock:          &fakeClock{now: fakeNow},
		KnownUpstreams: map[string]bool{"claude-haiku-5-5-low": true, "claude-haiku-5-5-high": true},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	high := "high"
	req := models.ChatRequest{
		Messages:        []models.Message{{Role: models.RoleUser, Content: "hi"}},
		ReasoningEffort: &high,
	}
	if err := dev.Chat(context.Background(), "claude-haiku-5-5-low", req, func(models.StreamEvent) error { return nil }); err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if got := <-uids; got != "claude-haiku-5-5-high" {
		t.Errorf("uid = %q, want rewritten high", got)
	}
}
