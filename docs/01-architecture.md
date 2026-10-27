# kyo-router - architecture

Layers: functional core (catalog, routing decisions) wrapped by an imperative
shell (HTTP in, HTTP out). One binary. No framework.

## Call graph (happy path, streaming request)

```ts
HTTP handlers (internal/api)
  -> api.parse (OpenAI wire -> domain ChatRequest)      [trust boundary]
    -> router.Execute (domain)
      -> router.resolveRoute (catalog lookup + rule match)
        -> router.tryChain (fallback loop, pre-first-byte only)
          -> connector.Chat (Connector interface)
            -> devin.Connector
              -> devin.auth.Token (TokenSource seam)
                -> httpx.Do (Transport seam)
                  -> server.codeium.com (Connect-RPC frames)
          -> opencode.Connector
            -> httpx.Do (Transport seam)
              -> opencode.ai/zen/go/v1 (OpenAI SSE or Responses SSE)
          -> connector.Error + Kind tag (each connector classifies its own)
      -> api.translate (domain StreamEvent -> OpenAI SSE)
        -> Client
```

Tests (same graph, only R swapped, no module mocks):

```ts
HTTP handlers (httptest)
  -> api.parse
    -> router.Execute
      -> router.tryChain
        -> fake connector (in-memory Connector implementation)
          -> fake Transport (httptest.Server)
```

## Module map

| Package | Role | Key types |
|---------|------|-----------|
| cmd/kyo-router | wiring only: config -> catalog -> connectors -> server | main |
| internal/config | load TOML + env, validate once at start | Config, ConnectorCfg, RouteCfg |
| internal/catalog | model catalog: build, resolve, protocol per model | Model, Protocol, Catalog |
| internal/api | OpenAI-compatible surface, SSE writer, error JSON | Server, handlers |
| internal/router | route match, fallback chain, retry classes | Router |
| internal/connector | Connector interface + error kinds | Connector, Kind, Error |
| internal/devin | Devin connector: JWT, Connect frames, translation | Connector, TokenSource |
| internal/opencode | OpenCode Go connector: split endpoints | Connector |
| internal/httpx | shared transport seam, status->Kind, SSE reader | Transport, UpstreamError, SSEScanner |
| internal/models | canonical domain types | ChatRequest, StreamEvent |

Dependency rule: api -> router -> connector -> (devin, opencode) -> httpx.
Nothing imports api. Connectors never import router.

## Pair note - HTTP layer

Options
  A. Framework (chi/echo/gin) - faster middleware setup - extra dep, second idiom
  B. stdlib net/http (Go 1.22 pattern mux) - zero deps, one idiom - manual middleware
Pick: B - single personal binary; stdlib mux covers method+path patterns.
Theory: standard library first (new page) - platform feature before dependency.
Rejected: A because a second HTTP idiom adds reader cost for no feature need.

## Pair note - canonical model

Options
  A. map[string]any passthrough - zero translation types - broad-dictionary
     anti-slop violation; provider quirks leak into routing
  B. typed canonical ChatRequest/StreamEvent + per-connector translation
     - typed invariants, one translation site per protocol - more code up front
Pick: B - data dominates (P5); each protocol translates in exactly one module.
Theory: functional core, imperative shell (existing page) - parse at the
boundary, pure decisions inside.
Rejected: A because unknown-shape data spreads protocol drift everywhere.

## Error kinds (E channel, scoped per layer)

| Kind | Source | Router action | API status |
|------|--------|---------------|------------|
| Auth | 401/403 upstream, refresh failure | refresh once, then next fallback | 502 |
| RateLimit | 429 | honor Retry-After, retry once, then next fallback | 429 |
| Upstream | 5xx | next fallback immediately | 502 |
| Network | timeout, refused, EOF | 2 retries with backoff, then next fallback | 502 |
| BadRequest | our validation | no retry | 400 |
| Unsupported | feature or model mismatch | no retry | 400 |
| UpstreamBad | upstream 4xx (not auth/rate) | no retry, surface | pass status |
| Internal | local failure (uuid gen, encode) | no retry, no fallback | 500 |

Classify by Kind tag, never by message text. Each connector translates its
provider errors into Kind before they cross the connector boundary
(anti-corruption layer, existing page).

## Cardinality

- Non-stream chat: one value (Effect).
- Streaming: many events (Stream). Devin upstream is a frame stream;
  OpenCode chat is SSE; Responses is SSE. Every connector emits canonical
  StreamEvents; the api layer writes the OpenAI SSE shape.

## Requirements (R) and seams

| Node needs | Provided by | Test swap |
|------------|-------------|-----------|
| Config | internal/config | fixture TOML |
| Catalog | internal/catalog | fixture catalog |
| HTTP transport | httpx.Client | httptest.Server |
| Devin token | TokenSource interface | fake token source |
| Clock (JWT expiry) | Clock interface | fake clock |
| Logger | slog | test logger |

## Resource scope

- One shared http.Client per connector; MaxIdleConnsPerHost tuned.
- Response bodies closed by the stream owner, even on error.
- Client disconnect cancels the upstream request via context.
- SSE writer flushes per event; no buffering of the whole stream.

## Request lifecycle

1. Parse and validate the OpenAI request once at the boundary.
2. Resolve the model in the catalog; unknown model -> 404 model_not_found.
3. Match routing rules; build the fallback chain.
4. Try the primary connector; classify any error by Kind.
5. Retry or fall back per the Kind table, before the first stream byte only.
6. Translate upstream events into domain StreamEvents.
7. Encode StreamEvents as OpenAI SSE; flush per event.
8. Log one line per request: model, connector, status, duration, stream flag.
   Keys and tokens never logged (log-redaction, existing page).
