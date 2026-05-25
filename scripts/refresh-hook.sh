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

# Workdir lives next to this script so the path is stable (for codex
# trust_workdir enrollment) and its parent is not world-writable —
# defeats the /tmp pre-creation attack.
script_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
workdir="$script_dir/.tmp-workdir"
mkdir -p "$workdir"
[ -f "$workdir/.gitignore" ] || printf '*\n' > "$workdir/.gitignore"

exec codex exec \
  --json \
  --ephemeral \
  -C "$workdir" \
  "Reply with the single word: ok"
