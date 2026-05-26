# codex-bridge

Small OpenAI-compatible proxy for reusing an existing Codex OAuth session.

This project does not implement OAuth login and does not use `OPENAI_API_KEY`.
It reads Codex auth from `auth.json`, validates the session against the Codex
models endpoint, then exposes a minimal OpenAI-style API:

- `GET /v1/models`
- `POST /v1/chat/completions`

Both streaming and non-streaming chat completions are supported. Function tool
definitions, assistant tool calls, tool outputs, and streamed tool-call argument
deltas are translated between OpenAI chat completions and Codex Responses API
shapes.

## Why

Similar bridges already existed when this project was built, but the available
implementations were either more complicated than needed or relied on third-party
runtime libraries. This proxy keeps the production code small and uses only the
Go standard library, reducing dependency surface and supply-chain risk. It is
also intended to make the entire production codebase easy to review end to end.

## Principles

- **Minimal** — production code stays small, uses only the Go standard library, and is easy to review end to end.
- **Usable** — surface mirrors the OpenAI Chat Completions API exactly enough to drop in.
- **Correctness** — observable behavior matches OpenAI semantics; streaming and non-streaming results agree.

## Install

```sh
go install go.inndy.tw/codex-bridge@latest
```

From a checkout:

```sh
go install .
```

## Run

```sh
codex-bridge
```

Default listen address is `127.0.0.1:8080`.

Useful flags:

```sh
codex-bridge \
  --addr 127.0.0.1:8080 \
  --auth-path ~/.codex/auth.json \
  --codex-base-url https://chatgpt.com/backend-api/codex \
  --codex-version 0.125.0
```

Auth lookup order:

1. `--auth-path`
2. `CODEX_HOME/auth.json`
3. `~/.codex/auth.json`

The auth file must contain `tokens.access_token`. If `tokens.account_id` is
missing, `codex-bridge` tries to derive it from `tokens.id_token`.

## Auth Failure Hook

`codex-bridge` validates auth at startup with `/models`. If Codex returns
`401` or `403`, it can run a user-provided hook, reload auth, and retry.
The same retry also happens for runtime upstream auth failures.

Hook execution uses argument slices, not shell string parsing:

```sh
codex-bridge \
  --auth-fail-hook codex \
  --auth-fail-hook-arg --prompt \
  --auth-fail-hook-arg hi \
  --auth-hook-timeout 60s
```

## Client Example

```sh
curl http://127.0.0.1:8080/v1/models
```

```sh
curl http://127.0.0.1:8080/v1/chat/completions \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer ignored" \
  -d '{
    "model": "gpt-5.4",
    "messages": [{"role": "user", "content": "Reply exactly: OK"}]
  }'
```

Streaming:

```sh
curl http://127.0.0.1:8080/v1/chat/completions \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer ignored" \
  -d '{
    "model": "gpt-5.4",
    "stream": true,
    "messages": [{"role": "user", "content": "Reply with exactly this sentence: one two three four five."}]
  }'
```

## Tests

Normal tests use fake upstream servers and do not spend tokens:

```sh
go test ./...
```

Live smoke tests are opt-in because they use the existing Codex session:

```sh
CODEX_BRIDGE_SMOKE=1 go test -v ./...
```

PowerShell:

```powershell
$env:CODEX_BRIDGE_SMOKE = "1"
go test -v ./...
```

Smoke logs prompt, response text, stream deltas, accumulated stream output, and
usage fields when present.

Tests use the official `github.com/openai/openai-go/v3` SDK where useful. The
SDK is test-only. To strip test dependencies:

```sh
rm *_test.go
go mod tidy
```

## Notes

- Existing Codex OAuth session only.
- No Codex OAuth login flow.
- No OpenAI API key fallback.
- Production code uses only Go standard library packages.
- Logging uses `log/slog` and does not log tokens or full prompt bodies.

## License

MIT. See [LICENSE](LICENSE).

## References and Thanks

`codex-bridge` is an independent, API-compatible Go implementation. It is not a
clean-room rewrite. The implementation is based on observed behavior and
interoperability requirements, and does not intentionally copy source code from
referenced projects.

These projects helped verify API behavior and edge cases:

- [OpenAI Codex](https://github.com/openai/codex), for the official Codex
  client behavior around auth headers, model catalog requests, Responses API
  request shape, usage fields, and reasoning stream events.
- [auth2api](https://github.com/AmazingAng/auth2api), for independent evidence
  of ChatGPT Codex backend quirks such as forced streaming, required
  instructions, and rejected public Responses fields.
- [codex-openai-proxy](https://github.com/Securiteru/codex-openai-proxy), for
  earlier proxy work in this problem space.
- `openai-oauth`, for another reference implementation of reusing existing
  Codex/OpenAI OAuth session material.

Thanks to the authors and maintainers of those projects. This repository does
not vendor their code. Referenced projects were used read-only.

## Legal and Risk Notice

`codex-bridge` is unofficial and is not affiliated with OpenAI.

Current public signals suggest OpenAI allows third-party Codex integrations:
OpenAI documents official Codex integration paths, and OpenClaw publicly
documents Codex OAuth use in external tools. In practice, OpenAI's current
stance appears permissive toward this kind of use.

That may change. This project depends on Codex OAuth/backend behavior that may
change due to product policy, EULA/terms updates, abuse controls, quota changes,
or backend implementation changes. If that happens, `codex-bridge` may stop
working or may no longer be appropriate to use.

Users are responsible for understanding the terms and risks that apply to their
own account and use case. Do not use this project for multi-user proxying,
resale, shared-account access, or production use where official OpenAI API or
Codex integration paths are required.

Reference: [OpenClaw Docs](https://web.archive.org/web/20260506014812/https://docs.openclaw.ai/concepts/oauth#openai-codex-chatgpt-oauth) state:
> OpenAI Codex OAuth is explicitly supported for use outside the Codex CLI, including OpenClaw workflows.
