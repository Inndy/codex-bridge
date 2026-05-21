# Known OpenAI Chat Completions incompatibilities

Standard OpenAI Chat Completions payloads that do **not** behave correctly when
proxied through `codex-bridge` to the ChatGPT-account Codex Responses backend.

Probed live against `gpt-5.4-mini` on 2026-05-21. Two failure classes:

## A. Hard 400 (Codex upstream rejects the request)

| Standard field | Result | Upstream detail |
|----------------|--------|-----------------|
| `temperature`  | 400 | `Unsupported parameter: temperature` |
| `top_p`        | 400 | `Unsupported parameter: top_p` |
| `stop`         | 400 | `Unsupported parameter: stop` |

These are forwarded by `toResponsesRequest` (conversion.go) — `Temperature`,
`TopP`, `Stop` are set on `responsesRequest` — but the Codex backend rejects all
three. They should be parsed-and-dropped like `max_tokens` already is, not
forwarded.

### Structural requirement: both `instructions` and `input` are mandatory

A standard Chat Completions request with messages of only one kind breaks:

| Payload | Result | Upstream detail |
|---------|--------|-----------------|
| system/developer messages only (no user/assistant) | 400 | `One of "input" or "previous_response_id" or 'prompt' or 'conversation_id' must be provided.` |
| user/assistant messages only (no system/developer)  | 400 | `Instructions are required` |

Root cause: `collectInstructions` maps system/developer → `instructions`, and
`toResponsesInput` maps user/assistant → `input`, skipping the other role. If
either set is empty, the corresponding Codex field is empty and the backend
400s. Vanilla OpenAI accepts a system-only or user-only prompt.

Possible fix: synthesize a minimal placeholder when one side is empty (e.g. an
empty-but-present instructions string, or fold a lone system prompt into the
input turn).

## B. Silent drop (HTTP 200 but the parameter is ignored)

These return 200 with a normal completion, but the requested behavior is
**not** applied — worse than a 400 because the client gets no signal.

| Standard field | Observed | Why |
|----------------|----------|-----|
| `response_format` (`json_object`) | ignored | not in `ChatCompletionRequest`; JSON mode NOT enforced |
| `n` (e.g. 2) | ignored, 1 choice returned | Codex Responses returns a single output |
| `max_tokens` | ignored | intentionally not forwarded (see conversion.go comment) |
| `max_completion_tokens` | ignored | not parsed at all |
| `presence_penalty` | ignored | not in `ChatCompletionRequest` |
| `frequency_penalty` | ignored | not in `ChatCompletionRequest` |
| `seed` | ignored | not parsed |
| `logprobs` | ignored | no logprobs emitted in response |

`response_format` is the most dangerous: clients that rely on guaranteed JSON
output will receive free-form text with no error.

