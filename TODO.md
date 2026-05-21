# codex-bridge TODO / bug reports

Tracked OpenAI Chat Completions compatibility gaps. Details + live probe
evidence in [KNOWN_INCOMPATIBILITIES.md](KNOWN_INCOMPATIBILITIES.md).
Probed against `gpt-5.4-mini` baseline `openai/gpt-4o-mini` (OpenRouter),
2026-05-21.

## Resolved

- [x] **Drop `temperature` / `top_p` / `stop`** before forwarding to Codex.
  All three caused Codex `Unsupported parameter` 400s. Now parsed-and-dropped
  in `toResponsesRequest`.
- [x] **Handle single-role prompts.** System-only, user-only, developer-only,
  and assistant-only requests no longer 400 on Codex's "instructions/input
  required" guardrail — bridge synthesizes a placeholder for the empty side
  and folds the present side into `input` when needed.
- [x] **Reject `n > 1`** with explicit 400. Codex returns one output;
  silent drop misled clients.
- [x] **Validate `response_format`.** `json_object` / `json_schema` are
  rejected with a 400 instead of silently returning free-form text.
- [x] **Validate `modalities`.** Any non-`text` modality is rejected up front
  since Codex produces text only.
- [x] **Honor `stream_options.include_usage`.** Bridge no longer emits the
  final usage chunk unconditionally — only when the client asks for it,
  matching OpenAI's default-off behavior.
- [x] **Parse `max_completion_tokens` / `presence_penalty` /
  `frequency_penalty` / `seed` / `logprobs` / `top_logprobs`** so vanilla
  OpenAI clients deserialize cleanly. Dropped silently like `max_tokens`.

## Documented (unavoidable backend limits)

These cannot be fixed at the bridge — Codex itself does not support them.
The bridge surfaces an explicit 400 (when correctness matters) or parses
and drops (when ignoring is harmless). See KNOWN_INCOMPATIBILITIES.md for
the full split.

- 400-on-arrival: `temperature`, `top_p`, `stop`, `max_tokens`,
  `max_completion_tokens`, `n>1`, `response_format` (json_object/schema),
  `modalities` (audio/non-text).
- Silent drop: `presence_penalty`, `frequency_penalty`, `seed`, `logprobs`,
  `top_logprobs`, `logit_bias`, `user`, `metadata`, `store`, `service_tier`,
  `prediction`.

## Open (low priority)

- [ ] **Legacy `functions` / `function_call` fields.** Real OpenAI accepts
  `function_call:"auto"` and rejects deprecated `functions:[...]` with a 400.
  Bridge silently drops both. Acceptable since the documented migration path
  is `tools` / `tool_choice`. Reject explicitly if a user reports confusion.
- [ ] **Reasoning models (`o*` / `gpt-5.x`) themselves reject
  `temperature`/`top_p` and require `max_completion_tokens`** — so for those
  model classes some gaps are model-level, not provider-level. Bridge currently
  drops both fields globally, which is the correct conservative choice.

## OpenAI / OpenRouter parity baseline — 2026-05-21

Probed `scripts/probe-openai-compat.sh` and
`scripts/probe-openai-compat-extra.sh` against codex-bridge (`gpt-5.4-mini`)
and OpenRouter `openai/gpt-4o-mini`.

| Param / case | codex-bridge (post-fix) | OpenAI gpt-4o-mini (via OR) | Verdict |
|--------------|--------------------------|------------------------------|---------|
| `temperature` / `top_p` / `stop` | 200, dropped | 200 | bridge drops (unavoidable) |
| system-only / user-only / developer-only prompt | 200 (folded) | 200 | fixed |
| assistant-first message | 200 (placeholder) | 200 | fixed |
| `response_format` json_object/json_schema | 400 explicit | 200 enforced | unavoidable — bridge 400s clearly |
| `n=2` | 400 explicit | 200, 2 choices | unavoidable — bridge 400s clearly |
| `modalities` `audio` | 400 explicit | 404 (no provider) | unavoidable — bridge 400s clearly |
| `stream_options.include_usage` | honored | honored | fixed |
| `presence_penalty` / `frequency_penalty` / `seed` / `logprobs` / `logit_bias` / `user` / `metadata` | 200, dropped | 200 | silent drop, documented |
| `max_tokens` / `max_completion_tokens` | 200, dropped | 200 | unavoidable |
