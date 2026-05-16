package main

import (
	"context"
	"sync"
	"time"
)

type ModelCache struct {
	upstream *UpstreamClient
	ttl      time.Duration

	mu        sync.Mutex
	models    []string
	expiresAt time.Time
}

func NewModelCache(upstream *UpstreamClient, ttl time.Duration) *ModelCache {
	return &ModelCache{upstream: upstream, ttl: ttl}
}

func (c *ModelCache) Models(ctx context.Context) ([]string, int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.models) > 0 && time.Now().Before(c.expiresAt) {
		return append([]string(nil), c.models...), httpStatusOK, nil
	}
	models, status, err := c.upstream.Models(ctx)
	if err != nil {
		return nil, status, err
	}
	c.models = append([]string(nil), models...)
	c.expiresAt = time.Now().Add(c.ttl)
	return append([]string(nil), models...), status, nil
}

const httpStatusOK = 200
