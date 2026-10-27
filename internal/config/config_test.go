package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// baseConfig is a valid file; the single %s is the Devin credentials path.
const baseConfig = `
[server]
bind = ":8080"

[connectors.opencode]
base_url = "https://opencode.ai/zen/go/v1"
api_key_env = "OPENCODE_GO_API_KEY"
session_header_mode = "request"

[connectors.devin]
credentials_path = "%s"
api_key_env = "DEVIN_API_KEY"
jwt_refresh_margin = "90s"
ide_version = "3.2.23"
extension_version = "1.48.2"

[[model]]
id = "devin/claude-haiku-5-5-low"
connector = "devin"
upstream = "claude-haiku-5-5-low"
protocol = "devin"
features = ["images", "thinking"]
aliases = ["devin/haiku"]

[[model]]
id = "opencode/grok-4.7"
connector = "opencode"
upstream = "grok-4.7"
protocol = "responses"

[[route]]
match = "devin/*"
fallbacks = ["opencode/grok-4.7"]

[[route]]
match = "opencode/*"
fallbacks = []
`

// deepseekSection appends a connector plus one model; TOML order is free, so
// tests can add it to baseConfig when they need the deepseek connector live.
const deepseekSection = `
[connectors.deepseek]
base_url = "https://api.deepseek.com"
api_key_env = "DEEPSEEK_API_KEY"

[[model]]
id = "deepseek/deepseek-flash"
connector = "deepseek"
upstream = "deepseek-flash"
protocol = "chat"
aliases = ["deepseek/flash"]
`

func writeFile(t *testing.T, dir, name, body string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	return path
}

func loadBody(t *testing.T, body func(credPath string) string) (*Config, error) {
	t.Helper()
	dir := t.TempDir()
	cred := writeFile(t, dir, "credentials.toml", "")
	return Load(writeFile(t, dir, "config.toml", body(cred)))
}

func TestLoadValid(t *testing.T) {
	t.Setenv("OPENCODE_GO_API_KEY", "oc_sk_test")
	t.Setenv("DEVIN_API_KEY", "")

	cfg, err := loadBody(t, func(cred string) string { return fmt.Sprintf(baseConfig, cred) })
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if cfg.Server.Bind != ":8080" {
		t.Errorf("bind = %q, want :8080", cfg.Server.Bind)
	}
	if len(cfg.Models) != 2 || len(cfg.Routes) != 2 {
		t.Fatalf("models = %d, routes = %d, want 2 and 2", len(cfg.Models), len(cfg.Routes))
	}
	if got := cfg.Connectors.OpenCode.APIKey.Reveal(); got != "oc_sk_test" {
		t.Errorf("api key = %q, want the env value", got)
	}
	if got := fmt.Sprint(cfg.Connectors.OpenCode.APIKey); got != "[redacted]" {
		t.Errorf("%%v of secret = %q, want [redacted]", got)
	}
	if got := fmt.Sprintf("%#v", cfg.Connectors.OpenCode.APIKey); got != "[redacted]" {
		t.Errorf("%%#v of secret = %q, want [redacted]", got)
	}
	if got := cfg.Connectors.Devin.JWTRefreshMargin.Value(); got != 90*time.Second {
		t.Errorf("jwt_refresh_margin = %v, want 90s", got)
	}
	if len(cfg.Models[0].Aliases) != 1 || cfg.Models[0].Aliases[0] != "devin/haiku" {
		t.Errorf("aliases = %v, want [devin/haiku]", cfg.Models[0].Aliases)
	}
}

func TestLoadDefaultsJWTRefreshMargin(t *testing.T) {
	t.Setenv("OPENCODE_GO_API_KEY", "oc_sk_test")
	t.Setenv("DEVIN_API_KEY", "")

	cfg, err := loadBody(t, func(cred string) string {
		return strings.Replace(fmt.Sprintf(baseConfig, cred), `jwt_refresh_margin = "90s"`+"\n", "", 1)
	})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := cfg.Connectors.Devin.JWTRefreshMargin.Value(); got != defaultJWTRefreshMargin {
		t.Errorf("jwt_refresh_margin = %v, want %v", got, defaultJWTRefreshMargin)
	}
}

func TestLoadUsesDevinEnvKeyOverFile(t *testing.T) {
	t.Setenv("OPENCODE_GO_API_KEY", "oc_sk_test")
	t.Setenv("DEVIN_API_KEY", "devin-session-token$fresh")

	// The file path does not exist: an env key must win without touching it.
	cfg, err := loadBody(t, func(string) string {
		return fmt.Sprintf(baseConfig, "/nonexistent/kyo-router/credentials.toml")
	})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := cfg.Connectors.Devin.APIKey.Reveal(); got != "devin-session-token$fresh" {
		t.Errorf("devin key = %q, want the env value", got)
	}
}

func TestLoadRejectsBadConfig(t *testing.T) {
	cases := []struct {
		name      string
		body      func(cred string) string
		wantField string
	}{
		{
			name: "empty bind",
			body: func(cred string) string {
				return strings.Replace(fmt.Sprintf(baseConfig, cred), `bind = ":8080"`, `bind = ""`, 1)
			},
			wantField: "server.bind",
		},
		{
			name: "client auth not enforced yet",
			body: func(cred string) string {
				return strings.Replace(fmt.Sprintf(baseConfig, cred), `bind = ":8080"`,
					"bind = \":8080\"\nrequire_api_key_env = \"KYO_ROUTER_API_KEY\"", 1)
			},
			wantField: "server.require_api_key_env",
		},
		{
			name:      "no models",
			body:      func(string) string { return "[server]\nbind = \":8080\"\n" },
			wantField: "model",
		},
		{
			name: "empty model id",
			body: func(cred string) string {
				return strings.Replace(fmt.Sprintf(baseConfig, cred),
					`id = "opencode/grok-4.7"`, `id = ""`, 1)
			},
			wantField: "model[1].id",
		},
		{
			name: "empty upstream",
			body: func(cred string) string {
				return strings.Replace(fmt.Sprintf(baseConfig, cred),
					`upstream = "grok-4.7"`, `upstream = ""`, 1)
			},
			wantField: "model[1].upstream",
		},
		{
			name: "unknown connector",
			body: func(cred string) string {
				return strings.Replace(fmt.Sprintf(baseConfig, cred),
					`connector = "opencode"`, `connector = "gemini"`, 1)
			},
			wantField: "model[1].connector",
		},
		{
			name: "unknown protocol",
			body: func(cred string) string {
				return strings.Replace(fmt.Sprintf(baseConfig, cred),
					`protocol = "responses"`, `protocol = "anthropic"`, 1)
			},
			wantField: "model[1].protocol",
		},
		{
			name: "alias outside provider namespace",
			body: func(cred string) string {
				return strings.Replace(fmt.Sprintf(baseConfig, cred),
					`aliases = ["devin/haiku"]`, `aliases = ["haiku"]`, 1)
			},
			wantField: "model[0].aliases[0]",
		},
		{
			name: "alias crosses provider namespace",
			body: func(cred string) string {
				return strings.Replace(fmt.Sprintf(baseConfig, cred),
					`aliases = ["devin/haiku"]`, `aliases = ["opencode/haiku"]`, 1)
			},
			wantField: "model[0].aliases[0]",
		},
		{
			name: "alias repeats own model id",
			body: func(cred string) string {
				return strings.Replace(fmt.Sprintf(baseConfig, cred),
					`aliases = ["devin/haiku"]`, `aliases = ["devin/claude-haiku-5-5-low"]`, 1)
			},
			wantField: "model[0].aliases[0]",
		},
		{
			name: "empty alias",
			body: func(cred string) string {
				return strings.Replace(fmt.Sprintf(baseConfig, cred),
					`aliases = ["devin/haiku"]`, `aliases = [""]`, 1)
			},
			wantField: "model[0].aliases[0]",
		},
		{
			name: "duplicate alias on one model",
			body: func(cred string) string {
				return strings.Replace(fmt.Sprintf(baseConfig, cred),
					`aliases = ["devin/haiku"]`, `aliases = ["devin/haiku", "devin/haiku"]`, 1)
			},
			wantField: "model[0].aliases[1]",
		},
		{
			name: "alias shadows another model id",
			body: func(cred string) string {
				return fmt.Sprintf(baseConfig, cred) + `
[[model]]
id = "devin/haiku"
connector = "devin"
upstream = "claude-haiku-5-5-medium"
protocol = "devin"
`
			},
			wantField: "model[0].aliases[0]",
		},
		{
			name: "deepseek base_url missing",
			body: func(cred string) string {
				return strings.Replace(fmt.Sprintf(baseConfig, cred)+deepseekSection,
					`base_url = "https://api.deepseek.com"`, `base_url = ""`, 1)
			},
			wantField: "connectors.deepseek.base_url",
		},
		{
			name:      "deepseek key env empty",
			body:      func(cred string) string { return fmt.Sprintf(baseConfig, cred) + deepseekSection },
			wantField: "connectors.deepseek.api_key_env",
		},
		{
			name: "opencode base_url missing",
			body: func(cred string) string {
				return strings.Replace(fmt.Sprintf(baseConfig, cred),
					`base_url = "https://opencode.ai/zen/go/v1"`, `base_url = ""`, 1)
			},
			wantField: "connectors.opencode.base_url",
		},
		{
			name:      "opencode key env empty",
			body:      func(cred string) string { return fmt.Sprintf(baseConfig, cred) },
			wantField: "connectors.opencode.api_key_env",
		},
		{
			name: "opencode session header mode invalid",
			body: func(cred string) string {
				return strings.Replace(fmt.Sprintf(baseConfig, cred),
					`session_header_mode = "request"`, `session_header_mode = "auto"`, 1)
			},
			wantField: "connectors.opencode.session_header_mode",
		},
		{
			name: "devin credentials file missing",
			body: func(string) string {
				return fmt.Sprintf(baseConfig, "/nonexistent/kyo-router/credentials.toml")
			},
			wantField: "connectors.devin.credentials_path",
		},
		{
			name: "devin key has no source",
			body: func(cred string) string {
				return strings.Replace(fmt.Sprintf(baseConfig, cred),
					`credentials_path = "`+cred+`"`, `credentials_path = ""`, 1)
			},
			wantField: "connectors.devin.api_key_env",
		},
		{
			name: "route fallback unknown",
			body: func(cred string) string {
				return strings.Replace(fmt.Sprintf(baseConfig, cred),
					`fallbacks = ["opencode/grok-4.7"]`, `fallbacks = ["opencode/missing"]`, 1)
			},
			wantField: "route[0].fallbacks[0]",
		},
		{
			name: "route match empty",
			body: func(cred string) string {
				return strings.Replace(fmt.Sprintf(baseConfig, cred), `match = "devin/*"`, `match = ""`, 1)
			},
			wantField: "route[0].match",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("OPENCODE_GO_API_KEY", "oc_sk_test")
			t.Setenv("DEVIN_API_KEY", "")
			t.Setenv("DEEPSEEK_API_KEY", "sk_test")
			if tc.wantField == "connectors.opencode.api_key_env" {
				t.Setenv("OPENCODE_GO_API_KEY", "")
			}
			if tc.wantField == "connectors.deepseek.api_key_env" {
				t.Setenv("DEEPSEEK_API_KEY", "")
			}

			_, err := loadBody(t, tc.body)
			if err == nil {
				t.Fatal("Load succeeded, want an error")
			}
			var fieldErr *FieldError
			if !errors.As(err, &fieldErr) {
				t.Fatalf("error %v is not a *FieldError", err)
			}
			if fieldErr.Field != tc.wantField {
				t.Errorf("field = %q, want %q", fieldErr.Field, tc.wantField)
			}
		})
	}
}

func TestLoadRejectsBadDuration(t *testing.T) {
	t.Setenv("OPENCODE_GO_API_KEY", "oc_sk_test")
	t.Setenv("DEVIN_API_KEY", "")

	_, err := loadBody(t, func(cred string) string {
		return strings.Replace(fmt.Sprintf(baseConfig, cred),
			`jwt_refresh_margin = "90s"`, `jwt_refresh_margin = "soon"`, 1)
	})
	if err == nil {
		t.Fatal("Load succeeded, want a duration parse error")
	}
}

func TestLoadDeepSeek(t *testing.T) {
	t.Setenv("OPENCODE_GO_API_KEY", "oc_sk_test")
	t.Setenv("DEVIN_API_KEY", "")
	t.Setenv("DEEPSEEK_API_KEY", "sk_live")

	cfg, err := loadBody(t, func(cred string) string {
		return fmt.Sprintf(baseConfig, cred) + deepseekSection
	})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := cfg.Connectors.DeepSeek.APIKey.Reveal(); got != "sk_live" {
		t.Errorf("deepseek key = %q, want the env value", got)
	}
	last := cfg.Models[len(cfg.Models)-1]
	if last.Connector != "deepseek" || last.Aliases[0] != "deepseek/flash" {
		t.Errorf("deepseek model = %+v", last)
	}
}

func TestLoadAcceptsSessionHeaderModes(t *testing.T) {
	cases := []struct{ name, mode string }{
		{name: "request", mode: "request"},
		{name: "fixed", mode: "fixed"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("OPENCODE_GO_API_KEY", "oc_sk_test")
			t.Setenv("DEVIN_API_KEY", "")
			cfg, err := loadBody(t, func(cred string) string {
				return strings.Replace(fmt.Sprintf(baseConfig, cred),
					`session_header_mode = "request"`,
					`session_header_mode = "`+tc.mode+`"`, 1)
			})
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if got := string(cfg.Connectors.OpenCode.SessionHeaderMode); got != tc.mode {
				t.Errorf("session_header_mode = %q, want %q", got, tc.mode)
			}
		})
	}
}

func TestMergeModelsIdempotentAndPreservesPins(t *testing.T) {
	pinned := []Model{
		{
			ID:        "devin/claude-haiku-5-5-low",
			Connector: "devin",
			Upstream:  "claude-haiku-5-5-low",
			Protocol:  "devin",
			Features:  []string{"images", "thinking"},
			Aliases:   []string{"devin/haiku"},
		},
		{
			ID:        "opencode/glm-5.3",
			Connector: "opencode",
			Upstream:  "glm-5.3",
			Protocol:  "chat",
			Aliases:   []string{"opencode/glm"},
		},
	}
	discovered := map[string][]Model{
		"opencode": {
			{ID: "opencode/grok-4.7", Connector: "opencode", Upstream: "grok-4.7", Protocol: "responses"},
		},
		"deepseek": {
			{ID: "deepseek/deepseek-flash", Connector: "deepseek", Upstream: "deepseek-flash", Protocol: "chat", Features: []string{"images", "thinking"}},
		},
	}

	merged, diff := MergeModels(pinned, discovered)
	if !diff.Changed {
		t.Fatal("first merge Changed = false, want true")
	}
	if len(diff.Added) != 2 || diff.Added[0] != "deepseek/deepseek-flash" || diff.Added[1] != "opencode/grok-4.7" {
		t.Errorf("added = %v, want sorted additions", diff.Added)
	}
	if len(diff.MissingPinned) != 1 || diff.MissingPinned[0] != "opencode/glm-5.3" {
		t.Errorf("missing_pinned = %v, want [opencode/glm-5.3] (devin was not in discovered map so not flagged)", diff.MissingPinned)
	}
	if len(merged) != 4 || merged[0].Aliases[0] != "devin/haiku" || merged[1].Aliases[0] != "opencode/glm" {
		t.Errorf("merged = %+v, want pinned entries preserved first", merged)
	}

	// Second run with the same discovered set must be a no-op (idempotent).
	merged2, diff2 := MergeModels(merged, discovered)
	if diff2.Changed || len(diff2.Added) != 0 {
		t.Errorf("second merge diff = %+v, want no change", diff2)
	}
	if len(merged2) != len(merged) {
		t.Errorf("second merge len = %d, want %d", len(merged2), len(merged))
	}
}

func TestWriteAtomicRoundTrip(t *testing.T) {
	t.Setenv("OPENCODE_GO_API_KEY", "oc_sk_test")
	t.Setenv("DEVIN_API_KEY", "")
	t.Setenv("DEEPSEEK_API_KEY", "sk_test")

	dir := t.TempDir()
	cred := writeFile(t, dir, "credentials.toml", "")
	path := writeFile(t, dir, "config.toml", fmt.Sprintf(baseConfig, cred)+deepseekSection)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("initial Load: %v", err)
	}
	if err := WriteAtomic(path, cfg); err != nil {
		t.Fatalf("WriteAtomic: %v", err)
	}
	reloaded, err := Load(path)
	if err != nil {
		t.Fatalf("reload after WriteAtomic: %v", err)
	}
	if len(reloaded.Models) != len(cfg.Models) || reloaded.Models[0].Aliases[0] != "devin/haiku" {
		t.Errorf("reloaded models = %+v", reloaded.Models)
	}
}
