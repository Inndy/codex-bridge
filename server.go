package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"
)

type Server struct {
	upstream *UpstreamClient
	auth     *AuthManager
	logger   *slog.Logger
}

func NewServer(upstream *UpstreamClient, auth *AuthManager, logger *slog.Logger) *Server {
	return &Server{
		upstream: upstream,
		auth:     auth,
		logger:   logger,
	}
}

func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/models", s.handleModels)
	mux.HandleFunc("/v1/chat/completions", s.handleChatCompletions)
	return cors(mux)
}

func cors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "authorization, content-type, accept, openai-organization, openai-project, x-stainless-arch, x-stainless-lang, x-stainless-os, x-stainless-package-version, x-stainless-retry-count, x-stainless-runtime, x-stainless-runtime-version, x-stainless-timeout")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) handleModels(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	requestID := newID("req_")
	if r.Method != http.MethodGet {
		writeOpenAIError(w, http.StatusMethodNotAllowed, "Method not allowed.", "invalid_request_error")
		return
	}
	models, status, err := s.modelsWithRetry(r.Context())
	if err != nil {
		code := http.StatusBadGateway
		if status > 0 {
			code = status
		}
		s.logger.ErrorContext(r.Context(), "models request failed", "request_id", requestID, "duration_ms", time.Since(start).Milliseconds(), "status", code, "error", err)
		writeOpenAIError(w, code, err.Error(), "upstream_error")
		return
	}
	data := make([]map[string]any, 0, len(models))
	for _, model := range models {
		data = append(data, map[string]any{
			"id":       model,
			"object":   "model",
			"created":  0,
			"owned_by": "codex",
		})
	}
	s.logger.InfoContext(r.Context(), "models request completed", "request_id", requestID, "duration_ms", time.Since(start).Milliseconds(), "status", http.StatusOK)
	writeJSON(w, http.StatusOK, map[string]any{
		"object": "list",
		"data":   data,
	})
}

func (s *Server) handleChatCompletions(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	requestID := newID("req_")
	if r.Method != http.MethodPost {
		writeOpenAIError(w, http.StatusMethodNotAllowed, "Method not allowed.", "invalid_request_error")
		return
	}
	req, body, err := parseAndValidateChatRequest(r)
	if err != nil {
		writeOpenAIError(w, http.StatusBadRequest, err.Error(), "invalid_request_error")
		return
	}
	resp, err := s.responsesWithRetry(r.Context(), body)
	if err != nil {
		s.logger.ErrorContext(r.Context(), "chat request failed", "request_id", requestID, "model", req.Model, "stream", req.Stream, "duration_ms", time.Since(start).Milliseconds(), "error", err)
		writeOpenAIError(w, http.StatusBadGateway, err.Error(), "upstream_error")
		return
	}
	defer drainAndClose(resp)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		writeOpenAIError(w, resp.StatusCode, upstreamError(resp.StatusCode, respBody).Error(), "upstream_error")
		return
	}
	id := newID("chatcmpl_")
	created := time.Now().Unix()
	if req.Stream {
		s.streamChat(w, r, resp, requestID, id, created, req.Model, start)
		return
	}
	s.completeChat(w, r, resp, requestID, id, created, req.Model, start)
}

func parseAndValidateChatRequest(r *http.Request) (ChatCompletionRequest, responsesRequest, error) {
	var req ChatCompletionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		return req, responsesRequest{}, errors.New("Invalid JSON body.")
	}
	if req.Model == "" {
		return req, responsesRequest{}, errors.New("`model` is required.")
	}
	if len(req.Messages) == 0 {
		return req, responsesRequest{}, errors.New("`messages` must be a non-empty array.")
	}
	body, err := toResponsesRequest(req)
	if err != nil {
		return req, responsesRequest{}, err
	}
	return req, body, nil
}

func (s *Server) modelsWithRetry(ctx context.Context) ([]string, int, error) {
	models, status, err := s.upstream.Models(ctx)
	if err == nil || !isAuthStatus(status) {
		return models, status, err
	}
	if hookErr := s.auth.HandleAuthFailure(ctx); hookErr != nil {
		return models, status, errors.Join(err, hookErr)
	}
	return s.upstream.Models(ctx)
}

func (s *Server) responsesWithRetry(ctx context.Context, body responsesRequest) (*http.Response, error) {
	resp, err := s.upstream.Responses(ctx, body)
	if err != nil || !isAuthStatus(resp.StatusCode) {
		return resp, err
	}
	drainAndClose(resp)
	upstreamErr := fmt.Errorf("upstream status %d before auth refresh", resp.StatusCode)
	if hookErr := s.auth.HandleAuthFailure(ctx); hookErr != nil {
		return nil, errors.Join(upstreamErr, hookErr)
	}
	return s.upstream.Responses(ctx, body)
}

func (s *Server) completeChat(w http.ResponseWriter, r *http.Request, resp *http.Response, requestID, id string, created int64, model string, start time.Time) {
	agg := NewStreamAggregator(s.logger)
	if err := readSSE(resp.Body, func(event SSEEvent) error {
		_, err := agg.ApplyEvent(event)
		return err
	}); err != nil {
		s.logger.ErrorContext(r.Context(), "chat aggregation failed", "request_id", requestID, "model", model, "duration_ms", time.Since(start).Milliseconds(), "error", err)
		writeOpenAIError(w, http.StatusBadGateway, err.Error(), "upstream_error")
		return
	}
	s.logger.InfoContext(r.Context(), "chat request completed", "request_id", requestID, "model", model, "stream", false, "duration_ms", time.Since(start).Milliseconds(), "status", http.StatusOK)
	writeJSON(w, http.StatusOK, agg.Completion(id, model, created))
}

func (s *Server) streamChat(w http.ResponseWriter, r *http.Request, resp *http.Response, requestID, id string, created int64, model string, start time.Time) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	flusher, _ := w.(http.Flusher)
	agg := NewStreamAggregator(s.logger)
	_ = writeSSE(w, streamChunk(id, created, model, map[string]any{"role": "assistant"}, nil))
	if flusher != nil {
		flusher.Flush()
	}
	err := readSSE(resp.Body, func(event SSEEvent) error {
		deltas, err := agg.ApplyEvent(event)
		if err != nil {
			return err
		}
		for _, delta := range deltas {
			if err := writeSSE(w, streamChunk(id, created, model, delta, nil)); err != nil {
				return err
			}
			if flusher != nil {
				flusher.Flush()
			}
		}
		return nil
	})
	if err != nil {
		s.logger.ErrorContext(r.Context(), "chat stream failed", "request_id", requestID, "model", model, "duration_ms", time.Since(start).Milliseconds(), "error", err)
		return
	}
	completion := agg.Completion(id, model, created)
	finish := completion.Choices[0].FinishReason
	_ = writeSSE(w, streamChunk(id, created, model, map[string]any{}, finish))
	if completion.Usage != nil {
		_ = writeSSE(w, map[string]any{
			"id":      id,
			"object":  "chat.completion.chunk",
			"created": created,
			"model":   model,
			"choices": []any{},
			"usage":   completion.Usage,
		})
	}
	_ = writeSSE(w, "[DONE]")
	if flusher != nil {
		flusher.Flush()
	}
	s.logger.InfoContext(r.Context(), "chat request completed", "request_id", requestID, "model", model, "stream", true, "duration_ms", time.Since(start).Milliseconds(), "status", http.StatusOK)
}
