package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidateAPIConfigCodex(t *testing.T) {
	cfg := &APIConfig{
		Provider:   ProviderCodex,
		CodexModel: "gpt-5-codex",
	}
	normalizeAPIConfig(cfg)
	if err := validateAPIConfig(cfg); err != nil {
		t.Fatalf("validateAPIConfig returned error: %v", err)
	}
	if strings.TrimSpace(cfg.CodexWorkingDir) == "" {
		t.Fatal("normalizeAPIConfig did not fill CodexWorkingDir")
	}
}

func TestValidateAPIConfigCodexRequiresModel(t *testing.T) {
	cfg := &APIConfig{Provider: ProviderCodex}
	if err := validateAPIConfig(cfg); err == nil {
		t.Fatal("expected missing Codex model error")
	}
}

func TestFetchModelContextWindowCodexReturnsZero(t *testing.T) {
	got := FetchModelContextWindow(&APIConfig{Provider: ProviderCodex, CodexModel: "gpt-5-codex"})
	if got != 0 {
		t.Fatalf("FetchModelContextWindow for Codex = %d, want 0", got)
	}
}

func TestBuildCodexPromptPreservesRolesAndJSONInstruction(t *testing.T) {
	prompt := buildCodexPrompt([]Message{
		{Role: "system", Content: "Return JSON only."},
		{Role: "user", Content: "Write a chapter outline."},
		{Role: "assistant", Content: "Earlier answer."},
		{Role: "tool", Content: "Tool result."},
	})
	for _, want := range []string{
		"valid JSON only",
		"[system]\nReturn JSON only.",
		"[user]\nWrite a chapter outline.",
		"[assistant]\nEarlier answer.",
		"[tool]\nTool result.",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt missing %q:\n%s", want, prompt)
		}
	}
}

func TestBuildCodexPromptUsesChineseInstructionForChinesePrompt(t *testing.T) {
	prompt := buildCodexPrompt([]Message{
		{Role: "user", Content: "请输出合法 JSON"},
	})
	for _, want := range []string{"只输出最终结果", "合法 JSON", "[user]\n请输出合法 JSON"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt missing %q:\n%s", want, prompt)
		}
	}
}

func TestExtractCodexTurnAgentText(t *testing.T) {
	got := extractCodexTurnAgentText(codexTurn{
		Items: []codexThreadItem{
			{Type: "userMessage", Text: "ignore"},
			{Type: "agentMessage", Text: "first"},
			{Type: "agentMessage", Text: "second"},
		},
	})
	if got != "first\nsecond" {
		t.Fatalf("extractCodexTurnAgentText = %q", got)
	}
}

func TestCodexStreamDisabledIsFatalForRetry(t *testing.T) {
	provider := &CodexProvider{}
	_, err := provider.Stream(context.Background(), LLMRequest{}, nil)
	if !errors.Is(err, errProviderStreamUnsupported) {
		t.Fatalf("Stream error = %v, want errProviderStreamUnsupported", err)
	}
	if !isFatalAPIError(err) {
		t.Fatal("stream unsupported should be fatal for direct stream retry paths")
	}
}

func TestCodexStartTurnStreamsDeltasAndIgnoresFinalDuplicate(t *testing.T) {
	ch := make(chan codexReadResult, 4)
	ch <- codexResult(1, `{"turn":{"id":"turn-1"}}`)
	ch <- codexNotification("item/agentMessage/delta", `{"threadId":"thread-1","turnId":"turn-1","delta":"hello"}`)
	ch <- codexNotification("item/agentMessage/delta", `{"threadId":"thread-1","turnId":"turn-1","delta":" world"}`)
	ch <- codexNotification("turn/completed", `{"threadId":"thread-1","turn":{"id":"turn-1","status":"completed","items":[{"type":"agentMessage","text":"hello world final"}]}}`)
	close(ch)

	var writer bytes.Buffer
	client := &codexRPCClient{writer: &writer, lines: ch}
	var chunks []string
	got, err := client.startTurnAndWait(context.Background(), APIConfig{
		CodexModel:      "gpt-5-codex",
		CodexWorkingDir: t.TempDir(),
	}, "thread-1", "prompt", func(chunk string) {
		chunks = append(chunks, chunk)
	}, nil)
	if err != nil {
		t.Fatalf("startTurnAndWait returned error: %v", err)
	}
	if got != "hello world" {
		t.Fatalf("content = %q, want %q", got, "hello world")
	}
	if len(chunks) != 2 || chunks[0] != "hello" || chunks[1] != " world" {
		t.Fatalf("chunks = %#v, want hello and world chunks", chunks)
	}
	if !strings.Contains(writer.String(), `"method":"turn/start"`) {
		t.Fatalf("turn/start request not written: %s", writer.String())
	}
}

func TestCodexStartTurnIgnoresUnknownNotifications(t *testing.T) {
	ch := make(chan codexReadResult, 3)
	ch <- codexResult(1, `{"turn":{"id":"turn-1"}}`)
	ch <- codexNotification("session/whatever", `{"threadId":"thread-1","turnId":"turn-1","note":"ignored"}`)
	ch <- codexNotification("turn/completed", `{"threadId":"thread-1","turn":{"id":"turn-1","status":"completed","items":[{"type":"agentMessage","text":"final text"}]}}`)
	close(ch)

	client := &codexRPCClient{writer: &bytes.Buffer{}, lines: ch}
	got, err := client.startTurnAndWait(context.Background(), APIConfig{
		CodexModel:      "gpt-5-codex",
		CodexWorkingDir: t.TempDir(),
	}, "thread-1", "prompt", nil, nil)
	if err != nil {
		t.Fatalf("startTurnAndWait returned error: %v", err)
	}
	if got != "final text" {
		t.Fatalf("content = %q, want final text", got)
	}
}

func TestCodexStartTurnFinalOnlyPushesOnce(t *testing.T) {
	ch := make(chan codexReadResult, 2)
	ch <- codexResult(1, `{"turn":{"id":"turn-1"}}`)
	ch <- codexNotification("turn/completed", `{"threadId":"thread-1","turn":{"id":"turn-1","status":"completed","items":[{"type":"agentMessage","text":"final text"}]}}`)
	close(ch)

	var writer bytes.Buffer
	client := &codexRPCClient{writer: &writer, lines: ch}
	var chunks []string
	got, err := client.startTurnAndWait(context.Background(), APIConfig{
		CodexModel:      "gpt-5-codex",
		CodexWorkingDir: t.TempDir(),
	}, "thread-1", "prompt", func(chunk string) {
		chunks = append(chunks, chunk)
	}, nil)
	if err != nil {
		t.Fatalf("startTurnAndWait returned error: %v", err)
	}
	if got != "final text" {
		t.Fatalf("content = %q, want final text", got)
	}
	if len(chunks) != 1 || chunks[0] != "final text" {
		t.Fatalf("chunks = %#v, want one final text chunk", chunks)
	}
}

func TestCodexStartTurnFailedDoesNotReturnContentAsSuccess(t *testing.T) {
	ch := make(chan codexReadResult, 2)
	ch <- codexResult(1, `{"turn":{"id":"turn-1"}}`)
	ch <- codexNotification("turn/completed", `{"threadId":"thread-1","turn":{"id":"turn-1","status":"failed","error":{"message":"bad model sk-1234567890abcdef"},"items":[{"type":"agentMessage","text":"partial"}]}}`)
	close(ch)

	client := &codexRPCClient{writer: &bytes.Buffer{}, lines: ch}
	got, err := client.startTurnAndWait(context.Background(), APIConfig{
		CodexModel:      "gpt-5-codex",
		CodexWorkingDir: t.TempDir(),
	}, "thread-1", "prompt", nil, nil)
	if err == nil {
		t.Fatalf("expected failed turn error, got content %q", got)
	}
	if got != "" {
		t.Fatalf("failed turn content = %q, want empty success content", got)
	}
	if strings.Contains(err.Error(), "sk-1234567890abcdef") || !strings.Contains(err.Error(), "sk-[REDACTED]") {
		t.Fatalf("failed turn error was not redacted: %v", err)
	}
}

func TestCodexStartTurnRedactsTurnStartError(t *testing.T) {
	ch := make(chan codexReadResult, 1)
	id, _ := json.Marshal(1)
	ch <- codexReadResult{msg: codexRPCMessage{
		ID:    id,
		Error: &codexRPCError{Code: -32000, Message: "bad token Bearer codex-secret-token"},
	}}
	close(ch)

	client := &codexRPCClient{writer: &bytes.Buffer{}, lines: ch}
	_, err := client.startTurnAndWait(context.Background(), APIConfig{
		CodexModel:      "gpt-5-codex",
		CodexWorkingDir: t.TempDir(),
	}, "thread-1", "prompt", nil, nil)
	if err == nil {
		t.Fatal("expected turn/start error")
	}
	if strings.Contains(err.Error(), "codex-secret-token") || !strings.Contains(err.Error(), "Bearer [REDACTED]") {
		t.Fatalf("turn/start error was not redacted: %v", err)
	}
}

func TestCodexStartTurnRedactsErrorNotification(t *testing.T) {
	ch := make(chan codexReadResult, 2)
	ch <- codexResult(1, `{"turn":{"id":"turn-1"}}`)
	ch <- codexNotification("error", `{"threadId":"thread-1","turnId":"turn-1","error":{"message":"token access_token=abc123"}}`)
	close(ch)

	client := &codexRPCClient{writer: &bytes.Buffer{}, lines: ch}
	_, err := client.startTurnAndWait(context.Background(), APIConfig{
		CodexModel:      "gpt-5-codex",
		CodexWorkingDir: t.TempDir(),
	}, "thread-1", "prompt", nil, nil)
	if err == nil {
		t.Fatal("expected error notification")
	}
	if strings.Contains(err.Error(), "abc123") || !strings.Contains(err.Error(), `access_token="[REDACTED]"`) {
		t.Fatalf("error notification was not redacted: %v", err)
	}
}

func TestCodexStartTurnIgnoresTransientReconnectNotification(t *testing.T) {
	ch := make(chan codexReadResult, 4)
	ch <- codexResult(1, `{"turn":{"id":"turn-1"}}`)
	ch <- codexNotification("error", `{"threadId":"thread-1","turnId":"turn-1","error":{"message":"Reconnecting... 2/5"}}`)
	ch <- codexNotification("item/agentMessage/delta", `{"threadId":"thread-1","turnId":"turn-1","delta":"ok"}`)
	ch <- codexNotification("turn/completed", `{"threadId":"thread-1","turn":{"id":"turn-1","status":"completed","items":[{"type":"agentMessage","text":"ok"}]}}`)
	close(ch)

	client := &codexRPCClient{writer: &bytes.Buffer{}, lines: ch}
	got, err := client.startTurnAndWait(context.Background(), APIConfig{
		CodexModel:      "gpt-5.5",
		CodexWorkingDir: t.TempDir(),
	}, "thread-1", "prompt", nil, nil)
	if err != nil {
		t.Fatalf("startTurnAndWait returned error: %v", err)
	}
	if got != "ok" {
		t.Fatalf("content = %q, want ok", got)
	}
}

func TestCodexRPCClientReportsInvalidJSONLine(t *testing.T) {
	client := newCodexRPCClient(&bytes.Buffer{}, strings.NewReader("{not json}\n"))
	_, err := client.read(context.Background())
	if err == nil || !strings.Contains(err.Error(), "decode codex app-server message") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestCodexRPCClientReadHonorsContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	client := &codexRPCClient{writer: &bytes.Buffer{}, lines: make(chan codexReadResult)}
	_, err := client.read(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("read error = %v, want context.Canceled", err)
	}
}

func TestValidateCodexWorkingDirRejectsDangerousDirectories(t *testing.T) {
	if err := validateCodexWorkingDir(t.TempDir()); err != nil {
		t.Fatalf("temp dir rejected: %v", err)
	}
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := validateCodexWorkingDir(cwd); err == nil {
		t.Fatal("expected current repository root to be rejected")
	}
	root := filepath.VolumeName(cwd) + string(os.PathSeparator)
	if err := validateCodexWorkingDir(root); err == nil {
		t.Fatalf("expected filesystem root %q to be rejected", root)
	}
}

func codexResult(id int, result string) codexReadResult {
	b, _ := json.Marshal(id)
	return codexReadResult{msg: codexRPCMessage{ID: b, Result: json.RawMessage(result)}}
}

func codexNotification(method, params string) codexReadResult {
	return codexReadResult{msg: codexRPCMessage{Method: method, Params: json.RawMessage(params)}}
}
