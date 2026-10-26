# kyo-router - milestones, checks, risks

Status 2026-10-26: M0-M2 delivered and verified live (three connectors
serve through /v1/chat/completions). M3 partial: internal/router chain +
Kind-table retries + pre-first-byte failover, slog request log, optional
server Bearer auth. Open: /v1/responses inbound, soak test, timeouts
review, README, discovery-pin catalog refresh.

## Pair note - ordering

Options
  A. router + fake connectors first, real connectors later - routing logic
     lands early - built on guessed protocol shapes
  B. real connectors first, routing rules last - protocol facts (frames, SSE
     shapes) de-risked first - routing arrives late
Pick: B - the protocol facts are the risk; routing is trivial once connectors
exist.
Theory: none - sequencing judgment from CODE_PHILOSOPHY P8 (small first).
Rejected: A because fake connectors bake in guessed shapes.

## M0 - scaffold

Deliverables: go.mod, cmd/kyo-router, internal/config, internal/catalog
(static list from config), internal/api with /v1/models + /healthz,
Dockerfile, compose, .gitignore (.env, credentials).

Checks:
- `go vet ./...` and `go test ./...` pass.
- `docker compose up -d --build` starts clean.
- `curl http://127.0.0.1:8080/v1/models` returns the configured catalog.
- `docker run --rm kyo-router:dev env | grep -E "oc_sk|devin-session"`
  prints nothing (no secrets in image).

Gate: all four pass.

## M1 - OpenCode Go connector

Deliverables: internal/opencode (chat/completions + responses split),
internal/httpx SSE reader, translation matrix rows for opencode, error
detection rules for opencode.

Checks:
- Contract tests against httptest fakes: chat passthrough identity,
  responses->chat translation, MissingSessionID -> BadRequest mapping,
  ModelProtocolUnsupported -> Unsupported mapping.
- Live: `curl -N` stream through the container on one chat model
  (opencode/glm-5.3) and one responses model (opencode/grok-4.7).

Gate: both live calls stream; contract tests green.

## M2 - Devin connector

Deliverables: internal/devin (GetUserJwt + JWT cache, GetChatMessage frames,
GetCliModelConfigs catalog), proto descriptors + generated stubs, thinking
map, synthetic streaming.

Checks:
- Contract tests: frame decode from a recorded fixture (flag 0x01 gzip,
  0x02 end-stream trailer), JWT refresh single-flight test with a fake clock,
  double-prefix guard test (401 case).
- Live: `curl -N` stream through the container on
  `devin/claude-haiku-5-5-low`.

Gate: live call streams; the catalog shows Devin families without fusion.

## M3 - routing, fallback, hardening

Deliverables: internal/router rules + fallback chain + retry classes,
/v1/responses inbound, timeouts, slog request log with redaction, README.

Checks:
- Failure-injection tests: primary 503 -> fallback 200; 429 with Retry-After
  -> one retry then fallback; 401 -> refresh once then fallback.
- Pre-first-byte failover only: a mid-stream upstream kill surfaces an SSE
  error event, no silent restart.
- Soak: 10-minute scripted stream loop, zero leaked goroutines
  (`go test -run Soak` with runtime.NumGoroutine before/after).
- Log line audit: no key or token substrings in output.

Gate: all checks pass; docs updated.

## Test plan summary

| Level | Target | Method |
|-------|--------|--------|
| Unit | catalog, config, error map | table-driven, fixtures |
| Contract | each connector vs its wire format | httptest fakes + recorded fixtures |
| Integration | api + router with fake connectors | in-memory Connector implementations |
| E2E | docker compose + real upstreams | curl scripts, gated on creds present |

No module-level mocks anywhere. Swap R through real seams (Transport,
TokenSource, Clock, Connector).

## Risks

| Risk | Mitigation | Check |
|------|------------|-------|
| Devin proto field drift | minimal proto, fixture tests | refresh fixture on change |
| Catalog drift (new models) | discovery + config pin (M3) | /v1/models diff on refresh |
| Session header policy change | config-selectable mode | MissingSessionID contract test |
| JWT expiry mid-stream | accepted: stream fails, refresh next call | refresh single-flight test |
| Thinking map wrong for bare "Thinking" variants | assumption from devin-pi-prime; verify live in M2 | live uid probe |
| OrbStack bind-mount path changes | credentials path is config, not hardcoded | M0 compose-up check |
