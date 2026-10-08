package harness_test

// 完成门禁测试：用桩 exec/delegate 工具验证门禁的拦截、续跑、证据缓存与指纹失效。

import (
	"context"       // 驱动 RunTurn。
	"encoding/json" // 桩工具的 Execute 签名需要。
	"strings"       // 断言门禁消息与原因。
	"sync"          // 保护桩工具的调用计数。
	"testing"       // Go 标准测试框架。

	"xcode/internal/harness" // 被测对象：TaskContract、RunOptions 与 RunTurn 门禁挂钩。
)

// scriptedTextModel 按预设依次返回纯文本响应；全部用尽后重复最后一条。
type scriptedTextModel struct {
	answers []string // 保存每次模型调用返回的文本。
	calls   int      // 记录被调用次数。
}

// Generate 返回下一条预设文本；这些测试不使用工具调用与流式事件。
func (m *scriptedTextModel) Generate(context.Context, []harness.Message, []harness.ToolSpec, harness.ModelEventSink) (harness.ModelResponse, error) {
	m.calls++
	index := m.calls - 1
	if index >= len(m.answers) {
		index = len(m.answers) - 1
	}
	return harness.ModelResponse{Content: m.answers[index], FinishReason: "stop"}, nil
}

// gateStubTool 是带调用计数的桩工具：按预设序列返回 Observation，用尽后重复最后一条。
type gateStubTool struct {
	name         string   // Registry 用来注册的名称。
	observations []string // 每次调用返回的 Observation 序列。
	mu           sync.Mutex
	calls        int
}

// Spec 返回最小工具声明。
func (t *gateStubTool) Spec() harness.ToolSpec { return harness.ToolSpec{Name: t.name} }

// Execute 返回下一条预设 Observation。
func (t *gateStubTool) Execute(context.Context, json.RawMessage) (harness.ToolResult, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.calls++
	index := t.calls - 1
	if index >= len(t.observations) {
		index = len(t.observations) - 1
	}
	return harness.ToolResult{Observation: t.observations[index]}, nil
}

// Calls 返回桩工具被调用的次数。
func (t *gateStubTool) Calls() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.calls
}

// newGateFixture 创建带 exec（与可选 delegate）桩工具的注册表。
func newGateFixture(withDelegate bool) *gateFixture {
	f := &gateFixture{exec: &gateStubTool{name: "exec", observations: []string{
		"command=x exit_code=0 duration_ms=1",
	}}}
	tools := []harness.Tool{f.exec}
	if withDelegate {
		f.delegate = &gateStubTool{name: "delegate", observations: []string{
			"[subagent sub-1 (reviewer) completed]\nGATE-VERDICT: PASS",
		}}
		tools = append(tools, f.delegate)
	}
	f.registry, _ = harness.NewRegistry(tools...)
	return f
}

// gateFixture 汇总门禁测试的桩工具与注册表。
type gateFixture struct {
	registry *harness.Registry
	exec     *gateStubTool
	delegate *gateStubTool
}

// fixedFingerprint 返回始终为同一指纹的函数。
func fixedFingerprint(value string) func() (string, error) {
	return func() (string, error) { return value, nil }
}

// gateOptions 组装启用门禁的 RunOptions。
func gateOptions(contract *harness.TaskContract, fp func() (string, error)) harness.RunOptions {
	return harness.RunOptions{Contract: contract, WorkspaceFingerprint: fp}
}

// TestCompletionGateBlocksThenContinues 验证验证命令失败时门禁拦截并回注证据，修复后放行。
func TestCompletionGateBlocksThenContinues(t *testing.T) {
	f := newGateFixture(false)
	f.exec.observations = []string{
		"command=go test exit_code=1 duration_ms=5", // 第一次取证：失败。
		"command=go test exit_code=0 duration_ms=5", // 第二次取证：通过。
	}
	contract := &harness.TaskContract{
		Goal:           "修复 add",
		VerifyCommands: []harness.VerifyCommand{{Name: "go test", Command: []string{"go", "test", "./..."}}},
	}
	model := &scriptedTextModel{answers: []string{"我修好了", "这次真的修好了"}}
	agent := harness.NewAgent(model, f.registry, 8, nil)

	result, err := agent.RunTurn(context.Background(), []harness.Message{{Role: "user", Content: "fix"}},
		gateOptions(contract, fixedFingerprint("fp-a")))
	if err != nil {
		t.Fatal(err)
	}
	if result.StopReason != harness.StopCompleted {
		t.Fatalf("stop reason = %q, want completed", result.StopReason)
	}
	if f.exec.Calls() != 2 {
		t.Fatalf("gate should verify twice (fail then pass), got %d", f.exec.Calls())
	}
	var gateMessages int
	for _, message := range result.Messages {
		if message.Role == "user" && strings.Contains(message.Content, "[completion-gate]") {
			gateMessages++
		}
	}
	if gateMessages != 1 {
		t.Fatalf("expected one injected gate message, got %d", gateMessages)
	}
	if len(contract.Evidence) != 2 || contract.Evidence[0].Passed || !contract.Evidence[1].Passed {
		t.Fatalf("evidence = %+v, want fail then pass", contract.Evidence)
	}
}

// TestCompletionGateMaxStepsWhenNeverSatisfied 验证门禁持续拦截时轮次在步数上限受控停止。
func TestCompletionGateMaxStepsWhenNeverSatisfied(t *testing.T) {
	f := newGateFixture(false)
	f.exec.observations = []string{"command=go test exit_code=1 duration_ms=5"}
	contract := &harness.TaskContract{
		Goal:           "修复 add",
		VerifyCommands: []harness.VerifyCommand{{Name: "go test", Command: []string{"go", "test"}}},
	}
	model := &scriptedTextModel{answers: []string{"完成了", "完成了", "完成了", "完成了"}}
	agent := harness.NewAgent(model, f.registry, 3, nil)

	result, err := agent.RunTurn(context.Background(), []harness.Message{{Role: "user", Content: "fix"}},
		gateOptions(contract, fixedFingerprint("fp-a")))
	if err == nil {
		t.Fatal("expected max steps error when gate never passes")
	}
	if result.StopReason != harness.StopMaxSteps {
		t.Fatalf("stop reason = %q, want max_steps", result.StopReason)
	}
}

// TestCompletionGateCachesEvidencePerFingerprint 验证同一指纹下已通过的验证命令不会重复执行。
func TestCompletionGateCachesEvidencePerFingerprint(t *testing.T) {
	f := newGateFixture(false)
	contract := &harness.TaskContract{
		Goal:           "修复 add",
		VerifyCommands: []harness.VerifyCommand{{Name: "go test", Command: []string{"go", "test"}}},
	}
	model := &scriptedTextModel{answers: []string{"done", "done", "done"}}
	agent := harness.NewAgent(model, f.registry, 8, nil)
	options := gateOptions(contract, fixedFingerprint("fp-a"))

	for i := 0; i < 3; i++ {
		if _, err := agent.RunTurn(context.Background(), []harness.Message{{Role: "user", Content: "fix"}}, options); err != nil {
			t.Fatal(err)
		}
	}
	if f.exec.Calls() != 1 {
		t.Fatalf("evidence should be cached per fingerprint, exec ran %d times", f.exec.Calls())
	}
}

// TestCompletionGateFingerprintChangeInvalidatesEvidence 验证工作区版本变化后旧证据失效、重新取证。
func TestCompletionGateFingerprintChangeInvalidatesEvidence(t *testing.T) {
	f := newGateFixture(false)
	fingerprints := []string{"fp-1", "fp-2", "fp-2"}
	call := 0
	contract := &harness.TaskContract{
		Goal:           "修复 add",
		VerifyCommands: []harness.VerifyCommand{{Name: "go test", Command: []string{"go", "test"}}},
	}
	model := &scriptedTextModel{answers: []string{"done", "done", "done"}}
	agent := harness.NewAgent(model, f.registry, 8, nil)
	options := gateOptions(contract, func() (string, error) {
		call++
		index := call - 1
		if index >= len(fingerprints) {
			index = len(fingerprints) - 1
		}
		return fingerprints[index], nil
	})

	for i := 0; i < 3; i++ {
		if _, err := agent.RunTurn(context.Background(), []harness.Message{{Role: "user", Content: "fix"}}, options); err != nil {
			t.Fatal(err)
		}
	}
	// 第 1 次取证（fp-1）；第 2 次指纹变 fp-2 重新取证；第 3 次指纹未变复用。
	if f.exec.Calls() != 2 {
		t.Fatalf("fingerprint change must invalidate evidence, exec ran %d times", f.exec.Calls())
	}
}

// TestCompletionGateReviewerIssuesThenPassCached 验证 reviewer 拦截、通过后按指纹缓存。
func TestCompletionGateReviewerIssuesThenPassCached(t *testing.T) {
	f := newGateFixture(true)
	f.delegate.observations = []string{
		"[subagent sub-1 (reviewer) completed]\nGATE-VERDICT: ISSUES: 缺少对边界情况的测试",
		"[subagent sub-2 (reviewer) completed]\nGATE-VERDICT: PASS",
	}
	contract := &harness.TaskContract{
		Goal:           "修复 add",
		Acceptance:     []string{"Add(2,3) 返回 5"},
		VerifyCommands: []harness.VerifyCommand{{Name: "go test", Command: []string{"go", "test"}}},
		Review:         true,
	}
	model := &scriptedTextModel{answers: []string{"done", "done", "done"}}
	agent := harness.NewAgent(model, f.registry, 8, nil)
	options := gateOptions(contract, fixedFingerprint("fp-a"))

	result, err := agent.RunTurn(context.Background(), []harness.Message{{Role: "user", Content: "fix"}}, options)
	if err != nil {
		t.Fatal(err)
	}
	if result.StopReason != harness.StopCompleted {
		t.Fatalf("stop reason = %q, want completed", result.StopReason)
	}

	// 第二轮：指纹未变且 reviewer 已通过，不应再调用 delegate。
	if _, err := agent.RunTurn(context.Background(), []harness.Message{{Role: "user", Content: "fix"}}, options); err != nil {
		t.Fatal(err)
	}
	if f.delegate.Calls() != 2 {
		t.Fatalf("reviewer should run once for issues and once for pass, got %d calls", f.delegate.Calls())
	}
}
