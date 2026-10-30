package main

import (
	"fmt"
	"os"
	"strings"

	"kyo-router/internal/config"
	"kyo-router/internal/deepseek"
	"kyo-router/internal/devin"
	"kyo-router/internal/opencode"
)

// resolveConfigPath picks flag, then env, then the working directory.
func resolveConfigPath(flagValue string) string {
	if flagValue != "" {
		return flagValue
	}
	if env := os.Getenv("KYO_ROUTER_CONFIG"); env != "" {
		return env
	}
	return "config.toml"
}

func newDeepSeekConnector(ds config.DeepSeek) (*deepseek.Connector, error) {
	return deepseek.New(deepseek.Options{
		APIKey:  ds.APIKey.Reveal(),
		BaseURL: ds.BaseURL,
	})
}

func newOpenCodeConnector(oc config.OpenCode, responsesModels []string) (*opencode.Connector, error) {
	return opencode.New(opencode.Options{
		APIKey:          oc.APIKey.Reveal(),
		BaseURL:         oc.BaseURL,
		SessionMode:     opencode.SessionMode(oc.SessionHeaderMode),
		ResponsesModels: responsesModels,
	})
}

// newDevinConnector resolves credentials like Load does: env override first,
// then the mounted credentials file. A custom api_key_env name is surfaced as
// DEVIN_API_KEY for the lookup, and connectors.devin.base_url acts as a
// fallback below DEVIN_API_SERVER_URL and the credentials file.
func newDevinConnector(d config.Devin, knownUpstreams map[string]bool) (*devin.Connector, error) {
	lookup := os.Getenv
	if d.APIKeyEnv != "" && d.APIKeyEnv != "DEVIN_API_KEY" {
		lookup = func(key string) string {
			if key == "DEVIN_API_KEY" {
				return os.Getenv(d.APIKeyEnv)
			}
			return os.Getenv(key)
		}
	}
	creds, err := devin.LoadCredentials(lookup, devin.CredentialFiles{
		CLIFile: d.CredentialsPath,
	})
	if err != nil {
		return nil, fmt.Errorf("devin credentials: %w", err)
	}
	if creds.BaseURL == "" {
		creds.BaseURL = strings.TrimRight(strings.TrimSpace(d.BaseURL), "/")
	}
	return devin.New(devin.Options{
		Credentials:      creds,
		IDEVersion:       d.IDEVersion,
		ExtensionVersion: d.ExtensionVersion,
		JWTRefreshMargin: d.JWTRefreshMargin.Value(),
		KnownUpstreams:   knownUpstreams,
	})
}
