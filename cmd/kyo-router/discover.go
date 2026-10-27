package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"time"

	"kyo-router/internal/config"
	"kyo-router/internal/deepseek"
	"kyo-router/internal/devin"
	"kyo-router/internal/opencode"
)

// knownOpenCodeResponses lists OpenCode upstream IDs verified to require /responses.
var knownOpenCodeResponses = map[string]bool{
	"grok-4.6":                   true,
	"grok-4.7":                   true,
	"gpt-6-luna":                 true,
	"muse-spark-1.3-contributor": true,
}

func runDiscover(args []string) error {
	fs := flag.NewFlagSet("discover", flag.ContinueOnError)
	cfgFlag := fs.String("config", "", "path to config.toml (default $KYO_ROUTER_CONFIG or ./config.toml)")
	writeFlag := fs.Bool("write", false, "write merged catalog back to config.toml")
	timeoutFlag := fs.Duration("timeout", 30*time.Second, "discovery timeout")
	if err := fs.Parse(args); err != nil {
		return err
	}

	path := *cfgFlag
	if path == "" {
		path = os.Getenv("KYO_ROUTER_CONFIG")
	}
	if path == "" {
		path = "config.toml"
	}

	cfg, err := config.Load(path)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), *timeoutFlag)
	defer cancel()

	discovered := fetchAllProviders(ctx, cfg)
	merged, diff := config.MergeModels(cfg.Models, discovered)

	slog.Info("discovery finished",
		"added", len(diff.Added),
		"missing_pinned", len(diff.MissingPinned),
		"total", len(merged),
	)
	for _, id := range diff.MissingPinned {
		slog.Warn("pinned model missing from upstream", "id", id)
	}
	if !*writeFlag || !diff.Changed {
		return nil
	}

	cfg.Models = merged
	if err := config.WriteAtomic(path, cfg); err != nil {
		return fmt.Errorf("write config: %w", err)
	}
	slog.Info("config updated", "path", path, "added", diff.Added)
	return nil
}

func fetchAllProviders(ctx context.Context, cfg *config.Config) map[string][]config.Model {
	out := make(map[string][]config.Model, 3)

	if devModels, err := discoverDevin(ctx, cfg); err != nil {
		slog.Warn("devin discovery skipped", "err", err)
	} else {
		out["devin"] = devModels
	}

	if ocModels, err := discoverOpenCode(ctx, cfg); err != nil {
		slog.Warn("opencode discovery skipped", "err", err)
	} else {
		out["opencode"] = ocModels
	}

	if dsModels, err := discoverDeepSeek(ctx, cfg); err != nil {
		slog.Warn("deepseek discovery skipped", "err", err)
	} else {
		out["deepseek"] = dsModels
	}
	return out
}

func discoverDevin(ctx context.Context, cfg *config.Config) ([]config.Model, error) {
	lookup := os.Getenv
	if name := cfg.Connectors.Devin.APIKeyEnv; name != "" && name != "DEVIN_API_KEY" {
		lookup = func(key string) string {
			if key == "DEVIN_API_KEY" {
				return os.Getenv(name)
			}
			return os.Getenv(key)
		}
	}
	creds, err := devin.LoadCredentials(lookup, devin.CredentialFiles{
		CLIFile: cfg.Connectors.Devin.CredentialsPath,
	})
	if err != nil {
		return nil, err
	}
	conn, err := devin.New(devin.Options{
		Credentials:      creds,
		IDEVersion:       cfg.Connectors.Devin.IDEVersion,
		ExtensionVersion: cfg.Connectors.Devin.ExtensionVersion,
		JWTRefreshMargin: cfg.Connectors.Devin.JWTRefreshMargin.Value(),
	})
	if err != nil {
		return nil, err
	}
	cat, err := conn.Catalog(ctx)
	if err != nil {
		return nil, err
	}
	models := make([]config.Model, 0, len(cat.Models))
	for _, m := range cat.Models {
		var features []string
		if m.SupportsImages {
			features = append(features, "images")
		}
		if _, ok := m.Thinking.Effective(); ok {
			features = append(features, "thinking")
		}
		models = append(models, config.Model{
			ID:        "devin/" + m.UID,
			Connector: "devin",
			Upstream:  m.UID,
			Protocol:  "devin",
			Features:  features,
		})
	}
	return models, nil
}

func discoverOpenCode(ctx context.Context, cfg *config.Config) ([]config.Model, error) {
	conn, err := opencode.New(opencode.Options{
		APIKey:      cfg.Connectors.OpenCode.APIKey.Reveal(),
		BaseURL:     cfg.Connectors.OpenCode.BaseURL,
		SessionMode: opencode.SessionMode(cfg.Connectors.OpenCode.SessionHeaderMode),
	})
	if err != nil {
		return nil, err
	}
	ids, err := conn.Models(ctx)
	if err != nil {
		return nil, err
	}
	models := make([]config.Model, 0, len(ids))
	for _, id := range ids {
		proto := "chat"
		if knownOpenCodeResponses[id] {
			proto = "responses"
		}
		models = append(models, config.Model{
			ID:        "opencode/" + id,
			Connector: "opencode",
			Upstream:  id,
			Protocol:  proto,
		})
	}
	return models, nil
}

func discoverDeepSeek(ctx context.Context, cfg *config.Config) ([]config.Model, error) {
	conn, err := deepseek.New(deepseek.Options{
		APIKey:  cfg.Connectors.DeepSeek.APIKey.Reveal(),
		BaseURL: cfg.Connectors.DeepSeek.BaseURL,
	})
	if err != nil {
		return nil, err
	}
	items, err := conn.Models(ctx)
	if err != nil {
		return nil, err
	}
	models := make([]config.Model, 0, len(items))
	for _, item := range items {
		var features []string
		if item.SupportsImages {
			features = append(features, "images")
		}
		if item.SupportsEffort {
			features = append(features, "thinking")
		}
		models = append(models, config.Model{
			ID:        "deepseek/" + item.ID,
			Connector: "deepseek",
			Upstream:  item.ID,
			Protocol:  "chat",
			Features:  features,
		})
	}
	return models, nil
}
