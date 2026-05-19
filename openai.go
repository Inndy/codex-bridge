package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"
)

type ChatCompletionResponse struct {
	ID      string       `json:"id"`
	Object  string       `json:"object"`
	Created int64        `json:"created"`
	Model   string       `json:"model"`
	Choices []ChatChoice `json:"choices"`
	Usage   any          `json:"usage,omitempty"`
}

type ChatChoice struct {
	Index        int                 `json:"index"`
	Message      ChatResponseMessage `json:"message"`
	FinishReason string              `json:"finish_reason,omitempty"`
}

type ChatResponseMessage struct {
	Role             string              `json:"role"`
	Content          *string             `json:"content"`
	ReasoningContent string              `json:"reasoning_content,omitempty"`
	ToolCalls        []OpenAIToolCallOut `json:"tool_calls,omitempty"`
}

type OpenAIToolCallOut struct {
	ID       string             `json:"id"`
	Type     string             `json:"type"`
	Function OpenAIFunctionCall `json:"function"`
}

type OpenAIFunctionCall struct {
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments"`
}

type StreamAggregator struct {
	text              string
	reasoning         string
	finishReason      string
	usage             any
	toolOrder         []string
	toolCalls         map[string]*OpenAIToolCallOut
	activeToolItemID  map[string]string
	syntheticByOutput map[int]string
	nextSyntheticTool int
}

// syntheticCallIDPrefix is the prefix used for fabricated tool-call IDs when
// the upstream Codex stream omits a real call_id. Using a distinct prefix from
// upstream's `call_...` namespace means the aggregator can never collapse a
// synthetic call onto a real one, and a client echoing it back as
// tool_call_id surfaces as an upstream rejection instead of silent corruption.
const syntheticCallIDPrefix = "synth_call_"

func NewStreamAggregator() *StreamAggregator {
	return &StreamAggregator{
		toolCalls:         map[string]*OpenAIToolCallOut{},
		activeToolItemID:  map[string]string{},
		syntheticByOutput: map[int]string{},
	}
}

func (a *StreamAggregator) ApplyEvent(event SSEEvent) ([]map[string]any, error) {
	if event.Data == "" || event.Data == "[DONE]" {
		return nil, nil
	}
	var raw map[string]any
	if err := json.Unmarshal([]byte(event.Data), &raw); err != nil {
		return nil, err
	}
	eventType, _ := raw["type"].(string)
	if eventType == "" {
		eventType = event.Event
	}

	var chunks []map[string]any
	switch eventType {
	case "response.output_text.delta":
		delta, _ := raw["delta"].(string)
		if delta != "" {
			a.text += delta
			chunks = append(chunks, map[string]any{"content": delta})
		}
	case "response.reasoning_summary_text.delta", "response.reasoning_text.delta":
		delta, _ := raw["delta"].(string)
		if delta != "" {
			a.reasoning += delta
			chunks = append(chunks, map[string]any{"reasoning_content": delta})
		}
	case "response.function_call_arguments.delta":
		callID := a.callIDForRaw(raw)
		delta, _ := raw["delta"].(string)
		if callID != "" && delta != "" {
			a.ensureToolCall(callID, "", "")
			a.toolCalls[callID].Function.Arguments += delta
			chunks = append(chunks, map[string]any{
				"tool_calls": []any{a.toolDelta(callID, "", delta)},
			})
		}
	case "response.output_item.added":
		if item, ok := raw["item"].(map[string]any); ok {
			if delta := a.applyFunctionItem(item, true); delta != nil {
				chunks = append(chunks, map[string]any{"tool_calls": []any{delta}})
			}
		}
	case "response.output_item.done":
		if item, ok := raw["item"].(map[string]any); ok {
			a.applyFunctionItem(item, false)
			a.applyMessageItem(item)
		}
	case "response.completed":
		if response, ok := raw["response"].(map[string]any); ok {
			a.applyCompletedResponse(response)
		}
	case "response.failed", "error":
		return nil, fmt.Errorf("upstream stream error: %s", event.Data)
	}
	return chunks, nil
}

func (a *StreamAggregator) Completion(id, model string, created int64) ChatCompletionResponse {
	var content *string
	if a.text != "" || len(a.toolOrder) == 0 {
		text := a.text
		content = &text
	}
	toolCalls := make([]OpenAIToolCallOut, 0, len(a.toolOrder))
	for _, callID := range a.toolOrder {
		toolCalls = append(toolCalls, *a.toolCalls[callID])
	}
	finish := a.finishReason
	if finish == "" {
		if len(toolCalls) > 0 {
			finish = "tool_calls"
		} else {
			finish = "stop"
		}
	}
	return ChatCompletionResponse{
		ID:      id,
		Object:  "chat.completion",
		Created: created,
		Model:   model,
		Choices: []ChatChoice{{
			Index: 0,
			Message: ChatResponseMessage{
				Role:             "assistant",
				Content:          content,
				ReasoningContent: a.reasoning,
				ToolCalls:        toolCalls,
			},
			FinishReason: finish,
		}},
		Usage: a.usage,
	}
}

func (a *StreamAggregator) applyFunctionItem(item map[string]any, emitStart bool) map[string]any {
	itemType, _ := item["type"].(string)
	if itemType != "function_call" {
		return nil
	}
	callID, _ := item["call_id"].(string)
	if callID == "" {
		callID, _ = item["id"].(string)
	}
	name, _ := item["name"].(string)
	args, _ := item["arguments"].(string)
	if callID == "" {
		return nil
	}
	itemID, _ := item["id"].(string)
	if itemID != "" {
		a.activeToolItemID[itemID] = callID
	}
	a.ensureToolCall(callID, name, args)
	if !emitStart {
		return nil
	}
	return a.toolDelta(callID, name, "")
}

func (a *StreamAggregator) applyMessageItem(item map[string]any) {
	itemType, _ := item["type"].(string)
	switch itemType {
	case "message":
		content, ok := item["content"].([]any)
		if !ok {
			return
		}
		for _, part := range content {
			m, ok := part.(map[string]any)
			if !ok {
				continue
			}
			text, _ := m["text"].(string)
			if text != "" && a.text == "" {
				a.text = text
			}
		}
	case "reasoning":
		summary, ok := item["summary"].([]any)
		if !ok {
			return
		}
		for _, part := range summary {
			m, ok := part.(map[string]any)
			if !ok {
				continue
			}
			text, _ := m["text"].(string)
			if text != "" {
				a.reasoning += text
			}
		}
	default:
		return
	}
}

func (a *StreamAggregator) applyCompletedResponse(response map[string]any) {
	if usage, ok := response["usage"]; ok {
		a.usage = toOpenAIUsage(usage)
	}
	if output, ok := response["output"].([]any); ok {
		for _, item := range output {
			if m, ok := item.(map[string]any); ok {
				a.applyFunctionItem(m, false)
				a.applyMessageItem(m)
			}
		}
	}
}

func toOpenAIUsage(raw any) any {
	usage, ok := raw.(map[string]any)
	if !ok {
		return raw
	}
	result := map[string]any{
		"prompt_tokens":     numberOrZero(usage["input_tokens"]),
		"completion_tokens": numberOrZero(usage["output_tokens"]),
		"total_tokens":      numberOrZero(usage["total_tokens"]),
	}
	if inputDetails, ok := usage["input_tokens_details"].(map[string]any); ok {
		if cached := numberOrZero(inputDetails["cached_tokens"]); cached != 0 {
			result["prompt_tokens_details"] = map[string]any{"cached_tokens": cached}
		}
	}
	if outputDetails, ok := usage["output_tokens_details"].(map[string]any); ok {
		if reasoning := numberOrZero(outputDetails["reasoning_tokens"]); reasoning != 0 {
			result["completion_tokens_details"] = map[string]any{"reasoning_tokens": reasoning}
		}
	}
	return result
}

func numberOrZero(value any) int64 {
	switch v := value.(type) {
	case float64:
		return int64(v)
	case int64:
		return v
	case int:
		return int64(v)
	case json.Number:
		n, _ := v.Int64()
		return n
	default:
		return 0
	}
}

func (a *StreamAggregator) callIDForRaw(raw map[string]any) string {
	if callID, _ := raw["call_id"].(string); callID != "" {
		return callID
	}
	if itemID, _ := raw["item_id"].(string); itemID != "" {
		if callID := a.activeToolItemID[itemID]; callID != "" {
			return callID
		}
		return itemID
	}
	if outputIndex, ok := raw["output_index"].(float64); ok {
		idx := int(outputIndex)
		if existing := a.syntheticByOutput[idx]; existing != "" {
			return existing
		}
		a.nextSyntheticTool++
		key := syntheticCallIDPrefix + strconv.Itoa(a.nextSyntheticTool)
		a.syntheticByOutput[idx] = key
		return key
	}
	a.nextSyntheticTool++
	return syntheticCallIDPrefix + strconv.Itoa(a.nextSyntheticTool)
}

func (a *StreamAggregator) ensureToolCall(callID, name, args string) {
	if existing, ok := a.toolCalls[callID]; ok {
		if existing.Function.Name == "" {
			existing.Function.Name = name
		}
		if existing.Function.Arguments == "" {
			existing.Function.Arguments = args
		}
		return
	}
	a.toolOrder = append(a.toolOrder, callID)
	a.toolCalls[callID] = &OpenAIToolCallOut{
		ID:   callID,
		Type: "function",
		Function: OpenAIFunctionCall{
			Name:      name,
			Arguments: args,
		},
	}
}

func (a *StreamAggregator) toolDelta(callID, name, args string) map[string]any {
	index := 0
	for i, id := range a.toolOrder {
		if id == callID {
			index = i
			break
		}
	}
	fn := map[string]any{"arguments": args}
	if name != "" {
		fn["name"] = name
	}
	delta := map[string]any{
		"index":    index,
		"function": fn,
	}
	if name != "" {
		delta["id"] = callID
		delta["type"] = "function"
	}
	return delta
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeOpenAIError(w http.ResponseWriter, status int, message, typ string) {
	writeJSON(w, status, map[string]any{
		"error": map[string]any{
			"message": message,
			"type":    typ,
		},
	})
}

func streamChunk(id string, created int64, model string, delta map[string]any, finishReason any) map[string]any {
	return map[string]any{
		"id":      id,
		"object":  "chat.completion.chunk",
		"created": created,
		"model":   model,
		"choices": []any{map[string]any{
			"index":         0,
			"delta":         delta,
			"finish_reason": finishReason,
		}},
	}
}

func drainAndClose(resp *http.Response) {
	if resp == nil || resp.Body == nil {
		return
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	_ = resp.Body.Close()
}

func newID(prefix string) string {
	return prefix + strconv.FormatInt(time.Now().UnixNano(), 36)
}
