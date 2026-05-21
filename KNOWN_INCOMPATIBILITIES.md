# Known OpenAI Chat Completions incompatibilities

Standard OpenAI Chat Completions payloads behavior when proxied through
`codex-bridge` to the ChatGPT-account Codex Responses backend.

Probed live against `gpt-5.4-mini` 2026-05-21, baseline `openai/gpt-4o-mini`
via OpenRouter. See `scripts/probe-openai-compat.sh` and
`scripts/probe-openai-compat-extra.sh`.

## Legend

- **Fixed** — bridge translates correctly after this commit.
- **Unavoidable** — Codex backend limitation; bridge returns explicit 400 so
  the client knows immediately instead of getting wrong output.
- **Silent drop (documented)** — field accepted for schema compat but ignored.
  No functional impact; documented so clients don't expect the behavior.

## Fixed

| Field / case | Old behavior | New behavior |
|--------------|--------------|--------------|
| `temperature` / `top_p` / `stop` | forwarded → Codex 400 | parsed and dropped |
| system-only prompt | Codex 400 "input required" | system text folded into `input` as user turn |
| user-only / assistant-only / developer-only prompt | Codex 400 "Instructions required" | placeholder instructions synthesized |
| `n > 1` | silently returns 1 choice | bridge 400 up front |
| `response_format` json_object / json_schema | silently returned free-form text | bridge 400 with clear message |
| `modalities` containing non-`text` | silently returned text | bridge 400 |
| `stream_options.include_usage` | usage chunk always sent | only sent when `include_usage: true` |

## Unavoidable — Codex backend rejects

The Codex backend rejects these even though vanilla OpenAI accepts them.
Bridge cannot forward — it now parses-and-drops or 400s explicitly.

| Field | Bridge action | Why |
|-------|---------------|-----|
| `temperature` | parsed, dropped | Codex 400 `Unsupported parameter: temperature` |
| `top_p` | parsed, dropped | Codex 400 `Unsupported parameter: top_p` |
| `stop` | parsed, dropped | Codex 400 `Unsupported parameter: stop` |
| `max_tokens` / `max_completion_tokens` | parsed, dropped | Codex 400 `Unsupported parameter: max_output_tokens` |
| `n > 1` | bridge 400 | Codex Responses returns one output |
| `response_format` `json_object` | bridge 400 | Codex has no structured-output enforcement |
| `response_format` `json_schema` | bridge 400 | Codex has no structured-output enforcement |
| `modalities` `audio` (or any non-`text`) | bridge 400 | Codex produces text only |

## Silent drop (documented)

These fields are accepted for schema parity but have no effect. They do not
break anything; the client just won't get the requested behavior.

| Field | Reason ignored |
|-------|----------------|
| `presence_penalty` / `frequency_penalty` | Codex API doesn't accept |
| `seed` | Codex API doesn't accept |
| `logprobs` / `top_logprobs` | Codex stream emits no logprobs |
| `logit_bias` | Codex API doesn't expose token-level biasing |
| `user` | Bridge has no per-request analytics surface |
| `metadata` | Bridge doesn't persist requests (`store: false` always) |
| `store` | Bridge always sets `store: false` upstream |
| `service_tier` | Codex backend is single-tier per ChatGPT account |
| `prediction` | Codex API doesn't accept speculative-decoding prefix |
| `parallel_tool_calls` | Forwarded; Codex respects it for tool-capable models |
| `functions` / `function_call` (legacy) | Use `tools` / `tool_choice` instead — bridge ignores legacy fields rather than emulating |

## Structural translation

`collectInstructions` (conversion.go) maps `system` and `developer` →
`instructions`; `toResponsesInput` maps `user`, `assistant`, `tool` → `input`.
Codex requires both fields non-empty:

- If `input` is empty, the synthesized instructions or fallback `"Please respond."`
  is folded into a user turn so the model has something to respond to.
- If `instructions` is empty, a placeholder is sent (`"You are a helpful assistant."`).

The fold means a system-only prompt sees its content delivered to the model as
a user message — semantics are slightly different from vanilla OpenAI (where
system messages prime the model) but the content reaches the model and a
response is produced.

