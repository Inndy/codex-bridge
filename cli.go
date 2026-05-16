package main

import (
	"flag"
	"fmt"
	"time"
)

type stringList []string

func (s *stringList) String() string {
	return fmt.Sprint([]string(*s))
}

func (s *stringList) Set(value string) error {
	*s = append(*s, value)
	return nil
}

type Config struct {
	Addr            string
	AuthPath        string
	CodexBaseURL    string
	CodexVersion    string
	AuthFailHook    string
	AuthFailHookArg []string
	AuthHookTimeout time.Duration
}

func parseConfig(args []string) (Config, error) {
	var cfg Config
	var hookArgs stringList
	fs := flag.NewFlagSet("codex-bridge", flag.ContinueOnError)
	fs.StringVar(&cfg.Addr, "addr", "127.0.0.1:8080", "listen address")
	fs.StringVar(&cfg.AuthPath, "auth-path", "", "path to Codex auth.json")
	fs.StringVar(&cfg.CodexBaseURL, "codex-base-url", "https://chatgpt.com/backend-api/codex", "Codex backend base URL")
	fs.StringVar(&cfg.CodexVersion, "codex-version", "0.111.0", "Codex client version used for model discovery")
	fs.StringVar(&cfg.AuthFailHook, "auth-fail-hook", "", "command to run after Codex auth failure")
	fs.Var(&hookArgs, "auth-fail-hook-arg", "argument for --auth-fail-hook; may be repeated")
	fs.DurationVar(&cfg.AuthHookTimeout, "auth-hook-timeout", time.Minute, "auth failure hook timeout")
	if err := fs.Parse(args); err != nil {
		return Config{}, err
	}
	cfg.AuthFailHookArg = []string(hookArgs)
	return cfg, nil
}
