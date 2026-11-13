package chatcompletions

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

func TestDeepSeekPlannerHandoffUsesJSONAndRecoversFromToolMarkup(t *testing.T) {
	const plan = "Inspect numeric comparison overrides and test both operand orders."
	for _, test := range []struct {
		name      string
		malformed bool
	}{
		{name: "first handoff"},
		{name: "corrected handoff", malformed: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			calls := 0
			transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
				calls++
				var body chatRequest
				if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
					t.Fatal(err)
				}
				if body.ToolChoice != "none" || len(body.Tools) != 0 ||
					body.ResponseFormat == nil || body.ResponseFormat.Type != "json_object" {
					t.Fatalf("expected a tool-free JSON request, got %+v", body)
				}
				last := body.Messages[len(body.Messages)-1]
				if last.Role != "user" || !strings.Contains(last.Content, `{"handoff":`) {
					t.Fatalf("missing JSON format instruction: %+v", last)
				}
				content := `{"handoff":"` + plan + `"}`
				if test.malformed && calls == 1 {
					content = `<｜｜DSML｜｜ calls><｜｜DSML｜｜ invoke name="exec"></｜｜DSML｜｜ invoke></｜｜DSML｜｜ calls>`
				}
				chunk, _ := json.Marshal(map[string]any{
					"choices": []map[string]any{{"index": 0, "delta": map[string]string{"content": content}}},
				})
				return sseResponse(string(chunk), `{"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`), nil
			})
			client, err := New(Config{BaseURL: "https://example.com", APIKey: testAPIKey, Model: "deepseek-flash",
				Provider: "deepseek", HTTPClient: &http.Client{Transport: transport}})
			if err != nil {
				t.Fatal(err)
			}
			budget := harness.NewStagedModelBudget(10, 0, true, nil)
			registry, err := harness.NewRegistry()
			if err != nil {
				t.Fatal(err)
			}
			agent := harness.NewAgent(budget.Wrap(client, "sub:planner"), registry, 3, nil)
			result, err := agent.RunWithOptions(t.Context(), "Diagnose the comparison issue.", harness.RunOptions{
				WorkerHandoff: func(_ context.Context, text string) error {
					if text != plan {
						t.Fatalf("handoff validator got %q, want %q", text, plan)
					}
					return nil
				},
				WorkerCorrectionTextOnly: true,
			})
			wantCalls := 1
			if test.malformed {
				wantCalls++
			}
			if err != nil || result.Answer != plan || calls != wantCalls {
				t.Fatalf("answer=%q calls=%d, want %d, err=%v", result.Answer, calls, wantCalls, err)
			}
		})
	}
}

func TestDeepSeekSubmissionCorrectionUsesOnlyJSONPatch(t *testing.T) {
	for _, test := range []struct {
		name       string
		content    string
		valid      bool
		nativeCall bool
	}{
		{name: "valid patch", content: `{"path":"demo.txt","operation":"update","old_string":"red","new_string":"blue"}`, valid: true},
		{name: "historical tool echoed as text", content: `<｜｜DSML｜｜ invoke name="read_file">`},
		{name: "unexpected command object", content: `{"command":["read_file","demo.txt"]}`},
		{name: "historical tool returned as a native call", nativeCall: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
				var body chatRequest
				if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
					t.Fatal(err)
				}
				if body.ToolChoice != "none" || len(body.Tools) != 0 || body.Thinking == nil || body.Thinking.Type != "disabled" ||
					body.ResponseFormat == nil || body.ResponseFormat.Type != "json_object" {
					t.Fatalf("correction sent unexpected request options: %+v", body)
				}
				last := body.Messages[len(body.Messages)-1]
				if last.Role != "user" || !strings.Contains(last.Content, "demo.txt contains red") ||
					!strings.Contains(last.Content, "Change red to blue.") {
					t.Fatalf("task or tool observation was lost: %+v", last)
				}
				for _, message := range body.Messages {
					if message.Role == "tool" || len(message.ToolCalls) > 0 || message.ToolCallID != "" || message.ReasoningContent != "" {
						t.Fatalf("old tool protocol leaked into correction: %+v", message)
					}
				}
				if test.nativeCall {
					return sseResponse(
						`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"unexpected","type":"function","function":{"name":"read_file","arguments":"{}"}}]},"finish_reason":"tool_calls"}]}`,
					), nil
				}
				chunk, _ := json.Marshal(map[string]any{
					"choices": []map[string]any{{"index": 0, "delta": map[string]string{"content": test.content}}},
				})
				return sseResponse(string(chunk), `{"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`), nil
			})
			client, err := New(Config{BaseURL: "https://example.com", APIKey: testAPIKey, Model: "deepseek-flash",
				Provider: "deepseek", Thinking: "enabled", ReasoningEffort: "high", HTTPClient: &http.Client{Transport: transport}})
			if err != nil {
				t.Fatal(err)
			}
			edit := &submissionTool{name: "apply_patch"}
			verify := &submissionTool{name: "exec"}
			registry, err := harness.NewRegistry(edit, verify)
			if err != nil {
				t.Fatal(err)
			}
			budget := harness.NewFunctionalModelBudget(2, 0, "single-review", nil)
			agent := harness.NewAgent(budget.Wrap(client, "agent"), registry, 3, nil)
			_, err = agent.RunTurn(t.Context(), []harness.Message{
				{Role: "user", Content: "Change red to blue."},
				{Role: "assistant", ReasoningContent: "old reasoning", ToolCalls: []harness.ToolCall{
					{ID: "read", Name: "read_file", Arguments: json.RawMessage(`{"path":"demo.txt"}`)},
				}},
				{Role: "tool", ToolCallID: "read", Content: "demo.txt contains red"},
			}, harness.RunOptions{
				Orchestrate: "single-review",
				Contract: &harness.TaskContract{
					Goal: "Change red to blue.", VerifyCommands: []harness.VerifyCommand{{Name: "test", Command: []string{"test"}}},
				},
				WorkspaceFingerprint: func() (string, error) { return "fp", nil },
				CandidatePatch:       func(context.Context) (bool, error) { return len(edit.args) > 0, nil },
				FunctionalRepair:     func(context.Context, string) error { t.Fatal("unexpected repair"); return nil },
			})
			if test.valid {
				if err != nil || string(edit.args) != test.content || len(verify.args) == 0 {
					t.Fatalf("patch was not applied and verified: err=%v patch=%s verify=%s", err, edit.args, verify.args)
				}
			} else if !errors.Is(err, harness.ErrIncompleteModelResponse) || len(edit.args) != 0 || len(verify.args) != 0 {
				t.Fatalf("invalid response reached the executor: err=%v patch=%s verify=%s", err, edit.args, verify.args)
			}
		})
	}
}

type submissionTool struct {
	name string
	args json.RawMessage
}

func (t *submissionTool) Spec() harness.ToolSpec { return harness.ToolSpec{Name: t.name} }

func (t *submissionTool) Execute(_ context.Context, args json.RawMessage) (harness.ToolResult, error) {
	t.args = args
	return harness.ToolResult{Observation: "command=test exit_code=0 duration_ms=1"}, nil
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
	if err == nil || !strings.Contains(err.Error(), "read chat completions SSE stream") {
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

// TestGenerateDropsMalformedCallAndKeepsValidOnes 验证一条非法参数的工具调用
// 只丢弃自身，不让整个请求失败——真实评测中一次流式拼接错误曾杀死整轮运行。
func TestGenerateDropsMalformedCallAndKeepsValidOnes(t *testing.T) {
	transport := staticSSETransport(
		`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call-ok","function":{"name":"read_file","arguments":"{\"path\":\"a.go\"}"}},{"index":1,"id":"call-bad","function":{"name":"read_file","arguments":"{broken"}}]},"finish_reason":"tool_calls"}]}`,
	)
	client := newTestClient(t, transport, 1)

	response, err := client.Generate(context.Background(), nil, nil, nil)
	if err != nil {
		t.Fatalf("malformed call must not fail the request: %v", err)
	}
	if len(response.ToolCalls) != 1 || response.ToolCalls[0].ID != "call-ok" {
		t.Fatalf("tool calls = %+v, want only call-ok", response.ToolCalls)
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
