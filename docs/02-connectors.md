# kyo-router - connectors

Three outbound connectors. Facts below come from the vault knowledge pages
(devin-subscription-model-api.md, last updated 2026-10-13;
droid-opencode-go-byok.md, last updated 2026-09-29), live probes, and the
DeepSeek API. Items marked [VERIFY] need a live probe during the build.

## Devin connector

### Auth chain

```ts
config.devin.credentials_path (mounted file)
  -> parse credentials.toml
    -> windsurf_api_key (already carries the devin-session-token$ prefix;
       never prefix it again - double prefix = 401)
    -> api_server_url (per account; default server.codeium.com)
  -> POST {api_server_url}/exa.auth_pb.AuthService/GetUserJwt
       content-type: application/proto, unary
       request metadata: apiKey = session token
    -> userJwt
  -> cache JWT until exp minus 60s
  -> refetch on expiry, single-flight (dedupe concurrent refetches)
```

Theory: materialized-computation-once (existing page) - one refetch, all
waiters share it.

### Model call

- `POST {api_server_url}/exa.api_server_pb.ApiServerService/GetChatMessage`
- `content-type: application/connect+proto`, `connect-protocol-version: 1`
- Connect streaming frames: 5-byte header (1 flag byte + uint32 big-endian
  length). Flag 0x01 = gzip payload. Flag 0x02 = end-stream JSON trailer.
- Errors surface as HTTP status or in the end-stream trailer `error` field.

Request essentials (verified working 2026-10-13):

| Field | Value |
|-------|-------|
| metadata.apiKey | session token from credentials.toml |
| metadata.userJwt | from GetUserJwt |
| ideName / extensionName | "windsurf" |
| ideVersion | "3.2.23" (older -> failed_precondition) |
| extensionVersion | "1.48.2" |
| chatModelUid | exact model_uid, effort baked into the suffix |
| requestType | CASCADE (5) |
| cascadeId / executionId | fresh UUIDs per call |
| prompt | flattened system prompt |
| history | chatMessagePrompts, source USER / SYSTEM (assistant) / TOOL |

Response stream fields: `deltaText`, `deltaThinking` / `deltaSignature`,
`deltaToolCalls` (id / name / argumentsJson), `stopReason`, `usage`.

### Catalog

- `POST .../ApiServerService/GetCliModelConfigs` (application/proto, unary,
  same metadata) -> `client_model_configs[]`: `model_uid`, `label`,
  `max_tokens` (context window), `model_info.max_output_tokens`,
  `model_family_metadata.model_family_label`, `supports_images`, `disabled`,
  `smart_friend_model_uid`.
- Verified 2026-10-13: ~260 entries, zero fusion rows, no `adaptive` entry,
  no cost_summary. `devin models list --format json` stays the richer fallback
  when the CLI is present.
- Excluded in code (`isExcludedUID`): the `adaptive` and `auto` picker
  entries and every `fusion-*` uid; a row whose family label names one of
  those is dropped the same way. `auto` is absent from the live list
  (verified 2026-10-26, 0 of 816 uids) - guarded ahead of it, per request.
- Thinking: effort lives in the uid suffix (`-low`, `-medium`, `-high`).
  Expose one model per family plus a thinking-level map, like
  devin-pi-prime does. Claude has no `off`; default falls to medium.
- The OpenAI-compatible AI Gateway (`{api_server_url}/api/v1`) returns 403
  permission_denied on this account. Do not build on it.

### Pair note - Devin transport

Options
  A. connect-go client + generated stubs from a minimal hand-written .proto
     (buf generate) - typed; gzip, framing, trailers handled by the library
     - need correct field numbers once
  B. hand-rolled HTTP client with a raw frame codec - zero codegen
     - reimplement framing, gzip, end-stream trailers; drift risk
  C. dynamic protobuf (descriptorpb + dynamicpb) from a descriptor set dumped
     out of the jeopi-catalog npm package - no hand proto - reflection-shaped
     API, harder to read
Pick: A - connect-go (connectrpc.com/connect/v2, v2.0.0, released 2026-10-07)
owns the wire format; the proto covers ~3 messages; field numbers are dumped
once from the TS descriptors in jeopi-catalog.
Theory: adapter-pattern (existing page) - wrap the foreign protocol behind
one interface.
Rejected: B (protocol drift cost, gzip correctness), C (reflection-shaped).
Risk: field numbers drift when Cognition changes protos. Check: a contract
test decodes a recorded frame fixture; refresh the fixture on upstream change.

### Pair note - thinking deltas

Options
  A. drop deltaThinking - simplest - loses reasoning that local clients show
  B. map to delta.reasoning_content (DeepSeek-style field) - clients that
     parse it keep reasoning - non-standard field
  C. inline <think> tags into deltaText - no schema change - pollutes content
Pick: B - matches what local clients already parse; per-model catalog flag.
Theory: none - wire-compat convention, no CS term fits.
Rejected: A (loses data), C (content pollution).

## OpenCode Go connector

### Endpoint and auth

- Base: `https://opencode.ai/zen/go/v1` (the Go subscription subset; NOT the
  full Zen catalog at `/zen/v1` - Zen-only ids fail with "Insufficient account
  funds", FreeTierError, or "Model is unavailable").
- Auth: `Authorization: Bearer oc_sk_...` from env `OPENCODE_GO_API_KEY`.
- `x-opencode-session` is required on `/chat/completions` (400
  MissingSessionID without it). `/models` does not require it. The
  User-Agent value is not enforced.

### Protocol split (verified 2026-09-29)

| Protocol | Models |
|----------|--------|
| chat/completions | deepseek-v4.1-flash, kimi-k2.7-code, glm-5.3-flash, qwen3.8-max, deepseek-v4-pro, glm-5.3, longcat-2.5-preview-free, space-bunny-free, mimo-v2.6-flash |
| responses | muse-spark-1.3-contributor, grok-4.7, grok-4.6, gpt-6-luna |

- `grok-4.7` and `muse-spark-1.3-contributor` return ModelProtocolUnsupported
  on `/chat/completions` and "not supported for format anthropic" on
  `/messages`; they only work on the Responses API.
- Verified 2026-10-26: `GET /models` on the Go base returns 200 with 38 ids;
  entries carry only id/object/created/owned_by (no protocol field), so the
  chat/responses split stays config-driven. `longcat-2.5-preview-free` and
  `space-bunny-free` are gone from the live list. Full verified list:
  docs/05-model-list-proposed.md.
- `reasoning_effort` forwards verbatim on chat models; the Responses path maps
  it to `reasoning.effort` + `summary: auto`. Verified live 2026-10-28: every
  chat id accepts `low`, `medium`, and `high` (33 of 34 return 200;
  `mimo-v2.5-pro` answers "Unsupported model" upstream for any request); all
  four responses ids accept `high` through the router.

### Pair note - session header

Options
  A. per-request UUID v4 - no cross-request state - a session-keyed upstream
     limit would never trigger
  B. fixed value per router process - stable, matches droid's stable-hex
     pattern - one bucket for all traffic
  C. config-selectable mode (request or fixed) - covers both - one more knob
Pick: C with default A - per-request UUID by default; `session_header_mode`
in config flips to a fixed value if upstream ever keys limits on it.
Theory: none - provider gate behavior, no CS term fits.
Rejected: B as default (single bucket risk), A as only option (no escape).

## DeepSeek connector

### Endpoint and auth

- Base: `https://api.deepseek.com` (OpenAI-compatible; `/v1` also accepted).
- Auth: `Authorization: Bearer sk-...` from env `DEEPSEEK_API_KEY`
  (platform.deepseek.com). No extra headers.
- Protocol: `chat/completions` only. The account also advertises an
  anthropic_messages surface; bridging it is an anti-goal (00-overview).

### Catalog (verified live 2026-10-26)

`GET /models` -> 200 with 2 ids. The response carries fields beyond the
OpenAI shape: `name`, `context_window`, `max_output_tokens`,
`input_modalities`, `effort.supported_levels` + `default_level`.

| id | name | context | max output | input | effort levels |
|----|------|---------|------------|-------|---------------|
| deepseek-flash | DeepSeek-V4.1-Flash | 1,048,576 | 393,216 | text+image | low, high (default), max |
| deepseek-v4-pro | DeepSeek-V4-Pro | 1,048,576 | 393,216 | text | low, high (default), max |

- Public ids keep the slug verbatim: `deepseek/deepseek-flash`,
  `deepseek/deepseek-v4-pro`; aliases `deepseek/flash`, `deepseek/pro`.
- Effort is a request-level parameter upstream, not baked into the id.
  The canonical `reasoning_effort` field passes verbatim
  (`low|medium|high|xhigh|max`); invalid values 400 upstream and surface
  as UpstreamBad.
- Reasoning streams as `delta.reasoning_content`; the connector emits it
  as a separate StreamEvent and the API relays it as OpenAI-style
  `delta.reasoning_content` (pick B in the thinking-delta decision).
  Non-stream folds it into `message.reasoning_content`.
- `stream_options` goes out only with `stream: true` - DeepSeek 400s on
  stream_options without it (found by live E2E 2026-10-26).

## Catalog unification

- Public ids: `devin/<model_uid>` (uid verbatim, effort suffix included),
  `opencode/<slug>` and `deepseek/<slug>` (slug verbatim).
- Sources: Devin GetCliModelConfigs (+ CLI fallback); OpenCode pinned list in
  config (+ /models cross-check when available); DeepSeek pinned list in
  config (+ /models cross-check).
- Entry fields: id, connector, upstream, protocol (chat / responses / devin),
  features (images, thinking levels, stream passthrough or synthesized),
  context tokens, max output tokens.
- Built once at start; `/v1/models` serves it; catalog is the single source
  of truth for routing (single-source-of-truth, existing page).

### Pair note - catalog source

Options
  A. runtime discovery every start (Devin API + OpenCode /models)
  B. static config list only
  C. discovery with config pin and override
Pick: C - discovery keeps the catalog fresh; pins guarantee the known-good
set survives API drift; discovery failure degrades to the pinned list with a
warning, never a hard failure.
Theory: single-source-of-truth (existing page).
Rejected: A (boot fails when an API hiccups), B (catalog rots silently).

### Model aliases

- Each `[[model]]` entry may carry `aliases`: extra public ids for the same
  model, each inside its provider namespace (`devin/`, `opencode/`).
- `catalog.Resolve` checks canonical ids first, then aliases; a miss is still
  `ErrModelNotFound`. The resolved model keeps its canonical id.
- Aliases appear in `/v1/models` so clients can pick them.
- Route fallbacks keep canonical ids; aliases are a client surface, not a
  routing shortcut.
- Config rejects: empty aliases, an alias equal to its own model id, an alias
  outside the model's provider namespace, a duplicate alias, and an alias
  that shadows another model's id.

### Pair note - alias shape

Options
  A. `aliases` field on each `[[model]]` - alias next to its target, one TOML
     shape - model block grows one field
  B. `[[alias]]` table mapping name -> target - one alias section - alias and
     target drift into two tables
  C. derived aliases from discovery metadata - zero config - uncontrolled
     public names, no say over what resolves
Pick: A - the alias is a property of the model and validates at load, next to
the id it shadows.
Theory: single-source-of-truth (existing page) - the catalog stays the one
place that maps a public name to an upstream id.
Rejected: B (one fact in two tables), C (no control over the public surface).

## Error mapping

Connector-internal Kind enum (see 01-architecture.md). Detection rules:

| Upstream signal | Kind | Notes |
|-----------------|------|-------|
| Devin HTTP 401/403, or trailer error after refresh | Auth | refresh once, then fallback |
| Devin failed_precondition (old ideVersion) | UpstreamBad | pin current versions |
| OpenCode 400 MissingSessionID | BadRequest | router bug, not client's |
| OpenCode 401 | Auth | bad or expired oc_sk_ key |
| OpenCode 429 | RateLimit | honor Retry-After |
| OpenCode ModelProtocolUnsupported | Unsupported | catalog protocol wrong |
| OpenCode FreeTierError | Unsupported | model not usable via Go key |
| OpenCode "Model is unavailable" | Unsupported | Zen-only id on Go base |
| OpenCode "Insufficient account funds" | Auth | surface with message |

OpenAI error JSON shape: `{"error": {"message", "type", "code", "param"}}`.
Kind -> type/code/status table lives in internal/api (one place).

## Streaming translation matrix

| Inbound | Upstream | Work |
|---------|----------|------|
| chat/completions | opencode chat | SSE passthrough |
| chat/completions | opencode responses | translate request chat->responses; translate SSE back |
| chat/completions | deepseek | SSE passthrough |
| chat/completions | devin | build GetChatMessage; frames -> SSE (synthesized); thinking -> reasoning_content; tool calls -> delta.tool_calls |
| responses (M3) | opencode responses | passthrough |
| responses (M3) | opencode chat | translate |

Failover happens before the first stream byte only. After that, the stream
fails with an SSE error event; the client retries.

## Web research notes (2026-10-26, exa)

connect-go facts:

- Module `connectrpc.com/connect` (v1 line) is at v1.21.1 and requires Go
  1.26.0; `connectrpc.com/connect/v2` (v2.0.0, 2026-10-07) is the current
  major: generated code takes and returns plain messages, no
  `connect.Request[T]` wrappers. The v1 module stays stable and supported
  indefinitely. Pick v2 for new code; fall back to v1 only on tooling
  friction. Sources: pkg.go.dev/connectrpc.com/connect,
  github.com/connectrpc/connect-go/releases.
- Client shape (v2): `connect.NewClient` + `connecthttp.NewTransport
  (http.DefaultClient, baseURL)`; Connect protocol is the default; gRPC and
  gRPC-Web via transport options. Source:
  connectrpc.com/docs/go/getting-started/.
- Streaming: all three protocols supported client-side; streaming responses
  always carry HTTP 200 and encode errors in trailers or end-of-body - this
  matches the vault facts about Devin's end-stream JSON trailer. Source:
  connectrpc.com/docs/go/streaming/.

Go OpenAI-proxy landscape (patterns worth reusing):

- Passthrough when client and upstream speak the same protocol; convert only
  on protocol mismatch (triproxy: "passes through, no conversion, all fields
  preserved"). Validates the passthrough row in the streaming matrix.
  Source: github.com/tomoncle/triproxy.
- Immediate SSE delivery via `http.ResponseController` (per-write flush)
  instead of blanket `Flusher` casts. Source: github.com/agentuity/llmproxy.
- Failover driven by configurable retry-on status codes plus exponential
  backoff; shared `openaicompat` base for any OpenAI-compatible upstream.
  Source: github.com/fossabot/butter.
- Constant-time bearer-token comparison for optional router auth.
  Source: github.com/nep-0/llm-router.
- Multi-stage builds producing ~20 MB distroless images are the norm in this
  space. Sources: github.com/showmikb/gateway-llm, github.com/fossabot/butter.

## Verification note (2026-10-26)

Probes ran with keys from .env via env vars only; no key value was printed or
stored. Statuses only, per the task.

- OpenCode Go: `GET /models` -> 200, 38 ids. `POST /chat/completions` on
  glm-5.3 (stream=false, x-opencode-session per request) -> 200,
  finish_reason=stop. `POST /responses` on grok-4.7 -> 200 both modes:
  non-stream output[] carries reasoning items (summary[].text) plus
  message items (content[].text); stream emits response.output_text.delta,
  output_item.done (reasoning summary lands there whole; grok does not
  stream reasoning deltas), response.completed with usage. Router serves
  opencode/glm and opencode/grok live through /v1/chat/completions.
- DeepSeek: `GET /models` -> 200, 2 ids (deepseek-flash, deepseek-v4-pro)
  with context_window/max_output_tokens/effort fields. Chat probe: 200
  streaming with reasoning_effort=low; delta.reasoning_content chunks
  precede delta.content; non-stream folds both into the message.
  stream_options-without-stream 400 found live and fixed.
- Devin: `devin models list` exit 0 (CLI 3000.11.205) - the
  credentials-file JWT flow works with the current key. 44 families,
  816 variants, 575 fusion rows excluded (zero fusion in the final list),
  1 CLI-only `adaptive` entry excluded, 240 uids usable. No `auto` uid
  exists in the live list; the exclusion guard covers it anyway.
- Thinking coverage: 226 uids carry an explicit effort level in the uid,
  10 are plain (reasoning false), 4 bare-thinking uids default to medium
  (marked defaulted).
- Streaming matrix: no change - the probe was non-streaming; M1 proves the
  streaming rows.
- Full verified list + proposed config entries: docs/05-model-list-proposed.md.
