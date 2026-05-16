package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

type Auth struct {
	AccessToken string
	AccountID   string
	SourcePath  string
}

type HookConfig struct {
	Command string
	Args    []string
	Timeout time.Duration
}

type AuthManager struct {
	authPath string
	hook     HookConfig
	logger   *slog.Logger

	mu   sync.RWMutex
	auth Auth
}

func NewAuthManager(authPath string, hook HookConfig, logger *slog.Logger) *AuthManager {
	return &AuthManager{
		authPath: authPath,
		hook:     hook,
		logger:   logger,
	}
}

func (m *AuthManager) Load(ctx context.Context) error {
	auth, err := LoadAuth(m.authPath)
	if err != nil {
		return err
	}
	m.mu.Lock()
	m.auth = auth
	m.mu.Unlock()
	m.logger.InfoContext(ctx, "loaded codex auth", "path", auth.SourcePath)
	return nil
}

func (m *AuthManager) Current() Auth {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.auth
}

func (m *AuthManager) HandleAuthFailure(ctx context.Context) error {
	if m.hook.Command == "" {
		return errors.New("upstream auth failed and no auth failure hook is configured")
	}
	if err := runAuthHook(ctx, m.hook, m.logger); err != nil {
		return err
	}
	return m.Load(ctx)
}

func LoadAuth(authPath string) (Auth, error) {
	candidates, err := authCandidates(authPath)
	if err != nil {
		return Auth{}, err
	}
	for _, candidate := range candidates {
		auth, err := loadAuthFile(candidate)
		if err == nil {
			return auth, nil
		}
	}
	if len(candidates) == 0 {
		return Auth{}, errors.New("no codex auth.json candidates found")
	}
	return Auth{}, fmt.Errorf("codex OAuth tokens not found in candidates: %s", strings.Join(candidates, ", "))
}

func authCandidates(authPath string) ([]string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	return authCandidatesWithHome(authPath, home, os.Getenv("CODEX_HOME"))
}

func authCandidatesWithHome(authPath, home, codexHome string) ([]string, error) {
	var candidates []string
	if authPath != "" {
		candidates = append(candidates, authPath)
	} else {
		if codexHome != "" {
			candidates = append(candidates, filepath.Join(codexHome, "auth.json"))
		}
		candidates = append(candidates, filepath.Join(home, ".codex", "auth.json"))
		matches, err := filepath.Glob(filepath.Join(home, ".codex*", "auth.json"))
		if err != nil {
			return nil, err
		}
		sort.Strings(matches)
		candidates = append(candidates, matches...)
	}
	return uniqueExistingPaths(candidates), nil
}

func uniqueExistingPaths(paths []string) []string {
	seen := make(map[string]struct{}, len(paths))
	result := make([]string, 0, len(paths))
	for _, p := range paths {
		if p == "" {
			continue
		}
		clean := filepath.Clean(p)
		if _, ok := seen[clean]; ok {
			continue
		}
		if _, err := os.Stat(clean); err != nil {
			continue
		}
		seen[clean] = struct{}{}
		result = append(result, clean)
	}
	return result
}

type codexAuthFile struct {
	Tokens struct {
		AccessToken string `json:"access_token"`
		AccountID   string `json:"account_id"`
		IDToken     string `json:"id_token"`
	} `json:"tokens"`
}

func loadAuthFile(path string) (Auth, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Auth{}, err
	}
	var file codexAuthFile
	if err := json.Unmarshal(data, &file); err != nil {
		return Auth{}, err
	}
	accessToken := strings.TrimSpace(file.Tokens.AccessToken)
	accountID := strings.TrimSpace(file.Tokens.AccountID)
	if accountID == "" {
		accountID = deriveAccountID(file.Tokens.IDToken)
	}
	if accessToken == "" {
		return Auth{}, errors.New("tokens.access_token missing")
	}
	if accountID == "" {
		return Auth{}, errors.New("tokens.account_id missing and could not be derived from id_token")
	}
	return Auth{
		AccessToken: accessToken,
		AccountID:   accountID,
		SourcePath:  path,
	}, nil
}

func deriveAccountID(idToken string) string {
	parts := strings.Split(idToken, ".")
	if len(parts) != 3 {
		return ""
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		payload, err = base64.URLEncoding.DecodeString(parts[1])
		if err != nil {
			return ""
		}
	}
	var claims map[string]any
	if err := json.Unmarshal(payload, &claims); err != nil {
		return ""
	}
	authClaim, ok := claims["https://api.openai.com/auth"].(map[string]any)
	if !ok {
		return ""
	}
	accountID, _ := authClaim["chatgpt_account_id"].(string)
	return accountID
}

func runAuthHook(ctx context.Context, hook HookConfig, logger *slog.Logger) error {
	timeout := hook.Timeout
	if timeout <= 0 {
		timeout = time.Minute
	}
	hookCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	logger.InfoContext(ctx, "running auth failure hook", "command", hook.Command, "timeout", timeout.String())
	cmd := exec.CommandContext(hookCtx, hook.Command, hook.Args...)
	output, err := cmd.CombinedOutput()
	if hookCtx.Err() == context.DeadlineExceeded {
		return fmt.Errorf("auth failure hook timed out after %s", timeout)
	}
	if err != nil {
		return fmt.Errorf("auth failure hook failed: %w: %s", err, strings.TrimSpace(string(output)))
	}
	logger.InfoContext(ctx, "auth failure hook completed")
	return nil
}
