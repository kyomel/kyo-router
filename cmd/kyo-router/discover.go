package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"time"

	"kyo-router/internal/config"
	"kyo-router/internal/opencode"
)

func runDiscover(args []string) error {
	fs := flag.NewFlagSet("discover", flag.ContinueOnError)
	cfgFlag := fs.String("config", "", "path to config.toml (default $KYO_ROUTER_CONFIG or ./config.toml)")
	writeFlag := fs.Bool("write", false, "write merged catalog back to config.toml (TOML comments are lost)")
	timeoutFlag := fs.Duration("timeout", 30*time.Second, "discovery timeout")
	if err := fs.Parse(args); err != nil {
		return err
	}

	path := resolveConfigPath(*cfgFlag)

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
		"skipped", len(diff.Skipped),
		"missing_pinned", len(diff.MissingPinned),
		"total", len(merged),
	)
	for _, id := range diff.MissingPinned {
		slog.Warn("pinned model missing from upstream", "id", id)
	}
	for _, id := range diff.Skipped {
		slog.Warn("discovered model id collides with a pin or alias", "id", id)
	}
	if !*writeFlag || !diff.Changed {
		return nil
	}

	cfg.Models = merged
	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("merged config invalid, not writing: %w", err)
	}
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
	conn, err := newDevinConnector(cfg.Connectors.Devin)
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
	conn, err := newOpenCodeConnector(cfg.Connectors.OpenCode, nil)
	if err != nil {
		return nil, err
	}
	ids, err := conn.Models(ctx)
	if err != nil {
		return nil, err
	}
	models := make([]config.Model, 0, len(ids))
	for _, id := range ids {
		models = append(models, config.Model{
			ID:        "opencode/" + id,
			Connector: "opencode",
			Upstream:  id,
			Protocol:  opencode.DefaultProtocol(id),
		})
	}
	return models, nil
}

func discoverDeepSeek(ctx context.Context, cfg *config.Config) ([]config.Model, error) {
	conn, err := newDeepSeekConnector(cfg.Connectors.DeepSeek)
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
