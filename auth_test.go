package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"flag"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
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

func TestAuthCandidatesOrder(t *testing.T) {
	home := t.TempDir()
	codexHome := filepath.Join(home, "codex-home")
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", codexHome)

	if got := authCandidates("/explicit/auth.json"); !sameStrings(got, []string{"/explicit/auth.json"}) {
		t.Fatalf("explicit path candidates = %#v", got)
	}

	want := []string{
		filepath.Join(codexHome, "auth.json"),
		filepath.Join(home, ".codex", "auth.json"),
	}
	if got := authCandidates(""); !sameStrings(got, want) {
		t.Fatalf("candidates = %#v, want %#v", got, want)
	}

	t.Setenv("CODEX_HOME", "")
	if got := authCandidates(""); !sameStrings(got, []string{filepath.Join(home, ".codex", "auth.json")}) {
		t.Fatalf("candidates without CODEX_HOME = %#v", got)
	}
}

func TestRunAuthHook(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "marker")
	err := runAuthHook(HookConfig{
		Command: os.Args[0],
		Args:    []string{"-test.run=TestAuthHookHelper", "--", marker},
		Timeout: 5 * time.Second,
	}, slog.New(slog.NewTextHandler(os.Stderr, nil)), &Auth{SourcePath: "/tmp/test", AccountID: "acct"})
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

func TestRunAuthHookPassesAuthContextEnv(t *testing.T) {
	envFile := filepath.Join(t.TempDir(), "env")
	err := runAuthHook(HookConfig{
		Command: os.Args[0],
		Args:    []string{"-test.run=TestAuthHookEnvCaptureHelper", "--", envFile},
		Timeout: 5 * time.Second,
	}, slog.New(slog.NewTextHandler(io.Discard, nil)), &Auth{SourcePath: "/tmp/codex/auth.json", AccountID: "acct_xyz"})
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(envFile)
	if err != nil {
		t.Fatal(err)
	}
	captured := string(data)
	if !strings.Contains(captured, "CODEX_BRIDGE_AUTH_PATH=/tmp/codex/auth.json") {
		t.Fatalf("missing CODEX_BRIDGE_AUTH_PATH in %q", captured)
	}
	if !strings.Contains(captured, "CODEX_BRIDGE_ACCOUNT_ID=acct_xyz") {
		t.Fatalf("missing CODEX_BRIDGE_ACCOUNT_ID in %q", captured)
	}
}

func TestAuthHookEnvCaptureHelper(t *testing.T) {
	args := flag.Args()
	if len(args) == 0 {
		return
	}
	body := "CODEX_BRIDGE_AUTH_PATH=" + os.Getenv("CODEX_BRIDGE_AUTH_PATH") + "\n" +
		"CODEX_BRIDGE_ACCOUNT_ID=" + os.Getenv("CODEX_BRIDGE_ACCOUNT_ID") + "\n"
	if err := os.WriteFile(args[0], []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	os.Exit(0)
}

func TestHandleAuthFailureCoalescesConcurrentCalls(t *testing.T) {
	dir := t.TempDir()
	authPath := filepath.Join(dir, "auth.json")
	writeJSONFile(t, authPath, map[string]any{
		"tokens": map[string]any{
			"access_token": "v1",
			"account_id":   "acct_1",
		},
	})
	counter := filepath.Join(dir, "counter")
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	manager := NewAuthManager(authPath, HookConfig{
		Command: os.Args[0],
		Args:    []string{"-test.run=TestAuthHookCountingHelper", "--", counter, authPath},
		Timeout: 5 * time.Second,
	}, logger)
	if err := manager.Load(context.Background()); err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	var errs atomic.Int32
	for range 8 {
		wg.Go(func() {
			if err := manager.HandleAuthFailure(context.Background()); err != nil {
				errs.Add(1)
			}
		})
	}
	wg.Wait()

	if errs.Load() != 0 {
		t.Fatalf("unexpected hook errors: %d", errs.Load())
	}
	data, err := os.ReadFile(counter)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(data); got != "1" {
		t.Fatalf("hook ran %s times, want 1", got)
	}
	if cur := manager.Current(); cur == nil || cur.AccessToken != "v2" {
		t.Fatalf("auth not refreshed: %+v", cur)
	}
}

func TestAuthHookCountingHelper(t *testing.T) {
	args := flag.Args()
	if len(args) < 2 {
		return
	}
	counterPath, authPath := args[0], args[1]
	f, err := os.OpenFile(counterPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	data, _ := io.ReadAll(f)
	n := 0
	for _, b := range data {
		if b >= '0' && b <= '9' {
			n = n*10 + int(b-'0')
		}
	}
	n++
	if _, err := f.WriteAt([]byte{byte('0' + n)}, 0); err != nil {
		t.Fatal(err)
	}
	writeAuthFile(t, authPath, "v2")
	os.Exit(0)
}


func TestTokenExpiry(t *testing.T) {
	now := time.Now().Truncate(time.Second)

	t.Run("reads exp claim", func(t *testing.T) {
		jwt := testJWT(map[string]any{"exp": now.Unix()})
		got, ok := tokenExpiry(jwt)
		if !ok {
			t.Fatal("expected ok=true")
		}
		if !got.Equal(now) {
			t.Fatalf("expiry = %v, want %v", got, now)
		}
	})

	t.Run("no exp claim returns false", func(t *testing.T) {
		jwt := testJWT(map[string]any{"sub": "user"})
		if _, ok := tokenExpiry(jwt); ok {
			t.Fatal("expected ok=false for token without exp")
		}
	})

	t.Run("non-JWT returns false", func(t *testing.T) {
		if _, ok := tokenExpiry("not-a-jwt"); ok {
			t.Fatal("expected ok=false for non-JWT")
		}
	})
}

func TestProactiveRefreshFiresBeforeExpiry(t *testing.T) {
	dir := t.TempDir()
	authPath := filepath.Join(dir, "auth.json")
	expiry := time.Now().Add(200 * time.Millisecond)
	initialToken := testJWT(map[string]any{"exp": expiry.Unix(), "sub": "u"})
	writeJSONFile(t, authPath, map[string]any{
		"tokens": map[string]any{
			"access_token": initialToken,
			"account_id":   "acct_1",
		},
	})

	triggered := filepath.Join(dir, "triggered")
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	manager := NewAuthManager(authPath, HookConfig{
		Command: os.Args[0],
		Args:    []string{"-test.run=TestAuthHookWriteTriggeredHelper", "--", triggered, authPath},
		Timeout: 5 * time.Second,
	}, logger)
	if err := manager.Load(context.Background()); err != nil {
		t.Fatal(err)
	}

	// Token expires in 200ms, well within the 10-minute margin, so the
	// goroutine fires immediately without waiting.
	manager.StartProactiveRefresh(t.Context())

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(triggered); err == nil {
			return // hook fired
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("proactive refresh hook did not fire within 5s")
}

func TestAuthHookWriteTriggeredHelper(t *testing.T) {
	args := flag.Args()
	if len(args) < 2 {
		return
	}
	if err := os.WriteFile(args[0], []byte("triggered"), 0o600); err != nil {
		t.Fatal(err)
	}
	writeAuthFile(t, args[1], "refreshed")
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
