# kyo-router - verified model catalog + proposed config list

Date: 2026-10-26. Sources: live probes (curl, this date, keys from .env via
env only, no values recorded), `devin models list --format json` (CLI
3000.11.205), docs/02-connectors.md split table (verified 2026-09-29).
This file is a proposal: config.toml is not edited yet.

## Counts

| Provider | Live total | Proposed list | Excluded |
|----------|-----------|---------------|----------|
| OpenCode Go | 38 ids | 38 | 0 (2 docs ids are gone from live) |
| Devin | 816 variants | 240 uids | 575 fusion + 1 adaptive (CLI-only) |
| DeepSeek | 2 ids | 2 | 0 |

## OpenCode Go - live ids vs protocol split

`GET /models` (200) carries only id/object/created/owned_by - no protocol
field - so the chat/responses split stays config-driven.

| id | avail | protocol | evidence |
|----|-------|----------|----------|
| deepseek-v4.1-flash | yes | chat | docs split 2026-09-29 |
| deepseek-v4-pro | yes | chat | docs split 2026-09-29 |
| glm-5.1 | yes | unverified | new since docs |
| glm-5.2 | yes | unverified | new since docs |
| glm-5.3 | yes | chat | docs split + probed 200 today |
| glm-5.3-flash | yes | chat | docs split 2026-09-29 |
| grok-4.6 | yes | responses | docs split 2026-09-29 |
| grok-4.7 | yes | responses | docs split 2026-09-29 |
| muse-spark-1.2-contributor | yes | unverified | new since docs |
| muse-spark-1.3-contributor | yes | responses | docs split 2026-09-29 |
| omen-alpha | yes | unverified | new since docs |
| gpt-5.6-luna | yes | unverified | new since docs |
| gpt-6-luna | yes | responses | docs split 2026-09-29 |
| claude-haiku-5-5 | yes | unverified | new since docs |
| hy3 | yes | unverified | new since docs |
| hy4-preview | yes | unverified | new since docs |
| kimi-k2.6 | yes | unverified | new since docs |
| kimi-k2.7-code | yes | chat | docs split 2026-09-29 |
| kimi-k3 | yes | unverified | new since docs |
| kimi-k3.1-pro | yes | unverified | new since docs |
| mimo-v2.5 | yes | unverified | new since docs |
| mimo-v2.5-pro | yes | unverified | new since docs |
| mimo-v2.6-flash | yes | chat | docs split 2026-09-29 |
| mimo-v2.6-pro | yes | unverified | new since docs |
| minimax-m2.5 | yes | unverified | new since docs |
| minimax-m2.7 | yes | unverified | new since docs |
| minimax-m3 | yes | unverified | new since docs |
| minimax-m3.1-flash | yes | unverified | new since docs |
| longcat-2.0 | yes | unverified | new since docs |
| step-5-preview | yes | unverified | new since docs |
| beam-free | yes | unverified | new since docs |
| qwen3.6-plus | yes | unverified | new since docs |
| qwen3.7-max | yes | unverified | new since docs |
| qwen3.7-plus | yes | unverified | new since docs |
| qwen3.8-flash | yes | unverified | new since docs |
| qwen3.8-max | yes | chat | docs split 2026-09-29 |
| iceman-contributor-free | yes | unverified | new since docs |
| gemini-4-argon | yes | unverified | new since docs |
| longcat-2.5-preview-free | no | - | gone from live list |
| space-bunny-free | no | - | gone from live list |

Probe record: glm-5.3 POST /chat/completions, stream=false, max_tokens=16,
x-opencode-session per request -> HTTP 200, finish_reason=stop. Responses
probe: pending M1 (no connector code exists yet).

## DeepSeek - live ids (verified 2026-10-26)

`GET https://api.deepseek.com/models` -> 200. Carries name, context_window,
max_output_tokens, input_modalities, effort levels - richer than the OpenAI
shape.

| id | name | context | max output | input | effort levels | alias |
|----|------|---------|------------|-------|---------------|-------|
| deepseek-flash | DeepSeek-V4.1-Flash | 1,048,576 | 393,216 | text+image | low, high (default), max | deepseek/flash |
| deepseek-v4-pro | DeepSeek-V4-Pro | 1,048,576 | 393,216 | text | low, high (default), max | deepseek/pro |

Protocol: chat. Effort is a request parameter, not an id suffix - sent as
`reasoning_effort`, verified live 2026-10-26 (stream + non-stream).

## Devin - usable uids (fusion excluded, zero fusion rows)

44 families, 816 variants. Fusion family = 575 variants (all fusion-* uids),
excluded. `adaptive` (1 variant) excluded: the CLI lists it but the
GetCliModelConfigs API catalog omits it, and M2 builds the catalog from the
API - config should match what M2 serves. `auto` (0 variants in the live
list) is excluded by the same predicate ahead of its appearance - requested
2026-10-26. Result: 240 uids, uid verbatim.

Levels = the effort suffix in the uid (low, medium, high, xhigh, max, none,
minimal). The tails -fast, -1m, -priority, -lightning are variant modifiers,
not levels. Claude families have no `none` level: an off request defaults to
medium.

- swe-2 (6): swe-2-high, swe-2-high-lightning, swe-2-max, swe-2-max-lightning, swe-2-medium, swe-2-medium-lightning
  - levels: high, max, medium
  - plain (no reasoning): 
  - defaulted (thinking -> medium): 
- claude-fable-5.1 (5): claude-fable-5-1-high, claude-fable-5-1-low, claude-fable-5-1-max, claude-fable-5-1-medium, claude-fable-5-1-xhigh
  - levels: high, low, max, medium, xhigh
  - plain (no reasoning): 
  - defaulted (thinking -> medium): 
- claude-opus-5.5 (10): claude-opus-5-5-high, claude-opus-5-5-high-fast, claude-opus-5-5-low, claude-opus-5-5-low-fast, claude-opus-5-5-max, claude-opus-5-5-max-fast, claude-opus-5-5-medium, claude-opus-5-5-medium-fast, claude-opus-5-5-xhigh, claude-opus-5-5-xhigh-fast
  - levels: high, low, max, medium, xhigh
  - plain (no reasoning): 
  - defaulted (thinking -> medium): 
- gpt-6-astra (10): gpt-6-astra-high, gpt-6-astra-high-priority, gpt-6-astra-low, gpt-6-astra-low-priority, gpt-6-astra-max, gpt-6-astra-max-priority, gpt-6-astra-medium, gpt-6-astra-medium-priority, gpt-6-astra-xhigh, gpt-6-astra-xhigh-priority
  - levels: high, low, max, medium, xhigh
  - plain (no reasoning): 
  - defaulted (thinking -> medium): 
- gpt-6-sol (12): gpt-6-sol-high, gpt-6-sol-high-priority, gpt-6-sol-low, gpt-6-sol-low-priority, gpt-6-sol-max, gpt-6-sol-max-priority, gpt-6-sol-medium, gpt-6-sol-medium-priority, gpt-6-sol-none, gpt-6-sol-none-priority, gpt-6-sol-xhigh, gpt-6-sol-xhigh-priority
  - levels: high, low, max, medium, none, xhigh
  - plain (no reasoning): 
  - defaulted (thinking -> medium): 
- gpt-6-luna (12): gpt-6-luna-high, gpt-6-luna-high-priority, gpt-6-luna-low, gpt-6-luna-low-priority, gpt-6-luna-max, gpt-6-luna-max-priority, gpt-6-luna-medium, gpt-6-luna-medium-priority, gpt-6-luna-none, gpt-6-luna-none-priority, gpt-6-luna-xhigh, gpt-6-luna-xhigh-priority
  - levels: high, low, max, medium, none, xhigh
  - plain (no reasoning): 
  - defaulted (thinking -> medium): 
- gemini-4-argon (3): gemini-4-argon-high, gemini-4-argon-low, gemini-4-argon-medium
  - levels: high, low, medium
  - plain (no reasoning): 
  - defaulted (thinking -> medium): 
- kimi-k3 (3): kimi-k3-high, kimi-k3-low, kimi-k3-max
  - levels: high, low, max
  - plain (no reasoning): 
  - defaulted (thinking -> medium): 
- glm-5.2 (6): glm-5-2, glm-5-2-1m, glm-5-2-max, glm-5-2-max-1m, glm-5-2-none, glm-5-2-none-1m
  - levels: max, none
  - plain (no reasoning): glm-5-2, glm-5-2-1m
  - defaulted (thinking -> medium): 
- glm-5.3 (3): glm-5-3-high, glm-5-3-low, glm-5-3-max
  - levels: high, low, max
  - plain (no reasoning): 
  - defaulted (thinking -> medium): 
- claude-sonnet-5.5 (5): claude-sonnet-5-5-high, claude-sonnet-5-5-low, claude-sonnet-5-5-max, claude-sonnet-5-5-medium, claude-sonnet-5-5-xhigh
  - levels: high, low, max, medium, xhigh
  - plain (no reasoning): 
  - defaulted (thinking -> medium): 
- gemini-3.8-flash (3): gemini-3-8-flash-high, gemini-3-8-flash-low, gemini-3-8-flash-medium
  - levels: high, low, medium
  - plain (no reasoning): 
  - defaulted (thinking -> medium): 
- claude-opus-4.7 (5): claude-opus-4-7-high, claude-opus-4-7-low, claude-opus-4-7-max, claude-opus-4-7-medium, claude-opus-4-7-xhigh
  - levels: high, low, max, medium, xhigh
  - plain (no reasoning): 
  - defaulted (thinking -> medium): 
- claude-opus-4.8 (10): claude-opus-4-8-high, claude-opus-4-8-high-fast, claude-opus-4-8-low, claude-opus-4-8-low-fast, claude-opus-4-8-max, claude-opus-4-8-max-fast, claude-opus-4-8-medium, claude-opus-4-8-medium-fast, claude-opus-4-8-xhigh, claude-opus-4-8-xhigh-fast
  - levels: high, low, max, medium, xhigh
  - plain (no reasoning): 
  - defaulted (thinking -> medium): 
- claude-opus-5 (10): claude-opus-5-high, claude-opus-5-high-fast, claude-opus-5-low, claude-opus-5-low-fast, claude-opus-5-max, claude-opus-5-max-fast, claude-opus-5-medium, claude-opus-5-medium-fast, claude-opus-5-xhigh, claude-opus-5-xhigh-fast
  - levels: high, low, max, medium, xhigh
  - plain (no reasoning): 
  - defaulted (thinking -> medium): 
- claude-fable-5 (5): claude-5-fable-high, claude-5-fable-low, claude-5-fable-max, claude-5-fable-medium, claude-5-fable-xhigh
  - levels: high, low, max, medium, xhigh
  - plain (no reasoning): 
  - defaulted (thinking -> medium): 
- claude-sonnet-5 (5): claude-sonnet-5-high, claude-sonnet-5-low, claude-sonnet-5-max, claude-sonnet-5-medium, claude-sonnet-5-xhigh
  - levels: high, low, max, medium, xhigh
  - plain (no reasoning): 
  - defaulted (thinking -> medium): 
- claude-haiku-5.5 (5): claude-haiku-5-5-high, claude-haiku-5-5-low, claude-haiku-5-5-max, claude-haiku-5-5-medium, claude-haiku-5-5-xhigh
  - levels: high, low, max, medium, xhigh
  - plain (no reasoning): 
  - defaulted (thinking -> medium): 
- gemini-3.5-flash (4): gemini-3-5-flash-high, gemini-3-5-flash-low, gemini-3-5-flash-medium, gemini-3-5-flash-minimal
  - levels: high, low, medium, minimal
  - plain (no reasoning): 
  - defaulted (thinking -> medium): 
- gemini-3.6-flash (4): gemini-3-6-flash-high, gemini-3-6-flash-low, gemini-3-6-flash-medium, gemini-3-6-flash-minimal
  - levels: high, low, medium, minimal
  - plain (no reasoning): 
  - defaulted (thinking -> medium): 
- gemini-3.7-flash (3): gemini-3-7-flash-high, gemini-3-7-flash-low, gemini-3-7-flash-medium
  - levels: high, low, medium
  - plain (no reasoning): 
  - defaulted (thinking -> medium): 
- gpt-5.6-sol (12): gpt-5-6-sol-high, gpt-5-6-sol-high-priority, gpt-5-6-sol-low, gpt-5-6-sol-low-priority, gpt-5-6-sol-max, gpt-5-6-sol-max-priority, gpt-5-6-sol-medium, gpt-5-6-sol-medium-priority, gpt-5-6-sol-none, gpt-5-6-sol-none-priority, gpt-5-6-sol-xhigh, gpt-5-6-sol-xhigh-priority
  - levels: high, low, max, medium, none, xhigh
  - plain (no reasoning): 
  - defaulted (thinking -> medium): 
- gpt-5.6-terra (12): gpt-5-6-terra-high, gpt-5-6-terra-high-priority, gpt-5-6-terra-low, gpt-5-6-terra-low-priority, gpt-5-6-terra-max, gpt-5-6-terra-max-priority, gpt-5-6-terra-medium, gpt-5-6-terra-medium-priority, gpt-5-6-terra-none, gpt-5-6-terra-none-priority, gpt-5-6-terra-xhigh, gpt-5-6-terra-xhigh-priority
  - levels: high, low, max, medium, none, xhigh
  - plain (no reasoning): 
  - defaulted (thinking -> medium): 
- gpt-5.6-luna (12): gpt-5-6-luna-high, gpt-5-6-luna-high-priority, gpt-5-6-luna-low, gpt-5-6-luna-low-priority, gpt-5-6-luna-max, gpt-5-6-luna-max-priority, gpt-5-6-luna-medium, gpt-5-6-luna-medium-priority, gpt-5-6-luna-none, gpt-5-6-luna-none-priority, gpt-5-6-luna-xhigh, gpt-5-6-luna-xhigh-priority
  - levels: high, low, max, medium, none, xhigh
  - plain (no reasoning): 
  - defaulted (thinking -> medium): 
- gpt-6.1-sol (10): gpt-6-1-sol-high, gpt-6-1-sol-high-priority, gpt-6-1-sol-low, gpt-6-1-sol-low-priority, gpt-6-1-sol-max, gpt-6-1-sol-max-priority, gpt-6-1-sol-medium, gpt-6-1-sol-medium-priority, gpt-6-1-sol-xhigh, gpt-6-1-sol-xhigh-priority
  - levels: high, low, max, medium, xhigh
  - plain (no reasoning): 
  - defaulted (thinking -> medium): 
- grok-4.6 (4): grok-4-6-high, grok-4-6-low, grok-4-6-medium, grok-4-6-xhigh
  - levels: high, low, medium, xhigh
  - plain (no reasoning): 
  - defaulted (thinking -> medium): 
- grok-4.7 (4): grok-4-7-high, grok-4-7-low, grok-4-7-medium, grok-4-7-xhigh
  - levels: high, low, medium, xhigh
  - plain (no reasoning): 
  - defaulted (thinking -> medium): 
- muse-wonder (6): muse-wonder-1-0-high, muse-wonder-1-0-low, muse-wonder-1-0-max, muse-wonder-1-0-medium, muse-wonder-1-0-minimal, muse-wonder-1-0-xhigh
  - levels: high, low, max, medium, minimal, xhigh
  - plain (no reasoning): 
  - defaulted (thinking -> medium): 
- glm-5.3-flash (3): glm-5-3-flash-high, glm-5-3-flash-low, glm-5-3-flash-max
  - levels: high, low, max
  - plain (no reasoning): 
  - defaulted (thinking -> medium): 
- deepseek-v4-flash (2): deepseek-v4-flash-high, deepseek-v4-flash-max
  - levels: high, max
  - plain (no reasoning): 
  - defaulted (thinking -> medium): 
- deepseek-v4.1-flash (4): deepseek-v4-1-flash-1017-high, deepseek-v4-1-flash-1017-low, deepseek-v4-1-flash-1017-max, deepseek-v4-1-flash-1017-none
  - levels: high, low, max, none
  - plain (no reasoning): 
  - defaulted (thinking -> medium): 
- swe-1.7 (2): swe-1-7, swe-1-7-medium
  - levels: medium
  - plain (no reasoning): swe-1-7
  - defaulted (thinking -> medium): 
- claude-opus-4.6 (4): claude-opus-4-6, claude-opus-4-6-1m, claude-opus-4-6-thinking, claude-opus-4-6-thinking-1m
  - levels: 
  - plain (no reasoning): claude-opus-4-6, claude-opus-4-6-1m
  - defaulted (thinking -> medium): claude-opus-4-6-thinking, claude-opus-4-6-thinking-1m
- gpt-5.4 (10): gpt-5-4-high, gpt-5-4-high-priority, gpt-5-4-low, gpt-5-4-low-priority, gpt-5-4-medium, gpt-5-4-medium-priority, gpt-5-4-none, gpt-5-4-none-priority, gpt-5-4-xhigh, gpt-5-4-xhigh-priority
  - levels: high, low, medium, none, xhigh
  - plain (no reasoning): 
  - defaulted (thinking -> medium): 
- gpt-5.5 (10): gpt-5-5-high, gpt-5-5-high-priority, gpt-5-5-low, gpt-5-5-low-priority, gpt-5-5-medium, gpt-5-5-medium-priority, gpt-5-5-none, gpt-5-5-none-priority, gpt-5-5-xhigh, gpt-5-5-xhigh-priority
  - levels: high, low, medium, none, xhigh
  - plain (no reasoning): 
  - defaulted (thinking -> medium): 
- gpt-5.4-mini (4): gpt-5-4-mini-high, gpt-5-4-mini-low, gpt-5-4-mini-medium, gpt-5-4-mini-xhigh
  - levels: high, low, medium, xhigh
  - plain (no reasoning): 
  - defaulted (thinking -> medium): 
- claude-sonnet-4.6 (4): claude-sonnet-4-6, claude-sonnet-4-6-1m, claude-sonnet-4-6-thinking, claude-sonnet-4-6-thinking-1m
  - levels: 
  - plain (no reasoning): claude-sonnet-4-6, claude-sonnet-4-6-1m
  - defaulted (thinking -> medium): claude-sonnet-4-6-thinking, claude-sonnet-4-6-thinking-1m
- kimi-k2.6 (1): kimi-k2-6
  - levels: 
  - plain (no reasoning): kimi-k2-6
  - defaulted (thinking -> medium): 
- kimi-k2.7 (1): kimi-k2-7
  - levels: 
  - plain (no reasoning): kimi-k2-7
  - defaulted (thinking -> medium): 
- nemotron-3-ultra (3): nemotron-3-ultra-high, nemotron-3-ultra-medium, nemotron-3-ultra-none
  - levels: high, medium, none
  - plain (no reasoning): 
  - defaulted (thinking -> medium): 
- swe-1.6 (1): swe-1-6
  - levels: 
  - plain (no reasoning): swe-1-6
  - defaulted (thinking -> medium): 
- deepseek-v4-pro (2): deepseek-v4-pro-high, deepseek-v4-pro-max
  - levels: high, max
  - plain (no reasoning): 
  - defaulted (thinking -> medium): 

## Thinking coverage

| Class | Count | Meaning |
|-------|-------|---------|
| effort in uid | 226 | level explicit, no default needed |
| plain | 10 | reasoning false |
| defaulted | 4 | bare thinking uid -> medium (assumption carried from devin-pi-prime) |

Defaulted uids: claude-opus-4-6-thinking, claude-opus-4-6-thinking-1m,
claude-sonnet-4-6-thinking, claude-sonnet-4-6-thinking-1m.

Plain uids (reasoning false): glm-5-2, glm-5-2-1m, swe-1-6, swe-1-7,
claude-opus-4-6, claude-opus-4-6-1m, claude-sonnet-4-6, claude-sonnet-4-6-1m,
kimi-k2-6, kimi-k2-7.

## Proposed config.toml model entries (not applied)

- Devin: one [[model]] per uid above: id = "devin/<uid>", connector = "devin",
  upstream = "<uid>", protocol = "devin", features = ["thinking"] when the
  uid carries a level or is a defaulted thinking uid, plus ["images"] per
  family capability (from GetCliModelConfigs in M2).
- OpenCode: one [[model]] per live id: id = "opencode/<id>",
  connector = "opencode", upstream = "<id>", protocol per the table above;
  unverified rows need a protocol probe before they enter config.
- DeepSeek: one [[model]] per live id: id = "deepseek/<id>",
  connector = "deepseek", upstream = "<id>", protocol = "chat",
  features = ["images","thinking"] on flash, ["thinking"] on v4-pro.
  Applied to config.toml 2026-10-26.
- Aliases: optional `aliases` per model, provider-prefixed short names
  (applied 2026-10-26: devin/haiku, opencode/glm, opencode/grok,
  deepseek/flash, deepseek/pro).
- Routes: keep the two family rules; fallbacks stay as configured today.
