package main

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
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
	upstream := NewUpstreamClient("https://chatgpt.com/backend-api/codex", "0.111.0", auth)
	models := NewModelCache(upstream, time.Minute)
	server := httptest.NewServer(NewServer(upstream, models, auth, logger).Routes())
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
