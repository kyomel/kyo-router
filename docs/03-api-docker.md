# kyo-router - API surface, config, Docker

## API surface

| Method | Path | Status | Notes |
|--------|------|--------|-------|
| GET | /v1/models | live | catalog list, OpenAI shape |
| POST | /v1/chat/completions | live | stream + non-stream; responses-protocol models translated |
| POST | /v1/responses | M3 | passthrough or translate |
| GET | /healthz | live | returns ok |

Chat extra field: `reasoning_effort` passes verbatim to DeepSeek
(low|medium|high|xhigh|max); Devin ignores it (effort is the uid suffix).
Reasoning streams as `delta.reasoning_content`, folds into
`message.reasoning_content` non-stream.

- No auth on the router by default. It binds inside the container and is
  published to 127.0.0.1 on the host only.
- Optional `server.require_api_key_env`: when set, the router checks the
  client Bearer token against that env value.
- Unknown model -> 404 with `model_not_found`. Malformed request -> 400.
  Upstream failures -> mapped per the Kind table in 01-architecture.md.

## Pair note - port publish

Options
  A. publish 127.0.0.1:8080:8080 - host-only reach - LAN devices cannot use it
  B. publish 8080:8080 - LAN reachable - other devices could burn quota
Pick: A - personal quota stays personal.
Theory: principle-of-least-privilege (existing page).
Rejected: B - no need for LAN exposure today.

## Config shape

One TOML file, path from `-config` flag or `KYO_ROUTER_CONFIG`. Secrets come
from env or mounted files, never from the config file.

```toml
[server]
bind = ":8080"
# require_api_key_env = "KYO_ROUTER_API_KEY"   # optional

[connectors.opencode]
base_url = "https://opencode.ai/zen/go/v1"
api_key_env = "OPENCODE_GO_API_KEY"
session_header_mode = "request"   # request | fixed

[connectors.devin]
credentials_path = "/run/secrets/devin-credentials.toml"
api_key_env = "DEVIN_API_KEY"     # set = wins over the file; empty = use the file
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
aliases = ["devin/haiku"]          # optional, provider-prefixed

[[model]]
id = "opencode/grok-4.7"
connector = "opencode"
upstream = "grok-4.7"
protocol = "responses"
aliases = ["opencode/grok"]

[[model]]
id = "deepseek/deepseek-v4-pro"
connector = "deepseek"
upstream = "deepseek-v4-pro"
protocol = "chat"
aliases = ["deepseek/pro"]

[[route]]
match = "devin/*"
fallbacks = ["opencode/glm-5.3"]

[[route]]
match = "opencode/*"
fallbacks = []
```

Env vars:

| Var | Purpose |
|-----|---------|
| OPENCODE_GO_API_KEY | OpenCode Go key (oc_sk_...) |
| DEEPSEEK_API_KEY | DeepSeek key (sk_...) from platform.deepseek.com |
| DEVIN_API_KEY | Devin model-API session token; wins over the credentials file |
| KYO_ROUTER_CONFIG | config file path override |
| KYO_ROUTER_API_KEY | optional client auth value |

Precedence: flag > env > config file default. Validate once at start; fail
loud on missing keys for enabled connectors.

## Dockerfile (draft, M0 finalizes)

```dockerfile
FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o /kyo-router ./cmd/kyo-router

FROM gcr.io/distroless/static:nonroot
COPY --from=build /kyo-router /kyo-router
USER nonroot
ENTRYPOINT ["/kyo-router"]
```

- No secrets in any layer. Keys arrive via env or a mounted file at runtime.
- distroless/static ships CA certs, so HTTPS to both providers works.
- Go 1.26 toolchain: current connect-go (v1.21.1, and v2.0.0) requires it
  (pkg.go.dev/connectrpc.com/connect).

### Pair note - image base

Options
  A. distroless/static nonroot - smallest surface, CA certs included - no shell
  B. alpine + ca-certificates - shell for debugging - bigger surface
Pick: A - debug via logs and local go run, not by shelling into prod.
Theory: principle-of-least-privilege (existing page).
Rejected: B - shell widens the surface of an always-on personal service.

## docker-compose.yml (draft, for OrbStack)

```yaml
services:
  kyo-router:
    build: .
    image: kyo-router:dev
    restart: unless-stopped
    ports:
      - "127.0.0.1:8080:8080"
    environment:
      - OPENCODE_GO_API_KEY=${OPENCODE_GO_API_KEY:?set in .env}
    volumes:
      - ${HOME}/.local/share/devin/credentials.toml:/run/secrets/devin-credentials.toml:ro
```

- `.env` next to the compose file holds `OPENCODE_GO_API_KEY`; `.env` is
  gitignored.
- No healthcheck block: distroless has no shell client for it. `restart:
  unless-stopped` covers crashes; `/healthz` is for humans and scripts.

## OrbStack notes

- Compose is fully compatible; OrbStack ships the compose and buildx CLI
  tools and keeps them current (docs.orbstack.dev/docker/).
- Bind mounts of macOS paths work seamlessly, VirtioFS-backed; volumes are
  faster than bind mounts for hot paths (docs.orbstack.dev/docker/file-sharing).
- Port publish to 127.0.0.1 is honored: include the address in the `-p` flag.
  Forwarded ports otherwise reach the LAN unless `docker.expose_ports_to_lan`
  is off - the 127.0.0.1 publish in the compose above is the belt, this
  setting is the braces (docs.orbstack.dev/docker/network).
- Containers also get `container-name.orb.local` domain names; a possible
  alternative to port publishing if the surface ever grows.
- M0 smoke run confirms all of the above on this machine.

## Local run steps

1. `docker context use orbstack`
2. Copy `.env-example` to `.env`, then fill in `OPENCODE_GO_API_KEY` and, when
   you use a fresh Devin key, `DEVIN_API_KEY`
3. `docker compose up -d --build`
4. `curl http://127.0.0.1:8080/healthz`
5. `curl http://127.0.0.1:8080/v1/models`
6. Stream check:
   `curl -N http://127.0.0.1:8080/v1/chat/completions -H 'Content-Type: application/json' -d '{"model":"opencode/glm-5.3","messages":[{"role":"user","content":"say hi"}],"stream":true}'`
7. Devin check (M2): same call with `"model":"devin/claude-haiku-5-5-low"`.
8. `docker compose down` when done. No persistent data exists.

Dev without Docker: `go run ./cmd/kyo-router -config config.toml` with the
env var set; the credentials path points at
`~/.local/share/devin/credentials.toml` directly.

## Secret hygiene checks

- `docker run --rm kyo-router:dev env | grep -E "oc_sk|devin-session"` prints
  nothing.
- `git status` never lists `.env` or any credentials file.
- Logs redact keys and tokens (log-redaction, existing page).

## Web research notes (2026-10-26, exa)

- OrbStack: compose fully compatible, CLI tools bundled
  (docs.orbstack.dev/docker/); bind mounts seamless
  (docs.orbstack.dev/docker/file-sharing); per-address port publish and the
  `docker.expose_ports_to_lan` setting (docs.orbstack.dev/docker/network);
  current release v2.2.3 with Docker 29.x and Compose 2.30+
  (docs.orbstack.dev/release-notes).
- Distroless multi-stage images are the norm in Go LLM gateways
  (github.com/showmikb/gateway-llm, github.com/fossabot/butter), so the
  distroless pick above follows ecosystem practice, not just theory.
