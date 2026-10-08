package recovery_test

// 测试：恢复分析的对账判定。三个崩溃窗口都以"构造磁盘状态"的方式注入：
//   W1（started 后未执行）→ journal 停在 started 但文件等于基线 → retry；
//   W2（副作用已发生、结果未落盘）→ journal 停在 started 且文件等于补丁结果 → applied；
//   W3（结果已落盘、下一次模型调用未开始）→ begin/end 配对完整、无终止记录 → 无未完成调用。

import (
	"context"       // 驱动快照 Capture。
	"encoding/json" // 构造工具调用参数。
	"os"            // 写入临时工作区文件。
	"path/filepath" // 拼接临时路径。
	"strings"       // 断言确定性说明内容。
	"testing"       // Go 标准测试框架。
	"time"          // 构造记录时间。

	"xcode/internal/checkpoint" // 用真实快照仓库构造基线证据。
	"xcode/internal/executor"   // 构造 journal 条目。
	"xcode/internal/harness"    // 复用 TurnRecord、Message 与 ToolCall。
	"xcode/internal/recovery"   // 被测对象：Analyze 与对账判定。
)

const (
	baselineText = "package main\n\nfunc add(a int, b int) int {\n\treturn a - b\n}\n"
	patchedText  = "package main\n\nfunc add(a int, b int) int {\n\treturn a + b\n}\n"
)

// fixture 汇总一个测试工作区的全部路径与证据容器。
type fixture struct {
	dir       string                  // 临时工作区根目录。
	workspace string                  // 传给 Analyze 的工作区路径。
	store     *checkpoint.Store       // 真实快照仓库，manifest 与 blob 由此产生。
	records   []harness.TurnRecord    // 待写入的轮次恢复日志。
	journal   []executor.JournalEntry // 待传入的 journal 条目。
}

// newFixture 创建空的临时工作区与真实快照仓库。
func newFixture(t *testing.T) *fixture {
	t.Helper()
	dir := t.TempDir()
	store, err := checkpoint.NewStore(filepath.Join(dir, ".xcode-state", "checkpoints"), dir)
	if err != nil {
		t.Fatal(err)
	}
	return &fixture{dir: dir, workspace: dir, store: store}
}

// writeFile 在工作区内写入文件内容。
func (f *fixture) writeFile(t *testing.T, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(f.dir, name), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// captureBaseline 在写入补丁前用真实管线捕获基线快照，返回 manifest 条目。
func (f *fixture) captureBaseline(t *testing.T, name string) checkpoint.Entry {
	t.Helper()
	entry, err := f.store.Capture(context.Background(), name)
	if err != nil {
		t.Fatal(err)
	}
	return entry
}

// journalEntry 构造一条带时间的 journal 记录。
func journalEntry(callID, tool string, state executor.CallState) executor.JournalEntry {
	return executor.JournalEntry{Time: time.Now().UTC(), ToolCallID: callID, Tool: tool, State: state, Summary: "test"}
}

// record 构造一条轮次恢复记录。
func record(kind harness.TurnRecordKind, message *harness.Message, callID, tool, arguments string) harness.TurnRecord {
	r := harness.TurnRecord{Kind: kind, Time: time.Now().UTC(), Message: message, Tool: tool, ToolCallID: callID}
	if arguments != "" {
		r.Arguments = json.RawMessage(arguments)
	}
	return r
}

// patchCall 构造一次 update 补丁调用。
func patchCall(id string) harness.ToolCall {
	return harness.ToolCall{ID: id, Name: "apply_patch", Arguments: json.RawMessage(
		`{"path":"calc.go","operation":"update","old_string":"return a - b","new_string":"return a + b"}`)}
}

// analyze 落盘轮次日志并执行恢复分析。
func (f *fixture) analyze(t *testing.T) *recovery.Plan {
	t.Helper()
	path := filepath.Join(f.dir, "recovery", "turn-latest.jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	encoder := json.NewEncoder(&b)
	for _, r := range f.records {
		if err := encoder.Encode(r); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	manifest, err := f.store.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	plan, err := recovery.Analyze(recovery.Input{
		Records:     f.records,
		Journal:     f.journal,
		Checkpoints: manifest,
		Store:       f.store,
		Workspace:   f.workspace,
	})
	if err != nil {
		t.Fatal(err)
	}
	return plan
}

// assistantWith 生成一条携带给定工具调用的 assistant 消息记录。
func assistantWith(calls ...harness.ToolCall) harness.TurnRecord {
	return record(harness.TurnRecordAssistant,
		&harness.Message{Role: "assistant", ToolCalls: calls}, "", "", "")
}

// TestAnalyzeWithoutUnfinishedTurn 验证空日志与已终止轮次都不产生恢复计划。
func TestAnalyzeWithoutUnfinishedTurn(t *testing.T) {
	f := newFixture(t)
	empty, err := recovery.Analyze(recovery.Input{Workspace: f.workspace})
	if err != nil {
		t.Fatal(err)
	}
	if !empty.Completed {
		t.Fatal("no records at all must report completed")
	}

	f.records = []harness.TurnRecord{
		record(harness.TurnRecordStarted, &harness.Message{Role: "user", Content: "task"}, "", "", ""),
		record(harness.TurnRecordCompleted, &harness.Message{Role: "assistant", Content: "done"}, "", "", ""),
	}
	plan := f.analyze(t)
	if !plan.Completed {
		t.Fatal("a terminated segment must report completed")
	}
	if len(plan.Pending) != 0 || len(plan.Verdicts) != 0 {
		t.Fatalf("terminated segment must have nothing to do, got %d pending / %d verdicts", len(plan.Pending), len(plan.Verdicts))
	}
}

// TestAnalyzeRetryWhenCallNeverStarted（W1）验证 journal 停在 approved 的调用可安全重试。
func TestAnalyzeRetryWhenCallNeverStarted(t *testing.T) {
	f := newFixture(t)
	f.writeFile(t, "calc.go", patchedText)
	f.records = []harness.TurnRecord{
		record(harness.TurnRecordStarted, &harness.Message{Role: "user", Content: "fix"}, "", "", ""),
		assistantWith(patchCall("c2")),
		record(harness.TurnRecordToolBegin, nil, "c2", "apply_patch", string(patchCall("c2").Arguments)),
	}
	f.journal = []executor.JournalEntry{journalEntry("c2", "apply_patch", executor.StateApproved)}

	plan := f.analyze(t)
	if len(plan.Verdicts) != 1 || plan.Verdicts[0].Verdict != recovery.VerdictRetry {
		t.Fatalf("verdict = %+v, want retry", plan.Verdicts)
	}
	if len(plan.Pending) != 1 || plan.Pending[0].ID != "c2" {
		t.Fatalf("pending = %+v, want c2", plan.Pending)
	}
	// 未开始的调用不需要占位消息，恢复预处理会补上真正的工具回应。
	if got := plan.Messages[len(plan.Messages)-1].Role; got == "tool" {
		t.Fatalf("retry verdict must not append a placeholder message, last role = %q", got)
	}
}

// TestAnalyzeRetryReadOnlyStarted（W1 变体）验证只读工具停在 started 也可以重试。
func TestAnalyzeRetryReadOnlyStarted(t *testing.T) {
	f := newFixture(t)
	f.records = []harness.TurnRecord{
		record(harness.TurnRecordStarted, &harness.Message{Role: "user", Content: "read"}, "", "", ""),
		assistantWith(harness.ToolCall{ID: "c1", Name: "read_file", Arguments: json.RawMessage(`{"path":"calc.go"}`)}),
		record(harness.TurnRecordToolBegin, nil, "c1", "read_file", `{"path":"calc.go"}`),
	}
	f.journal = []executor.JournalEntry{journalEntry("c1", "read_file", executor.StateStarted)}

	plan := f.analyze(t)
	if len(plan.Verdicts) != 1 || plan.Verdicts[0].Verdict != recovery.VerdictRetry {
		t.Fatalf("verdict = %+v, want retry", plan.Verdicts)
	}
	if !strings.Contains(plan.Verdicts[0].Reason, "read-only") {
		t.Fatalf("reason should mention read-only, got %q", plan.Verdicts[0].Reason)
	}
}

// TestAnalyzePatchAppliedAfterCrash（W2）验证补丁已生效时不重放并补确定性说明。
func TestAnalyzePatchAppliedAfterCrash(t *testing.T) {
	f := newFixture(t)
	f.writeFile(t, "calc.go", baselineText)
	f.captureBaseline(t, "calc.go") // 执行管线在补丁前捕获的基线。
	f.writeFile(t, "calc.go", patchedText)

	f.records = []harness.TurnRecord{
		record(harness.TurnRecordStarted, &harness.Message{Role: "user", Content: "fix"}, "", "", ""),
		assistantWith(patchCall("c2")),
		record(harness.TurnRecordToolBegin, nil, "c2", "apply_patch", string(patchCall("c2").Arguments)),
	}
	f.journal = []executor.JournalEntry{journalEntry("c2", "apply_patch", executor.StateStarted)}

	plan := f.analyze(t)
	if len(plan.Verdicts) != 1 || plan.Verdicts[0].Verdict != recovery.VerdictApplied {
		t.Fatalf("verdict = %+v, want applied", plan.Verdicts)
	}
	if len(plan.Pending) != 0 {
		t.Fatalf("applied patch must not be replayed, pending = %+v", plan.Pending)
	}
	last := plan.Messages[len(plan.Messages)-1]
	if last.ToolCallID != "c2" || !strings.Contains(last.Content, "already took effect") || !strings.Contains(last.Content, "not replayed") {
		t.Fatalf("deterministic note missing, got %+v", last)
	}
}

// TestAnalyzePatchRetryWhenBaselineIntact（W2 的另一面）验证补丁未生效时可安全重试。
func TestAnalyzePatchRetryWhenBaselineIntact(t *testing.T) {
	f := newFixture(t)
	f.writeFile(t, "calc.go", baselineText)
	f.captureBaseline(t, "calc.go")

	f.records = []harness.TurnRecord{
		record(harness.TurnRecordStarted, &harness.Message{Role: "user", Content: "fix"}, "", "", ""),
		assistantWith(patchCall("c2")),
		record(harness.TurnRecordToolBegin, nil, "c2", "apply_patch", string(patchCall("c2").Arguments)),
	}
	f.journal = []executor.JournalEntry{journalEntry("c2", "apply_patch", executor.StateStarted)}

	plan := f.analyze(t)
	if len(plan.Verdicts) != 1 || plan.Verdicts[0].Verdict != recovery.VerdictRetry {
		t.Fatalf("verdict = %+v, want retry", plan.Verdicts)
	}
	if len(plan.Pending) != 1 {
		t.Fatalf("pending = %+v, want the patch call", plan.Pending)
	}
}

// TestAnalyzePatchConflictOnExternalChange 验证文件既不是基线也不是补丁结果时判冲突。
func TestAnalyzePatchConflictOnExternalChange(t *testing.T) {
	f := newFixture(t)
	f.writeFile(t, "calc.go", baselineText)
	f.captureBaseline(t, "calc.go")
	f.writeFile(t, "calc.go", "// externally rewritten\n")

	f.records = []harness.TurnRecord{
		record(harness.TurnRecordStarted, &harness.Message{Role: "user", Content: "fix"}, "", "", ""),
		assistantWith(patchCall("c2")),
		record(harness.TurnRecordToolBegin, nil, "c2", "apply_patch", string(patchCall("c2").Arguments)),
	}
	f.journal = []executor.JournalEntry{journalEntry("c2", "apply_patch", executor.StateStarted)}

	plan := f.analyze(t)
	if len(plan.Verdicts) != 1 || plan.Verdicts[0].Verdict != recovery.VerdictConflict {
		t.Fatalf("verdict = %+v, want conflict", plan.Verdicts)
	}
	last := plan.Messages[len(plan.Messages)-1]
	if last.Content != "[recovery-pending]" {
		t.Fatalf("conflict keeps a placeholder for protocol integrity, got %q", last.Content)
	}
}

// TestAnalyzeCreateAndDeleteVerdicts 覆盖 create 与 delete 的已生效 / 未生效判定。
func TestAnalyzeCreateAndDeleteVerdicts(t *testing.T) {
	newCall := func(id, op, extra string) harness.ToolCall {
		args := `{"path":"new.go","operation":"` + op + `"` + extra + `}`
		return harness.ToolCall{ID: id, Name: "apply_patch", Arguments: json.RawMessage(args)}
	}

	// create 已生效：文件内容与新文件一致。
	f := newFixture(t)
	content := "package main\n"
	f.writeFile(t, "new.go", content)
	f.records = []harness.TurnRecord{
		record(harness.TurnRecordStarted, &harness.Message{Role: "user", Content: "x"}, "", "", ""),
		assistantWith(newCall("c1", "create", `,"content":"package main\n"`)),
		record(harness.TurnRecordToolBegin, nil, "c1", "apply_patch", string(newCall("c1", "create", `,"content":"package main\n"`).Arguments)),
	}
	f.journal = []executor.JournalEntry{journalEntry("c1", "apply_patch", executor.StateStarted)}
	if plan := f.analyze(t); plan.Verdicts[0].Verdict != recovery.VerdictApplied {
		t.Fatalf("create applied verdict = %+v", plan.Verdicts[0])
	}

	// create 未生效：文件仍不存在。
	f2 := newFixture(t)
	f2.records = []harness.TurnRecord{
		record(harness.TurnRecordStarted, &harness.Message{Role: "user", Content: "x"}, "", "", ""),
		assistantWith(newCall("c1", "create", `,"content":"package main\n"`)),
		record(harness.TurnRecordToolBegin, nil, "c1", "apply_patch", string(newCall("c1", "create", `,"content":"package main\n"`).Arguments)),
	}
	f2.journal = []executor.JournalEntry{journalEntry("c1", "apply_patch", executor.StateStarted)}
	if plan := f2.analyze(t); plan.Verdicts[0].Verdict != recovery.VerdictRetry {
		t.Fatalf("create retry verdict = %+v", plan.Verdicts[0])
	}

	// delete 已生效：目标文件已消失。
	f3 := newFixture(t)
	f3.writeFile(t, "old.go", baselineText)
	f3.captureBaseline(t, "old.go")
	if err := os.Remove(filepath.Join(f3.dir, "old.go")); err != nil {
		t.Fatal(err)
	}
	f3.records = []harness.TurnRecord{
		record(harness.TurnRecordStarted, &harness.Message{Role: "user", Content: "x"}, "", "", ""),
		assistantWith(newCall("c1", "delete", "")),
		record(harness.TurnRecordToolBegin, nil, "c1", "apply_patch", string(newCall("c1", "delete", "").Arguments)),
	}
	f3.journal = []executor.JournalEntry{journalEntry("c1", "apply_patch", executor.StateStarted)}
	if plan := f3.analyze(t); plan.Verdicts[0].Verdict != recovery.VerdictApplied {
		t.Fatalf("delete applied verdict = %+v", plan.Verdicts[0])
	}
}

// TestAnalyzeConfirmForUnknownExec 验证非幂等 unknown 调用只进人工清单，绝不进入重试。
func TestAnalyzeConfirmForUnknownExec(t *testing.T) {
	f := newFixture(t)
	f.records = []harness.TurnRecord{
		record(harness.TurnRecordStarted, &harness.Message{Role: "user", Content: "test"}, "", "", ""),
		assistantWith(harness.ToolCall{ID: "c3", Name: "exec", Arguments: json.RawMessage(`{"command":["go","test","./..."]}`)}),
		record(harness.TurnRecordToolBegin, nil, "c3", "exec", `{"command":["go","test","./..."]}`),
	}
	f.journal = []executor.JournalEntry{journalEntry("c3", "exec", executor.StateStarted)}

	plan := f.analyze(t)
	if len(plan.Verdicts) != 1 || plan.Verdicts[0].Verdict != recovery.VerdictConfirm {
		t.Fatalf("verdict = %+v, want confirm", plan.Verdicts)
	}
	if len(plan.Pending) != 0 {
		t.Fatalf("unknown exec must never be auto-retried, pending = %+v", plan.Pending)
	}
	plan.ResolveConfirm("c3", "[recovery] skipped note")
	last := plan.Messages[len(plan.Messages)-1]
	if last.Content == "[recovery-pending]" || !strings.Contains(last.Content, "skipped note") {
		t.Fatalf("ResolveConfirm must fill the placeholder, got %q", last.Content)
	}
}

// TestAnalyzeLostTerminalStates 验证 journal 已有终态的调用按终态补确定性说明且不重放。
func TestAnalyzeLostTerminalStates(t *testing.T) {
	cases := []struct {
		state    executor.CallState // 注入的 journal 终态。
		contains string             // 确定性说明必须包含的关键词。
		isError  bool               // 说明是否以 ERROR: 开头。
	}{
		{executor.StateSucceeded, "succeeded", false},
		{executor.StateFailed, "failed", true},
		{executor.StateDenied, "denied", true},
	}
	for _, tc := range cases {
		f := newFixture(t)
		f.records = []harness.TurnRecord{
			record(harness.TurnRecordStarted, &harness.Message{Role: "user", Content: "x"}, "", "", ""),
			assistantWith(harness.ToolCall{ID: "c9", Name: "search_text", Arguments: json.RawMessage(`{"query":"x"}`)}),
			record(harness.TurnRecordToolBegin, nil, "c9", "search_text", `{"query":"x"}`),
		}
		f.journal = []executor.JournalEntry{journalEntry("c9", "search_text", tc.state)}
		plan := f.analyze(t)
		if len(plan.Verdicts) != 1 || plan.Verdicts[0].Verdict != recovery.VerdictLost {
			t.Fatalf("state %q verdict = %+v, want lost", tc.state, plan.Verdicts)
		}
		got := plan.Messages[len(plan.Messages)-1].Content
		if !strings.Contains(got, tc.contains) {
			t.Fatalf("state %q note %q missing %q", tc.state, got, tc.contains)
		}
		if tc.isError != strings.HasPrefix(got, "ERROR:") {
			t.Fatalf("state %q note %q error prefix = %v, want %v", tc.state, got, strings.HasPrefix(got, "ERROR:"), tc.isError)
		}
	}
}

// TestAnalyzeIgnoresCompletedCalls（W3）验证 begin/end 配对完整的调用不再进入对账。
func TestAnalyzeIgnoresCompletedCalls(t *testing.T) {
	f := newFixture(t)
	c1 := harness.ToolCall{ID: "c1", Name: "read_file", Arguments: json.RawMessage(`{"path":"calc.go"}`)}
	c2 := patchCall("c2")
	f.records = []harness.TurnRecord{
		record(harness.TurnRecordStarted, &harness.Message{Role: "user", Content: "fix"}, "", "", ""),
		assistantWith(c1, c2),
		record(harness.TurnRecordToolBegin, nil, "c1", "read_file", string(c1.Arguments)),
		record(harness.TurnRecordToolEnd, &harness.Message{Role: "tool", Content: "ok", ToolCallID: "c1"}, "c1", "read_file", ""),
		record(harness.TurnRecordToolBegin, nil, "c2", "apply_patch", string(c2.Arguments)),
	}
	f.journal = []executor.JournalEntry{
		journalEntry("c1", "read_file", executor.StateSucceeded),
	}

	plan := f.analyze(t)
	// c1 已配对完成，不进判定；c2 只有 begin（journal 无记录 = 未开始）→ retry。
	if len(plan.Verdicts) != 1 || plan.Verdicts[0].Call.ID != "c2" {
		t.Fatalf("verdicts = %+v, want only c2", plan.Verdicts)
	}
	if plan.Verdicts[0].Verdict != recovery.VerdictRetry {
		t.Fatalf("c2 verdict = %+v, want retry", plan.Verdicts[0])
	}
	// 重建消息：user + assistant + c1 的 tool 回应，c2 留给恢复预处理。
	if len(plan.Messages) != 3 {
		t.Fatalf("rebuilt messages = %d, want 3", len(plan.Messages))
	}
}

// TestHasUnfinishedTurnStates 覆盖交互提示用的快速判断。
func TestHasUnfinishedTurnStates(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "turn-latest.jsonl")

	if unfinished, _, err := recovery.HasUnfinishedTurn(path); err != nil || unfinished {
		t.Fatalf("missing log must be finished/nil-error, got %v/%v", unfinished, err)
	}

	writeLog := func(lines ...harness.TurnRecord) {
		t.Helper()
		var b strings.Builder
		encoder := json.NewEncoder(&b)
		for _, r := range lines {
			if err := encoder.Encode(r); err != nil {
				t.Fatal(err)
			}
		}
		if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	started := record(harness.TurnRecordStarted, &harness.Message{Role: "user", Content: "x"}, "", "", "")
	completed := record(harness.TurnRecordCompleted, &harness.Message{Role: "assistant", Content: "y"}, "", "", "")

	writeLog(started, completed)
	if unfinished, _, err := recovery.HasUnfinishedTurn(path); err != nil || unfinished {
		t.Fatalf("terminated turn must not be unfinished, got %v/%v", unfinished, err)
	}

	writeLog(started)
	unfinished, since, err := recovery.HasUnfinishedTurn(path)
	if err != nil || !unfinished {
		t.Fatalf("unterminated turn must be unfinished, got %v/%v", unfinished, err)
	}
	if since.IsZero() {
		t.Fatal("unfinished turn must report its start time")
	}
}

// TestExecCommandText 覆盖确认清单的命令展示。
func TestExecCommandText(t *testing.T) {
	if got := recovery.ExecCommandText(json.RawMessage(`{"command":["go","test","./..."]}`)); got != "go test ./..." {
		t.Fatalf("command text = %q", got)
	}
	if got := recovery.ExecCommandText(json.RawMessage(`{}`)); got != "(unreadable arguments)" {
		t.Fatalf("fallback text = %q", got)
	}
}

// TestAnalyzePendingWhenCrashBeforeToolBegin 覆盖第四个崩溃窗口：
// assistant（含工具调用）已落盘，但进程在对应 tool.begin 之前崩溃。
// 这些调用必须进入 Pending 安全重试，否则重建的历史会违反模型协议。
func TestAnalyzePendingWhenCrashBeforeToolBegin(t *testing.T) {
	f := newFixture(t)
	c1 := harness.ToolCall{ID: "c1", Name: "read_file", Arguments: json.RawMessage(`{"path":"calc.go"}`)}
	c2 := patchCall("c2")
	f.records = []harness.TurnRecord{
		record(harness.TurnRecordStarted, &harness.Message{Role: "user", Content: "fix"}, "", "", ""),
		assistantWith(c1, c2),
		// c1 完整走完；c2 没有 tool.begin——崩溃落在两个边界之间。
		record(harness.TurnRecordToolBegin, nil, "c1", "read_file", string(c1.Arguments)),
		record(harness.TurnRecordToolEnd, &harness.Message{Role: "tool", Content: "ok", ToolCallID: "c1"}, "c1", "read_file", ""),
	}
	f.journal = []executor.JournalEntry{journalEntry("c1", "read_file", executor.StateSucceeded)}

	plan := f.analyze(t)
	if len(plan.Verdicts) != 1 || plan.Verdicts[0].Call.ID != "c2" {
		t.Fatalf("verdicts = %+v, want only c2", plan.Verdicts)
	}
	if plan.Verdicts[0].Verdict != recovery.VerdictRetry || !strings.Contains(plan.Verdicts[0].Reason, "never started") {
		t.Fatalf("c2 verdict = %+v, want retry/never started", plan.Verdicts[0])
	}
	if len(plan.Pending) != 1 || plan.Pending[0].ID != "c2" {
		t.Fatalf("pending = %+v, want c2", plan.Pending)
	}
}
