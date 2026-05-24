#!/bin/bash
# codex-bridge token refresh hook.
#
# Wire up with:
#   codex-bridge -auth-fail-hook /path/to/refresh-hook.sh
#
# codex detects an expired access token on startup and refreshes via OAuth
# before making any API call, writing the new tokens back to ~/.codex/auth.json.
# codex-bridge reloads auth.json after this script exits.
#
# No sandbox bypass needed: the text-only prompt never triggers shell tool use,
# so no approval prompts appear and sandbox mode is irrelevant.
set -euo pipefail

mkdir -p /tmp/.codex-bridge-refresh

exec codex exec \
  --json \
  --ephemeral \
  -C /tmp/.codex-bridge-refresh \
  "Reply with the single word: ok"
