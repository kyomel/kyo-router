// Command kyo-router serves one OpenAI-compatible endpoint in front of the
// configured subscriptions. This file is wiring only: config -> catalog -> api.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"kyo-router/internal/api"
	"kyo-router/internal/catalog"
	"kyo-router/internal/config"
	"kyo-router/internal/connector"
	"kyo-router/internal/router"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "discover" {
		if err := runDiscover(os.Args[2:]); err != nil {
			slog.Error("kyo-router discover failed", "err", err)
			os.Exit(1)
		}
		return
	}
	if err := run(); err != nil {
		slog.Error("kyo-router stopped", "err", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load(configPath())
	if err != nil {
		return err
	}

	cat, err := catalog.New(catalogModels(cfg.Models), time.Now().Unix())
	if err != nil {
		return err
	}

	backends, err := buildBackends(cfg)
	if err != nil {
		return err
	}
	rt := router.New(cat, backends, routeRules(cfg.Routes))

	srv := &http.Server{
		Addr:              cfg.Server.Bind,
		Handler:           api.New(cat, rt, cfg.Server.APIKey.Reveal(), slog.Default()),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
		// No WriteTimeout: SSE replies stay open for the whole completion.
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.ListenAndServe() }()
	slog.Info("kyo-router listening", "bind", cfg.Server.Bind, "models", len(cat.Models()))

	select {
	case err := <-serveErr:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("shutdown: %w", err)
	}
	slog.Info("kyo-router stopped")
	return nil
}

// configPath resolves the file path: flag, then env, then the working directory.
func configPath() string {
	path := flag.String("config", "", "path to config.toml (default $KYO_ROUTER_CONFIG or ./config.toml)")
	flag.Parse()
	return resolveConfigPath(*path)
}

// buildBackends constructs the connectors the configured models need. A
// missing key for an enabled connector is fatal - the alternative is models
// that resolve but can never serve.
func buildBackends(cfg *config.Config) (map[string]connector.Connector, error) {
	enabled := map[string]bool{}
	for _, m := range cfg.Models {
		enabled[m.Connector] = true
	}
	backends := map[string]connector.Connector{}

	if enabled["deepseek"] {
		ds, err := newDeepSeekConnector(cfg.Connectors.DeepSeek)
		if err != nil {
			return nil, err
		}
		backends["deepseek"] = ds
	}
	if enabled["opencode"] {
		// Responses-protocol upstreams take the other endpoint; the set comes
		// from the pinned protocol per model (02-connectors.md).
		var responsesModels []string
		for _, m := range cfg.Models {
			if m.Connector == "opencode" && m.Protocol == "responses" {
				responsesModels = append(responsesModels, m.Upstream)
			}
		}
		oc, err := newOpenCodeConnector(cfg.Connectors.OpenCode, responsesModels)
		if err != nil {
			return nil, err
		}
		backends["opencode"] = oc
	}
	if enabled["devin"] {
		dev, err := newDevinConnector(cfg.Connectors.Devin)
		if err != nil {
			return nil, err
		}
		backends["devin"] = dev
	}
	return backends, nil
}

func routeRules(routes []config.Route) []router.Rule {
	rules := make([]router.Rule, 0, len(routes))
	for _, r := range routes {
		rules = append(rules, router.Rule{Match: r.Match, Fallbacks: r.Fallbacks})
	}
	return rules
}

func catalogModels(configured []config.Model) []catalog.Model {
	models := make([]catalog.Model, 0, len(configured))
	for _, m := range configured {
		models = append(models, catalog.Model{
			ID:        m.ID,
			Connector: m.Connector,
			Upstream:  m.Upstream,
			// Config validated the value against the catalog protocol set, so
			// this conversion cannot introduce an unknown protocol.
			Protocol: catalog.Protocol(m.Protocol),
			Features: m.Features,
			Aliases:  m.Aliases,
		})
	}
	return models
}
