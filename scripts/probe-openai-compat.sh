#!/usr/bin/env bash
# Probe OpenAI Chat Completions param compatibility against any endpoint.
# Usage:
#   BASE_URL=http://127.0.0.1:1234/v1 MODEL=gpt-5.4-mini ./probe-openai-compat.sh
#   BASE_URL=https://api.openai.com/v1 MODEL=gpt-4o-mini API_KEY=sk-... ./probe-openai-compat.sh
#   BASE_URL=https://openrouter.ai/api/v1 MODEL=openai/gpt-4o-mini API_KEY=sk-or-... ./probe-openai-compat.sh
set -u

BASE_URL="${BASE_URL:?set BASE_URL, e.g. http://127.0.0.1:1234/v1}"
MODEL="${MODEL:?set MODEL}"
API_KEY="${API_KEY:-}"
URL="$BASE_URL/chat/completions"

auth=()
[ -n "$API_KEY" ] && auth=(-H "Authorization: Bearer $API_KEY")

base='"model":"'"$MODEL"'","messages":[{"role":"system","content":"terse"},{"role":"user","content":"say hi"}]'

probe() {
  local name="$1" data="$2"
  local out code body
  out=$(curl -s -w '\n%{http_code}' "$URL" -H 'Content-Type: application/json' "${auth[@]}" -d "$data")
  code=$(printf '%s' "$out" | tail -1)
  body=$(printf '%s' "$out" | head -n -1 | tr -d '\n' | head -c 200)
  printf '%-22s HTTP %s  %s\n' "$name" "$code" "$body"
}

echo "== $BASE_URL ($MODEL) =="
probe "baseline"           "{$base}"
probe "temperature"        "{$base,\"temperature\":0.7}"
probe "top_p"              "{$base,\"top_p\":0.9}"
probe "max_tokens"         "{$base,\"max_tokens\":16}"
probe "max_completion_tok" "{$base,\"max_completion_tokens\":16}"
probe "n=2"                "{$base,\"n\":2}"
probe "stop"               "{$base,\"stop\":[\"\\n\"]}"
probe "presence_penalty"   "{$base,\"presence_penalty\":0.5}"
probe "frequency_penalty"  "{$base,\"frequency_penalty\":0.5}"
probe "response_format"    "{$base,\"response_format\":{\"type\":\"json_object\"}}"
probe "seed"               "{$base,\"seed\":42}"
probe "logprobs"           "{$base,\"logprobs\":true}"
probe "system_only"        '{"model":"'"$MODEL"'","messages":[{"role":"system","content":"reply OK"}]}'
probe "user_only"          '{"model":"'"$MODEL"'","messages":[{"role":"user","content":"reply OK"}]}'
