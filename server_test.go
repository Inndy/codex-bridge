package main

import (
	"context"
	"encoding/json"
	"flag"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestModelsHandlerMapsUpstreamModels(t *testing.T) {
	proxy := newTestProxy(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer access_1" {
			t.Fatalf("authorization = %q", got)
		}
		if got := r.Header.Get("OpenAI-Beta"); got != "" {
			t.Fatalf("unexpected OpenAI-Beta header = %q", got)
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"models": []map[string]any{{"slug": "gpt-test"}},
		})
	}))
	defer proxy.Close()

	resp, err := http.Get(proxy.URL + "/v1/models")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	data := body["data"].([]any)
	if data[0].(map[string]any)["id"] != "gpt-test" {
		t.Fatalf("body = %#v", body)
	}
}

func TestChatHandlerNonStream(t *testing.T) {
	proxy := newTestProxy(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		var upstreamReq map[string]any
		if err := json.NewDecoder(r.Body).Decode(&upstreamReq); err != nil {
			t.Fatal(err)
		}
		if upstreamReq["stream"] != true {
			t.Fatalf("upstream request = %#v", upstreamReq)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"OK\"}\n\n")
		_, _ = io.WriteString(w, "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"usage\":{\"input_tokens\":1,\"output_tokens\":1,\"total_tokens\":2}}}\n\n")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer proxy.Close()

	resp, err := http.Post(proxy.URL+"/v1/chat/completions", "application/json", strings.NewReader(`{"model":"gpt-test","messages":[{"role":"user","content":"say ok"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d body=%s", resp.StatusCode, body)
	}
	var completion ChatCompletionResponse
	if err := json.NewDecoder(resp.Body).Decode(&completion); err != nil {
		t.Fatal(err)
	}
	if *completion.Choices[0].Message.Content != "OK" {
		t.Fatalf("completion = %#v", completion)
	}
}

func TestChatHandlerStream(t *testing.T) {
	proxy := newTestProxy(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"O\"}\n\n")
		_, _ = io.WriteString(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"K\"}\n\n")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer proxy.Close()

	resp, err := http.Post(proxy.URL+"/v1/chat/completions", "application/json", strings.NewReader(`{"model":"gpt-test","stream":true,"messages":[{"role":"user","content":"say ok"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), `"content":"O"`) || !strings.Contains(string(body), "data: [DONE]") {
		t.Fatalf("stream body = %s", body)
	}
}

func TestRuntimeAuthFailureRunsHookAndRetries(t *testing.T) {
	dir := t.TempDir()
	authPath := filepath.Join(dir, "auth.json")
	writeAuthFile(t, authPath, "old")
	var seen int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&seen, 1) == 1 {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if got := r.Header.Get("Authorization"); got != "Bearer new" {
			t.Fatalf("authorization after hook = %q", got)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"OK\"}\n\n")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer upstream.Close()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	auth := NewAuthManager(authPath, HookConfig{
		Command: os.Args[0],
		Args:    []string{"-test.run=TestRewriteAuthHookHelper", "--", authPath},
		Timeout: 5 * time.Second,
	}, logger)
	if err := auth.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	client := NewUpstreamClient(upstream.URL, "test", auth)
	server := httptest.NewServer(NewServer(client, auth, logger).Routes())
	defer server.Close()

	resp, err := http.Post(server.URL+"/v1/chat/completions", "application/json", strings.NewReader(`{"model":"gpt-test","messages":[{"role":"user","content":"say ok"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d body=%s", resp.StatusCode, body)
	}
}

func TestRewriteAuthHookHelper(t *testing.T) {
	args := flag.Args()
	if len(args) == 0 {
		return
	}
	writeAuthFile(t, args[0], "new")
	os.Exit(0)
}

func newTestProxy(t *testing.T, upstreamHandler http.Handler) *httptest.Server {
	t.Helper()
	upstream := httptest.NewServer(upstreamHandler)
	t.Cleanup(upstream.Close)
	dir := t.TempDir()
	authPath := filepath.Join(dir, "auth.json")
	writeAuthFile(t, authPath, "access_1")
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	auth := NewAuthManager(authPath, HookConfig{}, logger)
	if err := auth.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	client := NewUpstreamClient(upstream.URL, "test", auth)
	return httptest.NewServer(NewServer(client, auth, logger).Routes())
}

func writeAuthFile(t *testing.T, path, accessToken string) {
	t.Helper()
	writeJSONFile(t, path, map[string]any{
		"tokens": map[string]any{
			"access_token": accessToken,
			"account_id":   "acct_1",
		},
	})
}
