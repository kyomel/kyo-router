package devin

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/BurntSushi/toml"
	"google.golang.org/protobuf/proto"

	"kyo-router/internal/connector"
	"kyo-router/internal/devin/devinpb"
	"kyo-router/internal/httpx"
)

const (
	// SessionTokenPrefix marks a Devin model-quota session token. Adding it a
	// second time turns a working key into a 401.
	SessionTokenPrefix = "devin-session-token$"

	DefaultAPIURL = "https://server.codeium.com"

	DefaultIDEVersion         = "3.2.23"
	DefaultExtensionVersion   = "1.48.2"
	DefaultJWTRefreshMargin   = 60 * time.Second
	defaultCredentialsTimeout = 30 * time.Second
)

const (
	envAPIKey          = "DEVIN_API_KEY"
	envAPIServerURL    = "DEVIN_API_SERVER_URL"
	envKeyFile         = "DEVIN_KEY_FILE"
	envCredentialsPath = "DEVIN_CREDENTIALS_PATH"

	authPath = "/exa.auth_pb.AuthService/GetUserJwt"
)

// ErrNoCredentials means no model-api key exists in any configured source.
// Agent-api keys are a different product and are skipped, not returned.
var ErrNoCredentials = errors.New("devin: no model-api credentials")

// KeyKind selects the Devin product a key belongs to.
type KeyKind string

const (
	KeyKindModelAPI KeyKind = "model-api"
	KeyKindAgentAPI KeyKind = "agent-api"
)

var agentKeyPrefixes = []string{"apk_", "dev_", "pat_"}

// ClassifyKey reads the key family from its prefix. A session-token prefix is
// stripped first, so an already-normalized key classifies the same as its raw
// form.
func ClassifyKey(raw string) KeyKind {
	trimmed := strings.TrimPrefix(strings.TrimSpace(raw), SessionTokenPrefix)
	for _, prefix := range agentKeyPrefixes {
		if strings.HasPrefix(trimmed, prefix) {
			return KeyKindAgentAPI
		}
	}
	return KeyKindModelAPI
}

// NormalizeSessionToken adds the session-token prefix exactly once. A key that
// already carries it is returned unchanged, and an agent-api key passes through
// because it carries its own scheme.
func NormalizeSessionToken(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return ""
	}
	if strings.HasPrefix(trimmed, SessionTokenPrefix) {
		return trimmed
	}
	if ClassifyKey(trimmed) == KeyKindAgentAPI {
		return trimmed
	}
	return SessionTokenPrefix + trimmed
}

// CredentialSource names where a key came from.
type CredentialSource string

const (
	SourceEnv     CredentialSource = "env"
	SourceKeyFile CredentialSource = "key-file"
	SourceCLI     CredentialSource = "devin-cli"
)

// Credentials is one resolved model-api key and the Connect host it belongs to.
type Credentials struct {
	APIKey  string
	BaseURL string
	Source  CredentialSource
	Kind    KeyKind
}

// CredentialFiles names the two optional on-disk sources. Empty paths fall back
// to the matching environment variable, then to the Devin CLI default.
type CredentialFiles struct {
	KeyFile string
	CLIFile string
}

// LookupEnv is the environment seam. os.Getenv satisfies it.
type LookupEnv func(string) string

// LoadCredentials resolves a model-api key in the reference order:
// DEVIN_API_KEY, then the JSON key file, then the Devin CLI credentials.toml.
// A key of the wrong kind is skipped at every step so it cannot hide a usable
// key below it.
func LoadCredentials(lookup LookupEnv, files CredentialFiles) (Credentials, error) {
	if lookup == nil {
		lookup = os.Getenv
	}

	if raw := strings.TrimSpace(lookup(envAPIKey)); raw != "" {
		if creds, err := credentialsFromEnv(raw, lookup); err != nil {
			return Credentials{}, err
		} else if creds.Kind == KeyKindModelAPI {
			return creds, nil
		}
	}

	keyFile := files.KeyFile
	if keyFile == "" {
		keyFile = strings.TrimSpace(lookup(envKeyFile))
	}
	if keyFile != "" {
		creds, err := loadKeyFile(keyFile)
		if err != nil {
			return Credentials{}, err
		}
		if creds != nil && creds.Kind == KeyKindModelAPI {
			return *creds, nil
		}
	}

	cliFile := files.CLIFile
	if cliFile == "" {
		cliFile = strings.TrimSpace(lookup(envCredentialsPath))
	}
	if cliFile == "" {
		cliFile = defaultCLICredentialsPath()
	}
	if cliFile != "" {
		creds, err := loadCLICredentials(cliFile)
		if err != nil {
			return Credentials{}, err
		}
		if creds != nil && creds.Kind == KeyKindModelAPI {
			return *creds, nil
		}
	}

	return Credentials{}, ErrNoCredentials
}

func credentialsFromEnv(raw string, lookup LookupEnv) (Credentials, error) {
	key, err := cleanAPIKey(raw, envAPIKey)
	if err != nil {
		return Credentials{}, err
	}
	base := normalizeBaseURL(lookup(envAPIServerURL))
	return Credentials{APIKey: key, BaseURL: base, Source: SourceEnv, Kind: ClassifyKey(raw)}, nil
}

type keyFilePayload struct {
	APIKey       string `json:"apiKey"`
	APIServerURL string `json:"apiServerUrl"`
}

func loadKeyFile(path string) (*Credentials, error) {
	contents, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read Devin key file %s: %w", path, err)
	}
	var payload keyFilePayload
	if err := json.Unmarshal(contents, &payload); err != nil {
		return nil, fmt.Errorf("parse Devin key file %s: %w", path, err)
	}
	if strings.TrimSpace(payload.APIKey) == "" {
		return nil, fmt.Errorf("devin key file %s has no apiKey", path)
	}
	key, err := cleanAPIKey(payload.APIKey, path)
	if err != nil {
		return nil, err
	}
	return &Credentials{
		APIKey:  key,
		BaseURL: normalizeBaseURL(payload.APIServerURL),
		Source:  SourceKeyFile,
		Kind:    ClassifyKey(payload.APIKey),
	}, nil
}

type cliCredentials struct {
	WindsurfAPIKey string `toml:"windsurf_api_key"`
	APIServerURL   string `toml:"api_server_url"`
}

func loadCLICredentials(path string) (*Credentials, error) {
	contents, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read Devin CLI credentials %s: %w", path, err)
	}
	var parsed cliCredentials
	if err := toml.Unmarshal(contents, &parsed); err != nil {
		return nil, fmt.Errorf("parse Devin CLI credentials %s: %w", path, err)
	}
	if strings.TrimSpace(parsed.WindsurfAPIKey) == "" {
		return nil, nil
	}
	key, err := cleanAPIKey(parsed.WindsurfAPIKey, path)
	if err != nil {
		return nil, err
	}
	return &Credentials{
		APIKey:  key,
		BaseURL: normalizeBaseURL(parsed.APIServerURL),
		Source:  SourceCLI,
		Kind:    ClassifyKey(parsed.WindsurfAPIKey),
	}, nil
}

func defaultCLICredentialsPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".local", "share", "devin", "credentials.toml")
}

func cleanAPIKey(raw, origin string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", fmt.Errorf("empty Devin key in %s", origin)
	}
	if strings.IndexFunc(trimmed, unicode.IsSpace) >= 0 {
		return "", fmt.Errorf("devin key in %s contains whitespace", origin)
	}
	return NormalizeSessionToken(trimmed), nil
}

// normalizeBaseURL cleans a source-provided URL. Empty stays empty so the
// caller can layer a config fallback below the built-in default in New.
func normalizeBaseURL(raw string) string {
	return strings.TrimRight(strings.TrimSpace(raw), "/")
}

// Auth is one JWT plus the Connect host that issued it.
type Auth struct {
	UserJWT string
	BaseURL string
}

// TokenSource is the auth seam. Connectors call Token before every RPC; the
// implementation owns caching and refresh. Invalidate drops the cache so a
// retry after an upstream 401 mints a fresh token instead of replaying the
// rejected one.
type TokenSource interface {
	Token(ctx context.Context) (Auth, error)
	Invalidate()
}

// Clock is the time seam for JWT expiry and Retry-After.
type Clock interface {
	Now() time.Time
}

type systemClock struct{}

func (systemClock) Now() time.Time { return time.Now() }

// TokenSourceOptions builds the default JWT source.
type TokenSourceOptions struct {
	Credentials      Credentials
	Transport        httpx.Transport
	Clock            Clock
	RefreshMargin    time.Duration
	IDEVersion       string
	ExtensionVersion string
}

// NewTokenSource returns the cached, single-flight GetUserJwt source.
func NewTokenSource(opts TokenSourceOptions) (TokenSource, error) {
	if strings.TrimSpace(opts.Credentials.APIKey) == "" {
		return nil, errors.New("devin: token source needs an api key")
	}
	opts.Credentials.APIKey = NormalizeSessionToken(opts.Credentials.APIKey)
	if ClassifyKey(opts.Credentials.APIKey) != KeyKindModelAPI {
		return nil, errors.New("devin: token source needs a model-api key")
	}
	if strings.TrimSpace(opts.Credentials.BaseURL) == "" {
		opts.Credentials.BaseURL = DefaultAPIURL
	}
	if opts.Transport == nil {
		opts.Transport = &http.Client{Timeout: defaultCredentialsTimeout}
	}
	if opts.Clock == nil {
		opts.Clock = systemClock{}
	}
	if opts.RefreshMargin <= 0 {
		opts.RefreshMargin = DefaultJWTRefreshMargin
	}
	if opts.IDEVersion == "" {
		opts.IDEVersion = DefaultIDEVersion
	}
	if opts.ExtensionVersion == "" {
		opts.ExtensionVersion = DefaultExtensionVersion
	}
	return &jwtSource{
		credentials:      opts.Credentials,
		transport:        opts.Transport,
		clock:            opts.Clock,
		refreshMargin:    opts.RefreshMargin,
		ideVersion:       opts.IDEVersion,
		extensionVersion: opts.ExtensionVersion,
	}, nil
}

type cachedAuth struct {
	auth      Auth
	expiresAt time.Time
}

type jwtFlight struct {
	done      chan struct{}
	auth      Auth
	expiresAt time.Time
	err       error
}

type jwtSource struct {
	credentials      Credentials
	transport        httpx.Transport
	clock            Clock
	refreshMargin    time.Duration
	ideVersion       string
	extensionVersion string

	mu     sync.Mutex
	cached *cachedAuth
	flight *jwtFlight
}

// Token returns a cached JWT until it is within the refresh margin of expiry.
// Concurrent misses share one GetUserJwt call.
func (s *jwtSource) Token(ctx context.Context) (Auth, error) {
	for {
		s.mu.Lock()
		if s.cached != nil && s.clock.Now().Add(s.refreshMargin).Before(s.cached.expiresAt) {
			auth := s.cached.auth
			s.mu.Unlock()
			return auth, nil
		}
		if s.flight != nil {
			flight := s.flight
			s.mu.Unlock()
			select {
			case <-flight.done:
				if flight.err == nil {
					return flight.auth, nil
				}
				// The leader's context may have died while this waiter still
				// has budget; only a provider failure is shared.
				if errors.Is(flight.err, context.Canceled) || errors.Is(flight.err, context.DeadlineExceeded) {
					continue
				}
				return Auth{}, flight.err
			case <-ctx.Done():
				return Auth{}, connector.NetworkError(ctx.Err())
			}
		}
		flight := &jwtFlight{done: make(chan struct{})}
		s.flight = flight
		s.mu.Unlock()

		auth, expiresAt, err := s.fetch(ctx)

		s.mu.Lock()
		flight.auth = auth
		flight.expiresAt = expiresAt
		flight.err = err
		if err == nil {
			s.cached = &cachedAuth{auth: auth, expiresAt: expiresAt}
		}
		s.flight = nil
		close(flight.done)
		s.mu.Unlock()
		return auth, err
	}
}

// Invalidate drops the cached JWT. The next Token call fetches a fresh one.
func (s *jwtSource) Invalidate() {
	s.mu.Lock()
	s.cached = nil
	s.mu.Unlock()
}

func (s *jwtSource) fetch(ctx context.Context) (Auth, time.Time, error) {
	payload, err := proto.Marshal(&devinpb.GetUserJwtRequest{
		Metadata: newMetadata(s.credentials.APIKey, "", s.ideVersion, s.extensionVersion),
	})
	if err != nil {
		return Auth{}, time.Time{}, connector.UpstreamBad("encode_user_jwt", "cannot encode GetUserJwt request", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		strings.TrimRight(s.credentials.BaseURL, "/")+authPath, bytes.NewReader(payload))
	if err != nil {
		return Auth{}, time.Time{}, connector.BadRequest("auth_request", "cannot build GetUserJwt request")
	}
	req.Header.Set("Content-Type", "application/proto")
	req.Header.Set("Connect-Protocol-Version", "1")
	req.Header.Set("Accept", "*/*")

	resp, err := s.transport.Do(req)
	if err != nil {
		return Auth{}, time.Time{}, connector.NetworkError(err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return Auth{}, time.Time{}, connector.NetworkError(err)
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return Auth{}, time.Time{}, httpError(resp.StatusCode, resp.Header.Get("Retry-After"), s.clock.Now())
	}

	var decoded devinpb.GetUserJwtResponse
	if err := proto.Unmarshal(body, &decoded); err != nil {
		unzipped, gzipErr := gunzip(body)
		if gzipErr != nil {
			return Auth{}, time.Time{}, connector.UpstreamBad("decode_user_jwt", "GetUserJwt response is not protobuf", err)
		}
		if err := proto.Unmarshal(unzipped, &decoded); err != nil {
			return Auth{}, time.Time{}, connector.UpstreamBad("decode_user_jwt", "GetUserJwt response is not protobuf", err)
		}
	}
	if decoded.GetUserJwt() == "" {
		return Auth{}, time.Time{}, connector.UpstreamBad("empty_user_jwt", "GetUserJwt returned an empty user JWT", nil)
	}

	baseURL := s.credentials.BaseURL
	if custom := strings.TrimSpace(decoded.GetCustomApiServerUrl()); custom != "" {
		baseURL = strings.TrimRight(custom, "/")
	}
	auth := Auth{UserJWT: decoded.GetUserJwt(), BaseURL: baseURL}
	return auth, jwtExpiry(decoded.GetUserJwt()), nil
}

// jwtExpiry reads the exp claim. A missing or malformed claim returns the zero
// time, which makes the next Token call refetch.
func jwtExpiry(token string) time.Time {
	parts := strings.Split(token, ".")
	if len(parts) < 2 {
		return time.Time{}
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return time.Time{}
	}
	var claims struct {
		Exp float64 `json:"exp"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil || claims.Exp <= 0 {
		return time.Time{}
	}
	return time.Unix(int64(claims.Exp), 0)
}

func newMetadata(apiKey, userJWT, ideVersion, extensionVersion string) *devinpb.Metadata {
	return &devinpb.Metadata{
		IdeName:          "windsurf",
		IdeVersion:       ideVersion,
		ExtensionName:    "windsurf",
		ExtensionVersion: extensionVersion,
		ApiKey:           apiKey,
		Locale:           "en",
		UserJwt:          userJWT,
	}
}

func gunzip(payload []byte) ([]byte, error) {
	reader, err := gzip.NewReader(bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	defer reader.Close()
	return io.ReadAll(io.LimitReader(reader, 4<<20))
}
