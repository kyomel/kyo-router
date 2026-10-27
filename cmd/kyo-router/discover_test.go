package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"kyo-router/internal/config"
)

func TestRunDiscoverDryRunAndIdempotentWrite(t *testing.T) {
	ocServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models" {
			t.Errorf("opencode path = %q, want /models", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"data":[{"id":"glm-5.3"},{"id":"grok-4.7"},{"id":"qwen3.8-max"}]}`))
	}))
	defer ocServer.Close()

	dsServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models" {
			t.Errorf("deepseek path = %q, want /models", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"data":[{"id":"deepseek-flash","input_modalities":["text","image"],"effort":{"supported_levels":["low","high"]}},{"id":"deepseek-v4-pro","input_modalities":["text"],"effort":{"supported_levels":["low","high"]}}]}`))
	}))
	defer dsServer.Close()

	t.Setenv("OPENCODE_GO_API_KEY", "oc_sk_test")
	t.Setenv("DEEPSEEK_API_KEY", "sk_test")
	t.Setenv("DEVIN_API_KEY", "")

	dir := t.TempDir()
	credPath := filepath.Join(dir, "credentials.toml")
	if err := os.WriteFile(credPath, []byte(""), 0o600); err != nil {
		t.Fatalf("write credentials: %v", err)
	}
	cfgPath := filepath.Join(dir, "config.toml")
	body := fmt.Sprintf(`
[server]
bind = ":8080"

[connectors.opencode]
base_url = %q
api_key_env = "OPENCODE_GO_API_KEY"
session_header_mode = "request"

[connectors.devin]
credentials_path = %q
api_key_env = "DEVIN_API_KEY"
jwt_refresh_margin = "60s"
ide_version = "3.2.23"
extension_version = "1.48.2"

[connectors.deepseek]
base_url = %q
api_key_env = "DEEPSEEK_API_KEY"

[[model]]
id = "opencode/glm-5.3"
connector = "opencode"
upstream = "glm-5.3"
protocol = "chat"
aliases = ["opencode/glm"]

[[model]]
id = "deepseek/deepseek-flash"
connector = "deepseek"
upstream = "deepseek-flash"
protocol = "chat"
features = ["images", "thinking"]
aliases = ["deepseek/flash"]

[[route]]
match = "opencode/*"
fallbacks = []
`, ocServer.URL, credPath, dsServer.URL)
	if err := os.WriteFile(cfgPath, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	// Dry run leaves the file untouched.
	if err := runDiscover([]string{"-config", cfgPath}); err != nil {
		t.Fatalf("runDiscover dry-run: %v", err)
	}
	before, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("Load after dry-run: %v", err)
	}
	if len(before.Models) != 2 {
		t.Fatalf("dry-run models = %d, want 2", len(before.Models))
	}

	// Write run adds the newly discovered models and keeps existing aliases.
	if err := runDiscover([]string{"-config", cfgPath, "-write"}); err != nil {
		t.Fatalf("runDiscover write: %v", err)
	}
	after, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("Load after write: %v", err)
	}
	if len(after.Models) != 5 {
		t.Fatalf("write models = %d, want 5: %+v", len(after.Models), after.Models)
	}
	if after.Models[0].Aliases[0] != "opencode/glm" || after.Models[1].Aliases[0] != "deepseek/flash" {
		t.Errorf("pinned aliases lost: %+v", after.Models[:2])
	}

	// Second write run is idempotent.
	info1, err := os.Stat(cfgPath)
	if err != nil {
		t.Fatalf("stat 1: %v", err)
	}
	if err := runDiscover([]string{"-config", cfgPath, "-write"}); err != nil {
		t.Fatalf("runDiscover second write: %v", err)
	}
	info2, err := os.Stat(cfgPath)
	if err != nil {
		t.Fatalf("stat 2: %v", err)
	}
	if !info1.ModTime().Equal(info2.ModTime()) {
		t.Errorf("second write touched config.toml (mtime %v -> %v)", info1.ModTime(), info2.ModTime())
	}
}
