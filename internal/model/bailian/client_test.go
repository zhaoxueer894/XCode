package bailian

// 本文件使用内存中的假 HTTPS 传输验证百炼协议，不访问网络或读取真实 API Key。

import (
	"context"       // 构造正常与超时请求。
	"encoding/json" // 检查请求体和工具参数。
	"errors"        // 判断 APIError 与 Context 超时错误。
	"io"            // 构造测试响应 Body。
	"net/http"      // 构造假 Transport、Header 和状态响应。
	"reflect"       // 比较归一化事件顺序。
	"strings"       // 构造 SSE 并检查安全错误文本。
	"sync/atomic"   // 统计重试次数。
	"testing"       // Go 标准测试框架。
	"time"          // 控制重试和取消测试时长。

	"xcode/internal/harness" // 检查 Provider 归一化后的公共类型。
)

const testAPIKey = "test-only-secret"

// TestNewValidatesConfiguration 验证 Client 在联网前拒绝不安全或缺失的配置。
func TestNewValidatesConfiguration(t *testing.T) {
	tests := []struct {
		name   string
		config Config
	}{
		{name: "missing URL", config: Config{APIKey: testAPIKey}},
		{name: "plain HTTP", config: Config{BaseURL: "http://example.com/v1", APIKey: testAPIKey}},
		{name: "URL credentials", config: Config{BaseURL: "https://user@example.com/v1", APIKey: testAPIKey}},
		{name: "URL query", config: Config{BaseURL: "https://example.com/v1?token=bad", APIKey: testAPIKey}},
		{name: "missing key", config: Config{BaseURL: "https://example.com/v1"}},
		{name: "negative retries", config: Config{BaseURL: "https://example.com/v1", APIKey: testAPIKey, MaxRetries: -1}},
		{name: "negative delay", config: Config{BaseURL: "https://example.com/v1", APIKey: testAPIKey, RetryBaseDelay: -1}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := New(test.config); err == nil {
				t.Fatal("invalid configuration was accepted")
			}
		})
	}
}

// TestGenerateNormalizesTextUsageAndFinish 验证 HTTPS 请求与文本 SSE 被完整归一化。
func TestGenerateNormalizesTextUsageAndFinish(t *testing.T) {
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.Method != http.MethodPost || request.URL.String() != "https://example.com/v1/chat/completions" {
			t.Errorf("request = %s %s", request.Method, request.URL)
		}
		if got := request.Header.Get("Authorization"); got != "Bearer "+testAPIKey {
			t.Errorf("Authorization header was not configured")
		}
		if got := request.Header.Get("Accept"); got != "text/event-stream" {
			t.Errorf("Accept = %q", got)
		}

		var body chatRequest
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
		}
		if body.Model != "qwen-test" || !body.Stream || !body.StreamOptions.IncludeUsage {
			t.Errorf("request options = %#v", body)
		}
		if len(body.Messages) != 1 || body.Messages[0].Content != "你好" {
			t.Errorf("request messages = %#v", body.Messages)
		}
		if len(body.Tools) != 1 || body.Tools[0].Function.Name != "read_file" {
			t.Errorf("request tools = %#v", body.Tools)
		}

		return sseResponse(
			`{"choices":[{"index":0,"delta":{"content":"你"},"finish_reason":null}]}`,
			`{"choices":[{"index":0,"delta":{"content":"好"},"finish_reason":null}]}`,
			`{"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
			`{"choices":[],"usage":{"prompt_tokens":8,"completion_tokens":2,"total_tokens":10}}`,
		), nil
	})
	client := newTestClient(t, transport, 1)

	var events []harness.ModelEvent
	response, err := client.Generate(
		context.Background(),
		[]harness.Message{{Role: "user", Content: "你好"}},
		[]harness.ToolSpec{{Name: "read_file", InputSchema: map[string]any{"type": "object"}}},
		harness.ModelEventSinkFunc(func(event harness.ModelEvent) { events = append(events, event) }),
	)
	if err != nil {
		t.Fatal(err)
	}
	if response.Content != "你好" || response.FinishReason != "stop" || response.Usage.TotalTokens != 10 {
		t.Fatalf("response = %#v", response)
	}
	wantKinds := []harness.ModelEventKind{
		harness.ModelTextDelta,
		harness.ModelTextDelta,
		harness.ModelFinished,
		harness.ModelUsage,
	}
	gotKinds := make([]harness.ModelEventKind, 0, len(events))
	for _, event := range events {
		gotKinds = append(gotKinds, event.Kind)
	}
	if !reflect.DeepEqual(gotKinds, wantKinds) {
		t.Fatalf("event kinds = %v, want %v", gotKinds, wantKinds)
	}
}

// TestGenerateAssemblesToolCallDeltas 验证分散在多个 chunk 中的参数能组合成完整工具调用。
func TestGenerateAssemblesToolCallDeltas(t *testing.T) {
	transport := staticSSETransport(
		`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call-7","type":"function","function":{"name":"read_file","arguments":"{\"pa"}}]},"finish_reason":null}]}`,
		`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"th\":\"README.md\"}"}}]},"finish_reason":null}]}`,
		`{"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
	)
	client := newTestClient(t, transport, 1)

	var events []harness.ModelEvent
	response, err := client.Generate(context.Background(), nil, nil, harness.ModelEventSinkFunc(func(event harness.ModelEvent) {
		events = append(events, event)
	}))
	if err != nil {
		t.Fatal(err)
	}
	if len(response.ToolCalls) != 1 {
		t.Fatalf("tool calls = %#v", response.ToolCalls)
	}
	call := response.ToolCalls[0]
	if call.ID != "call-7" || call.Name != "read_file" || string(call.Arguments) != `{"path":"README.md"}` {
		t.Fatalf("tool call = %#v", call)
	}
	if len(events) != 3 || events[0].ArgumentsDelta != `{"pa` || events[1].ArgumentsDelta != `th":"README.md"}` {
		t.Fatalf("tool delta events = %#v", events)
	}
}

// TestGenerateReturnsSafeAPIErrorWithoutRetry 验证 401 会保留诊断字段、隐藏密钥且不重试。
func TestGenerateReturnsSafeAPIErrorWithoutRetry(t *testing.T) {
	var calls atomic.Int32
	transport := roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		calls.Add(1)
		return jsonErrorResponse(
			http.StatusUnauthorized,
			`{"error":{"message":"invalid credentials","type":"authentication_error","code":"invalid_api_key"}}`,
			http.Header{"X-Request-Id": []string{"request-401"}},
		), nil
	})
	client := newTestClient(t, transport, 2)

	_, err := client.Generate(context.Background(), nil, nil, nil)
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("error = %T %v, want APIError", err, err)
	}
	if apiErr.StatusCode != http.StatusUnauthorized || apiErr.RequestID != "request-401" || calls.Load() != 1 {
		t.Fatalf("API error = %#v, calls = %d", apiErr, calls.Load())
	}
	if strings.Contains(err.Error(), testAPIKey) {
		t.Fatal("API key leaked into error")
	}
}

// TestGenerateRetriesRateLimitBeforeStream 验证 429 只在流建立前按上限重试。
func TestGenerateRetriesRateLimitBeforeStream(t *testing.T) {
	var calls atomic.Int32
	transport := roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		if calls.Add(1) == 1 {
			return jsonErrorResponse(
				http.StatusTooManyRequests,
				`{"error":{"message":"slow down","type":"rate_limit","code":"rate_limit"}}`,
				http.Header{"Retry-After": []string{"0"}},
			), nil
		}
		return sseResponse(`{"choices":[{"index":0,"delta":{"content":"ok"},"finish_reason":"stop"}]}`), nil
	})
	client := newTestClient(t, transport, 1)

	response, err := client.Generate(context.Background(), nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if response.Content != "ok" || calls.Load() != 2 {
		t.Fatalf("response = %#v, calls = %d", response, calls.Load())
	}
}

// TestGenerateDoesNotRetryBrokenStartedStream 验证已收到 SSE 后的断流不会自动重放请求。
func TestGenerateDoesNotRetryBrokenStartedStream(t *testing.T) {
	var calls atomic.Int32
	transport := roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		calls.Add(1)
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body: &failingBody{
				data: []byte("data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"partial\"},\"finish_reason\":null}]}\n\n"),
				err:  errors.New("connection lost"),
			},
		}, nil
	})
	client := newTestClient(t, transport, 2)

	_, err := client.Generate(context.Background(), nil, nil, nil)
	if err == nil || !strings.Contains(err.Error(), "read bailian SSE stream") {
		t.Fatalf("error = %v, want stream read error", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("started stream was retried %d times", calls.Load())
	}
}

// TestGenerateHonorsContextDeadline 验证取消信号能够中止正在等待的 HTTP 请求。
func TestGenerateHonorsContextDeadline(t *testing.T) {
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		<-request.Context().Done()
		return nil, request.Context().Err()
	})
	client := newTestClient(t, transport, 2)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err := client.Generate(ctx, nil, nil, nil)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v, want context deadline exceeded", err)
	}
}

// TestGenerateRejectsInvalidToolCall 验证非法工具参数不会进入执行层。
func TestGenerateRejectsInvalidToolCall(t *testing.T) {
	transport := staticSSETransport(
		`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call-bad","function":{"name":"read_file","arguments":"not-json"}}]},"finish_reason":"tool_calls"}]}`,
	)
	client := newTestClient(t, transport, 1)

	_, err := client.Generate(context.Background(), nil, nil, nil)
	if err == nil || !strings.Contains(err.Error(), "invalid tool arguments JSON") {
		t.Fatalf("error = %v, want invalid tool arguments", err)
	}
}

// TestGenerateReturnsStreamError 验证 HTTP 200 流中的 Provider 错误也会结构化返回。
func TestGenerateReturnsStreamError(t *testing.T) {
	transport := staticSSETransport(
		`{"code":"DataInspectionFailed","message":"input rejected","request_id":"stream-request"}`,
	)
	client := newTestClient(t, transport, 1)

	_, err := client.Generate(context.Background(), nil, nil, nil)
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Code != "DataInspectionFailed" || apiErr.RequestID != "stream-request" {
		t.Fatalf("error = %#v", err)
	}
}

// newTestClient 使用内存 Transport 创建不会访问外网的百炼 Client。
func newTestClient(t *testing.T, transport http.RoundTripper, maxRetries int) *Client {
	t.Helper()
	client, err := New(Config{
		BaseURL:        "https://example.com/v1",
		APIKey:         testAPIKey,
		Model:          "qwen-test",
		HTTPClient:     &http.Client{Transport: transport},
		MaxRetries:     maxRetries,
		RetryBaseDelay: time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	return client
}

// staticSSETransport 返回一个每次都发送相同 SSE 流的内存 Transport。
func staticSSETransport(chunks ...string) http.RoundTripper {
	return roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		return sseResponse(chunks...), nil
	})
}

// sseResponse 把 JSON chunks 包装成符合 data 行与 DONE 结束标记的响应。
func sseResponse(chunks ...string) *http.Response {
	var stream strings.Builder
	for _, chunk := range chunks {
		stream.WriteString("data: ")
		stream.WriteString(chunk)
		stream.WriteString("\n\n")
	}
	stream.WriteString("data: [DONE]\n\n")
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       io.NopCloser(strings.NewReader(stream.String())),
	}
}

// jsonErrorResponse 创建一个带状态码、JSON 正文和诊断 Header 的失败响应。
func jsonErrorResponse(status int, body string, header http.Header) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     header,
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

// roundTripFunc 让测试函数直接充当 HTTP Transport。
type roundTripFunc func(*http.Request) (*http.Response, error)

// RoundTrip 把 HTTP 请求转发给测试提供的函数。
func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

// failingBody 先返回一段有效 SSE 数据，再模拟连接中断。
type failingBody struct {
	data []byte
	err  error
}

// Read 负责先交付测试数据，并在下一次读取时返回预设错误。
func (body *failingBody) Read(buffer []byte) (int, error) {
	if len(body.data) > 0 {
		read := copy(buffer, body.data)
		body.data = body.data[read:]
		return read, nil
	}
	return 0, body.err
}

// Close 满足 io.ReadCloser；测试 Body 没有额外资源需要释放。
func (body *failingBody) Close() error {
	return nil
}

// 编译期断言确保测试 Body 实现了 HTTP 响应所需的接口。
var _ io.ReadCloser = (*failingBody)(nil)
