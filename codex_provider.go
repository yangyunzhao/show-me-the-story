package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
)

const codexAppServerStderrLimit = 8192

var newCodexAppServerCmd = func(ctx context.Context) *exec.Cmd {
	return exec.CommandContext(ctx, "codex", "app-server", "--stdio")
}

type CodexProvider struct {
	cfg APIConfig
}

func (p *CodexProvider) Generate(ctx context.Context, req LLMRequest) (*LLMResponse, error) {
	return p.run(ctx, req, nil)
}

func (p *CodexProvider) Stream(ctx context.Context, req LLMRequest, onChunk func(string)) (*LLMResponse, error) {
	cfg := p.cfg
	normalizeAPIConfig(&cfg)
	if !cfg.CodexUseStreaming {
		return nil, fmt.Errorf("%w: Codex streaming is disabled; set codex_use_streaming=true", errProviderStreamUnsupported)
	}
	return p.runWithConfig(ctx, cfg, req, onChunk)
}

func (p *CodexProvider) run(ctx context.Context, req LLMRequest, onChunk func(string)) (*LLMResponse, error) {
	cfg := p.cfg
	normalizeAPIConfig(&cfg)
	return p.runWithConfig(ctx, cfg, req, onChunk)
}

func (p *CodexProvider) runWithConfig(ctx context.Context, cfg APIConfig, req LLMRequest, onChunk func(string)) (*LLMResponse, error) {
	if err := validateAPIConfig(&cfg); err != nil {
		return nil, err
	}
	if err := validateCodexWorkingDir(cfg.CodexWorkingDir); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(cfg.CodexWorkingDir, 0o755); err != nil {
		return nil, fmt.Errorf("create Codex working directory: %w", err)
	}

	tracker := taskTokensFromContext(ctx)
	tracker.beginCall(req.Messages)

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	cmd := newCodexAppServerCmd(runCtx)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, err
	}

	stderrBuf := &limitedBuffer{limit: codexAppServerStderrLimit}
	go func() {
		_, _ = io.Copy(stderrBuf, stderr)
	}()

	if err := cmd.Start(); err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return nil, fmt.Errorf("codex executable not found: install and log in to Codex CLI first")
		}
		return nil, fmt.Errorf("start codex app-server: %w", err)
	}
	defer func() {
		_ = stdin.Close()
		cancel()
		_ = cmd.Wait()
	}()

	client := newCodexRPCClient(stdin, stdout)
	if err := client.initialize(ctx); err != nil {
		return nil, withCodexStderr("initialize codex app-server", err, stderrBuf.String())
	}

	threadID, err := client.startThread(ctx, cfg)
	if err != nil {
		return nil, withCodexStderr("start Codex thread", err, stderrBuf.String())
	}

	prompt := buildCodexPrompt(req.Messages)
	content, err := client.startTurnAndWait(ctx, cfg, threadID, prompt, onChunk, tracker)
	if err != nil {
		return nil, withCodexStderr("run Codex turn", err, stderrBuf.String())
	}
	if strings.TrimSpace(content) == "" {
		return nil, fmt.Errorf("Codex returned empty content")
	}
	if tracker != nil {
		tracker.finishCall(0, 0, false, req.Messages, content)
	}
	return &LLMResponse{Content: content}, nil
}

type codexRPCClient struct {
	mu      sync.Mutex
	writer  io.Writer
	lines   <-chan codexReadResult
	nextID  int
	pending strings.Builder
}

type codexReadResult struct {
	msg codexRPCMessage
	err error
}

type codexRPCMessage struct {
	ID     json.RawMessage `json:"id,omitempty"`
	Method string          `json:"method,omitempty"`
	Params json.RawMessage `json:"params,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *codexRPCError  `json:"error,omitempty"`
}

type codexRPCError struct {
	Code    int    `json:"code,omitempty"`
	Message string `json:"message,omitempty"`
}

func newCodexRPCClient(writer io.Writer, reader io.Reader) *codexRPCClient {
	ch := make(chan codexReadResult, 16)
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 64*1024), 16*1024*1024)
	go func() {
		defer close(ch)
		for scanner.Scan() {
			line := bytes.TrimSpace(scanner.Bytes())
			if len(line) == 0 {
				continue
			}
			var msg codexRPCMessage
			if err := json.Unmarshal(line, &msg); err != nil {
				ch <- codexReadResult{err: fmt.Errorf("decode codex app-server message: %w", err)}
				continue
			}
			ch <- codexReadResult{msg: msg}
		}
		if err := scanner.Err(); err != nil {
			ch <- codexReadResult{err: err}
		}
	}()
	return &codexRPCClient{writer: writer, lines: ch}
}

func (c *codexRPCClient) initialize(ctx context.Context) error {
	var result struct {
		UserAgent string `json:"userAgent"`
	}
	params := map[string]any{
		"clientInfo": map[string]any{
			"name":    "show-me-the-story",
			"title":   "Show Me The Story",
			"version": version,
		},
		"capabilities": map[string]any{
			"experimentalApi":    false,
			"requestAttestation": false,
		},
	}
	if err := c.request(ctx, "initialize", params, &result); err != nil {
		return err
	}
	return c.notify("initialized", nil)
}

func (c *codexRPCClient) startThread(ctx context.Context, cfg APIConfig) (string, error) {
	params := map[string]any{
		"model":              cfg.CodexModel,
		"cwd":                cfg.CodexWorkingDir,
		"approvalPolicy":     "never",
		"sandbox":            "read-only",
		"serviceName":        "show-me-the-story",
		"personality":        "none",
		"ephemeral":          true,
		"sessionStartSource": "startup",
		"baseInstructions": strings.Join([]string{
			"You are serving as a text generation backend for a novel-writing application.",
			"Return only the requested final content.",
			"Do not edit files, run shell commands, inspect the working directory, or use tools.",
			"Do not mention Codex or implementation details.",
		}, "\n"),
	}
	var result struct {
		Thread struct {
			ID string `json:"id"`
		} `json:"thread"`
	}
	if err := c.request(ctx, "thread/start", params, &result); err != nil {
		return "", err
	}
	if strings.TrimSpace(result.Thread.ID) == "" {
		return "", fmt.Errorf("codex thread/start response missing thread.id")
	}
	return result.Thread.ID, nil
}

func (c *codexRPCClient) startTurnAndWait(ctx context.Context, cfg APIConfig, threadID, prompt string, onDelta func(string), tracker *TaskTokenUsage) (string, error) {
	c.pending.Reset()
	id := c.nextRequestID()
	params := map[string]any{
		"threadId":       threadID,
		"cwd":            cfg.CodexWorkingDir,
		"approvalPolicy": "never",
		"sandboxPolicy": map[string]any{
			"type":          "readOnly",
			"networkAccess": false,
		},
		"model":       cfg.CodexModel,
		"personality": "none",
		"input": []map[string]any{
			{
				"type":          "text",
				"text":          prompt,
				"text_elements": []any{},
			},
		},
	}
	if err := c.write(map[string]any{"jsonrpc": "2.0", "id": id, "method": "turn/start", "params": params}); err != nil {
		return "", err
	}

	var turnStarted bool
	var turnID string
	for {
		msg, err := c.read(ctx)
		if err != nil {
			if c.pending.Len() > 0 {
				return c.pending.String(), err
			}
			return "", err
		}
		if msg.Method != "" && len(msg.ID) > 0 && msg.Result == nil && msg.Error == nil {
			_ = c.writeUnsupportedServerRequest(msg)
			continue
		}
		if idMatches(msg.ID, id) {
			if msg.Error != nil {
				return "", fmt.Errorf("turn/start failed: %s", redactSensitiveText(msg.Error.Message))
			}
			var result struct {
				Turn struct {
					ID string `json:"id"`
				} `json:"turn"`
			}
			if len(msg.Result) > 0 {
				_ = json.Unmarshal(msg.Result, &result)
				turnID = result.Turn.ID
			}
			turnStarted = true
			continue
		}
		if msg.Method == "" {
			continue
		}
		if msg.Method == "turn/started" {
			if startedTurnID := parseCodexTurnStartedID(threadID, msg); startedTurnID != "" {
				turnID = startedTurnID
			}
			continue
		}
		done, content, err := c.handleTurnNotification(threadID, turnID, msg, onDelta, tracker)
		if err != nil {
			if c.pending.Len() > 0 {
				return c.pending.String(), err
			}
			return "", err
		}
		if done {
			pending := c.pending.String()
			if strings.TrimSpace(pending) != "" {
				return pending, nil
			}
			if strings.TrimSpace(content) != "" {
				if onDelta != nil {
					onDelta(content)
				}
				if tracker != nil {
					tracker.updateStreamContent(content)
				}
				return content, nil
			}
			return pending, nil
		}
		if !turnStarted {
			continue
		}
	}
}

func (c *codexRPCClient) handleTurnNotification(threadID, turnID string, msg codexRPCMessage, onDelta func(string), tracker *TaskTokenUsage) (bool, string, error) {
	switch msg.Method {
	case "item/agentMessage/delta":
		var params struct {
			ThreadID string `json:"threadId"`
			TurnID   string `json:"turnId"`
			Delta    string `json:"delta"`
		}
		if err := json.Unmarshal(msg.Params, &params); err != nil {
			return false, "", err
		}
		if codexEventMatches(threadID, turnID, params.ThreadID, params.TurnID) && params.Delta != "" {
			c.pending.WriteString(params.Delta)
			if onDelta != nil {
				onDelta(params.Delta)
			}
			if tracker != nil {
				tracker.updateStreamContent(c.pending.String())
			}
		}
	case "error":
		var params struct {
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
			ThreadID string `json:"threadId"`
			TurnID   string `json:"turnId"`
		}
		if err := json.Unmarshal(msg.Params, &params); err != nil {
			return false, "", err
		}
		if codexEventMatches(threadID, turnID, params.ThreadID, params.TurnID) {
			message := redactSensitiveText(params.Error.Message)
			if isCodexTransientNotification(message) {
				return false, "", nil
			}
			return false, "", fmt.Errorf("codex app-server error: %s", message)
		}
	case "turn/completed":
		var params codexTurnCompletedNotification
		if err := json.Unmarshal(msg.Params, &params); err != nil {
			return false, "", err
		}
		if !codexEventMatches(threadID, turnID, params.ThreadID, params.Turn.ID) {
			return false, "", nil
		}
		if params.Turn.Status == "failed" {
			return true, extractCodexTurnAgentText(params.Turn), fmt.Errorf("codex turn failed: %s", redactSensitiveText(params.Turn.Error.Message))
		}
		if params.Turn.Status == "interrupted" {
			return true, extractCodexTurnAgentText(params.Turn), fmt.Errorf("codex turn interrupted")
		}
		return true, extractCodexTurnAgentText(params.Turn), nil
	}
	return false, "", nil
}

func isCodexTransientNotification(message string) bool {
	return strings.HasPrefix(strings.TrimSpace(message), "Reconnecting...")
}

func parseCodexTurnStartedID(threadID string, msg codexRPCMessage) string {
	var params struct {
		ThreadID string `json:"threadId"`
		Turn     struct {
			ID string `json:"id"`
		} `json:"turn"`
	}
	if err := json.Unmarshal(msg.Params, &params); err != nil {
		return ""
	}
	if params.ThreadID != threadID {
		return ""
	}
	return params.Turn.ID
}

func codexEventMatches(expectedThreadID, expectedTurnID, gotThreadID, gotTurnID string) bool {
	if expectedThreadID != "" && gotThreadID != "" && gotThreadID != expectedThreadID {
		return false
	}
	if expectedTurnID != "" && gotTurnID != "" && gotTurnID != expectedTurnID {
		return false
	}
	return true
}

type codexTurnCompletedNotification struct {
	ThreadID string    `json:"threadId"`
	Turn     codexTurn `json:"turn"`
}

type codexTurn struct {
	ID     string            `json:"id"`
	Status string            `json:"status"`
	Error  codexTurnError    `json:"error"`
	Items  []codexThreadItem `json:"items"`
}

type codexTurnError struct {
	Message string `json:"message"`
}

type codexThreadItem struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

func extractCodexTurnAgentText(turn codexTurn) string {
	var b strings.Builder
	for _, item := range turn.Items {
		if item.Type == "agentMessage" && item.Text != "" {
			if b.Len() > 0 {
				b.WriteString("\n")
			}
			b.WriteString(item.Text)
		}
	}
	return b.String()
}

func (c *codexRPCClient) request(ctx context.Context, method string, params any, out any) error {
	id := c.nextRequestID()
	if err := c.write(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params}); err != nil {
		return err
	}
	for {
		msg, err := c.read(ctx)
		if err != nil {
			return err
		}
		if msg.Method != "" && len(msg.ID) > 0 && msg.Result == nil && msg.Error == nil {
			_ = c.writeUnsupportedServerRequest(msg)
			continue
		}
		if !idMatches(msg.ID, id) {
			continue
		}
		if msg.Error != nil {
			return fmt.Errorf("%s failed: %s", method, redactSensitiveText(msg.Error.Message))
		}
		if out == nil || len(msg.Result) == 0 {
			return nil
		}
		return json.Unmarshal(msg.Result, out)
	}
}

func (c *codexRPCClient) notify(method string, params any) error {
	return c.write(map[string]any{"jsonrpc": "2.0", "method": method, "params": params})
}

func (c *codexRPCClient) read(ctx context.Context) (codexRPCMessage, error) {
	select {
	case <-ctx.Done():
		return codexRPCMessage{}, ctx.Err()
	case res, ok := <-c.lines:
		if !ok {
			return codexRPCMessage{}, io.EOF
		}
		return res.msg, res.err
	}
}

func (c *codexRPCClient) nextRequestID() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.nextID++
	return c.nextID
}

func (c *codexRPCClient) write(v any) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if _, err := c.writer.Write(append(b, '\n')); err != nil {
		return err
	}
	return nil
}

func (c *codexRPCClient) writeUnsupportedServerRequest(msg codexRPCMessage) error {
	resp := map[string]any{
		"jsonrpc": "2.0",
		"id":      json.RawMessage(msg.ID),
		"error": map[string]any{
			"code":    -32601,
			"message": "method not supported",
		},
	}
	return c.write(resp)
}

func idMatches(raw json.RawMessage, id int) bool {
	return strings.TrimSpace(string(raw)) == fmt.Sprintf("%d", id)
}

func validateCodexWorkingDir(dir string) error {
	if strings.TrimSpace(dir) == "" {
		return fmt.Errorf("Codex working directory not configured")
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return fmt.Errorf("resolve Codex working directory: %w", err)
	}
	clean := filepath.Clean(abs)
	volumeRoot := filepath.VolumeName(clean) + string(os.PathSeparator)
	if samePath(clean, volumeRoot) {
		return fmt.Errorf("Codex working directory cannot be a filesystem root: %s", clean)
	}
	if home, err := os.UserHomeDir(); err == nil && strings.TrimSpace(home) != "" && samePath(clean, home) {
		return fmt.Errorf("Codex working directory cannot be the user home directory: %s", clean)
	}
	if cwd, err := os.Getwd(); err == nil && strings.TrimSpace(cwd) != "" && samePath(clean, cwd) {
		return fmt.Errorf("Codex working directory cannot be the source repository root: %s", clean)
	}
	return nil
}

func samePath(a, b string) bool {
	a = filepath.Clean(a)
	b = filepath.Clean(b)
	if os.PathSeparator == '\\' {
		return strings.EqualFold(a, b)
	}
	return a == b
}

func buildCodexPrompt(messages []Message) string {
	lang := detectCodexPromptLanguage(messages)
	wantsJSON := codexPromptWantsJSON(messages)
	var b strings.Builder
	if lang == LangEN {
		b.WriteString("Complete this model request for a novel-writing application.\n")
		b.WriteString("Return only the final answer. Do not explain your process, mention Codex, call tools, inspect files, or modify files.\n")
		if wantsJSON {
			b.WriteString("The final answer must be valid JSON only. Do not wrap it in Markdown.\n")
		}
		b.WriteString("\nOriginal role messages:\n")
	} else {
		b.WriteString("请为小说写作应用完成这次模型生成请求。\n")
		b.WriteString("只输出最终结果，不要解释过程，不要提到 Codex，不要调用工具，不要读取或修改文件。\n")
		if wantsJSON {
			b.WriteString("最终结果必须是合法 JSON，不能包裹 Markdown 代码块。\n")
		}
		b.WriteString("\n原始角色消息：\n")
	}
	for _, msg := range messages {
		role := strings.TrimSpace(msg.Role)
		if role == "" {
			role = "user"
		}
		b.WriteString("\n[")
		b.WriteString(role)
		b.WriteString("]\n")
		b.WriteString(msg.Content)
		b.WriteString("\n")
	}
	return b.String()
}

func detectCodexPromptLanguage(messages []Message) string {
	for _, msg := range messages {
		for _, r := range msg.Content {
			if r >= '\u4e00' && r <= '\u9fff' {
				return LangZH
			}
		}
	}
	return LangEN
}

func codexPromptWantsJSON(messages []Message) bool {
	for _, msg := range messages {
		lower := strings.ToLower(msg.Content)
		if strings.Contains(lower, "json") || strings.Contains(msg.Content, "合法 JSON") {
			return true
		}
	}
	return false
}

func withCodexStderr(prefix string, err error, stderr string) error {
	if strings.TrimSpace(stderr) == "" {
		return fmt.Errorf("%s: %w", prefix, err)
	}
	return fmt.Errorf("%s: %w; stderr: %s", prefix, err, redactSensitiveText(strings.TrimSpace(stderr)))
}

var sensitiveTextPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)bearer\s+[a-z0-9._~+/=-]+`),
	regexp.MustCompile(`sk-[a-z0-9_-]{8,}`),
	regexp.MustCompile(`(?i)("?(?:api[_-]?key|access[_-]?token|refresh[_-]?token|id[_-]?token|codex[_-]?token|auth[_-]?token|secret|password)"?\s*[:=]\s*)("[^"]*"|'[^']*'|[^\s,}]+)`),
}

func redactSensitiveText(s string) string {
	if s == "" {
		return s
	}
	s = sensitiveTextPatterns[0].ReplaceAllString(s, "Bearer [REDACTED]")
	s = sensitiveTextPatterns[1].ReplaceAllString(s, "sk-[REDACTED]")
	s = sensitiveTextPatterns[2].ReplaceAllString(s, `${1}"[REDACTED]"`)
	return s
}

type limitedBuffer struct {
	mu    sync.Mutex
	limit int
	buf   []byte
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.buf = append(b.buf, p...)
	if b.limit > 0 && len(b.buf) > b.limit {
		b.buf = b.buf[len(b.buf)-b.limit:]
	}
	return len(p), nil
}

func (b *limitedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return string(b.buf)
}
