package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
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
	logger            *slog.Logger
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

type streamEvent struct {
	Type        string             `json:"type"`
	Delta       string             `json:"delta"`
	CallID      string             `json:"call_id"`
	ItemID      string             `json:"item_id"`
	OutputIndex *int               `json:"output_index"`
	Item        *responseItem      `json:"item"`
	Response    *completedResponse `json:"response"`
}

type responseItem struct {
	Type      string        `json:"type"`
	ID        string        `json:"id"`
	CallID    string        `json:"call_id"`
	Name      string        `json:"name"`
	Arguments string        `json:"arguments"`
	Content   []contentPart `json:"content"`
	Summary   []contentPart `json:"summary"`
}

type contentPart struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type completedResponse struct {
	Status            string             `json:"status"`
	Usage             json.RawMessage    `json:"usage"`
	Output            []responseItem     `json:"output"`
	IncompleteDetails *incompleteDetails `json:"incomplete_details"`
}

type incompleteDetails struct {
	Reason string `json:"reason"`
}

// syntheticCallIDPrefix is the prefix used for fabricated tool-call IDs when
// the upstream Codex stream omits a real call_id. Using a distinct prefix from
// upstream's `call_...` namespace means the aggregator can never collapse a
// synthetic call onto a real one, and a client echoing it back as
// tool_call_id surfaces as an upstream rejection instead of silent corruption.
const syntheticCallIDPrefix = "synth_call_"

func NewStreamAggregator(logger *slog.Logger) *StreamAggregator {
	if logger == nil {
		logger = slog.Default()
	}
	return &StreamAggregator{
		logger:            logger,
		toolCalls:         map[string]*OpenAIToolCallOut{},
		activeToolItemID:  map[string]string{},
		syntheticByOutput: map[int]string{},
	}
}

func (a *StreamAggregator) ApplyEvent(event SSEEvent) ([]map[string]any, error) {
	if event.Data == "" || event.Data == "[DONE]" {
		return nil, nil
	}
	var ev streamEvent
	if err := json.Unmarshal([]byte(event.Data), &ev); err != nil {
		a.logger.Warn("upstream SSE event failed schema decode", "error", err, "data", event.Data)
		return nil, err
	}
	eventType := ev.Type
	if eventType == "" {
		eventType = event.Event
	}

	var chunks []map[string]any
	switch eventType {
	case "response.output_text.delta":
		if ev.Delta != "" {
			a.text += ev.Delta
			chunks = append(chunks, map[string]any{"content": ev.Delta})
		}
	case "response.reasoning_summary_text.delta", "response.reasoning_text.delta":
		if ev.Delta != "" {
			a.reasoning += ev.Delta
			chunks = append(chunks, map[string]any{"reasoning_content": ev.Delta})
		}
	case "response.function_call_arguments.delta":
		callID := a.callIDFor(ev)
		if callID != "" && ev.Delta != "" {
			a.ensureToolCall(callID, "", "")
			a.toolCalls[callID].Function.Arguments += ev.Delta
			chunks = append(chunks, map[string]any{
				"tool_calls": []any{a.toolDelta(callID, "", ev.Delta)},
			})
		}
	case "response.output_item.added":
		if ev.Item != nil {
			if delta := a.applyFunctionItem(ev.Item, true); delta != nil {
				chunks = append(chunks, map[string]any{"tool_calls": []any{delta}})
			}
		}
	case "response.output_item.done":
		if ev.Item != nil {
			a.applyFunctionItem(ev.Item, false)
			a.applyMessageItem(ev.Item)
		}
	case "response.completed":
		if ev.Response != nil {
			a.applyCompletedResponse(ev.Response)
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

func (a *StreamAggregator) applyFunctionItem(item *responseItem, emitStart bool) map[string]any {
	if item.Type != "function_call" {
		return nil
	}
	callID := item.CallID
	if callID == "" {
		callID = item.ID
	}
	if callID == "" {
		return nil
	}
	if item.ID != "" {
		a.activeToolItemID[item.ID] = callID
	}
	a.ensureToolCall(callID, item.Name, item.Arguments)
	if !emitStart {
		return nil
	}
	return a.toolDelta(callID, item.Name, "")
}

func (a *StreamAggregator) applyMessageItem(item *responseItem) {
	switch item.Type {
	case "message":
		// Streaming output_text deltas accumulate into a.text as they arrive.
		// When the terminal response.completed event later replays the full
		// message item, we keep the delta-accumulated text rather than
		// replacing it: stream order is authoritative, and duplicating both
		// would emit the body twice. Only fall back to the final message when
		// no deltas were seen.
		if a.text != "" {
			return
		}
		var b strings.Builder
		for _, part := range item.Content {
			if part.Text != "" {
				b.WriteString(part.Text)
			}
		}
		a.text = b.String()
	case "reasoning":
		for _, part := range item.Summary {
			if part.Text != "" {
				a.reasoning += part.Text
			}
		}
	}
}

func (a *StreamAggregator) applyCompletedResponse(response *completedResponse) {
	if len(response.Usage) > 0 {
		var raw any
		if err := json.Unmarshal(response.Usage, &raw); err == nil {
			a.usage = toOpenAIUsage(raw)
		} else {
			a.logger.Warn("upstream usage payload failed to decode", "error", err)
		}
	}
	for i := range response.Output {
		item := &response.Output[i]
		a.applyFunctionItem(item, false)
		a.applyMessageItem(item)
	}
	if finish := finishReasonFromResponse(response); finish != "" {
		a.finishReason = finish
	}
}

// finishReasonFromResponse maps a Codex/Responses-API terminal status onto an
// OpenAI Chat Completions finish_reason. "completed" returns the empty string
// so the caller's tool_calls-vs-stop heuristic still applies.
func finishReasonFromResponse(response *completedResponse) string {
	switch response.Status {
	case "incomplete":
		reason := ""
		if response.IncompleteDetails != nil {
			reason = response.IncompleteDetails.Reason
		}
		switch reason {
		case "content_filter":
			return "content_filter"
		default:
			return "length"
		}
	case "failed", "cancelled":
		return "stop"
	default:
		return ""
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

func (a *StreamAggregator) callIDFor(ev streamEvent) string {
	if ev.CallID != "" {
		return ev.CallID
	}
	if ev.ItemID != "" {
		if callID := a.activeToolItemID[ev.ItemID]; callID != "" {
			return callID
		}
		return ev.ItemID
	}
	if ev.OutputIndex != nil {
		idx := *ev.OutputIndex
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
