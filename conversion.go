package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

type ChatCompletionRequest struct {
	Model      string          `json:"model"`
	Messages   []ChatMessage   `json:"messages"`
	Stream     bool            `json:"stream"`
	Tools      []ChatTool      `json:"tools,omitempty"`
	ToolChoice json.RawMessage `json:"tool_choice,omitempty"`
	// Sampler params: parsed for vanilla-OpenAI client compatibility but never
	// forwarded — the ChatGPT-account Codex backend rejects all three with
	// HTTP 400 "Unsupported parameter". Dropping mirrors max_tokens treatment.
	// See KNOWN_INCOMPATIBILITIES.md.
	Temperature      *float64 `json:"temperature,omitempty"`
	TopP             *float64 `json:"top_p,omitempty"`
	Stop             any      `json:"stop,omitempty"`
	PresencePenalty  *float64 `json:"presence_penalty,omitempty"`
	FrequencyPenalty *float64 `json:"frequency_penalty,omitempty"`
	Seed             *int     `json:"seed,omitempty"`
	Logprobs         *bool    `json:"logprobs,omitempty"`
	TopLogprobs      *int     `json:"top_logprobs,omitempty"`
	// Token caps: parsed but never forwarded — Codex rejects max_output_tokens
	// with HTTP 400 and the upstream Codex CLI never sends it.
	MaxTokens           *int `json:"max_tokens,omitempty"`
	MaxCompletionTokens *int `json:"max_completion_tokens,omitempty"`
	// N is rejected up front when >1: Codex Responses always returns a single
	// output, so silently dropping extra choices would mislead clients.
	N *int `json:"n,omitempty"`
	// ResponseFormat is validated up front: json_object / json_schema cannot
	// be enforced through the Codex backend, so they error rather than
	// silently producing free-form text.
	ResponseFormat json.RawMessage `json:"response_format,omitempty"`
	// Modalities are validated up front: Codex only produces text, so any
	// value other than ["text"] (or omitted) errors. Silent drop would let a
	// client request audio and receive text with no signal.
	Modalities []string `json:"modalities,omitempty"`
	// StreamOptions.IncludeUsage controls whether the final SSE chunk carries
	// the usage block. Parsed so streamChat can match OpenAI's default-off
	// behavior instead of always emitting the chunk.
	StreamOptions     *StreamOptions `json:"stream_options,omitempty"`
	ParallelToolCalls *bool          `json:"parallel_tool_calls,omitempty"`
	ReasoningEffort   string         `json:"reasoning_effort,omitempty"`
}

type StreamOptions struct {
	IncludeUsage bool `json:"include_usage,omitempty"`
}

type ChatMessage struct {
	Role       string          `json:"role"`
	Content    json.RawMessage `json:"content,omitempty"`
	ToolCalls  []ChatToolCall  `json:"tool_calls,omitempty"`
	ToolCallID string          `json:"tool_call_id,omitempty"`
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

type responsesRequest struct {
	Model             string              `json:"model"`
	Input             []any               `json:"input"`
	Stream            bool                `json:"stream"`
	Store             bool                `json:"store"`
	Instructions      string              `json:"instructions,omitempty"`
	Tools             []responsesTool     `json:"tools,omitempty"`
	ToolChoice        any                 `json:"tool_choice,omitempty"`
	ParallelToolCalls *bool               `json:"parallel_tool_calls,omitempty"`
	Reasoning         *responsesReasoning `json:"reasoning,omitempty"`
}

type responsesReasoning struct {
	Effort string `json:"effort,omitempty"`
}

type responsesTool struct {
	Type        string          `json:"type"`
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
}

type responsesToolChoiceFunction struct {
	Type string `json:"type"`
	Name string `json:"name"`
}

type responsesMessageItem struct {
	Type    string                 `json:"type"`
	Role    string                 `json:"role"`
	Content []responsesContentPart `json:"content"`
}

type responsesContentPart struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type responsesFunctionCallItem struct {
	Type      string `json:"type"`
	CallID    string `json:"call_id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type responsesFunctionCallOutputItem struct {
	Type   string `json:"type"`
	CallID string `json:"call_id"`
	Output string `json:"output"`
}

// chatContentPart mirrors the OpenAI content-part schema for messages whose
// `content` is an array. Only `text` parts are accepted — image_url, input_audio,
// refusal, etc. are rejected up front so the bridge never silently drops content.
type chatContentPart struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// placeholderInstructions is sent to the Codex backend whenever a request has
// no system/developer messages. Codex 400s on empty instructions even though
// vanilla OpenAI accepts a user-only prompt — see KNOWN_INCOMPATIBILITIES.md.
const placeholderInstructions = "You are a helpful assistant."

func toResponsesRequest(req ChatCompletionRequest) (responsesRequest, error) {
	if req.N != nil && *req.N > 1 {
		return responsesRequest{}, fmt.Errorf("`n` > 1 is not supported (Codex returns a single output)")
	}
	if err := validateResponseFormat(req.ResponseFormat); err != nil {
		return responsesRequest{}, err
	}
	if err := validateModalities(req.Modalities); err != nil {
		return responsesRequest{}, err
	}
	instructions, err := collectInstructions(req.Messages)
	if err != nil {
		return responsesRequest{}, fmt.Errorf("system/developer message: %w", err)
	}
	input, err := toResponsesInput(req.Messages)
	if err != nil {
		return responsesRequest{}, err
	}
	// Codex requires both `instructions` and `input` to be non-empty. Vanilla
	// OpenAI accepts a system-only or user-only prompt, so synthesize a
	// placeholder user turn. The system text travels via `instructions` only;
	// folding it into `input` too would make the model see it twice.
	if len(input) == 0 {
		input = append(input, responseMessage("user", "Please respond."))
	}
	if instructions == "" {
		instructions = placeholderInstructions
	}
	tools, err := toResponsesTools(req.Tools)
	if err != nil {
		return responsesRequest{}, err
	}
	body := responsesRequest{
		Model:             req.Model,
		Input:             input,
		Stream:            true,
		Store:             false,
		Instructions:      instructions,
		Tools:             tools,
		ParallelToolCalls: req.ParallelToolCalls,
	}
	if len(req.ToolChoice) > 0 {
		choice, err := toResponsesToolChoice(req.ToolChoice)
		if err != nil {
			return responsesRequest{}, err
		}
		body.ToolChoice = choice
	}
	if req.ReasoningEffort != "" {
		body.Reasoning = &responsesReasoning{Effort: req.ReasoningEffort}
	}
	return body, nil
}

// validateModalities rejects modality lists the Codex backend cannot produce.
// Codex Responses returns text only, so a client asking for audio output must
// see a 400 instead of silently receiving text.
func validateModalities(modalities []string) error {
	for _, m := range modalities {
		if m != "text" {
			return fmt.Errorf("modalities: %q is not supported (Codex produces text only)", m)
		}
	}
	return nil
}

// validateResponseFormat rejects response_format types the Codex backend cannot
// enforce. Silent acceptance would return free-form text to a client expecting
// guaranteed JSON — worst possible failure mode.
func validateResponseFormat(raw json.RawMessage) error {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return nil
	}
	var format struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(trimmed, &format); err != nil {
		return fmt.Errorf("response_format: %w", err)
	}
	switch format.Type {
	case "", "text":
		return nil
	case "json_object", "json_schema":
		return fmt.Errorf("response_format type %q is not supported by the Codex backend", format.Type)
	default:
		return fmt.Errorf("response_format: unknown type %q", format.Type)
	}
}

func collectInstructions(messages []ChatMessage) (string, error) {
	var parts []string
	for i, msg := range messages {
		if msg.Role != "system" && msg.Role != "developer" {
			continue
		}
		text, err := contentText(msg.Content)
		if err != nil {
			return "", fmt.Errorf("messages[%d].content: %w", i, err)
		}
		if text != "" {
			parts = append(parts, text)
		}
	}
	return strings.Join(parts, "\n\n"), nil
}

func toResponsesInput(messages []ChatMessage) ([]any, error) {
	input := make([]any, 0, len(messages))
	for i, msg := range messages {
		switch msg.Role {
		case "system", "developer":
			continue
		case "assistant":
			text, err := contentText(msg.Content)
			if err != nil {
				return nil, fmt.Errorf("messages[%d].content: %w", i, err)
			}
			if text != "" {
				input = append(input, responseMessage("assistant", text))
			}
			for j, call := range msg.ToolCalls {
				if call.ID == "" {
					return nil, fmt.Errorf("messages[%d].tool_calls[%d]: id is required", i, j)
				}
				if call.Function.Name == "" {
					return nil, fmt.Errorf("messages[%d].tool_calls[%d].function.name is required", i, j)
				}
				input = append(input, responsesFunctionCallItem{
					Type:      "function_call",
					CallID:    call.ID,
					Name:      call.Function.Name,
					Arguments: call.Function.Arguments,
				})
			}
		case "tool":
			if msg.ToolCallID == "" {
				return nil, fmt.Errorf("messages[%d]: tool_call_id is required for tool role", i)
			}
			text, err := contentText(msg.Content)
			if err != nil {
				return nil, fmt.Errorf("messages[%d].content: %w", i, err)
			}
			input = append(input, responsesFunctionCallOutputItem{
				Type:   "function_call_output",
				CallID: msg.ToolCallID,
				Output: text,
			})
		case "user":
			text, err := contentText(msg.Content)
			if err != nil {
				return nil, fmt.Errorf("messages[%d].content: %w", i, err)
			}
			input = append(input, responseMessage("user", text))
		case "":
			return nil, fmt.Errorf("messages[%d]: role is required", i)
		default:
			return nil, fmt.Errorf("messages[%d]: unsupported role %q", i, msg.Role)
		}
	}
	return input, nil
}

func responseMessage(role, text string) responsesMessageItem {
	contentType := "input_text"
	if role == "assistant" {
		contentType = "output_text"
	}
	return responsesMessageItem{
		Type: "message",
		Role: role,
		Content: []responsesContentPart{{
			Type: contentType,
			Text: text,
		}},
	}
}

// contentText extracts the textual portion of a Chat Completions `content`
// field. The OpenAI schema allows either a JSON string or an array of typed
// content parts; any other shape (object, number, etc.) is rejected so a
// malformed request produces a 400 rather than silently being JSON-serialised
// back into the model prompt. Within an array, only `text` parts are accepted —
// image_url / input_audio / refusal etc. error out so content is never silently
// dropped.
func contentText(raw json.RawMessage) (string, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return "", nil
	}
	switch trimmed[0] {
	case '"':
		var s string
		if err := json.Unmarshal(trimmed, &s); err != nil {
			return "", fmt.Errorf("invalid string content: %w", err)
		}
		return s, nil
	case '[':
		var parts []chatContentPart
		if err := json.Unmarshal(trimmed, &parts); err != nil {
			return "", fmt.Errorf("invalid content parts: %w", err)
		}
		var b strings.Builder
		for _, part := range parts {
			if part.Type != "" && part.Type != "text" {
				return "", fmt.Errorf("unsupported content part type %q", part.Type)
			}
			b.WriteString(part.Text)
		}
		return b.String(), nil
	default:
		return "", errors.New("content must be a string or an array of content parts")
	}
}

func toResponsesTools(tools []ChatTool) ([]responsesTool, error) {
	if len(tools) == 0 {
		return nil, nil
	}
	result := make([]responsesTool, 0, len(tools))
	for i, tool := range tools {
		if tool.Type != "function" {
			return nil, fmt.Errorf("tools[%d]: unsupported tool type %q (only \"function\" is supported)", i, tool.Type)
		}
		if tool.Function.Name == "" {
			return nil, fmt.Errorf("tools[%d]: function.name is required", i)
		}
		item := responsesTool{
			Type:        "function",
			Name:        tool.Function.Name,
			Description: tool.Function.Description,
		}
		if len(tool.Function.Parameters) > 0 {
			var params json.RawMessage
			if err := json.Unmarshal(tool.Function.Parameters, &params); err != nil {
				return nil, fmt.Errorf("tools[%d].function.parameters: %w", i, err)
			}
			item.Parameters = params
		}
		result = append(result, item)
	}
	return result, nil
}

// toResponsesToolChoice converts the OpenAI Chat Completions `tool_choice`
// field to the Codex Responses form. The OpenAI schema permits either one of
// the string literals "auto" | "none" | "required", or an object of the form
// {"type":"function","function":{"name":"..."}}. Anything else is rejected so
// that a malformed client request surfaces as a 400 rather than silently
// becoming "auto" and changing tool-selection behaviour.
func toResponsesToolChoice(raw json.RawMessage) (any, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return nil, errors.New("tool_choice: empty")
	}
	switch trimmed[0] {
	case '"':
		var s string
		if err := json.Unmarshal(trimmed, &s); err != nil {
			return nil, fmt.Errorf("tool_choice: %w", err)
		}
		switch s {
		case "auto", "none", "required":
			return s, nil
		default:
			return nil, fmt.Errorf("tool_choice: unknown value %q", s)
		}
	case '{':
		var obj struct {
			Type     string `json:"type"`
			Function struct {
				Name string `json:"name"`
			} `json:"function"`
		}
		if err := json.Unmarshal(trimmed, &obj); err != nil {
			return nil, fmt.Errorf("tool_choice: %w", err)
		}
		if obj.Type != "function" {
			return nil, fmt.Errorf("tool_choice: expected type=\"function\", got %q", obj.Type)
		}
		if obj.Function.Name == "" {
			return nil, errors.New("tool_choice: function.name is required")
		}
		return responsesToolChoiceFunction{Type: "function", Name: obj.Function.Name}, nil
	default:
		return nil, errors.New("tool_choice: must be a string or {type, function} object")
	}
}
