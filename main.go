package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"time"
)

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	cfg, err := parseConfig(os.Args[1:])
	if err != nil {
		logger.Error("parse flags", "error", err)
		os.Exit(2)
	}
	ctx := context.Background()
	auth := NewAuthManager(cfg.AuthPath, HookConfig{
		Command: cfg.AuthFailHook,
		Args:    cfg.AuthFailHookArg,
		Timeout: cfg.AuthHookTimeout,
	}, logger)
	if err := auth.Load(ctx); err != nil {
		logger.Error("load auth", "error", err)
		os.Exit(1)
	}
	auth.StartAuthFileWatcher(ctx)
	auth.StartProactiveRefresh(ctx)
	upstream := NewUpstreamClient(cfg.CodexBaseURL, cfg.CodexVersion, auth)
	server := NewServer(upstream, auth, logger, cfg.CORSAllowAll)
	if _, status, err := server.modelsWithRetry(ctx); err != nil {
		logger.Error("startup auth validation failed", "status", status, "error", err)
		os.Exit(1)
	}
	httpServer := &http.Server{
		Addr:              cfg.Addr,
		Handler:           server.Routes(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	logger.Info("codex bridge listening", "addr", cfg.Addr)
	if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		logger.Error("server failed", "error", err)
		os.Exit(1)
	}
}
