// Package config reads the TOML file and its environment once at startup, then
// hands the rest of the program a validated value. Load is the only
// constructor; every field is trusted after it returns.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
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
	RequireAPIKeyEnv string `toml:"require_api_key_env,omitempty"`
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
	Features  []string `toml:"features,omitempty"`
	// Aliases are extra public ids for the same model, each inside the
	// connector's own namespace (devin/, opencode/).
	Aliases []string `toml:"aliases,omitempty"`
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

func (d Duration) MarshalText() ([]byte, error) {
	return []byte(time.Duration(d).String()), nil
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

// MergeDiff summarizes what changed between the pinned list and live discovery.
type MergeDiff struct {
	Added         []string
	MissingPinned []string
	Changed       bool
}

// MergeModels merges discovered models per connector into the pinned list.
// Existing entries keep their protocol, features, and aliases untouched.
func MergeModels(pinned []Model, discovered map[string][]Model) ([]Model, MergeDiff) {
	liveByConnector := make(map[string]map[string]Model, len(discovered))
	for conn, list := range discovered {
		m := make(map[string]Model, len(list))
		for _, item := range list {
			m[item.ID] = item
		}
		liveByConnector[conn] = m
	}

	seen := make(map[string]bool, len(pinned))
	merged := make([]Model, 0, len(pinned))
	var diff MergeDiff

	for _, existing := range pinned {
		seen[existing.ID] = true
		merged = append(merged, existing)
		if live, ok := liveByConnector[existing.Connector]; ok {
			if _, stillThere := live[existing.ID]; !stillThere {
				diff.MissingPinned = append(diff.MissingPinned, existing.ID)
			}
		}
	}

	var additions []Model
	for _, list := range discovered {
		for _, item := range list {
			if !seen[item.ID] {
				seen[item.ID] = true
				additions = append(additions, item)
			}
		}
	}
	slices.SortFunc(additions, func(a, b Model) int {
		if a.Connector != b.Connector {
			return strings.Compare(a.Connector, b.Connector)
		}
		return strings.Compare(a.ID, b.ID)
	})
	for _, add := range additions {
		merged = append(merged, add)
		diff.Added = append(diff.Added, add.ID)
	}
	diff.Changed = len(diff.Added) > 0
	return merged, diff
}

// WriteAtomic encodes cfg to a sibling temp file and renames it over path.
// When path is a single-file container bind mount (where creating a sibling in
// /etc or renaming over the mountpoint returns EACCES/EBUSY), it falls back to
// writing the encoded bytes in place.
func WriteAtomic(path string, cfg *Config) error {
	var buf bytes.Buffer
	if err := toml.NewEncoder(&buf).Encode(cfg); err != nil {
		return fmt.Errorf("encode config: %w", err)
	}
	tmp := filepath.Clean(path) + ".tmp"
	if err := os.WriteFile(tmp, buf.Bytes(), 0o600); err != nil {
		if isBindMountFallback(err) {
			return os.WriteFile(path, buf.Bytes(), 0o600)
		}
		return fmt.Errorf("write temp config: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		if isBindMountFallback(err) {
			return os.WriteFile(path, buf.Bytes(), 0o600)
		}
		return fmt.Errorf("rename config: %w", err)
	}
	return nil
}

func isBindMountFallback(err error) bool {
	return errors.Is(err, syscall.EBUSY) ||
		errors.Is(err, syscall.EACCES) ||
		errors.Is(err, syscall.EPERM) ||
		errors.Is(err, syscall.EXDEV)
}
