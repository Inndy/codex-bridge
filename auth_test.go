package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"flag"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLoadAuthIgnoresAPIKeyAndDerivesAccountID(t *testing.T) {
	dir := t.TempDir()
	authPath := filepath.Join(dir, "auth.json")
	idToken := testJWT(map[string]any{
		"https://api.openai.com/auth": map[string]any{
			"chatgpt_account_id": "acct_123",
		},
	})
	writeJSONFile(t, authPath, map[string]any{
		"OPENAI_API_KEY": "sk-ignored",
		"tokens": map[string]any{
			"access_token": "access_123",
			"id_token":     idToken,
		},
	})

	auth, err := LoadAuth(authPath)
	if err != nil {
		t.Fatal(err)
	}
	if auth.AccessToken != "access_123" {
		t.Fatalf("access token = %q", auth.AccessToken)
	}
	if auth.AccountID != "acct_123" {
		t.Fatalf("account id = %q", auth.AccountID)
	}
}

func TestLoadAuthRejectsAPIKeyOnly(t *testing.T) {
	dir := t.TempDir()
	authPath := filepath.Join(dir, "auth.json")
	writeJSONFile(t, authPath, map[string]any{
		"OPENAI_API_KEY": "sk-ignored",
	})

	if _, err := LoadAuth(authPath); err == nil {
		t.Fatal("expected API-key-only auth to fail")
	}
}

func TestAuthCandidatesOrderAndDedup(t *testing.T) {
	home := t.TempDir()
	codexHome := filepath.Join(home, "codex-home")
	mustMkdir(t, filepath.Join(home, ".codex"))
	mustMkdir(t, filepath.Join(home, ".codex-alt"))
	mustMkdir(t, codexHome)
	for _, path := range []string{
		filepath.Join(codexHome, "auth.json"),
		filepath.Join(home, ".codex", "auth.json"),
		filepath.Join(home, ".codex-alt", "auth.json"),
	} {
		writeJSONFile(t, path, map[string]any{})
	}

	candidates, err := authCandidatesWithHome("", home, codexHome)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		filepath.Join(codexHome, "auth.json"),
		filepath.Join(home, ".codex", "auth.json"),
		filepath.Join(home, ".codex-alt", "auth.json"),
	}
	if !sameStrings(candidates, want) {
		t.Fatalf("candidates = %#v, want %#v", candidates, want)
	}
}

func TestRunAuthHook(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "marker")
	err := runAuthHook(context.Background(), HookConfig{
		Command: os.Args[0],
		Args:    []string{"-test.run=TestAuthHookHelper", "--", marker},
		Timeout: 5 * time.Second,
	}, slog.New(slog.NewTextHandler(os.Stderr, nil)))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatal(err)
	}
}

func TestAuthHookHelper(t *testing.T) {
	args := flag.Args()
	if len(args) == 0 {
		return
	}
	if err := os.WriteFile(args[0], []byte("ok"), 0o600); err != nil {
		t.Fatal(err)
	}
	os.Exit(0)
}

func testJWT(payload map[string]any) string {
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none"}`))
	body, _ := json.Marshal(payload)
	return header + "." + base64.RawURLEncoding.EncodeToString(body) + ".sig"
}

func writeJSONFile(t *testing.T, path string, value any) {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func mustMkdir(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o700); err != nil {
		t.Fatal(err)
	}
}

func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
