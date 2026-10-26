package devin

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	"kyo-router/internal/connector"
	"kyo-router/internal/devin/devinpb"
)

type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

func makeJWT(t *testing.T, expiresAt time.Time) string {
	t.Helper()
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none","typ":"JWT"}`))
	payload, err := json.Marshal(map[string]int64{"exp": expiresAt.Unix()})
	if err != nil {
		t.Fatalf("marshal claims: %v", err)
	}
	return header + "." + base64.RawURLEncoding.EncodeToString(payload) + ".sig"
}

func TestNormalizeSessionToken(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"raw model token gets the prefix", "raw-key", SessionTokenPrefix + "raw-key"},
		{"prefixed token is unchanged", SessionTokenPrefix + "raw-key", SessionTokenPrefix + "raw-key"},
		{"normalizing twice never double-prefixes", NormalizeSessionToken(SessionTokenPrefix + "raw-key"), SessionTokenPrefix + "raw-key"},
		{"agent apk key passes through", "apk_123", "apk_123"},
		{"agent dev key passes through", "dev_123", "dev_123"},
		{"agent pat key passes through", "pat_123", "pat_123"},
		{"whitespace is trimmed", "  raw-key  ", SessionTokenPrefix + "raw-key"},
		{"empty stays empty", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := NormalizeSessionToken(tc.in); got != tc.want {
				t.Errorf("NormalizeSessionToken(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestClassifyKey(t *testing.T) {
	cases := map[string]KeyKind{
		"raw":                          KeyKindModelAPI,
		SessionTokenPrefix + "raw":     KeyKindModelAPI,
		"apk_1":                        KeyKindAgentAPI,
		"dev_1":                        KeyKindAgentAPI,
		"pat_1":                        KeyKindAgentAPI,
		SessionTokenPrefix + "apk_1":   KeyKindAgentAPI,
		SessionTokenPrefix + "raw-key": KeyKindModelAPI,
	}
	for key, want := range cases {
		if got := ClassifyKey(key); got != want {
			t.Errorf("ClassifyKey(%q) = %q, want %q", key, got, want)
		}
	}
}

func TestLoadCredentialsOrder(t *testing.T) {
	dir := t.TempDir()
	keyFile := filepath.Join(dir, "key.json")
	cliFile := filepath.Join(dir, "credentials.toml")

	writeFile := func(t *testing.T, path, contents string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}
	writeFile(t, keyFile, `{"apiKey":"key-file-raw","apiServerUrl":"https://key-file.example/"}`)
	writeFile(t, cliFile, "windsurf_api_key = \"cli-raw\"\napi_server_url = \"https://cli.example/\"\n")

	t.Run("env model key wins over files", func(t *testing.T) {
		lookup := envLookup(map[string]string{
			"DEVIN_API_KEY":        "env-raw",
			"DEVIN_API_SERVER_URL": "https://env.example/",
		})
		creds, err := LoadCredentials(lookup, CredentialFiles{KeyFile: keyFile, CLIFile: cliFile})
		if err != nil {
			t.Fatalf("LoadCredentials: %v", err)
		}
		if creds.APIKey != SessionTokenPrefix+"env-raw" {
			t.Errorf("api key = %q, want env key with one prefix", creds.APIKey)
		}
		if creds.BaseURL != "https://env.example" {
			t.Errorf("base url = %q", creds.BaseURL)
		}
		if creds.Source != SourceEnv {
			t.Errorf("source = %q, want env", creds.Source)
		}
	})

	t.Run("env agent key is skipped for the key file", func(t *testing.T) {
		lookup := envLookup(map[string]string{"DEVIN_API_KEY": "apk_agent"})
		creds, err := LoadCredentials(lookup, CredentialFiles{KeyFile: keyFile, CLIFile: cliFile})
		if err != nil {
			t.Fatalf("LoadCredentials: %v", err)
		}
		if creds.Source != SourceKeyFile {
			t.Errorf("source = %q, want key-file", creds.Source)
		}
		if creds.APIKey != SessionTokenPrefix+"key-file-raw" {
			t.Errorf("api key = %q", creds.APIKey)
		}
		if creds.BaseURL != "https://key-file.example" {
			t.Errorf("base url = %q", creds.BaseURL)
		}
	})

	t.Run("agent keys in both files are skipped for the CLI file", func(t *testing.T) {
		agentKeyFile := filepath.Join(dir, "agent.json")
		writeFile(t, agentKeyFile, `{"apiKey":"pat_agent"}`)
		lookup := envLookup(nil)
		creds, err := LoadCredentials(lookup, CredentialFiles{KeyFile: agentKeyFile, CLIFile: cliFile})
		if err != nil {
			t.Fatalf("LoadCredentials: %v", err)
		}
		if creds.Source != SourceCLI {
			t.Errorf("source = %q, want devin-cli", creds.Source)
		}
		if creds.APIKey != SessionTokenPrefix+"cli-raw" {
			t.Errorf("api key = %q", creds.APIKey)
		}
		if creds.BaseURL != "https://cli.example" {
			t.Errorf("base url = %q", creds.BaseURL)
		}
	})

	t.Run("no model-api key is an error", func(t *testing.T) {
		lookup := envLookup(map[string]string{"DEVIN_API_KEY": "apk_agent"})
		_, err := LoadCredentials(lookup, CredentialFiles{
			KeyFile: filepath.Join(dir, "missing.json"),
			CLIFile: filepath.Join(dir, "missing.toml"),
		})
		if !errors.Is(err, ErrNoCredentials) {
			t.Fatalf("err = %v, want ErrNoCredentials", err)
		}
	})

	t.Run("whitespace in a key is rejected", func(t *testing.T) {
		lookup := envLookup(map[string]string{"DEVIN_API_KEY": "raw key"})
		_, err := LoadCredentials(lookup, CredentialFiles{})
		if err == nil || !strings.Contains(err.Error(), "whitespace") {
			t.Fatalf("err = %v, want whitespace rejection", err)
		}
	})

	t.Run("an already prefixed key is not prefixed again", func(t *testing.T) {
		lookup := envLookup(map[string]string{"DEVIN_API_KEY": SessionTokenPrefix + "raw"})
		creds, err := LoadCredentials(lookup, CredentialFiles{})
		if err != nil {
			t.Fatalf("LoadCredentials: %v", err)
		}
		if creds.APIKey != SessionTokenPrefix+"raw" {
			t.Errorf("api key = %q, want unchanged", creds.APIKey)
		}
	})
}

func envLookup(values map[string]string) LookupEnv {
	return func(key string) string { return values[key] }
}

func TestJWTSingleFlight(t *testing.T) {
	var calls atomic.Int32
	clock := &fakeClock{now: time.Unix(1_700_000_000, 0)}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path != authPath {
			t.Errorf("path = %q, want %q", r.URL.Path, authPath)
		}
		time.Sleep(20 * time.Millisecond)
		writeProto(t, w, &devinpb.GetUserJwtResponse{
			UserJwt: makeJWT(t, clock.Now().Add(time.Hour)),
		})
	}))
	defer server.Close()

	source := newTestTokenSource(t, server, clock, "test-key", time.Minute)
	const waiters = 8
	results := make(chan Auth, waiters)
	errors := make(chan error, waiters)
	var group sync.WaitGroup
	for range waiters {
		group.Add(1)
		go func() {
			defer group.Done()
			auth, err := source.Token(context.Background())
			if err != nil {
				errors <- err
				return
			}
			results <- auth
		}()
	}
	group.Wait()
	close(results)
	close(errors)

	for err := range errors {
		t.Fatalf("Token: %v", err)
	}
	if got := calls.Load(); got != 1 {
		t.Errorf("GetUserJwt calls = %d, want 1 (single-flight)", got)
	}
	var first string
	for auth := range results {
		if first == "" {
			first = auth.UserJWT
		}
		if auth.UserJWT != first {
			t.Error("waiters received different JWTs")
		}
	}
}

func TestJWTSingleFlightSharesError(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		time.Sleep(20 * time.Millisecond)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	source := newTestTokenSource(t, server, &fakeClock{now: fakeNow}, "test-key", time.Minute)
	const waiters = 6
	errs := make(chan error, waiters)
	var group sync.WaitGroup
	for range waiters {
		group.Add(1)
		go func() {
			defer group.Done()
			_, err := source.Token(context.Background())
			errs <- err
		}()
	}
	group.Wait()
	close(errs)

	for err := range errs {
		if kind := connector.KindOf(err); kind != connector.KindUpstream {
			t.Errorf("kind = %q, want Upstream (err %v)", kind, err)
		}
	}
	if got := calls.Load(); got != 1 {
		t.Errorf("GetUserJwt calls = %d, want 1 (shared failure)", got)
	}
}

func TestJWTRefreshMargin(t *testing.T) {
	var calls atomic.Int32
	clock := &fakeClock{now: time.Unix(1_700_000_000, 0)}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		writeProto(t, w, &devinpb.GetUserJwtResponse{
			UserJwt: makeJWT(t, clock.Now().Add(2*time.Minute)),
		})
	}))
	defer server.Close()

	source := newTestTokenSource(t, server, clock, "test-key", time.Minute)
	if _, err := source.Token(context.Background()); err != nil {
		t.Fatalf("first Token: %v", err)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("calls = %d, want 1", got)
	}

	clock.Advance(30 * time.Second)
	if _, err := source.Token(context.Background()); err != nil {
		t.Fatalf("second Token: %v", err)
	}
	if got := calls.Load(); got != 1 {
		t.Errorf("calls after 30s = %d, want 1 (still outside margin)", got)
	}

	clock.Advance(31 * time.Second)
	if _, err := source.Token(context.Background()); err != nil {
		t.Fatalf("third Token: %v", err)
	}
	if got := calls.Load(); got != 2 {
		t.Errorf("calls after 61s = %d, want 2 (inside 60s margin)", got)
	}
}

// TestDoublePrefixGuard sends a raw key through the loader and asserts the
// upstream sees exactly one prefix. A second prefix is the documented 401.
func TestDoublePrefixGuard(t *testing.T) {
	var sawKey string
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read request: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		var request devinpb.GetUserJwtRequest
		if err := proto.Unmarshal(body, &request); err != nil {
			t.Errorf("decode request: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		sawKey = request.GetMetadata().GetApiKey()
		if strings.HasPrefix(sawKey, SessionTokenPrefix+SessionTokenPrefix) {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		writeProto(t, w, &devinpb.GetUserJwtResponse{
			UserJwt: makeJWT(t, time.Now().Add(time.Hour)),
		})
	}))
	defer server.Close()

	creds, err := LoadCredentials(envLookup(map[string]string{"DEVIN_API_KEY": "raw-key"}), CredentialFiles{})
	if err != nil {
		t.Fatalf("LoadCredentials: %v", err)
	}
	creds.BaseURL = server.URL

	source, err := NewTokenSource(TokenSourceOptions{
		Credentials: creds,
		Transport:   server.Client(),
		Clock:       &fakeClock{now: time.Now()},
	})
	if err != nil {
		t.Fatalf("NewTokenSource: %v", err)
	}
	if _, err := source.Token(context.Background()); err != nil {
		t.Fatalf("Token: %v", err)
	}
	if calls.Load() != 1 {
		t.Errorf("calls = %d, want 1", calls.Load())
	}
	if sawKey != SessionTokenPrefix+"raw-key" {
		t.Errorf("upstream key = %q, want exactly one prefix", sawKey)
	}
}

func TestGetUserJwtGzipFallback(t *testing.T) {
	clock := &fakeClock{now: time.Unix(1_700_000_000, 0)}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		payload, err := proto.Marshal(&devinpb.GetUserJwtResponse{
			UserJwt: makeJWT(t, clock.Now().Add(time.Hour)),
		})
		if err != nil {
			t.Errorf("marshal: %v", err)
			return
		}
		w.Header().Set("Content-Type", "application/proto")
		_, _ = w.Write(gzipBytes(t, payload))
	}))
	defer server.Close()

	source := newTestTokenSource(t, server, clock, "test-key", time.Minute)
	auth, err := source.Token(context.Background())
	if err != nil {
		t.Fatalf("Token: %v", err)
	}
	if auth.UserJWT == "" {
		t.Error("user JWT is empty")
	}
}

func TestGetUserJwtCustomBaseURL(t *testing.T) {
	clock := &fakeClock{now: time.Unix(1_700_000_000, 0)}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeProto(t, w, &devinpb.GetUserJwtResponse{
			UserJwt:            makeJWT(t, clock.Now().Add(time.Hour)),
			CustomApiServerUrl: "https://custom.example/",
		})
	}))
	defer server.Close()

	source := newTestTokenSource(t, server, clock, "test-key", time.Minute)
	auth, err := source.Token(context.Background())
	if err != nil {
		t.Fatalf("Token: %v", err)
	}
	if auth.BaseURL != "https://custom.example" {
		t.Errorf("base url = %q, want trimmed custom url", auth.BaseURL)
	}
}

func newTestTokenSource(t *testing.T, server *httptest.Server, clock Clock, key string, margin time.Duration) TokenSource {
	t.Helper()
	source, err := NewTokenSource(TokenSourceOptions{
		Credentials: Credentials{
			APIKey:  NormalizeSessionToken(key),
			BaseURL: server.URL,
			Source:  SourceEnv,
			Kind:    KeyKindModelAPI,
		},
		Transport:     server.Client(),
		Clock:         clock,
		RefreshMargin: margin,
	})
	if err != nil {
		t.Fatalf("NewTokenSource: %v", err)
	}
	return source
}

func writeProto(t *testing.T, w http.ResponseWriter, message proto.Message) {
	t.Helper()
	payload, err := proto.Marshal(message)
	if err != nil {
		t.Fatalf("marshal proto: %v", err)
	}
	w.Header().Set("Content-Type", "application/proto")
	_, _ = w.Write(payload)
}
