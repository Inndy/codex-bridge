package main

import (
	"strings"
	"testing"
)

func TestStreamAggregatorSyntheticIDsAreDistinct(t *testing.T) {
	agg := NewStreamAggregator()
	events := []string{
		`{"type":"response.function_call_arguments.delta","output_index":0,"delta":"a"}`,
		`{"type":"response.function_call_arguments.delta","output_index":0,"delta":"b"}`,
		`{"type":"response.function_call_arguments.delta","output_index":1,"delta":"c"}`,
		`{"type":"response.completed","response":{"status":"completed"}}`,
	}
	for _, data := range events {
		if _, err := agg.ApplyEvent(SSEEvent{Data: data}); err != nil {
			t.Fatal(err)
		}
	}
	completion := agg.Completion("chatcmpl_test", "gpt-test", 1)
	calls := completion.Choices[0].Message.ToolCalls
	if len(calls) != 2 {
		t.Fatalf("tool calls = %#v", calls)
	}
	for _, call := range calls {
		if !strings.HasPrefix(call.ID, "synth_call_") {
			t.Fatalf("synthetic id missing prefix: %q", call.ID)
		}
	}
	if calls[0].ID == calls[1].ID {
		t.Fatalf("synthetic ids collided: %q == %q", calls[0].ID, calls[1].ID)
	}
	if calls[0].Function.Arguments != "ab" || calls[1].Function.Arguments != "c" {
		t.Fatalf("arguments = %#v / %#v", calls[0].Function.Arguments, calls[1].Function.Arguments)
	}
}

func TestSyntheticAndRealCallIDsDoNotCollide(t *testing.T) {
	agg := NewStreamAggregator()
	events := []string{
		`{"type":"response.function_call_arguments.delta","output_index":0,"delta":"x"}`,
		`{"type":"response.output_item.added","item":{"id":"item_1","type":"function_call","call_id":"call_1","name":"read_file","arguments":""}}`,
		`{"type":"response.function_call_arguments.delta","call_id":"call_1","delta":"y"}`,
		`{"type":"response.completed","response":{"status":"completed"}}`,
	}
	for _, data := range events {
		if _, err := agg.ApplyEvent(SSEEvent{Data: data}); err != nil {
			t.Fatal(err)
		}
	}
	completion := agg.Completion("chatcmpl_test", "gpt-test", 1)
	calls := completion.Choices[0].Message.ToolCalls
	if len(calls) != 2 {
		t.Fatalf("tool calls = %#v", calls)
	}
	ids := map[string]bool{}
	for _, call := range calls {
		ids[call.ID] = true
	}
	if !ids["call_1"] {
		t.Fatalf("real call_1 missing: %#v", calls)
	}
	if len(ids) != 2 {
		t.Fatalf("expected distinct ids, got %#v", ids)
	}
}

func TestFinishReasonMapping(t *testing.T) {
	cases := []struct {
		name   string
		event  string
		expect string
	}{
		{"completed_text_only", `{"type":"response.completed","response":{"status":"completed"}}`, "stop"},
		{"incomplete_length", `{"type":"response.completed","response":{"status":"incomplete","incomplete_details":{"reason":"max_output_tokens"}}}`, "length"},
		{"incomplete_filter", `{"type":"response.completed","response":{"status":"incomplete","incomplete_details":{"reason":"content_filter"}}}`, "content_filter"},
		{"incomplete_unknown_reason", `{"type":"response.completed","response":{"status":"incomplete"}}`, "length"},
		{"cancelled", `{"type":"response.completed","response":{"status":"cancelled"}}`, "stop"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			agg := NewStreamAggregator()
			if _, err := agg.ApplyEvent(SSEEvent{Data: c.event}); err != nil {
				t.Fatal(err)
			}
			completion := agg.Completion("chatcmpl_test", "gpt-test", 1)
			if got := completion.Choices[0].FinishReason; got != c.expect {
				t.Fatalf("finish_reason = %q, want %q", got, c.expect)
			}
		})
	}
}

func TestReadSSE(t *testing.T) {
	var events []SSEEvent
	err := readSSE(strings.NewReader("event: message\ndata: {\"x\":1}\n\ndata: [DONE]\n\n"), func(event SSEEvent) error {
		events = append(events, event)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 {
		t.Fatalf("events = %#v", events)
	}
	if events[0].Event != "message" || events[0].Data != `{"x":1}` {
		t.Fatalf("first event = %#v", events[0])
	}
	if events[1].Data != "[DONE]" {
		t.Fatalf("done event = %#v", events[1])
	}
}

func TestStreamAggregatorTextAndToolCalls(t *testing.T) {
	agg := NewStreamAggregator()
	events := []string{
		`{"type":"response.reasoning_summary_text.delta","delta":"thinking"}`,
		`{"type":"response.reasoning_text.delta","delta":" raw"}`,
		`{"type":"response.output_text.delta","delta":"O"}`,
		`{"type":"response.output_text.delta","delta":"K"}`,
		`{"type":"response.output_item.added","item":{"id":"item_1","type":"function_call","call_id":"call_1","name":"read_file","arguments":""}}`,
		`{"type":"response.function_call_arguments.delta","item_id":"item_1","delta":"{\"path\""}`,
		`{"type":"response.function_call_arguments.delta","item_id":"item_1","delta":":\"a\"}"}`,
		`{"type":"response.completed","response":{"status":"completed","usage":{"input_tokens":2,"output_tokens":3,"total_tokens":5,"input_tokens_details":{"cached_tokens":1},"output_tokens_details":{"reasoning_tokens":2}}}}`,
	}
	var chunkCount int
	for _, data := range events {
		chunks, err := agg.ApplyEvent(SSEEvent{Data: data})
		if err != nil {
			t.Fatal(err)
		}
		chunkCount += len(chunks)
	}
	completion := agg.Completion("chatcmpl_test", "gpt-test", 1)
	if *completion.Choices[0].Message.Content != "OK" {
		t.Fatalf("content = %#v", completion.Choices[0].Message.Content)
	}
	if completion.Choices[0].Message.ReasoningContent != "thinking raw" {
		t.Fatalf("reasoning = %q", completion.Choices[0].Message.ReasoningContent)
	}
	if completion.Choices[0].Message.ToolCalls[0].Function.Arguments != `{"path":"a"}` {
		t.Fatalf("tool args = %#v", completion.Choices[0].Message.ToolCalls)
	}
	if completion.Choices[0].FinishReason != "tool_calls" {
		t.Fatalf("finish reason = %q", completion.Choices[0].FinishReason)
	}
	usage := completion.Usage.(map[string]any)
	if usage["prompt_tokens"] != int64(2) || usage["completion_tokens"] != int64(3) || usage["total_tokens"] != int64(5) {
		t.Fatalf("usage = %#v", usage)
	}
	if details := usage["completion_tokens_details"].(map[string]any); details["reasoning_tokens"] != int64(2) {
		t.Fatalf("usage details = %#v", usage)
	}
	if chunkCount != 7 {
		t.Fatalf("chunk count = %d", chunkCount)
	}
}
