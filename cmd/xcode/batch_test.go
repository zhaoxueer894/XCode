package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"xcode/internal/harness"
)

type batchTransport func(*http.Request) (*http.Response, error)

func (f batchTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type batchStep struct {
	tool      string
	arguments any
}

// The real provider protocol and CLI wiring run against an in-memory HTTPS
// transport. This proves plumbing, not model intelligence, and spends no API money.
func batchFixture(t *testing.T, steps []batchStep, inspect func(map[string]any)) (string, []string) {
	t.Helper()
	root := t.TempDir()
	t.Chdir(root)
	t.Setenv("DASHSCOPE_API_KEY", "fake-batch-test-secret")
	t.Setenv("DASHSCOPE_BASE_URL", "https://batch.invalid/v1")
	t.Setenv("DASHSCOPE_MODEL", "fixture-model")
	old := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = old })
	next := 0
	http.DefaultTransport = batchTransport(func(r *http.Request) (*http.Response, error) {
		var request map[string]any
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			return nil, err
		}
		if inspect != nil {
			inspect(request)
		}
		delta := map[string]any{"content": "done"}
		finish := "stop"
		if next < len(steps) {
			step := steps[next]
			args, _ := json.Marshal(step.arguments)
			delta = map[string]any{"tool_calls": []any{map[string]any{"index": 0, "id": fmt.Sprintf("call-%d", next), "type": "function", "function": map[string]any{"name": step.tool, "arguments": string(args)}}}}
			finish = "tool_calls"
		}
		next++
		chunk, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": finish}}})
		body := "data: " + string(chunk) + "\n\ndata: {\"choices\":[],\"usage\":{\"prompt_tokens\":5,\"completion_tokens\":2,\"total_tokens\":7}}\n\ndata: [DONE]\n\n"
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}, nil
	})
	args := []string{"--provider", "bailian", "--provider-retries", "0", "--workspace", root,
		"--non-interactive", "--no-delegate", "--max-steps", "12", "--max-model-calls", "20",
		"--event-log", filepath.Join(root, "events.jsonl"), "--result-file", filepath.Join(root, "result.json")}
	return root, args
}

func readBatchResult(t *testing.T, root string) runRecord {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, "result.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "fake-batch-test-secret") {
		t.Fatal("result leaks API key")
	}
	var record runRecord
	if err := json.Unmarshal(data, &record); err != nil {
		t.Fatal(err)
	}
	return record
}

func TestBatchActualCLIReadsWritesCreatesAndVerifies(t *testing.T) {
	steps := []batchStep{
		{"read_file", map[string]any{"path": "calc.go"}},
		{"apply_patch", map[string]any{"path": "calc.go", "operation": "update", "old_string": "return a - b", "new_string": "return a + b"}},
		{"apply_patch", map[string]any{"path": "double.go", "operation": "create", "content": "package calc\nfunc Double(x int) int { return x*2 }\n"}},
		{"exec", map[string]any{"command": []string{"go", "test", "./..."}}},
	}
	root, args := batchFixture(t, steps, nil)
	files := map[string]string{
		"go.mod":        "module fixture\n\ngo 1.26\n",
		"calc.go":       "package calc\nfunc Add(a,b int) int { return a - b }\n",
		"calc_test.go":  "package calc\nimport \"testing\"\nfunc TestCalc(t *testing.T) { if Add(2,3)!=5 || Double(3)!=6 { t.Fatal(\"wrong result\") } }\n",
		"policy.json":   `{"rules":[{"id":"test","tool":"exec","command_prefix":["go","test"],"decision":"allow"}]}`,
		"contract.json": `{"goal":"fix calculation","verify_commands":[{"name":"tests","command":["go","test","./..."]}]}`,
	}
	for name, data := range files {
		if err := os.WriteFile(filepath.Join(root, name), []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	args = append(args, "--policy", filepath.Join(root, "policy.json"), "--contract", filepath.Join(root, "contract.json"), "fix calculation")
	if err := runCLI(args); err != nil {
		t.Fatal(err)
	}
	// Independent post-run check; do not trust the final answer or CLI exit alone.
	cmd := exec.Command("go", "test", "./...")
	cmd.Dir = root
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("verification: %v\n%s", err, output)
	}
	got := readBatchResult(t, root)
	if got.Status != "completed" || got.Model.Calls != 5 || got.Model.Usage.TotalTokens != 35 || got.Model.MissingUsage != 0 {
		t.Fatalf("record = %+v", got)
	}
	if !got.Config.NoDelegate || got.ConfigHash == "" || got.Config.ContractHash == "" || got.BinaryVersion == "" {
		t.Fatalf("missing run identity: %+v", got)
	}
}

func TestBatchDisablesDelegateAndPreservesBudgetFailure(t *testing.T) {
	toolBearingRequests := 0
	root, args := batchFixture(t, []batchStep{
		{"read_file", map[string]any{"path": "README.md"}},
		{"read_file", map[string]any{"path": "go.mod"}},
	}, func(request map[string]any) {
		// 预算内最后一个允许的请求会禁用工具：此刻的工具调用结果永远无人消费。
		advertised, ok := request["tools"].([]any)
		if !ok {
			return
		}
		toolBearingRequests++
		for _, tool := range advertised {
			if tool.(map[string]any)["function"].(map[string]any)["name"] == "delegate" {
				t.Error("delegate advertised in single-agent mode")
			}
		}
	})
	_ = os.WriteFile(filepath.Join(root, "README.md"), []byte("hello"), 0o600)
	_ = os.WriteFile(filepath.Join(root, "go.mod"), []byte("module fixture\n"), 0o600)
	args = append(args, "--max-model-calls", "2", "read README.md")
	if err := runCLI(args); !errors.Is(err, harness.ErrBudget) {
		t.Fatalf("error = %v", err)
	}
	if toolBearingRequests != 1 {
		t.Fatalf("expected exactly one tool-bearing request before the final one, got %d", toolBearingRequests)
	}
	got := readBatchResult(t, root)
	if got.Status != "stopped" || got.StopReason != harness.StopBudget || got.Model.Calls != 2 {
		t.Fatalf("record = %+v", got)
	}
	// A failure must still leave complete, closed event and recovery logs.
	if err := os.Rename(filepath.Join(root, "events.jsonl"), filepath.Join(root, "closed.jsonl")); err != nil {
		t.Fatal(err)
	}
}

func TestBatchDeniesUnapprovedCommandWithoutPrompt(t *testing.T) {
	root, args := batchFixture(t, []batchStep{{"exec", map[string]any{"command": []string{"go", "version"}}}}, nil)
	if err := runCLI(append(args, "run go version")); err != nil {
		t.Fatal(err)
	}
	got := readBatchResult(t, root)
	if got.Approvals != 1 {
		t.Fatalf("approval requests = %d", got.Approvals)
	}
	trace, _ := os.ReadFile(filepath.Join(root, "events.jsonl"))
	if !strings.Contains(string(trace), "no approver is available") {
		t.Fatal("missing approval denial")
	}
}

func TestBatchRecordsStartupFailureAndPromptFile(t *testing.T) {
	root, args := batchFixture(t, nil, nil)
	_ = os.WriteFile(filepath.Join(root, "task.txt"), []byte("read README.md"), 0o600)
	args = append(args, "--provider", "unknown", "--prompt-file", filepath.Join(root, "task.txt"))
	if err := runCLI(args); err == nil {
		t.Fatal("unknown provider accepted")
	}
	got := readBatchResult(t, root)
	if got.Status != "error" || got.Error == "" || got.PromptHash == "" {
		t.Fatalf("record = %+v", got)
	}
}

func TestBatchRecordsTimeout(t *testing.T) {
	root, args := batchFixture(t, nil, nil)
	http.DefaultTransport = batchTransport(func(r *http.Request) (*http.Response, error) { <-r.Context().Done(); return nil, r.Context().Err() })
	if err := runCLI(append(args, "--timeout", "100ms", "task")); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v", err)
	}
	if got := readBatchResult(t, root); got.Status != "timeout" || got.Model.Calls != 1 || got.Model.MissingUsage != 1 {
		t.Fatalf("record = %+v", got)
	}
}

func TestBatchParentAndChildShareCallLimit(t *testing.T) {
	steps := []batchStep{
		{"delegate", map[string]any{"role": "developer", "prompt": "read README.md"}},
		{"read_file", map[string]any{"path": "README.md"}},
	}
	root, args := batchFixture(t, steps, nil)
	_ = os.WriteFile(filepath.Join(root, "README.md"), []byte("hello"), 0o600)
	err := runCLI(append(args, "--no-delegate=false", "--max-model-calls", "3", "delegate the task"))
	if !errors.Is(err, harness.ErrBudget) {
		t.Fatalf("error = %v", err)
	}
	got := readBatchResult(t, root)
	if got.Model.Calls != 3 || got.Model.Usage.TotalTokens != 21 || got.Config.NoDelegate {
		t.Fatalf("record = %+v", got)
	}
	trace, _ := os.ReadFile(filepath.Join(root, "events.jsonl"))
	if !strings.Contains(string(trace), `"agent_id":"sub-1"`) {
		t.Fatal("worker did not run")
	}
}

func TestBatchIsolatedIgnoresAmbientConfig(t *testing.T) {
	root, args := batchFixture(t, nil, nil)
	_ = os.Mkdir(filepath.Join(root, ".xcode"), 0o700)
	_ = os.WriteFile(filepath.Join(root, ".xcode/config"), []byte("provider=unknown\nbailian-model=wrong-model\n"), 0o600)
	if err := runCLI(append(args, "--isolated", "task")); err != nil {
		t.Fatal(err)
	}
	got := readBatchResult(t, root)
	if !got.Config.Isolated || got.Config.Model != "fixture-model" {
		t.Fatalf("record = %+v", got)
	}
}

func TestBatchDoesNotRetryProviderFailureWhenDisabled(t *testing.T) {
	root, args := batchFixture(t, nil, nil)
	calls := 0
	http.DefaultTransport = batchTransport(func(*http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: 503, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"error":{"message":"overloaded"}}`))}, nil
	})
	if err := runCLI(append(args, "task")); err == nil {
		t.Fatal("provider failure lost")
	}
	if calls != 1 {
		t.Fatalf("provider attempts = %d", calls)
	}
	if got := readBatchResult(t, root); got.Status != "error" || got.Model.Calls != 1 {
		t.Fatalf("record = %+v", got)
	}
}

func TestBatchDeepseekConfigAndThinkingContinuation(t *testing.T) {
	requests := 0
	root, args := batchFixture(t, []batchStep{{"read_file", map[string]any{"path": "README.md"}}}, func(request map[string]any) {
		requests++
		if request["model"] != "deepseek-flash" || request["thinking"].(map[string]any)["type"] != "enabled" || request["reasoning_effort"] != "high" {
			t.Error("DeepSeek model/thinking settings missing")
		}
		if requests == 2 {
			found := false
			for _, msg := range request["messages"].([]any) {
				if msg.(map[string]any)["reasoning_content"] == "fixture continuation" {
					found = true
				}
			}
			if !found {
				t.Error("thinking state was lost between tool calls")
			}
		}
	})
	old := http.DefaultTransport
	http.DefaultTransport = batchTransport(func(r *http.Request) (*http.Response, error) {
		response, err := old.RoundTrip(r)
		if err != nil {
			return nil, err
		}
		body, _ := io.ReadAll(response.Body)
		_ = response.Body.Close()
		response.Body = io.NopCloser(strings.NewReader(strings.Replace(string(body), `"delta":{`, `"delta":{"reasoning_content":"fixture continuation",`, 1)))
		return response, nil
	})
	_ = os.WriteFile(filepath.Join(root, "README.md"), []byte("hello"), 0o600)
	_ = os.Mkdir(filepath.Join(root, ".xcode"), 0o700)
	_ = os.WriteFile(filepath.Join(root, ".xcode/config"), []byte("deepseek-api-key=sk-test-only-00000000000000000000\ndeepseek-model=deepseek-flash\n"), 0o600)
	if err := runCLI(append(args, "--provider", "deepseek", "read README.md")); err != nil {
		t.Fatal(err)
	}
	got := readBatchResult(t, root)
	if got.Config.Provider != "deepseek" || got.Config.Model != "deepseek-flash" || got.Config.Thinking != "enabled" || got.Config.Endpoint != "https://api.deepseek.com" {
		t.Fatalf("config = %+v", got.Config)
	}
	data, _ := os.ReadFile(filepath.Join(root, "result.json"))
	if strings.Contains(string(data), "sk-test-only-") {
		t.Fatal("config credential leaked into result")
	}
}
