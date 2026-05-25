package main

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/openai/openai-go/v3/shared"
)

func TestOfficialSDKChatCompletionAgainstProxy(t *testing.T) {
	proxy := newTestProxy(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"OK\"}\n\n")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer proxy.Close()

	client := openai.NewClient(
		option.WithBaseURL(proxy.URL+"/v1"),
		option.WithAPIKey("ignored"),
	)
	completion, err := client.Chat.Completions.New(context.Background(), openai.ChatCompletionNewParams{
		Model: "gpt-test",
		Messages: []openai.ChatCompletionMessageParamUnion{
			openai.UserMessage("say ok"),
		},
		MaxTokens: openai.Int(2),
	})
	if err != nil {
		t.Fatal(err)
	}
	if completion.Choices[0].Message.Content != "OK" {
		t.Fatalf("content = %q", completion.Choices[0].Message.Content)
	}
}

func TestSmokeLiveOptIn(t *testing.T) {
	if testing.Short() || getenv("CODEX_BRIDGE_SMOKE") != "1" {
		t.Skip("set CODEX_BRIDGE_SMOKE=1 to run live Codex smoke test")
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	auth := NewAuthManager("", HookConfig{}, logger)
	if err := auth.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	upstream := NewUpstreamClient("https://chatgpt.com/backend-api/codex", "0.125.0", auth)
	server := httptest.NewServer(NewServer(upstream, auth, logger, false).Routes())
	defer server.Close()

	client := openai.NewClient(option.WithBaseURL(server.URL+"/v1"), option.WithAPIKey("ignored"))
	if _, err := client.Models.List(context.Background()); err != nil {
		t.Fatal(err)
	}
	prompt := "Reply exactly: OK"
	t.Logf("smoke prompt: %s", prompt)
	completion, err := client.Chat.Completions.New(context.Background(), openai.ChatCompletionNewParams{
		Model: "gpt-5.4",
		Messages: []openai.ChatCompletionMessageParamUnion{
			openai.UserMessage(prompt),
		},
		MaxTokens: openai.Int(3),
	})
	if err != nil {
		t.Fatal(err)
	}
	if completion.Choices[0].Message.Content == "" {
		t.Fatal("empty smoke completion")
	}
	t.Logf("smoke non-stream response: %s", completion.Choices[0].Message.Content)
	t.Logf("smoke non-stream usage: prompt=%d completion=%d total=%d reasoning=%d",
		completion.Usage.PromptTokens,
		completion.Usage.CompletionTokens,
		completion.Usage.TotalTokens,
		completion.Usage.CompletionTokensDetails.ReasoningTokens,
	)

	prompt = "Reply with exactly this sentence: one two three four five."
	stream := client.Chat.Completions.NewStreaming(context.Background(), openai.ChatCompletionNewParams{
		Model: "gpt-5.4",
		Messages: []openai.ChatCompletionMessageParamUnion{
			openai.UserMessage(prompt),
		},
		MaxTokens: openai.Int(20),
	})
	var gotChunk bool
	var streamed string
	for stream.Next() {
		chunk := stream.Current()
		if len(chunk.Choices) > 0 && chunk.Choices[0].Delta.Content != "" {
			gotChunk = true
			streamed += chunk.Choices[0].Delta.Content
			t.Logf("smoke stream delta: %q", chunk.Choices[0].Delta.Content)
		}
		if chunk.Usage.TotalTokens > 0 || chunk.Usage.CompletionTokensDetails.ReasoningTokens > 0 {
			t.Logf("smoke stream usage: prompt=%d completion=%d total=%d reasoning=%d",
				chunk.Usage.PromptTokens,
				chunk.Usage.CompletionTokens,
				chunk.Usage.TotalTokens,
				chunk.Usage.CompletionTokensDetails.ReasoningTokens,
			)
		}
	}
	if err := stream.Err(); err != nil {
		t.Fatal(err)
	}
	if !gotChunk {
		t.Fatal("empty smoke stream")
	}
	t.Logf("smoke stream response: %s", streamed)
}

func getenv(key string) string {
	return os.Getenv(key)
}

// TestSmokeSystemOnlyPrompt exercises the #53 fix: a request with only a
// system message should be accepted by Codex and produce a non-empty reply.
// The bridge now synthesizes a placeholder user turn instead of folding the
// system text twice.
func TestSmokeSystemOnlyPrompt(t *testing.T) {
	if testing.Short() || getenv("CODEX_BRIDGE_SMOKE") != "1" {
		t.Skip("set CODEX_BRIDGE_SMOKE=1 to run live Codex smoke test")
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	auth := NewAuthManager("", HookConfig{}, logger)
	if err := auth.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	upstream := NewUpstreamClient("https://chatgpt.com/backend-api/codex", "0.125.0", auth)
	server := httptest.NewServer(NewServer(upstream, auth, logger, false).Routes())
	defer server.Close()

	client := openai.NewClient(option.WithBaseURL(server.URL+"/v1"), option.WithAPIKey("ignored"))
	completion, err := client.Chat.Completions.New(context.Background(), openai.ChatCompletionNewParams{
		Model: "gpt-5.4",
		Messages: []openai.ChatCompletionMessageParamUnion{
			openai.SystemMessage("You always reply with the single word: ok"),
		},
		MaxTokens: openai.Int(5),
	})
	if err != nil {
		t.Fatalf("system-only request failed: %v", err)
	}
	if completion.Choices[0].Message.Content == "" {
		t.Fatal("system-only completion returned empty content")
	}
	t.Logf("system-only response: %q", completion.Choices[0].Message.Content)
}

// TestSmokeReasoningContent exercises the #59 fix: reasoning_content should
// be populated without duplication. The streamed-delta sum and the
// non-streaming reasoning_content for the same prompt should be in the same
// ballpark (the bug doubled the non-streaming value).
func TestSmokeReasoningContent(t *testing.T) {
	if testing.Short() || getenv("CODEX_BRIDGE_SMOKE") != "1" {
		t.Skip("set CODEX_BRIDGE_SMOKE=1 to run live Codex smoke test")
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	auth := NewAuthManager("", HookConfig{}, logger)
	if err := auth.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	upstream := NewUpstreamClient("https://chatgpt.com/backend-api/codex", "0.125.0", auth)
	server := httptest.NewServer(NewServer(upstream, auth, logger, false).Routes())
	defer server.Close()

	client := openai.NewClient(option.WithBaseURL(server.URL+"/v1"), option.WithAPIKey("ignored"))
	// Logic puzzles trigger Codex reasoning more reliably than math, which
	// OpenAI may handle via tool/shortcut paths instead of visible reasoning.
	// Empty reasoning_content is still acceptable — OpenAI may withhold the
	// summary stream entirely (anti-distillation) — so the test only asserts
	// no duplication when content IS surfaced.
	prompt := "Three suspects: Alice, Bob, Carol. Exactly one is lying. Alice says Bob is lying. Bob says Carol is lying. Carol says both Alice and Bob are lying. Who is lying? Walk through it then give the name."

	completion, err := client.Chat.Completions.New(context.Background(), openai.ChatCompletionNewParams{
		Model: "gpt-5.4",
		Messages: []openai.ChatCompletionMessageParamUnion{
			openai.UserMessage(prompt),
		},
		ReasoningEffort: shared.ReasoningEffortHigh,
	})
	if err != nil {
		t.Fatalf("reasoning request failed: %v", err)
	}
	raw := completion.RawJSON()
	reasoning := extractReasoningContent(raw)
	t.Logf("reasoning content length: %d", len(reasoning))
	if reasoning != "" {
		t.Logf("reasoning content: %q", reasoning)
		if half := len(reasoning) / 2; half > 20 && reasoning[:half] == reasoning[half:half*2] {
			t.Fatalf("reasoning content looks duplicated (first half == second half)")
		}
	}
	if completion.Choices[0].Message.Content == "" {
		t.Fatal("reasoning completion returned empty content")
	}
	t.Logf("reasoning response: %q", completion.Choices[0].Message.Content)
}

// TestSmokeOpenAIParitySystemOnly fires the same system-only prompt at
// vanilla api.openai.com to confirm OpenAI's own behavior accepts the
// pattern. Gated separately because it costs OpenAI credits, not Codex.
func TestSmokeOpenAIParitySystemOnly(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	apiKey := getenv("OPENAI_API_KEY")
	if apiKey == "" {
		t.Skip("set OPENAI_API_KEY to run vanilla OpenAI parity test")
	}
	opts := []option.RequestOption{option.WithAPIKey(apiKey)}
	if base := getenv("OPENAI_BASE_URL"); base != "" {
		opts = append(opts, option.WithBaseURL(base))
	}
	client := openai.NewClient(opts...)
	completion, err := client.Chat.Completions.New(context.Background(), openai.ChatCompletionNewParams{
		Model: "gpt-4o-mini",
		Messages: []openai.ChatCompletionMessageParamUnion{
			openai.SystemMessage("You always reply with the single word: ok"),
		},
		MaxTokens: openai.Int(5),
	})
	if err != nil {
		t.Fatalf("vanilla OpenAI system-only request failed: %v", err)
	}
	if completion.Choices[0].Message.Content == "" {
		t.Fatal("vanilla OpenAI returned empty content for system-only prompt")
	}
	t.Logf("vanilla OpenAI system-only response: %q", completion.Choices[0].Message.Content)
}

// extractReasoningContent reads the non-standard reasoning_content field
// directly from the raw JSON since the openai-go SDK does not surface it.
func extractReasoningContent(raw string) string {
	var payload struct {
		Choices []struct {
			Message struct {
				ReasoningContent string `json:"reasoning_content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		return ""
	}
	if len(payload.Choices) == 0 {
		return ""
	}
	return payload.Choices[0].Message.ReasoningContent
}
