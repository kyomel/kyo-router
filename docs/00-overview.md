# kyo-router - overview

Personal router. One OpenAI-compatible endpoint on the laptop. Three
providers behind it:

- Devin CLI subscription: Connect-RPC model API at `server.codeium.com`,
  key from `~/.local/share/devin/credentials.toml`, JWT flow.
- OpenCode Go subscription: `https://opencode.ai/zen/go/v1`, Bearer
  `oc_sk_...`, `x-opencode-session` header, split chat/completions vs
  responses.
- DeepSeek metered API: `https://api.deepseek.com`, Bearer `sk-...`,
  OpenAI-compatible chat.

Runs local in Docker on OrbStack. No cloud. No secrets in the image.

This plan is the contract for the build. Code lands in later phases.

## Happy path

```ts
Client (pi, agent, curl)
  -> router HTTP (OpenAI surface)
    -> api.parse (trust boundary)
      -> router.Execute (route + fallback chain)
        -> Connector.Chat (devin or opencode)
          -> upstream (Connect frames or OpenAI SSE)
        -> connerr.Classify (Kind tag)
      -> api.translate (StreamEvent -> OpenAI SSE)
        -> Client
```

## Key decisions (options, pick, theory)

| Choice | Options | Pick | Theory |
|--------|---------|------|--------|
| HTTP layer | framework vs stdlib | stdlib net/http | standard library first (new page) |
| Canonical model | passthrough map vs typed | typed ChatRequest/StreamEvent | functional core, imperative shell (existing page) |
| Devin transport | connect-go stubs vs raw frames vs dynamic proto | connect-go + minimal .proto | adapter-pattern (existing page) |
| Thinking deltas | drop vs reasoning_content vs think tags | reasoning_content | none |
| Catalog source | discovery vs static vs hybrid | hybrid (discovery + pin) | single-source-of-truth (existing page) |
| Image base | distroless vs alpine | distroless nonroot | principle-of-least-privilege (existing page) |
| Port publish | host-only vs LAN | 127.0.0.1 only | principle-of-least-privilege (existing page) |
| Milestone order | router-first vs connectors-first | connectors first | none (P8 small first) |

Full pair notes live in 01-architecture.md, 02-connectors.md, 03-api-docker.md,
04-milestones.md.

## Anti-goals

- Multi-tenant keys, billing, quotas, teams UI.
- Protocol bridging to Anthropic messages or Google.
- A plugin marketplace in-process.
- Embeddings endpoints (no consumer today).

## Surface

| Method | Path | Status |
|--------|------|--------|
| GET | /v1/models | live |
| POST | /v1/chat/completions | live for all three providers |
| POST | /v1/responses | M3 |
| GET | /healthz | live |

Chat requests may carry `reasoning_effort` (OpenAI field, verbatim):
DeepSeek forwards it; Devin effort stays in the uid suffix and ignores
the field. Reasoning streams back as `delta.reasoning_content`.

## Model naming

| Prefix | Id form | Example |
|--------|---------|---------|
| devin/ | model_uid verbatim, effort suffix included | devin/claude-haiku-5-5-low |
| opencode/ | slug verbatim | opencode/grok-4.7 |
| deepseek/ | slug verbatim | deepseek/deepseek-v4-pro |

Aliases: each `[[model]]` may declare provider-prefixed short names
(`devin/haiku`, `opencode/glm`, `deepseek/pro`); they resolve to the
canonical id and appear in /v1/models. Devin picker uids (`adaptive`,
`auto`) and `fusion-*` are never served.

## Milestones

M0 scaffold. M1 OpenCode Go connector. M2 Devin connector. M3 routing,
fallback, hardening. Detail and per-phase checks in 04-milestones.md.
Status 2026-10-26: M0-M2 connectors live; router chain + Kind retries +
request log + optional Bearer live (M3 minus /v1/responses and soak).

## Risk table

| Risk | Mitigation | Check |
|------|------------|-------|
| Devin proto field drift | minimal proto + fixture tests | refresh fixture on change |
| Catalog drift | discovery + config pin | /v1/models diff on refresh |
| Session header policy change | config-selectable mode | MissingSessionID contract test |
| Mid-stream failover | pre-first-byte failover only | failure-injection test |
| Thinking map assumptions | live uid probe in M2 | live check |

## Doc guide

- 01-architecture.md - layers, call graph, modules, error kinds, seams
- 02-connectors.md - Devin and OpenCode Go auth, catalog, protocol split,
  error map, streaming matrix
- 03-api-docker.md - surface, config, Dockerfile, compose, run steps
- 04-milestones.md - phases, checks, test plan, risks
