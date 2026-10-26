# kyo-router

OpenAI-compatible gateway in front of three subscription providers: Devin, OpenCode Go, and DeepSeek. One Go binary. No framework.

## What it does

- Serves `POST /v1/chat/completions` (stream and non-stream) and `GET /v1/models`.
- Routes each request by model id to one of three connectors.
- Falls back to configured models on upstream failure (before the first stream byte only).
- Translates between OpenAI chat format, OpenCode Responses format, and Devin's Connect-RPC protocol.
- Streams reasoning as `delta.reasoning_content` (DeepSeek-style).

## Quick start (Docker, OrbStack)

1. Copy `.env-example` to `.env`.
2. Fill `OPENCODE_GO_API_KEY` (required). Fill `DEEPSEEK_API_KEY` and/or `DEVIN_API_KEY` as needed.
3. Run `docker context use orbstack`.
4. Run `docker compose up -d --build`.
5. Check health: `curl http://127.0.0.1:8080/healthz`.
6. List models: `curl http://127.0.0.1:8080/v1/models`.
7. Stream a chat:

```
curl -N http://127.0.0.1:8080/v1/chat/completions \
  -H 'Content-Type: application/json' \
  -d '{"model":"opencode/glm-5.3","messages":[{"role":"user","content":"say hi"}],"stream":true}'
```

Stop with `docker compose down`. No data persists.

## Quick start (local, no Docker)

1. Set `OPENCODE_GO_API_KEY` (and any other keys) in the shell.
2. Run `go run ./cmd/kyo-router -config config.toml`.
3. Use the same curl commands above.

The Devin credentials path points at `~/.local/share/devin/credentials.toml` by default.

## Configuration

Secrets come from env or mounted files. Never from `config.toml`.

`config.toml` shape:

```toml
[server]
bind = ":8080"
# require_api_key_env = "KYO_ROUTER_API_KEY"   # optional client Bearer auth

[connectors.opencode]
base_url = "https://opencode.ai/zen/go/v1"
api_key_env = "OPENCODE_GO_API_KEY"
session_header_mode = "request"   # request | fixed

[connectors.devin]
credentials_path = "/run/secrets/devin-credentials.toml"
api_key_env = "DEVIN_API_KEY"
base_url = ""                     # empty = use api_server_url from the file
jwt_refresh_margin = "60s"
ide_version = "3.2.23"
extension_version = "1.48.2"

[connectors.deepseek]
base_url = "https://api.deepseek.com"
api_key_env = "DEEPSEEK_API_KEY"

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
aliases = ["opencode/grok"]

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

[[model]]
id = "deepseek/deepseek-v4-pro"
connector = "deepseek"
upstream = "deepseek-v4-pro"
protocol = "chat"
features = ["thinking"]
aliases = ["deepseek/pro"]

[[route]]
match = "devin/*"
fallbacks = ["opencode/glm-5.3"]

[[route]]
match = "opencode/*"
fallbacks = []

[[route]]
match = "deepseek/*"
fallbacks = []
```

Env vars:

| Var | Purpose |
|-----|---------|
| `OPENCODE_GO_API_KEY` | OpenCode Go key (`oc_sk_...`) |
| `DEEPSEEK_API_KEY` | DeepSeek key (`sk_...`) |
| `DEVIN_API_KEY` | Devin model-API session token (wins over the credentials file) |
| `KYO_ROUTER_CONFIG` | config file path override |
| `KYO_ROUTER_API_KEY` | optional client auth value |

Precedence: flag > env > config file default. Validation runs once at start. Missing keys for enabled connectors fail loudly.

## API surface

| Method | Path | Status | Notes |
|--------|------|--------|-------|
| `GET` | `/healthz` | live | returns `ok` |
| `GET` | `/v1/models` | live | catalog list, OpenAI shape (aliases included) |
| `POST` | `/v1/chat/completions` | live | stream + non-stream; responses-protocol models translated |
| `POST` | `/v1/responses` | planned | passthrough or translate |

Chat extra field: `reasoning_effort` passes verbatim to DeepSeek (`low|medium|high|xhigh|max`). Devin ignores it (effort is the uid suffix). Reasoning streams as `delta.reasoning_content` and folds into `message.reasoning_content` for non-stream replies.

- No auth on the router by default. It binds inside the container and publishes to `127.0.0.1` on the host only.
- Optional `server.require_api_key_env`: when set, the router checks the client Bearer token against that env value.
- Unknown model returns `404` with `model_not_found`. Malformed request returns `400`. Upstream failures map per the Kind table in `docs/01-architecture.md`.

Error JSON shape: `{"error": {"message", "type", "code", "param"}}`. Classify by `type` (Kind tag), never by message text.

## Model ids

Public ids use a provider prefix:

- `devin/<model_uid>` (uid verbatim, effort suffix included)
- `opencode/<slug>`
- `deepseek/<slug>`

Aliases (provider-prefixed short names) also appear in `/v1/models`: `devin/haiku`, `opencode/glm`, `opencode/grok`, `deepseek/flash`, `deepseek/pro`.

Route fallbacks keep canonical ids. Aliases are a client surface, not a routing shortcut.

## Development

```
go vet ./...
go test ./...
```

No module-level mocks. Tests swap real seams (Transport, TokenSource, Clock, Connector).

## Docs

Detailed design lives in `docs/`:

- `00-overview.md` - overview
- `01-architecture.md` - layers, call graph, modules, error kinds, seams
- `02-connectors.md` - Devin and OpenCode Go auth, catalog, protocol split, error map, streaming matrix
- `03-api-docker.md` - surface, config, Dockerfile, compose, run steps
- `04-milestones.md` - phases, checks, test plan, risks
- `05-model-list-proposed.md` - verified model catalog + proposed config list

## License

Not specified.