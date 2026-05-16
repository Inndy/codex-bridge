package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"runtime"
	"strings"
	"time"
)

type UpstreamClient struct {
	baseURL      string
	codexVersion string
	httpClient   *http.Client
	auth         *AuthManager
}

func NewUpstreamClient(baseURL, codexVersion string, auth *AuthManager) *UpstreamClient {
	return &UpstreamClient{
		baseURL:      strings.TrimRight(baseURL, "/"),
		codexVersion: codexVersion,
		httpClient:   &http.Client{Timeout: 10 * time.Minute},
		auth:         auth,
	}
}

func (c *UpstreamClient) Models(ctx context.Context) ([]string, int, error) {
	path := "/models"
	if c.codexVersion != "" {
		path += "?client_version=" + url.QueryEscape(c.codexVersion)
	}
	req, err := c.newRequest(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, 0, err
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, resp.StatusCode, upstreamError(resp.StatusCode, body)
	}
	var parsed struct {
		Models []struct {
			Slug string `json:"slug"`
			ID   string `json:"id"`
		} `json:"models"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, resp.StatusCode, fmt.Errorf("decode models response: %w", err)
	}
	var models []string
	seen := make(map[string]struct{})
	for _, model := range parsed.Models {
		id := model.Slug
		if id == "" {
			id = model.ID
		}
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		models = append(models, id)
	}
	if len(models) == 0 {
		return nil, resp.StatusCode, fmt.Errorf("codex returned no models")
	}
	return models, resp.StatusCode, nil
}

func (c *UpstreamClient) Responses(ctx context.Context, body map[string]any) (*http.Response, error) {
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := c.newRequest(ctx, http.MethodPost, "/responses", bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	return c.httpClient.Do(req)
}

func (c *UpstreamClient) newRequest(ctx context.Context, method, path string, body io.Reader) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, body)
	if err != nil {
		return nil, err
	}
	auth := c.auth.Current()
	version := c.codexVersion
	if version == "" {
		version = "0.125.0"
	}
	req.Header.Set("Authorization", "Bearer "+auth.AccessToken)
	req.Header.Set("chatgpt-account-id", auth.AccountID)
	req.Header.Set("originator", "codex_cli_rs")
	req.Header.Set("version", version)
	req.Header.Set("User-Agent", "codex_cli_rs/"+version+" ("+runtime.GOOS+"; "+runtime.GOARCH+")")
	return req, nil
}

func upstreamError(status int, body []byte) error {
	message := strings.TrimSpace(string(body))
	if message == "" {
		message = http.StatusText(status)
	}
	return fmt.Errorf("upstream status %d: %s", status, message)
}

func isAuthStatus(status int) bool {
	return status == http.StatusUnauthorized || status == http.StatusForbidden
}
