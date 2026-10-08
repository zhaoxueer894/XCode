package harness_test

// 测试：轮次恢复日志的读写与 nil 安全、RunTurn 在三个安全边界的落盘顺序、
// 以及恢复预处理对 ResumePending 调用的补执行。

import (
	"context"       // 驱动 RunTurn 的运行上下文。
	"encoding/json" // 构造与校验工具调用参数。
	"os"            // 在临时目录中落盘恢复日志。
	"path/filepath" // 拼接临时文件路径。
	"strings"       // 断言恢复日志的记录种类序列。
	"testing"       // Go 标准测试框架。

	"xcode/internal/harness" // 被测对象：TurnLog 与 Agent。
)

// finalAnswerModel 只在第一次被调用时返回最终文本答案，供恢复预处理之后的循环使用。
type finalAnswerModel struct {
	calls int // 记录被调用的次数，用于断言恢复轮没有多做模型请求。
}

// Generate 返回固定文本答案；这些测试不关心流式事件，因此忽略 sink。
func (m *finalAnswerModel) Generate(context.Context, []harness.Message, []harness.ToolSpec, harness.ModelEventSink) (harness.ModelResponse, error) {
	m.calls++
	return harness.ModelResponse{Content: "done", FinishReason: "stop"}, nil
}

// toolThenAnswerModel 第一次调用返回一个工具调用，之后返回最终文本答案，
// 模拟"模型 → 工具 → 模型"的真实轮次形状。
type toolThenAnswerModel struct {
	calls int              // 记录被调用的次数。
	call  harness.ToolCall // 保存第一次调用返回的工具调用。
}

// Generate 按调用次数返回工具调用或最终答案。
func (m *toolThenAnswerModel) Generate(context.Context, []harness.Message, []harness.ToolSpec, harness.ModelEventSink) (harness.ModelResponse, error) {
	m.calls++
	if m.calls == 1 {
		return harness.ModelResponse{ToolCalls: []harness.ToolCall{m.call}, FinishReason: "tool_calls"}, nil
	}
	return harness.ModelResponse{Content: "done", FinishReason: "stop"}, nil
}

// TestTurnLogAppendAndReadRoundtrip 验证五类边界记录写入后可以完整读回。
func TestTurnLogAppendAndReadRoundtrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "recovery", "turn-latest.jsonl")
	log, err := harness.NewTurnLog(path)
	if err != nil {
		t.Fatal(err)
	}

	call := harness.ToolCall{ID: "call-1", Name: "apply_patch", Arguments: json.RawMessage(`{"path":"calc.go"}`)}
	log.AppendStarted("修复 add", false)
	log.AppendAssistant(1, harness.Message{Role: "assistant", ToolCalls: []harness.ToolCall{call}})
	log.AppendToolBegin(1, call)
	log.AppendToolEnd(1, call, "ok")
	log.AppendCompleted(1, "done")
	if err := log.Close(); err != nil {
		t.Fatal(err)
	}
	if err := log.Err(); err != nil {
		t.Fatalf("turn log write error: %v", err)
	}

	records, err := harness.ReadTurnLog(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 5 {
		t.Fatalf("expected 5 records, got %d", len(records))
	}
	wantKinds := []harness.TurnRecordKind{
		harness.TurnRecordStarted, harness.TurnRecordAssistant,
		harness.TurnRecordToolBegin, harness.TurnRecordToolEnd, harness.TurnRecordCompleted,
	}
	turnID := records[0].TurnID
	if turnID == "" {
		t.Fatal("turn.started must assign a turn id")
	}
	runID := records[0].RunID
	if runID == "" {
		t.Fatal("records must carry a run id")
	}
	for i, record := range records {
		if record.Kind != wantKinds[i] {
			t.Fatalf("record %d kind = %q, want %q", i, record.Kind, wantKinds[i])
		}
		if record.RunID != runID || record.TurnID != turnID {
			t.Fatalf("record %d lost shared ids: run=%q turn=%q", i, record.RunID, record.TurnID)
		}
		if record.Time.IsZero() {
			t.Fatalf("record %d missing time", i)
		}
	}
	if records[2].ToolCallID != "call-1" || string(records[2].Arguments) != `{"path":"calc.go"}` {
		t.Fatalf("tool.begin lost call identity or arguments: %+v", records[2])
	}
	if records[4].Message == nil || records[4].Message.Content != "done" {
		t.Fatalf("completed record lost answer: %+v", records[4])
	}
}

// TestTurnLogNilReceiverIsSafe 验证未挂载日志时全部方法静默，行为与原实现一致。
func TestTurnLogNilReceiverIsSafe(t *testing.T) {
	var log *harness.TurnLog
	log.AppendStarted("x", false)
	log.AppendAssistant(1, harness.Message{})
	log.AppendToolBegin(1, harness.ToolCall{})
	log.AppendToolEnd(1, harness.ToolCall{}, "out")
	log.AppendCompleted(1, "answer")
	if err := log.Err(); err != nil {
		t.Fatalf("nil log must not report errors, got %v", err)
	}
	if err := log.Close(); err != nil {
		t.Fatalf("nil close must be a no-op, got %v", err)
	}
}

// TestReadTurnLogMissingFileReturnsEmpty 验证没有恢复日志时返回空而不是错误。
func TestReadTurnLogMissingFileReturnsEmpty(t *testing.T) {
	records, err := harness.ReadTurnLog(filepath.Join(t.TempDir(), "absent.jsonl"))
	if err != nil {
		t.Fatalf("missing file must not be an error, got %v", err)
	}
	if len(records) != 0 {
		t.Fatalf("expected no records, got %d", len(records))
	}
}

// TestTurnLogNewTurnGetsNewID 验证同一运行内第二次轮次会拿到新的轮次编号。
func TestTurnLogNewTurnGetsNewID(t *testing.T) {
	path := filepath.Join(t.TempDir(), "turn-latest.jsonl")
	log, err := harness.NewTurnLog(path)
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()
	log.AppendStarted("first", false)
	log.AppendCompleted(1, "a")
	log.AppendStarted("second", false)
	log.AppendCompleted(1, "b")
	records, err := harness.ReadTurnLog(path)
	if err != nil {
		t.Fatal(err)
	}
	if records[0].TurnID == "" || records[0].TurnID == records[2].TurnID {
		t.Fatalf("each turn.started must mint a fresh turn id: %q vs %q", records[0].TurnID, records[2].TurnID)
	}
}

// newTurnLogAgent 组装一个挂载恢复日志的 Agent：模型只回最终答案，工具返回固定输出。
func newTurnLogAgent(t *testing.T, log *harness.TurnLog) *harness.Agent {
	t.Helper()
	registry, err := harness.NewRegistry(staticTool{name: "apply_patch", output: "patched"})
	if err != nil {
		t.Fatal(err)
	}
	agent := harness.NewAgent(&finalAnswerModel{}, registry, 4, nil)
	agent.SetTurnLog(log)
	return agent
}

// TestRunTurnWritesTurnLogBoundaries 验证正常轮次按顺序留下全部边界记录并以终止记录结束。
func TestRunTurnWritesTurnLogBoundaries(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "turn-latest.jsonl")
	log, err := harness.NewTurnLog(path)
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()

	call := harness.ToolCall{ID: "call-9", Name: "apply_patch", Arguments: json.RawMessage(`{"path":"calc.go"}`)}
	registry, err := harness.NewRegistry(staticTool{name: "apply_patch", output: "patched"})
	if err != nil {
		t.Fatal(err)
	}
	agent := harness.NewAgent(&toolThenAnswerModel{call: call}, registry, 4, nil)
	agent.SetTurnLog(log)
	result, err := agent.RunTurn(context.Background(), []harness.Message{
		{Role: "user", Content: "fix"},
	}, harness.RunOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if result.StopReason != harness.StopCompleted {
		t.Fatalf("stop reason = %q, want completed", result.StopReason)
	}

	records, err := harness.ReadTurnLog(path)
	if err != nil {
		t.Fatal(err)
	}
	var kinds []string
	for _, record := range records {
		kinds = append(kinds, string(record.Kind))
	}
	joined := strings.Join(kinds, ",")
	// 两轮轮次的完整边界序列：开始 → 首个 assistant（含工具调用）→ 工具开始/完成 →
	// 最终 assistant（最终答案）→ 终止记录。
	want := "turn.started,assistant.message,tool.begin,tool.end,assistant.message,turn.completed"
	if joined != want {
		t.Fatalf("boundary sequence = %q, want %q", joined, want)
	}
	if records[3].Message.Content != "patched" {
		t.Fatalf("tool.end should carry the model-visible output, got %q", records[3].Message.Content)
	}
	if records[4].Message.Content != "done" {
		t.Fatalf("final assistant should carry the answer, got %q", records[4].Message.Content)
	}
}

// TestRunTurnExecutesResumePending 验证恢复预处理只补执行缺失的调用，
// 使"每个 tool_call 都有对应 tool 消息"的协议在进入模型循环前重新成立。
func TestRunTurnExecutesResumePending(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "turn-latest.jsonl")
	log, err := harness.NewTurnLog(path)
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()

	executed := 0
	registry, err := harness.NewRegistry(countingTool{name: "apply_patch", count: &executed, output: "replayed"})
	if err != nil {
		t.Fatal(err)
	}
	model := &finalAnswerModel{}
	agent := harness.NewAgent(model, registry, 4, nil)
	agent.SetTurnLog(log)

	c1 := harness.ToolCall{ID: "c1", Name: "apply_patch", Arguments: json.RawMessage(`{"path":"a.go"}`)}
	c2 := harness.ToolCall{ID: "c2", Name: "apply_patch", Arguments: json.RawMessage(`{"path":"b.go"}`)}
	messages := []harness.Message{
		{Role: "user", Content: "fix"},
		{Role: "assistant", ToolCalls: []harness.ToolCall{c1, c2}},
		{Role: "tool", Content: "done earlier", ToolCallID: "c1"},
	}
	result, err := agent.RunTurn(context.Background(), messages, harness.RunOptions{ResumePending: []harness.ToolCall{c2}})
	if err != nil {
		t.Fatal(err)
	}
	if result.StopReason != harness.StopCompleted {
		t.Fatalf("stop reason = %q, want completed", result.StopReason)
	}
	if executed != 1 {
		t.Fatalf("resume preprocessing must execute exactly the pending call, executed %d", executed)
	}
	if model.calls != 1 {
		t.Fatalf("model should be called once after preprocessing, got %d", model.calls)
	}

	// 消息历史必须重新完整：c1 的旧回应 + c2 的补执行回应 + 最终 assistant。
	var responses []string
	for _, message := range result.Messages {
		if message.Role == "tool" {
			responses = append(responses, message.ToolCallID)
		}
	}
	if len(responses) != 2 || responses[0] != "c1" || responses[1] != "c2" {
		t.Fatalf("tool responses = %v, want [c1 c2]", responses)
	}

	records, err := harness.ReadTurnLog(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, record := range records {
		if record.Kind == harness.TurnRecordToolBegin && record.Step != 0 {
			t.Fatalf("resume preprocessing must be recorded at step 0, got %d", record.Step)
		}
	}
}

// countingTool 在 staticTool 的基础上统计执行次数，供恢复预处理断言使用。
type countingTool struct {
	name   string // Registry 用来注册和查找工具的名称。
	output string // Execute 返回的固定 Observation。
	count  *int   // 保存执行次数的共享计数器。
}

// Spec 返回最小工具声明。
func (t countingTool) Spec() harness.ToolSpec { return harness.ToolSpec{Name: t.name} }

// Execute 累加计数并返回固定结果。
func (t countingTool) Execute(context.Context, json.RawMessage) (harness.ToolResult, error) {
	*t.count++
	return harness.ToolResult{Observation: t.output}, nil
}

// TestAgentWithoutTurnLogKeepsEvaluationsSilent 验证不挂日志的 Agent 完全不产生恢复日志文件。
func TestAgentWithoutTurnLogKeepsEvaluationsSilent(t *testing.T) {
	registry, err := harness.NewRegistry(staticTool{name: "apply_patch", output: "x"})
	if err != nil {
		t.Fatal(err)
	}
	agent := harness.NewAgent(&finalAnswerModel{}, registry, 4, nil)
	result, err := agent.RunTurn(context.Background(), []harness.Message{{Role: "user", Content: "hi"}}, harness.RunOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if result.StopReason != harness.StopCompleted {
		t.Fatalf("stop reason = %q, want completed", result.StopReason)
	}
	entries, err := os.ReadDir(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("no recovery artifacts should exist without SetTurnLog, found %d entries", len(entries))
	}
}
