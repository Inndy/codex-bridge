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
	"strings"
	"sync"
	"sync/atomic"
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
	authPath  string
	hook      HookConfig
	logger    *slog.Logger
	auth      atomic.Pointer[Auth]
	refreshMu sync.Mutex
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
	m.auth.Store(auth)
	m.logger.InfoContext(ctx, "loaded codex auth", "path", auth.SourcePath)
	return nil
}

func (m *AuthManager) Current() *Auth {
	return m.auth.Load()
}

func (m *AuthManager) HandleAuthFailure(ctx context.Context) error {
	if m.hook.Command == "" {
		return errors.New("upstream auth failed and no auth failure hook is configured")
	}
	seen := m.auth.Load()
	m.refreshMu.Lock()
	defer m.refreshMu.Unlock()
	if m.auth.Load() != seen {
		return nil
	}
	if err := runAuthHook(ctx, m.hook, m.logger, seen); err != nil {
		return err
	}
	return m.Load(ctx)
}

func LoadAuth(authPath string) (*Auth, error) {
	candidates := authCandidates(authPath)
	if len(candidates) == 0 {
		return nil, errors.New("no codex auth.json candidates found")
	}
	var attempted []string
	for _, candidate := range candidates {
		auth, err := loadAuthFile(candidate)
		if err == nil {
			return auth, nil
		}
		attempted = append(attempted, candidate)
	}
	return nil, fmt.Errorf("codex OAuth tokens not found in candidates: %s", strings.Join(attempted, ", "))
}

func authCandidates(authPath string) []string {
	if authPath != "" {
		return []string{authPath}
	}
	var candidates []string
	if codexHome := os.Getenv("CODEX_HOME"); codexHome != "" {
		candidates = append(candidates, filepath.Join(codexHome, "auth.json"))
	}
	if home, err := os.UserHomeDir(); err == nil {
		candidates = append(candidates, filepath.Join(home, ".codex", "auth.json"))
	}
	return candidates
}

type codexAuthFile struct {
	Tokens struct {
		AccessToken string `json:"access_token"`
		AccountID   string `json:"account_id"`
		IDToken     string `json:"id_token"`
	} `json:"tokens"`
}

func loadAuthFile(path string) (*Auth, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var file codexAuthFile
	if err := json.Unmarshal(data, &file); err != nil {
		return nil, err
	}
	accountID := file.Tokens.AccountID
	if accountID == "" {
		accountID = deriveAccountID(file.Tokens.IDToken)
	}
	if file.Tokens.AccessToken == "" {
		return nil, errors.New("tokens.access_token missing")
	}
	if accountID == "" {
		return nil, errors.New("tokens.account_id missing and could not be derived from id_token")
	}
	return &Auth{
		AccessToken: file.Tokens.AccessToken,
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
	var claims struct {
		OpenAIAuth struct {
			ChatGPTAccountID string `json:"chatgpt_account_id"`
		} `json:"https://api.openai.com/auth"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return ""
	}
	return claims.OpenAIAuth.ChatGPTAccountID
}

const proactiveRefreshMargin = 10 * time.Minute
const proactiveRefreshRetry = 5 * time.Minute

// tokenExpiry parses the "exp" claim from a JWT access token.
// Returns the expiry time and true, or zero time and false if unavailable.
func tokenExpiry(accessToken string) (time.Time, bool) {
	parts := strings.Split(accessToken, ".")
	if len(parts) != 3 {
		return time.Time{}, false
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		payload, err = base64.URLEncoding.DecodeString(parts[1])
		if err != nil {
			return time.Time{}, false
		}
	}
	var claims struct {
		Exp int64 `json:"exp"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil || claims.Exp == 0 {
		return time.Time{}, false
	}
	return time.Unix(claims.Exp, 0), true
}

// StartProactiveRefresh starts a background goroutine that fires the auth hook
// proactively before the access token expires. No-op if no hook is configured.
func (m *AuthManager) StartProactiveRefresh(ctx context.Context) {
	if m.hook.Command == "" {
		return
	}
	go m.proactiveRefreshLoop(ctx)
}

func (m *AuthManager) proactiveRefreshLoop(ctx context.Context) {
	for {
		auth := m.auth.Load()
		if auth == nil {
			select {
			case <-ctx.Done():
				return
			case <-time.After(time.Minute):
				continue
			}
		}
		expiry, ok := tokenExpiry(auth.AccessToken)
		if !ok {
			m.logger.WarnContext(ctx, "access token has no exp claim; proactive refresh disabled")
			return
		}
		wait := time.Until(expiry.Add(-proactiveRefreshMargin))
		if wait > 0 {
			select {
			case <-ctx.Done():
				return
			case <-time.After(wait):
			}
		}
		// Skip if another goroutine already refreshed while we were sleeping.
		if m.auth.Load() != auth {
			continue
		}
		m.logger.InfoContext(ctx, "proactive token refresh", "expiry", expiry)
		if err := m.HandleAuthFailure(ctx); err != nil {
			m.logger.WarnContext(ctx, "proactive token refresh failed", "error", err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(proactiveRefreshRetry):
			}
		}
	}
}

func runAuthHook(ctx context.Context, hook HookConfig, logger *slog.Logger, failing *Auth) error {
	timeout := hook.Timeout
	if timeout <= 0 {
		timeout = time.Minute
	}
	hookCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	logger.InfoContext(ctx, "running auth failure hook", "command", hook.Command, "timeout", timeout.String())
	cmd := exec.CommandContext(hookCtx, hook.Command, hook.Args...)
	cmd.Env = append(os.Environ(), authHookEnv(failing)...)
	// Hook output is discarded: a refresh command may print tokens
	// (e.g. `codex login --verbose`), and we forward errors through structured
	// logs and HTTP responses where any captured bytes would leak. Operators
	// debugging a broken hook should run it directly outside this process.
	err := cmd.Run()
	if hookCtx.Err() == context.DeadlineExceeded {
		return fmt.Errorf("auth failure hook timed out after %s", timeout)
	}
	if err != nil {
		return fmt.Errorf("auth failure hook failed: %w", err)
	}
	logger.InfoContext(ctx, "auth failure hook completed")
	return nil
}

func authHookEnv(failing *Auth) []string {
	if failing == nil {
		return nil
	}
	return []string{
		"CODEX_BRIDGE_AUTH_PATH=" + failing.SourcePath,
		"CODEX_BRIDGE_ACCOUNT_ID=" + failing.AccountID,
	}
}
