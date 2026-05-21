# codex-bridge TODO / bug reports

Tracked OpenAI Chat Completions compatibility gaps. Details + live probe
evidence in [KNOWN_INCOMPATIBILITIES.md](KNOWN_INCOMPATIBILITIES.md).
Probed against `gpt-5.4-mini`, 2026-05-21.

## Bugs — forwarded params the Codex backend rejects (cause hard 400)

- [ ] **Drop `temperature` before forwarding.** Codex upstream → 400
  `Unsupported parameter: temperature`. Currently set in `toResponsesRequest`
  (conversion.go). Parse-and-drop like `max_tokens`.
- [ ] **Drop `top_p` before forwarding.** Codex upstream → 400
  `Unsupported parameter: top_p`.
- [ ] **Drop `stop` before forwarding.** Codex upstream → 400
  `Unsupported parameter: stop`.
  - Decision needed: silently drop (matches `max_tokens` behavior) vs. return a
    clear 400 from the bridge naming the unsupported field. Silent drop maximizes
    client compatibility; explicit error is more honest. Recommend silent drop
    for sampler params to match OpenAI clients that always send `temperature`.

## Bug — both `instructions` and `input` are required

- [ ] **Handle single-role prompts.** A standard request with only
  system/developer messages → 400 `One of "input" ... must be provided`; only
  user/assistant messages → 400 `Instructions are required`.
  Vanilla OpenAI accepts either alone. Synthesize a placeholder for the empty
  side in `toResponsesRequest` (e.g. empty-string instructions allowed, or fold a
  lone system prompt into `input`).

## Bugs — silently dropped params (HTTP 200 but ignored, no client signal)

- [ ] **`response_format` (`json_object` / `json_schema`) ignored.** Highest
  priority: clients relying on guaranteed JSON get free-form text silently.
  Either forward to the Responses `text.format` field or return 400 if
  unsupported.
- [ ] **`n > 1` ignored** — only one choice returned. Reject `n>1` with 400, or
  document the single-choice limit.
- [ ] **`presence_penalty` / `frequency_penalty` ignored** — not parsed.
- [ ] **`seed` ignored** — not parsed.
- [ ] **`logprobs` / `top_logprobs` ignored** — no logprobs in response.
- [ ] **`max_completion_tokens` ignored** — not parsed (only `max_tokens` is,
  and it is intentionally dropped). At least parse it for parity.

## OpenAI / OpenRouter parity — CONFIRMED 2026-05-21

Probed `scripts/probe-openai-compat.sh` against codex-bridge (`gpt-5.4-mini`),
OpenAI `gpt-4o-mini`, and OpenRouter `openai/gpt-4o-mini`.

| Param / case | codex-bridge | OpenAI gpt-4o-mini | OpenRouter | Verdict |
|--------------|--------------|--------------------|------------|---------|
| `temperature` | 400 | 200 | 200 | Codex-backend limit → bridge should DROP |
| `top_p`       | 400 | 200 | 200 | Codex-backend limit → bridge should DROP |
| `stop`        | 400 | 200 | 200 | Codex-backend limit → bridge should DROP |
| system-only prompt | 400 | 200 | 200 | **bridge gap** (dual requirement) |
| user-only prompt   | 400 | 200 | 200 | **bridge gap** (dual requirement) |
| `response_format` json_object | 200, **ignored** (not forwarded) | 200 / 400-guardrail (enforced) | same as OpenAI | **bridge gap** — silently drops |
| `n=2` | 200, **1 choice** | 200, **2 choices** | 200 | **bridge gap** — extra choices lost |
| `presence_penalty` / `frequency_penalty` | 200, dropped | 200 | 200 | bridge silently drops |
| `seed` / `logprobs` | 200, dropped | 200 | 200 | bridge silently drops |
| `max_tokens` / `max_completion_tokens` | 200, dropped | 200 | 200 | bridge drops (max_tokens intentional) |

Conclusions:
- `temperature` / `top_p` / `stop` rejection is a **Codex-backend** limit (real
  OpenAI accepts them) → bridge should parse-and-drop, not forward.
- The dual `instructions` + `input` requirement and the silent drops of
  `response_format` / `n` / penalties / `seed` / `logprobs` are **bridge-side**
  translation gaps — real OpenAI honors all of them.
- `response_format` proof: OpenAI 400s with `'messages' must contain the word
  'json'` (i.e. it *processes* the field); codex-bridge never forwards it, so
  JSON mode is unenforced.

- [ ] Note: newer OpenAI reasoning models (o-series / gpt-5.x) themselves reject
  `temperature`/`top_p` and require `max_completion_tokens` — so for those model
  classes some gaps are model behavior, not provider behavior. Record per-model
  if targeting reasoning models through the bridge.
