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
			{Role: "system", Content: json.RawMessage(`"be brief"`)},
			{Role: "user", Content: json.RawMessage(`"read config"`)},
			{Role: "assistant", Content: json.RawMessage(`""`), ToolCalls: []ChatToolCall{{
				ID: "call_1",
				Function: ChatToolFunction{
					Name:      "read_file",
					Arguments: `{"path":"config.json"}`,
				},
			}}},
			{Role: "tool", ToolCallID: "call_1", Content: json.RawMessage(`"{\"ok\":true}"`)},
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

	body, err := toResponsesRequest(req)
	if err != nil {
		t.Fatal(err)
	}
	if body.Instructions != "be brief" {
		t.Fatalf("instructions = %q", body.Instructions)
	}
	if body.Model != "gpt-test" || body.Stream != true || body.Store != false {
		t.Fatalf("unexpected common body fields: %#v", body)
	}
	if body.ParallelToolCalls == nil || *body.ParallelToolCalls != false {
		t.Fatalf("parallel_tool_calls = %#v, want pointer to false", body.ParallelToolCalls)
	}
	if len(body.Input) != 3 {
		t.Fatalf("input length = %d", len(body.Input))
	}
	if _, ok := body.Input[1].(responsesFunctionCallItem); !ok {
		t.Fatalf("assistant tool call not converted: %#v", body.Input[1])
	}
	if _, ok := body.Input[2].(responsesFunctionCallOutputItem); !ok {
		t.Fatalf("tool output not converted: %#v", body.Input[2])
	}
	if len(body.Tools) == 0 || body.Tools[0].Name != "read_file" {
		t.Fatalf("tools = %#v", body.Tools)
	}
	choice, ok := body.ToolChoice.(responsesToolChoiceFunction)
	if !ok || choice.Name != "read_file" {
		t.Fatalf("tool choice = %#v", body.ToolChoice)
	}
}

// contentText is the single rejection point for unsupported chat content parts
// (image_url, input_audio, refusal, ...). Test it directly here; the same path
// is what toResponsesRequest invokes for every message body.
func TestContentTextRejectsUnsupportedParts(t *testing.T) {
	cases := []struct {
		name    string
		content json.RawMessage
		wantErr bool
	}{
		{"plain string ok", json.RawMessage(`"hello"`), false},
		{"text parts ok", json.RawMessage(`[{"type":"text","text":"hello"}]`), false},
		{"image_url rejected", json.RawMessage(`[{"type":"image_url","image_url":{"url":"x"}}]`), true},
		{"input_audio rejected", json.RawMessage(`[{"type":"input_audio"}]`), true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := contentText(c.content)
			if (err != nil) != c.wantErr {
				t.Fatalf("err = %v, wantErr = %v", err, c.wantErr)
			}
		})
	}
}

func TestContentTextHandlesArrayTextParts(t *testing.T) {
	got, err := contentText(json.RawMessage(`[{"type":"text","text":"hello"},{"type":"text","text":" world"}]`))
	if err != nil {
		t.Fatal(err)
	}
	if got != "hello world" {
		t.Fatalf("content = %q", got)
	}
}

func TestContentTextRejectsObject(t *testing.T) {
	if _, err := contentText(json.RawMessage(`{"unexpected":true}`)); err == nil {
		t.Fatal("expected error for object content")
	}
}

func TestToResponsesToolChoiceRejectsMalformed(t *testing.T) {
	cases := []string{
		`"any"`,
		`{"type":"function"}`,
		`{"type":"function","function":{"name":""}}`,
		`42`,
	}
	for _, c := range cases {
		if _, err := toResponsesToolChoice(json.RawMessage(c)); err == nil {
			t.Errorf("expected error for %s", c)
		}
	}
}

func TestToResponsesToolsRejectsNonFunction(t *testing.T) {
	_, err := toResponsesTools([]ChatTool{{Type: "code_interpreter"}})
	if err == nil {
		t.Fatal("expected error for unsupported tool type")
	}
}

func TestToResponsesInputRejectsEmptyRole(t *testing.T) {
	_, err := toResponsesInput([]ChatMessage{{Role: "", Content: json.RawMessage(`"hi"`)}})
	if err == nil {
		t.Fatal("expected error for empty role")
	}
}
