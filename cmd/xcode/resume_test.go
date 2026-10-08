package main

// 测试：--resume 恢复流程的端到端行为。
// 崩溃状态以"构造磁盘证据"的方式注入（轮次日志 + journal + 快照 manifest），
// 与真实崩溃在恢复器眼中的形态完全一致。

import (
	"context"       // 驱动恢复轮次。
	"encoding/json" // 落盘与读取轮次日志、会话状态。
	"os"            // 写入与读取夹具文件。
	"path/filepath" // 拼接夹具路径。
	"strings"       // 断言冲突错误与确定性说明。
	"testing"       // Go 标准测试框架。

	"xcode/internal/checkpoint" // 真实快照仓库，产生 manifest 证据。
	"xcode/internal/executor"   // 构造 journal 条目。
	"xcode/internal/harness"    // Agent、Message 与 SessionState。
	"xcode/internal/model"      // ScriptedDemo 提供确定性的恢复轮答案。
	"xcode/internal/policy"     // 组装执行管线所需的最小策略引擎。
	"xcode/internal/recovery"   // 断言判定常量。
)

const (
	resumeBaseline = "package main\n\nfunc add(a int, b int) int {\n\treturn a - b\n}\n"
	resumePatched  = "package main\n\nfunc add(a int, b int) int {\n\treturn a + b\n}\n"
)

// resumeStubTool 是恢复测试专用的最小工具，注册进 Registry 仅为满足装配。
type resumeStubTool struct {
	name string // Registry 用来注册和查找工具的名称。
}

// Spec 返回最小工具声明。
func (t resumeStubTool) Spec() harness.ToolSpec { return harness.ToolSpec{Name: t.name} }

// Execute 不应被这些测试触达；触达即失败。
func (t resumeStubTool) Execute(context.Context, json.RawMessage) (harness.ToolResult, error) {
	return harness.ToolResult{}, context.Canceled
}

// explodingModel 只要被调用就报错，用来断言"没有未完成轮次时不会启动模型"。
type explodingModel struct{}

// Generate 返回显式错误。
func (explodingModel) Generate(context.Context, []harness.Message, []harness.ToolSpec, harness.ModelEventSink) (harness.ModelResponse, error) {
	return harness.ModelResponse{}, context.Canceled
}

// writeResumeFixture 写入轮次恢复日志与 journal 条目，返回工作区根目录。
func writeResumeFixture(t *testing.T, records []harness.TurnRecord, entries []executor.JournalEntry) string {
	t.Helper()
	dir := t.TempDir()
	logPath := turnLogPath(dir)
	if err := os.MkdirAll(filepath.Dir(logPath), 0o700); err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	encoder := json.NewEncoder(&b)
	for _, r := range records {
		if err := encoder.Encode(r); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(logPath, []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	journalDir := filepath.Join(dir, ".xcode-state", "journal")
	if err := os.MkdirAll(journalDir, 0o700); err != nil {
		t.Fatal(err)
	}
	var j strings.Builder
	jencoder := json.NewEncoder(&j)
	for _, e := range entries {
		if err := jencoder.Encode(e); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(journalDir, "run-20260926T080000.000000000Z.jsonl"), []byte(j.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

// resumeTurnRecord 构造一条轮次恢复记录。
func resumeTurnRecord(kind harness.TurnRecordKind, message *harness.Message, callID, tool, arguments string) harness.TurnRecord {
	r := harness.TurnRecord{Kind: kind, Message: message, Tool: tool, ToolCallID: callID}
	if arguments != "" {
		r.Arguments = json.RawMessage(arguments)
	}
	return r
}

// patchCrashRecords 构造"assistant 发起 update 补丁后崩溃、只有 begin"的日志段。
func patchCrashRecords() ([]harness.TurnRecord, []executor.JournalEntry) {
	call := harness.ToolCall{ID: "c2", Name: "apply_patch", Arguments: json.RawMessage(
		`{"path":"calc.go","operation":"update","old_string":"return a - b","new_string":"return a + b"}`)}
	records := []harness.TurnRecord{
		resumeTurnRecord(harness.TurnRecordStarted, &harness.Message{Role: "user", Content: "修复 add"}, "", "", ""),
		resumeTurnRecord(harness.TurnRecordAssistant, &harness.Message{Role: "assistant", ToolCalls: []harness.ToolCall{call}}, "", "", ""),
		resumeTurnRecord(harness.TurnRecordToolBegin, nil, "c2", "apply_patch", string(call.Arguments)),
	}
	entries := []executor.JournalEntry{
		{ToolCallID: "c2", Tool: "apply_patch", State: executor.StateStarted, Summary: "side effects imminent"},
	}
	return records, entries
}

// newResumeCheckpoints 为工作区建立真实快照仓库并可选地捕获基线。
func newResumeCheckpoints(t *testing.T, workspace string, captureBaseline bool) *checkpoint.Store {
	t.Helper()
	store, err := checkpoint.NewStore(filepath.Join(workspace, ".xcode-state", "checkpoints"), workspace)
	if err != nil {
		t.Fatal(err)
	}
	if captureBaseline {
		if err := os.WriteFile(filepath.Join(workspace, "calc.go"), []byte(resumeBaseline), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := store.Capture(context.Background(), "calc.go"); err != nil {
			t.Fatal(err)
		}
	}
	return store
}

// newResumeAgent 组装恢复轮使用的 Agent：空脚本模型直接给出最终总结。
func newResumeAgent(t *testing.T) *harness.Agent {
	t.Helper()
	registry, err := harness.NewRegistry(resumeStubTool{name: "apply_patch"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := policy.NewEngine(policy.ModeWorkspaceWrite); err != nil {
		t.Fatal(err)
	}
	return harness.NewAgent(model.NewScriptedDemo(nil), registry, 4, nil)
}

// TestRunResumeNoUnfinishedTurn 验证已终止轮次不会触发恢复，也不会调用模型。
func TestRunResumeNoUnfinishedTurn(t *testing.T) {
	dir := writeResumeFixture(t, []harness.TurnRecord{
		resumeTurnRecord(harness.TurnRecordStarted, &harness.Message{Role: "user", Content: "x"}, "", "", ""),
		resumeTurnRecord(harness.TurnRecordCompleted, &harness.Message{Role: "assistant", Content: "done"}, "", "", ""),
	}, nil)
	agent := harness.NewAgent(explodingModel{}, nilRegistryOrFail(t), 4, nil)
	if err := runResume(context.Background(), agent, dir, newResumeCheckpoints(t, dir, false), "", harness.RunOptions{}); err != nil {
		t.Fatalf("completed turn must be a no-op, got %v", err)
	}
}

// nilRegistryOrFail 供无工具执行的断言使用；nil runner 只要不触达即安全。
func nilRegistryOrFail(t *testing.T) *harness.Registry {
	t.Helper()
	registry, err := harness.NewRegistry(resumeStubTool{name: "apply_patch"})
	if err != nil {
		t.Fatal(err)
	}
	return registry
}

// TestRunResumeRefusesConflict 验证文件与两种预期都不符时拒绝恢复且退出码路径为错误。
func TestRunResumeRefusesConflict(t *testing.T) {
	records, entries := patchCrashRecords()
	dir := writeResumeFixture(t, records, entries)
	// 先捕获基线，再模拟外部改动——顺序不能颠倒，否则基线捕获会把外部改动覆盖回基线内容。
	store := newResumeCheckpoints(t, dir, true)
	if err := os.WriteFile(filepath.Join(dir, "calc.go"), []byte("// externally rewritten\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	err := runResume(context.Background(), newResumeAgent(t), dir, store, "", harness.RunOptions{})
	if err == nil || !strings.Contains(err.Error(), "recovery conflict") {
		t.Fatalf("conflict must abort recovery with an error, got %v", err)
	}
	// 拒绝恢复时不得写回会话状态。
	if _, err := os.Stat(sessionPath(dir)); !os.IsNotExist(err) {
		t.Fatalf("refused recovery must not write session state, stat err = %v", err)
	}
}

// TestRunResumeAppliedPatchCompletesTurn 验证补丁已生效时跳过重放、继续轮次并写回会话状态。
func TestRunResumeAppliedPatchCompletesTurn(t *testing.T) {
	records, entries := patchCrashRecords()
	dir := writeResumeFixture(t, records, entries)
	store := newResumeCheckpoints(t, dir, true)
	// W2：崩溃前补丁已经把文件写成目标内容。
	if err := os.WriteFile(filepath.Join(dir, "calc.go"), []byte(resumePatched), 0o600); err != nil {
		t.Fatal(err)
	}

	err := runResume(context.Background(), newResumeAgent(t), dir, store, "", harness.RunOptions{MaxSteps: 4})
	if err != nil {
		t.Fatalf("applied-patch recovery should complete, got %v", err)
	}

	// 会话状态必须写回：包含确定性说明与恢复轮的最终答案，轮次计数为一。
	state, err := harness.LoadSessionState(sessionPath(dir))
	if err != nil {
		t.Fatal(err)
	}
	if state == nil {
		t.Fatal("resume must persist the session state")
	}
	if state.TurnCount != 1 {
		t.Fatalf("resume counts as one turn, got %d", state.TurnCount)
	}
	var foundNote, foundAnswer bool
	for _, message := range state.Messages {
		if strings.Contains(message.Content, "already took effect") && strings.Contains(message.Content, "not replayed") {
			foundNote = true
		}
		if strings.Contains(message.Content, "已按脚本完成") {
			foundAnswer = true
		}
	}
	if !foundNote || !foundAnswer {
		t.Fatalf("session transcript must keep the recovery note and the final answer, note=%v answer=%v", foundNote, foundAnswer)
	}

	// 文件保持补丁后的内容，没有被二次修改。
	data, err := os.ReadFile(filepath.Join(dir, "calc.go"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != resumePatched {
		t.Fatal("applied patch must not be replayed or altered by recovery")
	}

	// 判定常量健全性：确认清单常量与 recovery 包保持一致。
	if recovery.VerdictConfirm != "confirm" {
		t.Fatalf("unexpected confirm verdict value %q", recovery.VerdictConfirm)
	}
}

// TestRunResumePresetludeInjected 验证 L2 前导在恢复轮被注入到消息历史最前。
func TestRunResumePresetludeInjected(t *testing.T) {
	records, entries := patchCrashRecords()
	dir := writeResumeFixture(t, records, entries)
	store := newResumeCheckpoints(t, dir, true)
	if err := os.WriteFile(filepath.Join(dir, "calc.go"), []byte(resumePatched), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := runResume(context.Background(), newResumeAgent(t), dir, store, "项目约定：使用中文注释", harness.RunOptions{MaxSteps: 4}); err != nil {
		t.Fatal(err)
	}
	state, err := harness.LoadSessionState(sessionPath(dir))
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Messages) == 0 || !strings.Contains(state.Messages[0].Content, "[project-memory]") ||
		!strings.Contains(state.Messages[0].Content, "项目约定") {
		t.Fatalf("prelude must be message 0, got %+v", state.Messages)
	}
}
