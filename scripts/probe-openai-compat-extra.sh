#!/usr/bin/env bash
# Extended OpenAI Chat Completions compat probes — covers fields the original
# probe-openai-compat.sh does not. Same usage:
#   BASE_URL=http://127.0.0.1:1234/v1 MODEL=gpt-5.4-mini ./probe-openai-compat-extra.sh
#   BASE_URL=https://llm-gw.home.inndy.tw/openrouter/v1 MODEL=openai/gpt-4o-mini ./probe-openai-compat-extra.sh
set -u

BASE_URL="${BASE_URL:?set BASE_URL}"
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
  body=$(printf '%s' "$out" | head -n -1 | tr -d '\n' | head -c 220)
  printf '%-32s HTTP %s  %s\n' "$name" "$code" "$body"
}

echo "== $BASE_URL ($MODEL) =="

probe "developer_only"     '{"model":"'"$MODEL"'","messages":[{"role":"developer","content":"reply OK"}]}'
probe "developer_plus_user" '{"model":"'"$MODEL"'","messages":[{"role":"developer","content":"be brief"},{"role":"user","content":"hi"}]}'

probe "tool_choice_required" "{$base,\"tools\":[{\"type\":\"function\",\"function\":{\"name\":\"ping\",\"parameters\":{\"type\":\"object\"}}}],\"tool_choice\":\"required\"}"
probe "tool_choice_none"     "{$base,\"tools\":[{\"type\":\"function\",\"function\":{\"name\":\"ping\",\"parameters\":{\"type\":\"object\"}}}],\"tool_choice\":\"none\"}"
probe "tool_choice_specific" "{$base,\"tools\":[{\"type\":\"function\",\"function\":{\"name\":\"ping\",\"parameters\":{\"type\":\"object\"}}}],\"tool_choice\":{\"type\":\"function\",\"function\":{\"name\":\"ping\"}}}"

probe "parallel_tool_calls_false" "{$base,\"parallel_tool_calls\":false}"

probe "logit_bias"         "{$base,\"logit_bias\":{\"50256\":-100}}"
probe "user_field"         "{$base,\"user\":\"abc-123\"}"

probe "stream_options"     "{$base,\"stream\":true,\"stream_options\":{\"include_usage\":true}}"

probe "store_true"         "{$base,\"store\":true}"
probe "metadata"           "{$base,\"metadata\":{\"k\":\"v\"}}"

probe "modalities_text"    "{$base,\"modalities\":[\"text\"]}"
probe "modalities_audio"   "{$base,\"modalities\":[\"text\",\"audio\"]}"

probe "functions_legacy"   "{$base,\"functions\":[{\"name\":\"ping\",\"parameters\":{\"type\":\"object\"}}]}"
probe "function_call_legacy" "{$base,\"function_call\":\"auto\"}"

probe "response_format_text"   "{$base,\"response_format\":{\"type\":\"text\"}}"
probe "response_format_schema" "{$base,\"response_format\":{\"type\":\"json_schema\",\"json_schema\":{\"name\":\"x\",\"schema\":{\"type\":\"object\"}}}}"

probe "top_logprobs"       "{$base,\"logprobs\":true,\"top_logprobs\":3}"

probe "prediction"         "{$base,\"prediction\":{\"type\":\"content\",\"content\":\"prefix\"}}"

probe "service_tier"       "{$base,\"service_tier\":\"auto\"}"

probe "empty_string_user"  '{"model":"'"$MODEL"'","messages":[{"role":"system","content":"x"},{"role":"user","content":""}]}'
probe "null_content_user"  '{"model":"'"$MODEL"'","messages":[{"role":"system","content":"x"},{"role":"user","content":null}]}'

probe "image_url_content"  '{"model":"'"$MODEL"'","messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"https://example.com/x.png"}}]}]}'

probe "assistant_first"    '{"model":"'"$MODEL"'","messages":[{"role":"assistant","content":"hi"},{"role":"user","content":"continue"}]}'

probe "tool_no_assistant_prior" '{"model":"'"$MODEL"'","messages":[{"role":"user","content":"hi"},{"role":"tool","tool_call_id":"call_x","content":"{}"}]}'

probe "bad_model"          '{"model":"definitely-not-a-real-model","messages":[{"role":"user","content":"hi"}]}'

probe "messages_empty"     "{\"model\":\"$MODEL\",\"messages\":[]}"
probe "no_messages_field"  "{\"model\":\"$MODEL\"}"
