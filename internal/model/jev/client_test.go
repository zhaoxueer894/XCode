package jev_test

// jev 客户端离线测试：httptest 模拟 systemone 端点，验证报文形状、鉴权头、
// 响应解析与错误路径——全程不访问真实服务、不消耗配额。

import (
	"context"           // 请求上下文。
	"io"                // 读取测试服务器收到的请求体。
	"net/http"          // httptest 与状态码。
	"net/http/httptest" // 模拟端点。
	"strings"           // 断言鉴权头。
	"testing"           // Go 标准测试框架。
	"time"              // 构造超时。

	"xcode/internal/model/jev" // 被测客户端。
)

// newTestServer 启动一个可自定义处理的模拟端点，并返回指向它的客户端。
func newTestServer(t *testing.T, handler http.HandlerFunc) (*jev.Client, *strings.Builder) {
	t.Helper()
	var received strings.Builder
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		received.Write(body)
		received.WriteString("\n--\n")
		received.WriteString(r.Header.Get("Authorization"))
		handler(w, r)
	}))
	t.Cleanup(server.Close)
	client, err := jev.New("test-key", server.URL, "jev-test")
	if err != nil {
		t.Fatal(err)
	}
	return client, &received
}

// TestSystemOneRequestShapeAndParsing 验证请求形状、Bearer 头与答案解析。
func TestSystemOneRequestShapeAndParsing(t *testing.T) {
	client, received := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"model":"jev-1.13.0","answers":{"unit-0":{"choice":"keep","probabilities":{"keep":0.91,"drop":0.09},"confidence":0.93}}}`))
	})
	answers, err := client.SystemOne(context.Background(),
		map[string]string{"unit-0": "(user) goal text"},
		map[string]jev.Question{"unit-0": {
			Type:         jev.QuestionChoice,
			Instructions: "决定该证据单元去留",
			Criteria:     map[string]string{"keep": "最近窗口", "drop": "只读结果"},
		}})
	if err != nil {
		t.Fatal(err)
	}
	answer, ok := answers["unit-0"]
	if !ok {
		t.Fatal("answer for unit-0 missing")
	}
	if answer.Choice != "keep" || answer.Confidence != 0.93 {
		t.Fatalf("answer = %+v", answer)
	}
	if answer.Probabilities["keep"] != 0.91 {
		t.Fatalf("probabilities = %+v", answer.Probabilities)
	}
	if answer.Model != "jev-1.13.0" {
		t.Fatalf("model = %q", answer.Model)
	}

	// 请求体必须包含 model/state/questions 三段。
	raw := received.String()
	for _, want := range []string{`"model":"jev-test"`, `"state":{"unit-0"`, `"instructions":"决定该证据单元去留"`, `"criteria":{"drop"`} {
		if !strings.Contains(raw, want) {
			t.Fatalf("request body missing %s:\n%s", want, raw)
		}
	}
	// 鉴权必须是 Bearer 头且 Key 不落日志。
	if !strings.Contains(raw, "Bearer test-key") {
		t.Fatalf("missing bearer header:\n%s", raw)
	}
}

// TestSystemOneHTTPErrorSurfaces 验证非 2xx 返回带状态码的错误（触发调用方回退）。
func TestSystemOneHTTPErrorSurfaces(t *testing.T) {
	client, _ := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"quota exceeded"}`, http.StatusPaymentRequired)
	})
	_, err := client.SystemOne(context.Background(),
		map[string]string{"u": "x"},
		map[string]jev.Question{"u": {Type: jev.QuestionChoice, Instructions: "i"}})
	if err == nil || !strings.Contains(err.Error(), "402") {
		t.Fatalf("expected 402 error, got %v", err)
	}
}

// TestSystemOneEmptyAnswersIsError 验证空答案视为协议错误。
func TestSystemOneEmptyAnswersIsError(t *testing.T) {
	client, _ := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"model":"jev-1.13.0","answers":{}}`))
	})
	if _, err := client.SystemOne(context.Background(),
		map[string]string{"u": "x"},
		map[string]jev.Question{"u": {Type: jev.QuestionChoice, Instructions: "i"}}); err == nil {
		t.Fatal("empty answers must be an error")
	}
}

// TestNewRequiresKey 验证缺少 Key 时构造即失败，不发起任何请求。
func TestNewRequiresKey(t *testing.T) {
	if _, err := jev.New("", "", ""); err == nil {
		t.Fatal("empty api key must be rejected")
	}
}

// TestSystemOneTimeoutSurfaces 验证超时以错误返回（触发回退）。
func TestSystemOneTimeoutSurfaces(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(200 * time.Millisecond)
		_, _ = w.Write([]byte(`{"answers":{"u":{"choice":"keep","confidence":1}}}`))
	}))
	t.Cleanup(server.Close)
	client, err := jev.New("k", server.URL, "jev-test")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := client.SystemOne(ctx,
		map[string]string{"u": "x"},
		map[string]jev.Question{"u": {Type: jev.QuestionChoice, Instructions: "i"}}); err == nil {
		t.Fatal("timeout must surface as error")
	}
}
