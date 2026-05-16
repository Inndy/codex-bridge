package main

import (
	"encoding/json"
	"testing"
)

func TestToResponsesRequestConvertsMessagesAndTools(t *testing.T) {
	rawChoice := json.RawMessage(`{"type":"function","function":{"name":"read_file"}}`)
	req := ChatCompletionRequest{
		Model:             "gpt-test",
		Stream:            false,
		ToolChoice:        rawChoice,
		ParallelToolCalls: new(false),
		Messages: []ChatMessage{
			{Role: "system", Content: "be brief"},
			{Role: "user", Content: "read config"},
			{Role: "assistant", Content: "", ToolCalls: []ChatToolCall{{
				ID: "call_1",
				Function: ChatToolFunction{
					Name:      "read_file",
					Arguments: `{"path":"config.json"}`,
				},
			}}},
			{Role: "tool", ToolCallID: "call_1", Content: `{"ok":true}`},
		},
		Tools: []ChatTool{{
			Type: "function",
			Function: ChatToolFunction{
				Name:        "read_file",
				Description: "read file",
				Parameters:  json.RawMessage(`{"type":"object"}`),
			},
		}},
	}

	body := toResponsesRequest(req)
	if body["instructions"] != "be brief" {
		t.Fatalf("instructions = %#v", body["instructions"])
	}
	if body["model"] != "gpt-test" || body["stream"] != true || body["store"] != false {
		t.Fatalf("unexpected common body fields: %#v", body)
	}
	if _, ok := body["parallel_tool_calls"]; ok {
		t.Fatalf("parallel_tool_calls should be stripped for codex: %#v", body)
	}
	input := body["input"].([]any)
	if len(input) != 3 {
		t.Fatalf("input length = %d", len(input))
	}
	if input[1].(map[string]any)["type"] != "function_call" {
		t.Fatalf("assistant tool call not converted: %#v", input[1])
	}
	if input[2].(map[string]any)["type"] != "function_call_output" {
		t.Fatalf("tool output not converted: %#v", input[2])
	}
	tools := body["tools"].([]any)
	if tools[0].(map[string]any)["name"] != "read_file" {
		t.Fatalf("tools = %#v", tools)
	}
	choice := body["tool_choice"].(map[string]any)
	if choice["name"] != "read_file" {
		t.Fatalf("tool choice = %#v", choice)
	}
}

func TestContentTextHandlesArrayTextParts(t *testing.T) {
	got := contentText([]any{
		map[string]any{"type": "text", "text": "hello"},
		map[string]any{"type": "image_url", "image_url": map[string]any{"url": "x"}},
		map[string]any{"type": "text", "text": " world"},
	})
	if got != "hello world" {
		t.Fatalf("content = %q", got)
	}
}
