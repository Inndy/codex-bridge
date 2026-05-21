package main

import (
	"bytes"
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

// Codex rejects temperature/top_p/stop with HTTP 400 even though vanilla
// OpenAI accepts them. The bridge must parse-and-drop, never forward.
func TestToResponsesRequestDropsUnsupportedSamplerParams(t *testing.T) {
	temp, topP := 0.7, 0.9
	req := ChatCompletionRequest{
		Model:       "gpt-test",
		Temperature: &temp,
		TopP:        &topP,
		Stop:        []string{"\n"},
		Messages: []ChatMessage{
			{Role: "system", Content: json.RawMessage(`"be brief"`)},
			{Role: "user", Content: json.RawMessage(`"hi"`)},
		},
	}
	body, err := toResponsesRequest(req)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	for _, banned := range []string{`"temperature"`, `"top_p"`, `"stop"`} {
		if bytes.Contains(encoded, []byte(banned)) {
			t.Fatalf("forwarded body must not contain %s: %s", banned, encoded)
		}
	}
}

func TestToResponsesRequestSynthesizesMissingInstructions(t *testing.T) {
	req := ChatCompletionRequest{
		Model: "gpt-test",
		Messages: []ChatMessage{
			{Role: "user", Content: json.RawMessage(`"hi"`)},
		},
	}
	body, err := toResponsesRequest(req)
	if err != nil {
		t.Fatal(err)
	}
	if body.Instructions == "" {
		t.Fatal("expected synthesized instructions for user-only prompt, got empty")
	}
	if len(body.Input) != 1 {
		t.Fatalf("expected user input preserved, got %d items", len(body.Input))
	}
}

func TestToResponsesRequestFoldsSystemOnlyPromptIntoInput(t *testing.T) {
	req := ChatCompletionRequest{
		Model: "gpt-test",
		Messages: []ChatMessage{
			{Role: "system", Content: json.RawMessage(`"reply OK"`)},
		},
	}
	body, err := toResponsesRequest(req)
	if err != nil {
		t.Fatal(err)
	}
	if len(body.Input) == 0 {
		t.Fatal("expected system content folded into input, got empty input")
	}
	msg, ok := body.Input[0].(responsesMessageItem)
	if !ok || msg.Role != "user" {
		t.Fatalf("expected folded user message, got %#v", body.Input[0])
	}
	if len(msg.Content) == 0 || msg.Content[0].Text != "reply OK" {
		t.Fatalf("expected system text in folded input, got %#v", msg.Content)
	}
	if body.Instructions == "" {
		t.Fatal("expected placeholder instructions, got empty")
	}
}

func TestToResponsesRequestRejectsNGreaterThanOne(t *testing.T) {
	n := 2
	req := ChatCompletionRequest{
		Model:    "gpt-test",
		N:        &n,
		Messages: []ChatMessage{{Role: "user", Content: json.RawMessage(`"hi"`)}},
	}
	if _, err := toResponsesRequest(req); err == nil {
		t.Fatal("expected error for n>1")
	}
}

func TestToResponsesRequestAllowsNEqualOne(t *testing.T) {
	n := 1
	req := ChatCompletionRequest{
		Model:    "gpt-test",
		N:        &n,
		Messages: []ChatMessage{{Role: "user", Content: json.RawMessage(`"hi"`)}},
	}
	if _, err := toResponsesRequest(req); err != nil {
		t.Fatalf("n=1 must be accepted, got %v", err)
	}
}

func TestValidateResponseFormat(t *testing.T) {
	cases := []struct {
		name    string
		raw     string
		wantErr bool
	}{
		{"empty", ``, false},
		{"null", `null`, false},
		{"text", `{"type":"text"}`, false},
		{"json_object rejected", `{"type":"json_object"}`, true},
		{"json_schema rejected", `{"type":"json_schema","json_schema":{"name":"x","schema":{}}}`, true},
		{"unknown rejected", `{"type":"bogus"}`, true},
		{"malformed rejected", `[`, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := validateResponseFormat(json.RawMessage(c.raw))
			if (err != nil) != c.wantErr {
				t.Fatalf("err = %v, wantErr = %v", err, c.wantErr)
			}
		})
	}
}

// Extra OpenAI fields the bridge accepts for schema compatibility but never
// acts on. JSON decode must not error so vanilla clients keep working.
func TestChatCompletionRequestParsesCompatFields(t *testing.T) {
	raw := []byte(`{
		"model":"gpt-test",
		"messages":[{"role":"user","content":"hi"}],
		"presence_penalty":0.1,
		"frequency_penalty":0.2,
		"seed":42,
		"logprobs":true,
		"top_logprobs":5,
		"max_completion_tokens":16
	}`)
	var req ChatCompletionRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		t.Fatal(err)
	}
	if req.Seed == nil || *req.Seed != 42 {
		t.Fatalf("seed = %#v", req.Seed)
	}
	if req.MaxCompletionTokens == nil || *req.MaxCompletionTokens != 16 {
		t.Fatalf("max_completion_tokens = %#v", req.MaxCompletionTokens)
	}
	if req.Logprobs == nil || !*req.Logprobs {
		t.Fatalf("logprobs = %#v", req.Logprobs)
	}
}
