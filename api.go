package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type ChatRequest struct {
	Model         string         `json:"model"`
	Messages      []Message      `json:"messages"`
	Stream        bool           `json:"stream,omitempty"`
	StreamOptions *streamOptions `json:"stream_options,omitempty"`
	MaxTokens     int            `json:"max_tokens,omitempty"`
}

type streamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

type tokenUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type ChatResponse struct {
	Choices []struct {
		Message Message `json:"message"`
	} `json:"choices"`
	Usage *tokenUsage `json:"usage,omitempty"`
}

type LLMRequest struct {
	Messages []Message
}

type LLMResponse struct {
	Content string
}

type LLMProvider interface {
	Generate(ctx context.Context, req LLMRequest) (*LLMResponse, error)
	Stream(ctx context.Context, req LLMRequest, onChunk func(string)) (*LLMResponse, error)
}

var errProviderStreamUnsupported = errors.New("provider stream unsupported")

type OpenAIProvider struct {
	cfg APIConfig
}

func providerFromConfig(apiCfg *APIConfig) (LLMProvider, error) {
	if apiCfg == nil {
		return nil, fmt.Errorf("API 配置为空")
	}
	cfg := *apiCfg
	normalizeAPIConfig(&cfg)

	switch cfg.Provider {
	case ProviderOpenAICompatible:
		return &OpenAIProvider{cfg: cfg}, nil
	case ProviderCodex:
		return &CodexProvider{cfg: cfg}, nil
	default:
		return nil, fmt.Errorf("不支持的模型提供方: %s", cfg.Provider)
	}
}

func normalizeURL(base string) string {
	base = strings.TrimSpace(base)
	base = strings.TrimSuffix(base, "/")

	if !strings.HasSuffix(base, "/v1") && !strings.Contains(base, "/v1/") {
		if !strings.Contains(base, "11434") {
			base = base + "/v1"
		}
	}

	return base + "/chat/completions"
}

// FetchModelContextWindow 从 API 的 /models 端点获取指定模型的上下文窗口大小。
// 成功返回 context_window > 0，失败返回 0（调用方应使用默认值）。
func FetchModelContextWindow(apiCfg *APIConfig) int {
	provider := ProviderOpenAICompatible
	if apiCfg != nil && apiCfg.Provider != "" {
		provider = apiCfg.Provider
	}
	if provider != ProviderOpenAICompatible {
		return 0
	}
	if apiCfg == nil || strings.TrimSpace(apiCfg.BaseURL) == "" || strings.TrimSpace(apiCfg.Model) == "" {
		return 0
	}
	base := strings.TrimSpace(apiCfg.BaseURL)
	base = strings.TrimSuffix(base, "/")
	if !strings.HasSuffix(base, "/v1") && !strings.Contains(base, "/v1/") {
		if !strings.Contains(base, "11434") {
			base = base + "/v1"
		}
	}
	modelsURL := base + "/models/" + apiCfg.Model

	req, err := http.NewRequest("GET", modelsURL, nil)
	if err != nil {
		return 0
	}
	if apiCfg.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiCfg.APIKey)
	}

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return 0
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return 0
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0
	}

	var result struct {
		ContextWindow int `json:"context_window"`
	}
	if err := json.Unmarshal(body, &result); err != nil || result.ContextWindow <= 0 {
		return 0
	}
	return result.ContextWindow
}

func validateAPIConfig(apiCfg *APIConfig) error {
	if apiCfg == nil {
		return fmt.Errorf("API 配置为空")
	}
	cfg := *apiCfg
	normalizeAPIConfig(&cfg)

	switch cfg.Provider {
	case ProviderOpenAICompatible:
		if strings.TrimSpace(cfg.BaseURL) == "" {
			return fmt.Errorf("API Base URL 未配置")
		}
		if strings.TrimSpace(cfg.Model) == "" {
			return fmt.Errorf("Model 未配置")
		}
		return nil
	case ProviderCodex:
		if strings.TrimSpace(cfg.CodexModel) == "" {
			return fmt.Errorf("Codex model not configured")
		}
		if strings.TrimSpace(cfg.CodexWorkingDir) == "" {
			return fmt.Errorf("Codex working directory not configured")
		}
		return nil
	default:
		return fmt.Errorf("不支持的模型提供方: %s", cfg.Provider)
	}
}

func isFatalAPIError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, errProviderStreamUnsupported) {
		return true
	}
	msg := err.Error()
	// 注意：不要把所有 "dial tcp" 都当作致命错误——
	// "dial tcp ... i/o timeout" 等临时网络故障应当重试。
	if strings.Contains(msg, "connection refused") ||
		strings.Contains(msg, "no such host") {
		return true
	}
	if strings.Contains(msg, "状态码: 401") ||
		strings.Contains(msg, "状态码: 403") ||
		strings.Contains(msg, "状态码: 404") {
		return true
	}
	if strings.Contains(msg, "context canceled") {
		return true
	}
	if strings.Contains(msg, "API 配置为空") ||
		strings.Contains(msg, "不支持的模型提供方") {
		return true
	}
	if strings.Contains(msg, "Codex model") ||
		strings.Contains(msg, "Codex working directory") ||
		strings.Contains(msg, "codex executable not found") ||
		strings.Contains(msg, "codex app-server") ||
		strings.Contains(msg, "codex turn failed") ||
		strings.Contains(msg, "codex turn interrupted") ||
		strings.Contains(msg, "turn/start failed") {
		return true
	}
	return false
}

func CallAPI(ctx context.Context, apiCfg *APIConfig, system, user string) (string, error) {
	return CallAPIMessages(ctx, apiCfg, []Message{
		{Role: "system", Content: system},
		{Role: "user", Content: user},
	})
}

// CallAPIMessages 以完整的多轮消息数组调用 API。
// 内部优先走流式并缓冲全文，使 token 计数在等待期间也能更新；流式不可用时回退同步请求。
func CallAPIMessages(ctx context.Context, apiCfg *APIConfig, messages []Message) (string, error) {
	result, err := CallAPIStreamMessages(ctx, apiCfg, messages, nil)
	if err == nil && result != "" {
		return result, nil
	}
	if ctx.Err() != nil {
		if result != "" {
			return result, ctx.Err()
		}
		return "", ctx.Err()
	}
	if result != "" {
		return result, err
	}
	if err != nil && isFatalAPIError(err) && !errors.Is(err, errProviderStreamUnsupported) {
		return "", err
	}
	// Fall back for providers whose stream path is unavailable.
	provider, providerErr := providerFromConfig(apiCfg)
	if providerErr != nil {
		return "", providerErr
	}
	resp, err := provider.Generate(ctx, LLMRequest{Messages: messages})
	if err != nil {
		return "", err
	}
	if resp == nil {
		return "", fmt.Errorf("接口未响应有效 Choices 文本")
	}
	return resp.Content, nil
}

// Generate performs a synchronous OpenAI-compatible HTTP call.
func (p *OpenAIProvider) Generate(ctx context.Context, req LLMRequest) (*LLMResponse, error) {
	fullURL := normalizeURL(p.cfg.BaseURL)
	tracker := taskTokensFromContext(ctx)
	tracker.beginCall(req.Messages)

	reqBody := ChatRequest{
		Model:     p.cfg.Model,
		Messages:  req.Messages,
		MaxTokens: p.cfg.MaxTokens,
	}

	bts, err := json.Marshal(reqBody)
	if err != nil {
		return nil, err
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", fullURL, bytes.NewBuffer(bts))
	if err != nil {
		return nil, err
	}

	httpReq.Header.Set("Content-Type", "application/json")
	if p.cfg.APIKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+p.cfg.APIKey)
	}

	timeout := time.Duration(p.cfg.HTTPTimeoutSeconds) * time.Second
	client := &http.Client{Timeout: timeout}
	resp, err := client.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("API 响应错误，状态码: %d, 返回内容: %s", resp.StatusCode, redactSensitiveText(string(bodyBytes)))
	}

	var chatResp ChatResponse
	if err := json.Unmarshal(bodyBytes, &chatResp); err != nil {
		return nil, err
	}

	if len(chatResp.Choices) > 0 {
		content := chatResp.Choices[0].Message.Content
		if tracker != nil {
			if chatResp.Usage != nil {
				tracker.finishCall(chatResp.Usage.PromptTokens, chatResp.Usage.CompletionTokens, true, req.Messages, content)
			} else {
				tracker.finishCall(0, 0, false, req.Messages, content)
			}
		}
		return &LLMResponse{Content: content}, nil
	}
	return nil, fmt.Errorf("接口未响应有效 Choices 文本")
}

func CallAPIWithRetry(ctx context.Context, apiCfg *APIConfig, system, user string) string {
	retryCount := 0
	for {
		if ctx.Err() != nil {
			return ""
		}
		result, err := CallAPI(ctx, apiCfg, system, user)
		if err == nil && result != "" {
			return result
		}
		if isFatalAPIError(err) {
			fmt.Printf(" ❌ [致命错误] %v，不再重试\n", err)
			return ""
		}

		retryCount++
		waitTime := getWaitTime(retryCount)
		fmt.Printf(" ⚠️ [错误] API调用失败: %v。第 %d 次重试，等待 %ds 后重试...\n", err, retryCount, waitTime)
		select {
		case <-time.After(time.Duration(waitTime) * time.Second):
		case <-ctx.Done():
			return ""
		}
	}
}

func CallAPIWithRetryLog(ctx context.Context, apiCfg *APIConfig, system, user string, logger *LogBroadcaster) string {
	retryCount := 0
	for {
		if ctx.Err() != nil {
			return ""
		}
		result, err := CallAPI(ctx, apiCfg, system, user)
		if err == nil && result != "" {
			return result
		}
		if isFatalAPIError(err) {
			logger.ErrorKey("log.fatal_no_retry", err)
			return ""
		}

		retryCount++
		waitTime := getWaitTime(retryCount)
		logger.WarnKey("log.api_retry", err, retryCount, waitTime)
		select {
		case <-time.After(time.Duration(waitTime) * time.Second):
		case <-ctx.Done():
			return ""
		}
	}
}

func getWaitTime(retry int) int {
	if retry > 6 {
		return 30
	}
	return retry * 5
}

type streamDelta struct {
	Choices []struct {
		Delta struct {
			Content string `json:"content"`
		} `json:"delta"`
	} `json:"choices"`
	Usage *tokenUsage `json:"usage,omitempty"`
}

func CallAPIStream(ctx context.Context, apiCfg *APIConfig, system, user string, onChunk func(string)) (string, error) {
	return CallAPIStreamMessages(ctx, apiCfg, []Message{
		{Role: "system", Content: system},
		{Role: "user", Content: user},
	}, onChunk)
}

// CallAPIStreamMessages 以完整的多轮消息数组调用 API（流式）。
func CallAPIStreamMessages(ctx context.Context, apiCfg *APIConfig, messages []Message, onChunk func(string)) (string, error) {
	provider, err := providerFromConfig(apiCfg)
	if err != nil {
		return "", err
	}
	resp, err := provider.Stream(ctx, LLMRequest{Messages: messages}, onChunk)
	if err != nil {
		if resp != nil {
			return resp.Content, err
		}
		return "", err
	}
	if resp == nil {
		return "", fmt.Errorf("流式响应为空")
	}
	return resp.Content, nil
}

// Stream performs an OpenAI-compatible streaming HTTP call.
func (p *OpenAIProvider) Stream(ctx context.Context, req LLMRequest, onChunk func(string)) (*LLMResponse, error) {
	fullURL := normalizeURL(p.cfg.BaseURL)
	tracker := taskTokensFromContext(ctx)
	tracker.beginCall(req.Messages)

	reqBody := ChatRequest{
		Model:         p.cfg.Model,
		Messages:      req.Messages,
		Stream:        true,
		StreamOptions: &streamOptions{IncludeUsage: true},
		MaxTokens:     p.cfg.MaxTokens,
	}

	bts, err := json.Marshal(reqBody)
	if err != nil {
		return nil, err
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", fullURL, bytes.NewBuffer(bts))
	if err != nil {
		return nil, err
	}

	httpReq.Header.Set("Content-Type", "application/json")
	if p.cfg.APIKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+p.cfg.APIKey)
	}

	timeout := time.Duration(p.cfg.HTTPTimeoutSeconds) * time.Second
	client := &http.Client{Timeout: timeout}
	resp, err := client.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("API 响应错误，状态码: %d, 返回内容: %s", resp.StatusCode, redactSensitiveText(string(bodyBytes)))
	}

	var fullContent strings.Builder
	scanner := bufio.NewScanner(resp.Body)
	var streamUsage *tokenUsage

	for scanner.Scan() {
		if ctx.Err() != nil {
			return &LLMResponse{Content: fullContent.String()}, ctx.Err()
		}
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")
		if data == "[DONE]" {
			break
		}

		var delta streamDelta
		if err := json.Unmarshal([]byte(data), &delta); err != nil {
			continue
		}
		if delta.Usage != nil {
			streamUsage = delta.Usage
		}
		if len(delta.Choices) > 0 && delta.Choices[0].Delta.Content != "" {
			chunk := delta.Choices[0].Delta.Content
			fullContent.WriteString(chunk)
			if tracker != nil {
				tracker.updateStreamContent(fullContent.String())
			}
			if onChunk != nil {
				onChunk(chunk)
			}
		}
	}

	result := fullContent.String()
	if result == "" {
		return nil, fmt.Errorf("流式响应为空")
	}
	if tracker != nil {
		if streamUsage != nil {
			tracker.finishCall(streamUsage.PromptTokens, streamUsage.CompletionTokens, true, req.Messages, result)
		} else {
			tracker.finishCall(0, 0, false, req.Messages, result)
		}
	}
	return &LLMResponse{Content: result}, nil
}

func CallAPIStreamWithRetry(ctx context.Context, apiCfg *APIConfig, system, user string, onChunk func(string)) string {
	retryCount := 0
	for {
		if ctx.Err() != nil {
			return ""
		}
		result, err := CallAPIStream(ctx, apiCfg, system, user, onChunk)
		if err == nil && result != "" {
			return result
		}
		if isFatalAPIError(err) {
			fmt.Printf(" ❌ [致命错误] %v，不再重试\n", err)
			return ""
		}

		retryCount++
		waitTime := getWaitTime(retryCount)
		fmt.Printf(" ⚠️ [错误] 流式API调用失败: %v。第 %d 次重试，等待 %ds 后重试...\n", err, retryCount, waitTime)
		select {
		case <-time.After(time.Duration(waitTime) * time.Second):
		case <-ctx.Done():
			return ""
		}
	}
}

func CallAPIStreamWithRetryLog(ctx context.Context, apiCfg *APIConfig, system, user string, onChunk func(string), logger *LogBroadcaster) string {
	retryCount := 0
	for {
		if ctx.Err() != nil {
			return ""
		}
		result, err := CallAPIStream(ctx, apiCfg, system, user, onChunk)
		if err == nil && result != "" {
			return result
		}
		if isFatalAPIError(err) {
			logger.ErrorKey("log.fatal_no_retry", err)
			return ""
		}

		retryCount++
		waitTime := getWaitTime(retryCount)
		logger.WarnKey("log.api_stream_retry", err, retryCount, waitTime)
		select {
		case <-time.After(time.Duration(waitTime) * time.Second):
		case <-ctx.Done():
			return ""
		}
	}
}
