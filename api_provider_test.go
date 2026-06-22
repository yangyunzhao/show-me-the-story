package main

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDefaultAndLegacyAPIConfigUseOpenAIProvider(t *testing.T) {
	cfg := DefaultAPIConfig()
	if cfg.Provider != ProviderOpenAICompatible {
		t.Fatalf("DefaultAPIConfig provider = %q, want %q", cfg.Provider, ProviderOpenAICompatible)
	}
	if cfg.HTTPTimeoutSeconds != 300 {
		t.Fatalf("DefaultAPIConfig timeout = %d, want 300", cfg.HTTPTimeoutSeconds)
	}
	if cfg.ContextBudgetTokens != defaultContextBudgetTokens {
		t.Fatalf("DefaultAPIConfig context budget = %d, want %d", cfg.ContextBudgetTokens, defaultContextBudgetTokens)
	}

	legacy := &APIConfig{}
	normalizeAPIConfig(legacy)
	if legacy.Provider != ProviderOpenAICompatible {
		t.Fatalf("legacy provider = %q, want %q", legacy.Provider, ProviderOpenAICompatible)
	}
}

func TestLoadAPIConfigBackfillsLegacyProvider(t *testing.T) {
	path := filepath.Join(t.TempDir(), "api.json")
	if err := os.WriteFile(path, []byte(`{"http_timeout_seconds":10,"context_budget_tokens":123}`), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadAPIConfig(path)
	if err != nil {
		t.Fatalf("LoadAPIConfig returned error: %v", err)
	}
	if cfg.Provider != ProviderOpenAICompatible {
		t.Fatalf("provider = %q, want %q", cfg.Provider, ProviderOpenAICompatible)
	}
	if cfg.HTTPTimeoutSeconds != 10 || cfg.ContextBudgetTokens != 123 {
		t.Fatalf("unexpected defaults: timeout=%d budget=%d", cfg.HTTPTimeoutSeconds, cfg.ContextBudgetTokens)
	}
}

func TestProviderFromConfigSelectsSupportedProviders(t *testing.T) {
	openAI, err := providerFromConfig(&APIConfig{Provider: ProviderOpenAICompatible})
	if err != nil {
		t.Fatalf("providerFromConfig openai returned error: %v", err)
	}
	if _, ok := openAI.(*OpenAIProvider); !ok {
		t.Fatalf("openai provider type = %T, want *OpenAIProvider", openAI)
	}

	codex, err := providerFromConfig(&APIConfig{Provider: ProviderCodex, CodexModel: "gpt-5-codex"})
	if err != nil {
		t.Fatalf("providerFromConfig codex returned error: %v", err)
	}
	if _, ok := codex.(*CodexProvider); !ok {
		t.Fatalf("codex provider type = %T, want *CodexProvider", codex)
	}

	_, err = providerFromConfig(&APIConfig{Provider: APIProviderType("unknown")})
	if err == nil {
		t.Fatal("expected unknown provider error")
	}
	if !isFatalAPIError(err) {
		t.Fatalf("unknown provider error should be fatal: %v", err)
	}
}

func TestValidateAPIConfigProviderRules(t *testing.T) {
	if err := validateAPIConfig(&APIConfig{Provider: ProviderOpenAICompatible, Model: "gpt-4"}); err == nil {
		t.Fatal("expected missing base URL error for OpenAI-compatible provider")
	}
	if err := validateAPIConfig(&APIConfig{Provider: ProviderOpenAICompatible, BaseURL: "http://localhost:11434", Model: "llama"}); err != nil {
		t.Fatalf("OpenAI-compatible config rejected: %v", err)
	}
	if err := validateAPIConfig(&APIConfig{Provider: ProviderCodex, CodexModel: "gpt-5-codex"}); err != nil {
		t.Fatalf("Codex config without OpenAI fields rejected: %v", err)
	}
}

func TestValidateCodexWorkingDirRejectsHome(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		t.Skip("user home directory is unavailable")
	}
	if err := validateCodexWorkingDir(home); err == nil {
		t.Fatalf("expected user home directory %q to be rejected", home)
	}
}

func TestFatalAPIErrorClassifiesCodexAndTransientErrors(t *testing.T) {
	fatalMessages := []string{
		"codex executable not found: install and log in to Codex CLI first",
		"run Codex turn: codex turn failed: model unavailable",
		"run Codex turn: codex turn interrupted",
		"turn/start failed: bad model",
		"Codex working directory cannot be a filesystem root",
	}
	for _, msg := range fatalMessages {
		if !isFatalAPIError(fmt.Errorf("%s", msg)) {
			t.Fatalf("error %q should be fatal", msg)
		}
	}

	transientMessages := []string{
		"dial tcp 127.0.0.1:443: i/o timeout",
		"read: connection reset by peer",
	}
	for _, msg := range transientMessages {
		if isFatalAPIError(fmt.Errorf("%s", msg)) {
			t.Fatalf("error %q should be retryable", msg)
		}
	}
}

func TestRedactSensitiveText(t *testing.T) {
	input := `Authorization: Bearer codex-secret-token
api_key=sk-1234567890abcdef
standalone sk-abcdef1234567890
{"access_token":"abc123","refresh_token":"def456","password":"pw"}`
	got := redactSensitiveText(input)
	for _, secret := range []string{"codex-secret-token", "sk-1234567890abcdef", "sk-abcdef1234567890", "abc123", "def456", "pw"} {
		if strings.Contains(got, secret) {
			t.Fatalf("redacted text still contains %q: %s", secret, got)
		}
	}
	for _, want := range []string{"Bearer [REDACTED]", "sk-[REDACTED]", `"[REDACTED]"`} {
		if !strings.Contains(got, want) {
			t.Fatalf("redacted text missing %q: %s", want, got)
		}
	}
}

func TestOpenAIProviderErrorRedactsResponseBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"bad key sk-1234567890abcdef","access_token":"abc123"}`))
	}))
	defer server.Close()

	provider := &OpenAIProvider{cfg: APIConfig{
		BaseURL:            server.URL,
		Model:              "test-model",
		HTTPTimeoutSeconds: 5,
	}}
	_, err := provider.Generate(context.Background(), LLMRequest{Messages: []Message{{Role: "user", Content: "hi"}}})
	if err == nil {
		t.Fatal("expected HTTP error")
	}
	got := err.Error()
	for _, secret := range []string{"sk-1234567890abcdef", "abc123"} {
		if strings.Contains(got, secret) {
			t.Fatalf("error still contains secret %q: %s", secret, got)
		}
	}
}

func TestCodexStartThreadUsesConservativeSecurityParams(t *testing.T) {
	ch := make(chan codexReadResult, 1)
	ch <- codexResult(1, `{"thread":{"id":"thread-1"}}`)
	close(ch)

	var writer bytes.Buffer
	client := &codexRPCClient{writer: &writer, lines: ch}
	got, err := client.startThread(context.Background(), APIConfig{
		CodexModel:      "gpt-5-codex",
		CodexWorkingDir: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("startThread returned error: %v", err)
	}
	if got != "thread-1" {
		t.Fatalf("thread id = %q, want thread-1", got)
	}
	req := writer.String()
	for _, want := range []string{
		`"method":"thread/start"`,
		`"approvalPolicy":"never"`,
		`"sandbox":"read-only"`,
		`"ephemeral":true`,
		`Do not edit files, run shell commands, inspect the working directory, or use tools.`,
	} {
		if !strings.Contains(req, want) {
			t.Fatalf("thread/start request missing %q: %s", want, req)
		}
	}
}

func TestCodexRPCRequestReturnsServerError(t *testing.T) {
	ch := make(chan codexReadResult, 1)
	ch <- codexReadResult{msg: codexRPCMessage{
		ID:    []byte("1"),
		Error: &codexRPCError{Code: -32000, Message: "bad request refresh_token=abc123"},
	}}
	close(ch)

	client := &codexRPCClient{writer: &bytes.Buffer{}, lines: ch}
	err := client.request(context.Background(), "initialize", map[string]any{}, nil)
	if err == nil || !strings.Contains(err.Error(), `initialize failed: bad request refresh_token="[REDACTED]"`) || strings.Contains(err.Error(), "abc123") {
		t.Fatalf("unexpected error: %v", err)
	}
}
