// Package config reads the TOML file and its environment once at startup, then
// hands the rest of the program a validated value. Load is the only
// constructor; every field is trusted after it returns.
package config

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
)

const defaultJWTRefreshMargin = 60 * time.Second

// Config mirrors config.toml. Secrets are never in the file: they come from
// env or a mounted credentials file and are resolved by Load.
type Config struct {
	Server     Server     `toml:"server"`
	Connectors Connectors `toml:"connectors"`
	Models     []Model    `toml:"model"`
	Routes     []Route    `toml:"route"`
}

type Server struct {
	Bind             string `toml:"bind"`
	RequireAPIKeyEnv string `toml:"require_api_key_env"`
	APIKey           Secret `toml:"-"`
}

type Connectors struct {
	OpenCode OpenCode `toml:"opencode"`
	Devin    Devin    `toml:"devin"`
	DeepSeek DeepSeek `toml:"deepseek"`
}

type OpenCode struct {
	BaseURL           string            `toml:"base_url"`
	APIKeyEnv         string            `toml:"api_key_env"`
	APIKey            Secret            `toml:"-"`
	SessionHeaderMode SessionHeaderMode `toml:"session_header_mode"`
}

// SessionHeaderMode selects how x-opencode-session is filled (02-connectors.md).
type SessionHeaderMode string

const (
	SessionHeaderRequest SessionHeaderMode = "request"
	SessionHeaderFixed   SessionHeaderMode = "fixed"
)

// DeepSeek is a plain OpenAI-compatible upstream: Bearer key, no extra
// headers (02-connectors.md).
type DeepSeek struct {
	BaseURL   string `toml:"base_url"`
	APIKeyEnv string `toml:"api_key_env"`
	APIKey    Secret `toml:"-"`
}

type Devin struct {
	CredentialsPath string `toml:"credentials_path"`
	// APIKeyEnv names the env var that overrides the credentials file. Env wins
	// so a freshly generated key needs no mount; empty falls back to the file.
	APIKeyEnv        string   `toml:"api_key_env"`
	APIKey           Secret   `toml:"-"`
	BaseURL          string   `toml:"base_url"`
	JWTRefreshMargin Duration `toml:"jwt_refresh_margin"`
	IDEVersion       string   `toml:"ide_version"`
	ExtensionVersion string   `toml:"extension_version"`
}

type Model struct {
	ID        string   `toml:"id"`
	Connector string   `toml:"connector"`
	Upstream  string   `toml:"upstream"`
	Protocol  string   `toml:"protocol"`
	Features  []string `toml:"features"`
	// Aliases are extra public ids for the same model, each inside the
	// connector's own namespace (devin/, opencode/).
	Aliases []string `toml:"aliases"`
}

type Route struct {
	Match     string   `toml:"match"`
	Fallbacks []string `toml:"fallbacks"`
}

// Load reads path, resolves env-backed values, and validates the whole file.
// Any failure is fatal at startup, which is the only place a bad config can be
// caught.
func Load(path string) (*Config, error) {
	cfg := new(Config)
	if _, err := toml.DecodeFile(path, cfg); err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	if err := cfg.validate(); err != nil {
		return nil, fmt.Errorf("config %s: %w", path, err)
	}
	return cfg, nil
}

// FieldError names the config key that failed, so a startup error points at the
// file instead of a generic message.
type FieldError struct {
	Field   string
	Problem string
}

func (e *FieldError) Error() string { return e.Field + ": " + e.Problem }

func fieldError(field, format string, args ...any) error {
	return &FieldError{Field: field, Problem: fmt.Sprintf(format, args...)}
}

// Secret keeps a credential out of logs and error text: fmt prints [redacted].
// Reveal is the only way to read the value, which makes the use visible.
type Secret string

func (s Secret) String() string {
	if s == "" {
		return ""
	}
	return "[redacted]"
}

func (s Secret) GoString() string { return s.String() }

func (s Secret) Reveal() string { return string(s) }

// Duration decodes TOML duration strings such as "60s".
type Duration time.Duration

func (d Duration) Value() time.Duration { return time.Duration(d) }

func (d *Duration) UnmarshalText(text []byte) error {
	parsed, err := time.ParseDuration(string(text))
	if err != nil {
		return fmt.Errorf("invalid duration %q: %w", text, err)
	}
	*d = Duration(parsed)
	return nil
}

func (c *Config) validate() error {
	if c.Server.Bind == "" {
		return fieldError("server.bind", "must not be empty")
	}
	if c.Server.RequireAPIKeyEnv != "" {
		key := os.Getenv(c.Server.RequireAPIKeyEnv)
		if key == "" {
			return fieldError("server.require_api_key_env",
				"env %s is empty", c.Server.RequireAPIKeyEnv)
		}
		c.Server.APIKey = Secret(key)
	}
	if len(c.Models) == 0 {
		return fieldError("model", "at least one model is required")
	}

	enabled := make(map[string]bool)
	modelIDs := make(map[string]bool, len(c.Models))
	aliasFields := make(map[string]string)
	for i, m := range c.Models {
		field := fmt.Sprintf("model[%d]", i)
		if m.ID == "" {
			return fieldError(field+".id", "must not be empty")
		}
		if m.Upstream == "" {
			return fieldError(field+".upstream", "must not be empty")
		}
		switch m.Connector {
		case "opencode", "devin", "deepseek":
		default:
			return fieldError(field+".connector", "unknown connector %q", m.Connector)
		}
		switch m.Protocol {
		case "devin", "chat", "responses":
		default:
			return fieldError(field+".protocol", "unknown protocol %q", m.Protocol)
		}
		for j, alias := range m.Aliases {
			aliasField := fmt.Sprintf("%s.aliases[%d]", field, j)
			switch {
			case alias == "":
				return fieldError(aliasField, "must not be empty")
			case alias == m.ID:
				return fieldError(aliasField, "alias repeats the model id")
			case !strings.HasPrefix(alias, m.Connector+"/"):
				return fieldError(aliasField, "alias %q must live in the %s/ namespace", alias, m.Connector)
			}
			if _, dup := aliasFields[alias]; dup {
				return fieldError(aliasField, "duplicate alias %q", alias)
			}
			aliasFields[alias] = aliasField
		}
		enabled[m.Connector] = true
		modelIDs[m.ID] = true
	}
	for alias, field := range aliasFields {
		if modelIDs[alias] {
			return fieldError(field, "alias %q shadows a model id", alias)
		}
	}

	if enabled["opencode"] {
		if err := c.validateOpenCode(); err != nil {
			return err
		}
	}
	if enabled["devin"] {
		if err := c.validateDevin(); err != nil {
			return err
		}
	}
	if enabled["deepseek"] {
		if err := c.validateDeepSeek(); err != nil {
			return err
		}
	}

	for i, r := range c.Routes {
		if r.Match == "" {
			return fieldError(fmt.Sprintf("route[%d].match", i), "must not be empty")
		}
		for j, fallback := range r.Fallbacks {
			if !modelIDs[fallback] {
				return fieldError(fmt.Sprintf("route[%d].fallbacks[%d]", i, j),
					"unknown model %q", fallback)
			}
		}
	}
	return nil
}

func (c *Config) validateOpenCode() error {
	oc := &c.Connectors.OpenCode
	if oc.BaseURL == "" {
		return fieldError("connectors.opencode.base_url", "must not be empty")
	}
	if oc.APIKeyEnv == "" {
		return fieldError("connectors.opencode.api_key_env", "must not be empty")
	}
	switch oc.SessionHeaderMode {
	case SessionHeaderRequest, SessionHeaderFixed:
	default:
		return fieldError("connectors.opencode.session_header_mode",
			"must be %q or %q, got %q", SessionHeaderRequest, SessionHeaderFixed, oc.SessionHeaderMode)
	}
	key := os.Getenv(oc.APIKeyEnv)
	if key == "" {
		return fieldError("connectors.opencode.api_key_env", "env %s is empty", oc.APIKeyEnv)
	}
	oc.APIKey = Secret(key)
	return nil
}

func (c *Config) validateDeepSeek() error {
	ds := &c.Connectors.DeepSeek
	if ds.BaseURL == "" {
		return fieldError("connectors.deepseek.base_url", "must not be empty")
	}
	if ds.APIKeyEnv == "" {
		return fieldError("connectors.deepseek.api_key_env", "must not be empty")
	}
	key := os.Getenv(ds.APIKeyEnv)
	if key == "" {
		return fieldError("connectors.deepseek.api_key_env", "env %s is empty", ds.APIKeyEnv)
	}
	ds.APIKey = Secret(key)
	return nil
}

func (c *Config) validateDevin() error {
	d := &c.Connectors.Devin
	if d.JWTRefreshMargin == 0 {
		d.JWTRefreshMargin = Duration(defaultJWTRefreshMargin)
	}
	if d.APIKeyEnv != "" {
		if key := os.Getenv(d.APIKeyEnv); key != "" {
			d.APIKey = Secret(key)
			return nil
		}
	}
	if d.CredentialsPath == "" {
		if d.APIKeyEnv != "" {
			return fieldError("connectors.devin.api_key_env",
				"env %s is empty and credentials_path is not set", d.APIKeyEnv)
		}
		return fieldError("connectors.devin.credentials_path", "must not be empty")
	}
	info, err := os.Stat(d.CredentialsPath)
	if err != nil {
		return fieldError("connectors.devin.credentials_path", "%v", err)
	}
	if info.IsDir() {
		return fieldError("connectors.devin.credentials_path",
			"%s is a directory; mount the credentials file, not its parent", d.CredentialsPath)
	}
	return nil
}
