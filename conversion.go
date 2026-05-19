package main

import (
	"encoding/json"
	"strings"
)

type ChatCompletionRequest struct {
	Model       string          `json:"model"`
	Messages    []ChatMessage   `json:"messages"`
	Stream      bool            `json:"stream"`
	Tools       []ChatTool      `json:"tools,omitempty"`
	ToolChoice  json.RawMessage `json:"tool_choice,omitempty"`
	Temperature *float64        `json:"temperature,omitempty"`
	TopP        *float64        `json:"top_p,omitempty"`
	Stop        any             `json:"stop,omitempty"`
	// MaxTokens is parsed for compatibility but never forwarded: the
	// ChatGPT-account Codex backend rejects max_output_tokens with HTTP 400
	// "Unsupported parameter", and the upstream Codex CLI never sends it.
	MaxTokens         *int   `json:"max_tokens,omitempty"`
	ParallelToolCalls *bool  `json:"parallel_tool_calls,omitempty"`
	ReasoningEffort   string `json:"reasoning_effort,omitempty"`
}

type ChatMessage struct {
	Role       string         `json:"role"`
	Content    any            `json:"content,omitempty"`
	ToolCalls  []ChatToolCall `json:"tool_calls,omitempty"`
	ToolCallID string         `json:"tool_call_id,omitempty"`
}

type ChatToolCall struct {
	ID       string           `json:"id"`
	Type     string           `json:"type"`
	Function ChatToolFunction `json:"function"`
}

type ChatToolFunction struct {
	Name        string          `json:"name,omitempty"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
	Arguments   string          `json:"arguments,omitempty"`
}

type ChatTool struct {
	Type     string           `json:"type"`
	Function ChatToolFunction `json:"function"`
}

func toResponsesRequest(req ChatCompletionRequest) map[string]any {
	body := map[string]any{
		"model":  req.Model,
		"input":  toResponsesInput(req.Messages),
		"stream": true,
		"store":  false,
	}
	if instructions := collectInstructions(req.Messages); instructions != "" {
		body["instructions"] = instructions
	}
	if len(req.Tools) > 0 {
		body["tools"] = toResponsesTools(req.Tools)
	}
	if req.ParallelToolCalls != nil {
		body["parallel_tool_calls"] = *req.ParallelToolCalls
	}
	if len(req.ToolChoice) > 0 {
		if choice := toResponsesToolChoice(req.ToolChoice); choice != nil {
			body["tool_choice"] = choice
		}
	}
	if req.Temperature != nil {
		body["temperature"] = *req.Temperature
	}
	if req.TopP != nil {
		body["top_p"] = *req.TopP
	}
	if req.Stop != nil {
		body["stop"] = req.Stop
	}
	if req.ReasoningEffort != "" {
		body["reasoning"] = map[string]any{"effort": req.ReasoningEffort}
	}
	return body
}

func collectInstructions(messages []ChatMessage) string {
	var parts []string
	for _, msg := range messages {
		if msg.Role == "system" || msg.Role == "developer" {
			text := contentText(msg.Content)
			if text != "" {
				parts = append(parts, text)
			}
		}
	}
	return strings.Join(parts, "\n\n")
}

func toResponsesInput(messages []ChatMessage) []any {
	input := make([]any, 0, len(messages))
	for _, msg := range messages {
		switch msg.Role {
		case "system", "developer":
			continue
		case "assistant":
			text := contentText(msg.Content)
			if text != "" {
				input = append(input, responseMessage("assistant", text))
			}
			for _, call := range msg.ToolCalls {
				if call.ID == "" || call.Function.Name == "" {
					continue
				}
				input = append(input, map[string]any{
					"type":      "function_call",
					"call_id":   call.ID,
					"name":      call.Function.Name,
					"arguments": call.Function.Arguments,
				})
			}
		case "tool":
			if msg.ToolCallID == "" {
				continue
			}
			input = append(input, map[string]any{
				"type":    "function_call_output",
				"call_id": msg.ToolCallID,
				"output":  contentText(msg.Content),
			})
		default:
			role := msg.Role
			if role == "" {
				role = "user"
			}
			input = append(input, responseMessage(role, contentText(msg.Content)))
		}
	}
	return input
}

func responseMessage(role, text string) map[string]any {
	contentType := "input_text"
	if role == "assistant" {
		contentType = "output_text"
	}
	return map[string]any{
		"type": "message",
		"role": role,
		"content": []any{
			map[string]any{
				"type": contentType,
				"text": text,
			},
		},
	}
}

func contentText(content any) string {
	switch value := content.(type) {
	case nil:
		return ""
	case string:
		return value
	case []any:
		var b strings.Builder
		for _, item := range value {
			m, ok := item.(map[string]any)
			if !ok {
				continue
			}
			if m["type"] == "text" {
				if text, ok := m["text"].(string); ok {
					b.WriteString(text)
				}
			}
		}
		return b.String()
	default:
		encoded, err := json.Marshal(value)
		if err != nil {
			return ""
		}
		return string(encoded)
	}
}

func toResponsesTools(tools []ChatTool) []any {
	result := make([]any, 0, len(tools))
	for _, tool := range tools {
		if tool.Type != "function" || tool.Function.Name == "" {
			continue
		}
		item := map[string]any{
			"type": "function",
			"name": tool.Function.Name,
		}
		if tool.Function.Description != "" {
			item["description"] = tool.Function.Description
		}
		if len(tool.Function.Parameters) > 0 {
			var params any
			if json.Unmarshal(tool.Function.Parameters, &params) == nil {
				item["parameters"] = params
			}
		}
		result = append(result, item)
	}
	return result
}

func toResponsesToolChoice(raw json.RawMessage) any {
	var value any
	if json.Unmarshal(raw, &value) != nil {
		return nil
	}
	switch choice := value.(type) {
	case string:
		if choice == "auto" || choice == "none" || choice == "required" {
			return choice
		}
	case map[string]any:
		fn, ok := choice["function"].(map[string]any)
		if !ok {
			return "auto"
		}
		name, _ := fn["name"].(string)
		if name == "" {
			return "auto"
		}
		return map[string]any{"type": "function", "name": name}
	}
	return "auto"
}
