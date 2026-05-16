package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"time"
)

type Server struct {
	upstream *UpstreamClient
	models   *ModelCache
	auth     *AuthManager
	logger   *slog.Logger
}

func NewServer(upstream *UpstreamClient, models *ModelCache, auth *AuthManager, logger *slog.Logger) *Server {
	return &Server{
		upstream: upstream,
		models:   models,
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
	models, status, err := s.models.Models(r.Context())
	if err != nil && isAuthStatus(status) {
		if hookErr := s.auth.HandleAuthFailure(r.Context()); hookErr == nil {
			models, status, err = s.models.Models(r.Context())
		} else {
			err = errors.Join(err, hookErr)
		}
	}
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
	var req ChatCompletionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeOpenAIError(w, http.StatusBadRequest, "Invalid JSON body.", "invalid_request_error")
		return
	}
	if req.Model == "" {
		writeOpenAIError(w, http.StatusBadRequest, "`model` is required.", "invalid_request_error")
		return
	}
	if len(req.Messages) == 0 {
		writeOpenAIError(w, http.StatusBadRequest, "`messages` must be a non-empty array.", "invalid_request_error")
		return
	}
	resp, err := s.createResponseWithRetry(r.Context(), toResponsesRequest(req))
	if err != nil {
		s.logger.ErrorContext(r.Context(), "chat request failed", "request_id", requestID, "model", req.Model, "stream", req.Stream, "duration_ms", time.Since(start).Milliseconds(), "error", err)
		writeOpenAIError(w, http.StatusBadGateway, err.Error(), "upstream_error")
		return
	}
	defer drainAndClose(resp)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		writeOpenAIError(w, resp.StatusCode, upstreamError(resp.StatusCode, body).Error(), "upstream_error")
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

func (s *Server) createResponseWithRetry(ctx context.Context, body map[string]any) (*http.Response, error) {
	resp, err := s.upstream.Responses(ctx, body)
	if err != nil {
		return nil, err
	}
	if !isAuthStatus(resp.StatusCode) {
		return resp, nil
	}
	drainAndClose(resp)
	if err := s.auth.HandleAuthFailure(ctx); err != nil {
		return nil, err
	}
	return s.upstream.Responses(ctx, body)
}

func (s *Server) completeChat(w http.ResponseWriter, r *http.Request, resp *http.Response, requestID, id string, created int64, model string, start time.Time) {
	agg := NewStreamAggregator()
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
	agg := NewStreamAggregator()
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
