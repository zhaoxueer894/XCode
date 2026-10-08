// Package bailian 实现阿里云百炼 Provider 的网络与协议适配。
package bailian

import (
	"bufio"         // 按行读取 SSE 响应。
	"bytes"         // 为每次重试重新创建相同请求体。
	"context"       // 取消 HTTP 请求和重试等待。
	"encoding/json" // 编解码百炼 JSON。
	"fmt"           // 构造不包含密钥的诊断错误。
	"io"            // 有上限地读取错误响应正文。
	"net/http"      // 发送 HTTPS 请求。
	"net/url"       // 校验百炼 API Base URL。
	"sort"          // 按工具调用 index 稳定排序。
	"strconv"       // 解析 Retry-After 秒数。
	"strings"       // 拼接文本、参数和请求 URL。
	"time"          // 实现可取消的有界退避。

	"xcode/internal/harness"
)

const (
	defaultModel      = "qwen-plus"
	defaultMaxRetries = 2
	defaultRetryDelay = 300 * time.Millisecond
	maxRetryDelay     = 10 * time.Second
	maxErrorBodyBytes = 64 << 10 // 错误正文最多读取 64 KiB。
	maxSSETokenBytes  = 2 << 20  // 单个 SSE data 行最多允许 2 MiB。
)

// Config 保存创建百炼 Client 所需的显式配置。
type Config struct {
	BaseURL        string        // 与 API Key 同一业务空间的 compatible-mode Base URL。
	APIKey         string        // 只用于 Authorization Header，不得记录到日志。
	Model          string        // 百炼模型 ID；空值使用 qwen-plus。
	HTTPClient     *http.Client  // 允许测试注入假的 Transport；nil 使用标准 Client。
	MaxRetries     int           // 临时失败后的额外尝试次数；0 使用默认值。
	RetryBaseDelay time.Duration // 指数退避初始值；0 使用默认值。
}

// Client 实现 harness.Model，并隐藏百炼鉴权、HTTP 和 SSE 协议细节。
type Client struct {
	endpoint       string
	apiKey         string
	model          string
	httpClient     *http.Client
	maxRetries     int
	retryBaseDelay time.Duration
}

// APIError 表示可以安全返回给 Harness 的结构化 Provider 错误。
type APIError struct {
	StatusCode int    // HTTP 状态码；流内错误可能是 200。
	Type       string // 服务端错误类型。
	Code       string // 服务端错误码。
	Message    string // 不包含 API Key 的服务端说明。
	RequestID  string // 用于在百炼控制台或工单中定位请求。
}

// Error 负责把结构化百炼错误转换成人可以阅读的单行文本。
func (e *APIError) Error() string {
	return fmt.Sprintf(
		"bailian API error: status=%d type=%q code=%q request_id=%q message=%q",
		e.StatusCode,
		e.Type,
		e.Code,
		e.RequestID,
		e.Message,
	)
}

// New 负责校验百炼配置，并创建一个尚未发起网络请求的 Client。
func New(config Config) (*Client, error) {
	baseURL := strings.TrimSpace(config.BaseURL)
	if baseURL == "" {
		return nil, fmt.Errorf("bailian base URL is required")
	}
	parsed, err := url.Parse(baseURL)
	if err != nil {
		return nil, fmt.Errorf("parse bailian base URL: %w", err)
	}
	if parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil {
		return nil, fmt.Errorf("bailian base URL must be HTTPS with a valid host and no user info")
	}
	if parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, fmt.Errorf("bailian base URL must not contain query or fragment")
	}
	if strings.TrimSpace(config.APIKey) == "" {
		return nil, fmt.Errorf("DASHSCOPE_API_KEY is required for bailian provider")
	}

	model := strings.TrimSpace(config.Model)
	if model == "" {
		model = defaultModel
	}
	httpClient := config.HTTPClient
	if httpClient == nil {
		// 整次运行的超时由外层 Context 控制。
		httpClient = &http.Client{}
	}
	maxRetries := config.MaxRetries
	if maxRetries == 0 {
		maxRetries = defaultMaxRetries
	}
	if maxRetries < 0 {
		return nil, fmt.Errorf("max retries must not be negative")
	}
	retryDelay := config.RetryBaseDelay
	if retryDelay == 0 {
		retryDelay = defaultRetryDelay
	}
	if retryDelay < 0 {
		return nil, fmt.Errorf("retry base delay must not be negative")
	}

	return &Client{
		endpoint:       strings.TrimRight(baseURL, "/") + "/chat/completions",
		apiKey:         config.APIKey,
		model:          model,
		httpClient:     httpClient,
		maxRetries:     maxRetries,
		retryBaseDelay: retryDelay,
	}, nil
}

// Generate 负责完成一次百炼流式模型调用，并转换成 Harness 统一事件与最终响应。
func (c *Client) Generate(
	ctx context.Context,
	messages []harness.Message,
	tools []harness.ToolSpec,
	sink harness.ModelEventSink,
) (harness.ModelResponse, error) {
	if err := ctx.Err(); err != nil {
		return harness.ModelResponse{}, err
	}
	requestBody, err := json.Marshal(c.buildRequest(messages, tools))
	if err != nil {
		return harness.ModelResponse{}, fmt.Errorf("encode bailian request: %w", err)
	}

	response, err := c.doWithRetry(ctx, requestBody)
	if err != nil {
		return harness.ModelResponse{}, err
	}
	defer response.Body.Close()

	return c.consumeSSE(ctx, response, sink)
}

// buildRequest 负责把 Harness 消息和工具声明转换成百炼请求体。
func (c *Client) buildRequest(messages []harness.Message, specs []harness.ToolSpec) chatRequest {
	request := chatRequest{
		Model:         c.model,
		Stream:        true,
		StreamOptions: streamOptions{IncludeUsage: true},
		Messages:      make([]chatMessage, 0, len(messages)),
		Tools:         make([]chatTool, 0, len(specs)),
	}

	for _, message := range messages {
		wireMessage := chatMessage{
			Role:       message.Role,
			Content:    message.Content,
			ToolCallID: message.ToolCallID,
			ToolCalls:  make([]chatToolCall, 0, len(message.ToolCalls)),
		}
		for _, call := range message.ToolCalls {
			wireMessage.ToolCalls = append(wireMessage.ToolCalls, chatToolCall{
				ID:   call.ID,
				Type: "function",
				Function: chatToolCallFunction{
					Name:      call.Name,
					Arguments: string(call.Arguments),
				},
			})
		}
		request.Messages = append(request.Messages, wireMessage)
	}

	for _, spec := range specs {
		request.Tools = append(request.Tools, chatTool{
			Type: "function",
			Function: chatToolFunction{
				Name:        spec.Name,
				Description: spec.Description,
				Parameters:  spec.InputSchema,
			},
		})
	}
	return request
}

// doWithRetry 负责发送请求，并只在响应流开始前处理临时错误的有界重试。
func (c *Client) doWithRetry(ctx context.Context, body []byte) (*http.Response, error) {
	for attempt := 0; attempt <= c.maxRetries; attempt++ {
		request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(body))
		if err != nil {
			return nil, fmt.Errorf("create bailian request: %w", err)
		}
		request.Header.Set("Authorization", "Bearer "+c.apiKey)
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Accept", "text/event-stream")
		request.Header.Set("User-Agent", "xcode/0.2")

		response, err := c.httpClient.Do(request)
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			if attempt == c.maxRetries {
				return nil, fmt.Errorf("send bailian request after %d attempts: %w", attempt+1, err)
			}
			if err := sleepContext(ctx, c.backoff(attempt, "")); err != nil {
				return nil, err
			}
			continue
		}

		if response.StatusCode >= 200 && response.StatusCode < 300 {
			return response, nil
		}

		// 关闭失败响应 Body 后才能安全发起下一次尝试。
		errorBody, readErr := io.ReadAll(io.LimitReader(response.Body, maxErrorBodyBytes))
		response.Body.Close()
		if readErr != nil {
			return nil, fmt.Errorf("read bailian error response: %w", readErr)
		}
		apiErr := decodeAPIError(response.StatusCode, requestIDFromHeader(response.Header), errorBody)
		if !isRetryableStatus(response.StatusCode) || attempt == c.maxRetries {
			return nil, apiErr
		}
		if err := sleepContext(ctx, c.backoff(attempt, response.Header.Get("Retry-After"))); err != nil {
			return nil, err
		}
	}

	return nil, fmt.Errorf("bailian retry loop ended unexpectedly")
}

// toolCallAccumulator 保存同一 index 的工具调用增量。
type toolCallAccumulator struct {
	id        string
	name      string
	arguments strings.Builder
}

// consumeSSE 负责读取百炼 SSE 流，并同时产生过程事件与最终模型响应。
func (c *Client) consumeSSE(
	ctx context.Context,
	response *http.Response,
	sink harness.ModelEventSink,
) (harness.ModelResponse, error) {
	emit := func(event harness.ModelEvent) {
		if sink != nil {
			sink.EmitModelEvent(event)
		}
	}

	var content strings.Builder
	usage := harness.Usage{}
	finishReason := ""
	toolCalls := make(map[int]*toolCallAccumulator)
	sawFinish := false

	scanner := bufio.NewScanner(response.Body)
	// 工具参数可能较长，因此提高 Scanner 默认的 64 KiB 上限。
	scanner.Buffer(make([]byte, 64<<10), maxSSETokenBytes)
	for scanner.Scan() {
		if err := ctx.Err(); err != nil {
			return harness.ModelResponse{}, err
		}
		line := scanner.Text()
		if line == "" || strings.HasPrefix(line, ":") {
			continue
		}
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "[DONE]" {
			break
		}
		if data == "" {
			continue
		}

		// 百炼可能在已经建立的 HTTP 200 流中返回结构化错误。
		var errorPayload apiErrorEnvelope
		if err := json.Unmarshal([]byte(data), &errorPayload); err == nil && hasAPIError(errorPayload) {
			return harness.ModelResponse{}, newAPIError(
				response.StatusCode,
				firstNonEmpty(requestIDFromHeader(response.Header), errorPayload.RequestID),
				errorPayload,
			)
		}

		var chunk chatChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			return harness.ModelResponse{}, fmt.Errorf("decode bailian SSE data: %w", err)
		}
		if chunk.Usage != nil {
			usage = harness.Usage{
				InputTokens:  chunk.Usage.PromptTokens,
				OutputTokens: chunk.Usage.CompletionTokens,
				TotalTokens:  chunk.Usage.TotalTokens,
			}
			emit(harness.ModelEvent{Kind: harness.ModelUsage, Usage: usage})
		}

		for _, choice := range chunk.Choices {
			if choice.Delta.Content != "" {
				content.WriteString(choice.Delta.Content)
				emit(harness.ModelEvent{
					Kind:      harness.ModelTextDelta,
					TextDelta: choice.Delta.Content,
				})
			}
			for _, delta := range choice.Delta.ToolCalls {
				accumulator := toolCalls[delta.Index]
				if accumulator == nil {
					accumulator = &toolCallAccumulator{}
					toolCalls[delta.Index] = accumulator
				}
				if delta.ID != "" {
					accumulator.id = delta.ID
				}
				if delta.Function.Name != "" {
					accumulator.name = delta.Function.Name
				}
				accumulator.arguments.WriteString(delta.Function.Arguments)
				emit(harness.ModelEvent{
					Kind:           harness.ModelToolCallDelta,
					ToolCallIndex:  delta.Index,
					ToolCallID:     accumulator.id,
					ToolName:       accumulator.name,
					ArgumentsDelta: delta.Function.Arguments,
				})
			}
			if choice.FinishReason != "" {
				finishReason = choice.FinishReason
				sawFinish = true
				emit(harness.ModelEvent{
					Kind:         harness.ModelFinished,
					FinishReason: finishReason,
				})
			}
		}
	}
	if err := scanner.Err(); err != nil {
		if ctx.Err() != nil {
			return harness.ModelResponse{}, ctx.Err()
		}
		return harness.ModelResponse{}, fmt.Errorf("read bailian SSE stream: %w", err)
	}
	if !sawFinish {
		return harness.ModelResponse{}, fmt.Errorf("bailian SSE stream ended before finish event")
	}

	indices := make([]int, 0, len(toolCalls))
	for index := range toolCalls {
		indices = append(indices, index)
	}
	sort.Ints(indices)
	completedCalls := make([]harness.ToolCall, 0, len(indices))
	for _, index := range indices {
		accumulator := toolCalls[index]
		if accumulator.id == "" || accumulator.name == "" {
			return harness.ModelResponse{}, fmt.Errorf("incomplete tool call at index %d", index)
		}
		arguments := accumulator.arguments.String()
		if arguments == "" {
			arguments = "{}"
		}
		if !json.Valid([]byte(arguments)) {
			return harness.ModelResponse{}, fmt.Errorf("invalid tool arguments JSON at index %d", index)
		}
		completedCalls = append(completedCalls, harness.ToolCall{
			ID:        accumulator.id,
			Name:      accumulator.name,
			Arguments: json.RawMessage(arguments),
		})
	}

	return harness.ModelResponse{
		Content:      content.String(),
		ToolCalls:    completedCalls,
		Usage:        usage,
		FinishReason: finishReason,
	}, nil
}

// decodeAPIError 负责把非 2xx 响应正文转换成安全的 APIError。
func decodeAPIError(status int, requestID string, body []byte) *APIError {
	var payload apiErrorEnvelope
	if err := json.Unmarshal(body, &payload); err != nil {
		return &APIError{
			StatusCode: status,
			Message:    http.StatusText(status),
			RequestID:  requestID,
		}
	}
	return newAPIError(status, firstNonEmpty(requestID, payload.RequestID), payload)
}

// newAPIError 负责从百炼错误中挑选允许进入日志的安全字段。
func newAPIError(status int, requestID string, payload apiErrorEnvelope) *APIError {
	apiErr := &APIError{
		StatusCode: status,
		RequestID:  requestID,
		Code:       stringifyCode(payload.Code),
		Message:    payload.Message,
	}
	if payload.Error != nil {
		apiErr.Type = payload.Error.Type
		apiErr.Code = stringifyCode(payload.Error.Code)
		apiErr.Message = payload.Error.Message
	}
	if apiErr.Message == "" {
		apiErr.Message = http.StatusText(status)
	}
	return apiErr
}

// hasAPIError 判断一个已解析 JSON 是否是百炼错误对象。
func hasAPIError(payload apiErrorEnvelope) bool {
	if payload.Error != nil {
		return payload.Error.Message != "" || payload.Error.Code != nil || payload.Error.Type != ""
	}
	return payload.Message != "" || payload.Code != nil
}

// requestIDFromHeader 从百炼可能使用的响应 Header 中提取 Request ID。
func requestIDFromHeader(header http.Header) string {
	return firstNonEmpty(
		header.Get("X-Request-Id"),
		header.Get("X-Dashscope-Request-Id"),
		header.Get("Request-Id"),
	)
}

// stringifyCode 把字符串、数字或 null 错误码统一转换成字符串。
func stringifyCode(code any) string {
	if code == nil {
		return ""
	}
	return fmt.Sprint(code)
}

// isRetryableStatus 判断一个 HTTP 状态是否属于允许重试的临时故障。
func isRetryableStatus(status int) bool {
	switch status {
	case http.StatusTooManyRequests,
		http.StatusInternalServerError,
		http.StatusBadGateway,
		http.StatusServiceUnavailable,
		http.StatusGatewayTimeout:
		return true
	default:
		return false
	}
}

// backoff 计算下一次请求前的等待时间，并限制最大值。
func (c *Client) backoff(attempt int, retryAfter string) time.Duration {
	if delay, ok := parseRetryAfter(retryAfter); ok {
		if delay > maxRetryDelay {
			return maxRetryDelay
		}
		return delay
	}
	delay := c.retryBaseDelay * time.Duration(1<<attempt)
	if delay > maxRetryDelay {
		return maxRetryDelay
	}
	return delay
}

// parseRetryAfter 解析 Retry-After 支持的秒数或 HTTP 日期格式。
func parseRetryAfter(value string) (time.Duration, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, false
	}
	if seconds, err := strconv.Atoi(value); err == nil && seconds >= 0 {
		return time.Duration(seconds) * time.Second, true
	}
	when, err := http.ParseTime(value)
	if err != nil {
		return 0, false
	}
	delay := time.Until(when)
	if delay < 0 {
		delay = 0
	}
	return delay, true
}

// sleepContext 执行一次能够立即响应取消或超时的退避等待。
func sleepContext(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// firstNonEmpty 从多个候选字符串中返回第一个非空值。
func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
