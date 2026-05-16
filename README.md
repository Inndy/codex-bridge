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
  --codex-version 0.111.0
```

Auth lookup order:

1. `--auth-path`
2. `CODEX_HOME/auth.json`
3. `~/.codex/auth.json`
4. sorted `~/.codex*/auth.json`

The auth file must contain `tokens.access_token` and `tokens.account_id`.
If `tokens.account_id` is missing, `codex-bridge` tries to derive it from
`tokens.id_token`.

## Auth Failure Hook

`codex-bridge` validates auth at startup with `/models`. If Codex returns
`401` or `403`, it can run a user-provided hook once, reload auth, and retry.
The same one-time retry also happens for runtime upstream auth failures.

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
find . -name "*_test.go" | xargs rm
go mod tidy
```

## Notes

- Existing Codex OAuth session only.
- No Codex OAuth login flow.
- No OpenAI API key fallback.
- Production code uses only Go standard library packages.
- Logging uses `log/slog` and does not log tokens or full prompt bodies.
