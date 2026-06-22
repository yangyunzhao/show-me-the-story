package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestGetAPIConfigReturnsProviderFields(t *testing.T) {
	h, _ := newAPIConfigTestHandlers(t)
	h.apiCfg = &APIConfig{
		Provider:          ProviderOpenAICompatible,
		APIKey:            "sk-test",
		BaseURL:           "http://compat.local/v1",
		Model:             "compat-model",
		CodexModel:        "gpt-5-codex",
		CodexWorkingDir:   t.TempDir(),
		CodexUseStreaming: true,
	}

	rec := httptest.NewRecorder()
	h.GetAPIConfig(rec, httptest.NewRequest(http.MethodGet, "/api/config/api", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}

	var got APIConfig
	decodeJSONResponse(t, rec.Body.Bytes(), &got)
	if got.Provider != ProviderOpenAICompatible || got.BaseURL != "http://compat.local/v1" || got.Model != "compat-model" {
		t.Fatalf("OpenAI-compatible fields missing: %#v", got)
	}
	if got.CodexModel != "gpt-5-codex" || got.CodexWorkingDir == "" || !got.CodexUseStreaming {
		t.Fatalf("Codex fields missing: %#v", got)
	}
}

func TestPutAPIConfigBackfillsLegacyProvider(t *testing.T) {
	h, apiPath := newAPIConfigTestHandlers(t)
	body := `{"base_url":"http://localhost:11434","model":"llama","http_timeout_seconds":10}`

	rec := httptest.NewRecorder()
	h.PutAPIConfig(rec, httptest.NewRequest(http.MethodPut, "/api/config/api", strings.NewReader(body)))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}

	var got APIConfig
	decodeJSONResponse(t, rec.Body.Bytes(), &got)
	if got.Provider != ProviderOpenAICompatible {
		t.Fatalf("provider = %q, want %q", got.Provider, ProviderOpenAICompatible)
	}
	if got.HTTPTimeoutSeconds != 10 || got.ContextBudgetTokens != defaultContextBudgetTokens {
		t.Fatalf("unexpected defaults: timeout=%d budget=%d", got.HTTPTimeoutSeconds, got.ContextBudgetTokens)
	}

	var saved APIConfig
	data, err := os.ReadFile(apiPath)
	if err != nil {
		t.Fatal(err)
	}
	decodeJSONResponse(t, data, &saved)
	if saved.Provider != ProviderOpenAICompatible {
		t.Fatalf("saved provider = %q, want %q", saved.Provider, ProviderOpenAICompatible)
	}
}

func TestPutAPIConfigKeepsInactiveProviderFieldsFromFullPayload(t *testing.T) {
	h, apiPath := newAPIConfigTestHandlers(t)

	codexPayload := `{
		"provider":"codex",
		"api_key":"sk-retained",
		"base_url":"http://compat.local/v1",
		"model":"compat-model",
		"max_tokens":8192,
		"context_budget_tokens":123456,
		"codex_model":"gpt-5-codex",
		"codex_working_dir":"` + filepath.ToSlash(t.TempDir()) + `",
		"codex_use_streaming":true
	}`
	rec := httptest.NewRecorder()
	h.PutAPIConfig(rec, httptest.NewRequest(http.MethodPut, "/api/config/api", strings.NewReader(codexPayload)))
	if rec.Code != http.StatusOK {
		t.Fatalf("codex save status = %d, body = %s", rec.Code, rec.Body.String())
	}

	var saved APIConfig
	data, err := os.ReadFile(apiPath)
	if err != nil {
		t.Fatal(err)
	}
	decodeJSONResponse(t, data, &saved)
	if saved.Provider != ProviderCodex || saved.BaseURL != "http://compat.local/v1" || saved.Model != "compat-model" || saved.APIKey != "sk-retained" {
		t.Fatalf("codex save did not retain OpenAI-compatible fields: %#v", saved)
	}
	if !saved.CodexUseStreaming || saved.CodexModel != "gpt-5-codex" {
		t.Fatalf("codex fields not saved: %#v", saved)
	}

	openAIPayload := `{
		"provider":"openai_compatible",
		"api_key":"sk-retained",
		"base_url":"http://compat.local/v1",
		"model":"compat-model",
		"context_budget_tokens":123456,
		"codex_model":"gpt-5-codex",
		"codex_working_dir":"` + filepath.ToSlash(t.TempDir()) + `",
		"codex_use_streaming":true
	}`
	rec = httptest.NewRecorder()
	h.PutAPIConfig(rec, httptest.NewRequest(http.MethodPut, "/api/config/api", strings.NewReader(openAIPayload)))
	if rec.Code != http.StatusOK {
		t.Fatalf("openai save status = %d, body = %s", rec.Code, rec.Body.String())
	}

	data, err = os.ReadFile(apiPath)
	if err != nil {
		t.Fatal(err)
	}
	decodeJSONResponse(t, data, &saved)
	if saved.Provider != ProviderOpenAICompatible || saved.CodexModel != "gpt-5-codex" || !saved.CodexUseStreaming {
		t.Fatalf("openai save did not retain Codex fields: %#v", saved)
	}
}

func TestPutAPIConfigSaveFailureDoesNotReplaceInMemoryConfig(t *testing.T) {
	h := NewHandlers(&APIConfig{
		Provider: ProviderOpenAICompatible,
		BaseURL:  "http://old.local",
		Model:    "old-model",
	}, filepath.Join(t.TempDir(), "missing", "api.json"), NewLogBroadcaster(), t.TempDir(), "test")

	rec := httptest.NewRecorder()
	h.PutAPIConfig(rec, httptest.NewRequest(http.MethodPut, "/api/config/api", strings.NewReader(`{"provider":"codex","codex_model":"gpt-5-codex"}`)))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500, body = %s", rec.Code, rec.Body.String())
	}
	if h.apiCfg.Provider != ProviderOpenAICompatible || h.apiCfg.Model != "old-model" {
		t.Fatalf("in-memory config was replaced after failed save: %#v", h.apiCfg)
	}
}

func TestAPIConfigEndpointsRejectWritesWhileTaskRunning(t *testing.T) {
	h, _ := newAPIConfigTestHandlers(t)
	if !h.tryStartTask() {
		t.Fatal("failed to start test task")
	}
	defer h.endTask()

	rec := httptest.NewRecorder()
	h.PutAPIConfig(rec, httptest.NewRequest(http.MethodPut, "/api/config/api", strings.NewReader(`{}`)))
	if rec.Code != http.StatusConflict {
		t.Fatalf("PUT status = %d, want 409", rec.Code)
	}

	rec = httptest.NewRecorder()
	h.PostAPITest(rec, httptest.NewRequest(http.MethodPost, "/api/config/api/test", strings.NewReader(`{}`)))
	if rec.Code != http.StatusConflict {
		t.Fatalf("POST test status = %d, want 409", rec.Code)
	}
}

func TestPostAPITestCodexUsesCodexModelAndOmitsResponseSample(t *testing.T) {
	installFakeCodexAppServer(t, "final")
	h, _ := newAPIConfigTestHandlers(t)

	var openAIHits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		openAIHits.Add(1)
		w.WriteHeader(http.StatusTeapot)
	}))
	defer server.Close()

	payload := `{
		"provider":"codex",
		"api_key":"sk-should-not-leak",
		"base_url":"` + server.URL + `",
		"model":"should-not-be-used",
		"codex_model":"gpt-5-codex",
		"codex_working_dir":"` + filepath.ToSlash(t.TempDir()) + `"
	}`
	rec := httptest.NewRecorder()
	h.PostAPITest(rec, httptest.NewRequest(http.MethodPost, "/api/config/api/test", strings.NewReader(payload)))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if openAIHits.Load() != 0 {
		t.Fatalf("Codex API test unexpectedly called OpenAI-compatible server %d times", openAIHits.Load())
	}
	body := rec.Body.String()
	for _, forbidden := range []string{"sample", "fake codex response", "sk-should-not-leak", "should-not-be-used"} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("test response leaked %q: %s", forbidden, body)
		}
	}

	var got map[string]any
	decodeJSONResponse(t, rec.Body.Bytes(), &got)
	if got["model"] != "gpt-5-codex" {
		t.Fatalf("model = %#v, want gpt-5-codex", got["model"])
	}
	if got["response_chars"].(float64) <= 0 {
		t.Fatalf("response_chars = %#v, want > 0", got["response_chars"])
	}
}

func TestPostAPITestCodexTimeoutLogsProviderContext(t *testing.T) {
	installFakeCodexAppServer(t, "hang")
	oldTimeout := apiTestTimeoutForProvider
	apiTestTimeoutForProvider = func(APIConfig) time.Duration {
		return 20 * time.Millisecond
	}
	t.Cleanup(func() {
		apiTestTimeoutForProvider = oldTimeout
	})

	h, _ := newAPIConfigTestHandlers(t)
	logs := h.logger.Subscribe()
	defer h.logger.Unsubscribe(logs)

	payload := `{
		"provider":"codex",
		"codex_model":"gpt-5-codex",
		"codex_working_dir":"` + filepath.ToSlash(t.TempDir()) + `"
	}`
	rec := httptest.NewRecorder()
	h.PostAPITest(rec, httptest.NewRequest(http.MethodPost, "/api/config/api/test", strings.NewReader(payload)))
	if rec.Code != http.StatusGatewayTimeout {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "连接超时") || strings.Contains(body, "fake codex response") {
		t.Fatalf("unexpected timeout response: %s", body)
	}

	var sawStart, sawTimeout bool
	deadline := time.After(200 * time.Millisecond)
	for !(sawStart && sawTimeout) {
		select {
		case msg := <-logs:
			entry, ok := msg.Data.(LogEntry)
			if !ok {
				continue
			}
			if entry.Level == "info" && strings.Contains(entry.Msg, "provider=codex") && strings.Contains(entry.Msg, "timeout=1秒") {
				sawStart = true
			}
			if entry.Level == "warn" && strings.Contains(entry.Msg, "连接测试超时") && strings.Contains(entry.Msg, "gpt-5-codex") {
				sawTimeout = true
			}
		case <-deadline:
			t.Fatalf("missing expected log entries: sawStart=%v sawTimeout=%v", sawStart, sawTimeout)
		}
	}
}

func TestCallAPIMessagesCodexFallsBackToGenerateWhenStreamingDisabled(t *testing.T) {
	installFakeCodexAppServer(t, "final")
	cfg := &APIConfig{
		Provider:        ProviderCodex,
		CodexModel:      "gpt-5-codex",
		CodexWorkingDir: t.TempDir(),
	}
	got, err := CallAPIMessages(context.Background(), cfg, []Message{{Role: "user", Content: "Hi"}})
	if err != nil {
		t.Fatalf("CallAPIMessages returned error: %v", err)
	}
	if got != "fake codex response" {
		t.Fatalf("content = %q, want fake codex response", got)
	}
}

func TestCallAPIStreamMessagesCodexUsesAppServerWhenStreamingEnabled(t *testing.T) {
	installFakeCodexAppServer(t, "stream")
	cfg := &APIConfig{
		Provider:          ProviderCodex,
		CodexModel:        "gpt-5-codex",
		CodexWorkingDir:   t.TempDir(),
		CodexUseStreaming: true,
	}
	var chunks []string
	got, err := CallAPIStreamMessages(context.Background(), cfg, []Message{{Role: "user", Content: "Hi"}}, func(chunk string) {
		chunks = append(chunks, chunk)
	})
	if err != nil {
		t.Fatalf("CallAPIStreamMessages returned error: %v", err)
	}
	if got != "fake stream response" {
		t.Fatalf("content = %q, want fake stream response", got)
	}
	if strings.Join(chunks, "") != "fake stream response" {
		t.Fatalf("chunks = %#v", chunks)
	}
}

func TestCodexProviderGenerateHonorsContextCancel(t *testing.T) {
	installFakeCodexAppServer(t, "hang")
	cfg := APIConfig{
		Provider:        ProviderCodex,
		CodexModel:      "gpt-5-codex",
		CodexWorkingDir: t.TempDir(),
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	_, err := (&CodexProvider{cfg: cfg}).Generate(ctx, LLMRequest{Messages: []Message{{Role: "user", Content: "Hi"}}})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Generate error = %v, want context deadline exceeded", err)
	}
}

func newAPIConfigTestHandlers(t *testing.T) (*Handlers, string) {
	t.Helper()
	apiPath := filepath.Join(t.TempDir(), "api.json")
	cfg := DefaultAPIConfig()
	return NewHandlers(cfg, apiPath, NewLogBroadcaster(), t.TempDir(), "test"), apiPath
}

func decodeJSONResponse(t *testing.T, data []byte, dst any) {
	t.Helper()
	if err := json.Unmarshal(data, dst); err != nil {
		t.Fatalf("decode JSON %q: %v", string(data), err)
	}
}

func installFakeCodexAppServer(t *testing.T, mode string) {
	t.Helper()
	old := newCodexAppServerCmd
	newCodexAppServerCmd = func(ctx context.Context) *exec.Cmd {
		cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=TestHelperProcessCodexAppServer", "--")
		cmd.Env = append(os.Environ(),
			"SHOW_ME_THE_STORY_FAKE_CODEX=1",
			"SHOW_ME_THE_STORY_FAKE_CODEX_MODE="+mode,
		)
		return cmd
	}
	t.Cleanup(func() {
		newCodexAppServerCmd = old
	})
}

func TestHelperProcessCodexAppServer(t *testing.T) {
	if os.Getenv("SHOW_ME_THE_STORY_FAKE_CODEX") != "1" {
		return
	}

	out := bufio.NewWriter(os.Stdout)
	enc := json.NewEncoder(out)
	write := func(v any) {
		_ = enc.Encode(v)
		_ = out.Flush()
	}
	writeResult := func(id json.RawMessage, result any) {
		write(map[string]any{
			"jsonrpc": "2.0",
			"id":      id,
			"result":  result,
		})
	}
	writeNotification := func(method string, params any) {
		write(map[string]any{
			"jsonrpc": "2.0",
			"method":  method,
			"params":  params,
		})
	}

	scanner := bufio.NewScanner(os.Stdin)
	threadID := "thread-1"
	turnID := "turn-1"
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		var req struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		if err := json.Unmarshal(line, &req); err != nil {
			os.Exit(2)
		}
		if len(req.ID) == 0 {
			continue
		}

		switch req.Method {
		case "initialize":
			writeResult(req.ID, map[string]any{"userAgent": "fake-codex"})
		case "thread/start":
			writeResult(req.ID, map[string]any{"thread": map[string]any{"id": threadID}})
		case "turn/start":
			writeResult(req.ID, map[string]any{"turn": map[string]any{"id": turnID}})
			mode := os.Getenv("SHOW_ME_THE_STORY_FAKE_CODEX_MODE")
			if mode == "hang" {
				time.Sleep(10 * time.Minute)
			}
			if mode == "stream" {
				writeNotification("item/agentMessage/delta", map[string]any{"threadId": threadID, "turnId": turnID, "delta": "fake "})
				writeNotification("item/agentMessage/delta", map[string]any{"threadId": threadID, "turnId": turnID, "delta": "stream response"})
				writeNotification("turn/completed", map[string]any{
					"threadId": threadID,
					"turn":     map[string]any{"id": turnID, "status": "completed", "items": []any{}},
				})
			} else {
				writeNotification("turn/completed", map[string]any{
					"threadId": threadID,
					"turn": map[string]any{
						"id":     turnID,
						"status": "completed",
						"items":  []any{map[string]any{"type": "agentMessage", "text": "fake codex response"}},
					},
				})
			}
		default:
			writeResult(req.ID, map[string]any{})
		}
	}
	os.Exit(0)
}
