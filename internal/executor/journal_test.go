package executor

// 本文件验证 ToolCall 状态 journal 的读写、终态判定与 unknown 归并。

import (
	"context"       // 为被拒绝的调用构造可取消上下文。
	"encoding/json" // 构造工具调用的原始参数。
	"path/filepath" // 定位临时工作区内的 journal 与快照目录。
	"testing"
	"time"

	"xcode/internal/checkpoint" // 拒绝路径不产生快照的依赖装配。
	"xcode/internal/harness"    // 构造 Registry 与 ToolCall。
	"xcode/internal/policy"     // workspace-write 档位触发 ask 决策。
)

// TestJournalRoundTrip 验证状态迁移追加后可原样读回。
func TestJournalRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "journal", "run-test.jsonl")
	journal, err := OpenJournal(path)
	if err != nil {
		t.Fatal(err)
	}
	journal.Record("call-1", "exec", StatePlanned, "mutating=false")
	journal.Record("call-1", "exec", StateApproved, "allowed without interactive approval")
	journal.Record("call-1", "exec", StateStarted, "side effects imminent")
	journal.Record("call-1", "exec", StateSucceeded, "exec:git status")
	if err := journal.Err(); err != nil {
		t.Fatal(err)
	}
	if err := journal.Close(); err != nil {
		t.Fatal(err)
	}

	entries, err := ReadJournalFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 4 {
		t.Fatalf("entries = %d, want 4", len(entries))
	}
	wantStates := []CallState{StatePlanned, StateApproved, StateStarted, StateSucceeded}
	for index, want := range wantStates {
		if entries[index].State != want || entries[index].ToolCallID != "call-1" {
			t.Fatalf("entry %d = %#v, want state %s", index, entries[index], want)
		}
		if entries[index].Time.IsZero() {
			t.Fatalf("entry %d has zero time", index)
		}
	}
}

// TestUnknownCallsMarksNonTerminal 验证只有非终态调用被归并为 unknown。
func TestUnknownCallsMarksNonTerminal(t *testing.T) {
	base := time.Now().UTC()
	entries := []JournalEntry{
		{Time: base, ToolCallID: "a", Tool: "exec", State: StatePlanned},
		{Time: base, ToolCallID: "a", Tool: "exec", State: StateStarted},
		{Time: base, ToolCallID: "b", Tool: "exec", State: StatePlanned},
		{Time: base, ToolCallID: "b", Tool: "exec", State: StateSucceeded},
		{Time: base, ToolCallID: "c", Tool: "apply_patch", State: StateDenied},
		{Time: base, ToolCallID: "d", Tool: "exec", State: StateFailed},
	}
	unknown := UnknownCalls(entries)
	if len(unknown) != 1 || unknown[0].ToolCallID != "a" || unknown[0].State != StateUnknown {
		t.Fatalf("unknown = %#v, want single call a marked unknown", unknown)
	}
	if want := "last recorded state: started"; unknown[0].Summary != want {
		t.Fatalf("summary = %q, want %q", unknown[0].Summary, want)
	}
}

// TestListJournalFilesSorted 验证运行文件按名称（即时间）升序列出。
func TestListJournalFilesSorted(t *testing.T) {
	directory := t.TempDir()
	for _, name := range []string{"run-b.jsonl", "run-a.jsonl"} {
		journal, err := OpenJournal(filepath.Join(directory, name))
		if err != nil {
			t.Fatal(err)
		}
		journal.Record("x", "exec", StatePlanned, "")
		if err := journal.Close(); err != nil {
			t.Fatal(err)
		}
	}
	files, err := ListJournalFiles(directory)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 2 || filepath.Base(files[0]) != "run-a.jsonl" {
		t.Fatalf("files = %#v", files)
	}
}

// TestJournalNilReceiver 验证未挂载 journal 时 Record 是安静空操作。
func TestJournalNilReceiver(t *testing.T) {
	var journal *Journal
	journal.Record("call", "exec", StatePlanned, "")
	if err := journal.Err(); err != nil {
		t.Fatalf("nil journal Err = %v", err)
	}
	if err := journal.Close(); err != nil {
		t.Fatalf("nil journal Close = %v", err)
	}
}

// denyProbeTool 声明命令意图的最小工具；被拒绝时 Execute 不应被调用。
type denyProbeTool struct{ calls int }

// Spec 返回 exec 工具声明，使调用进入命令审批路径。
func (t *denyProbeTool) Spec() harness.ToolSpec { return harness.ToolSpec{Name: "exec"} }

// DescribeIntent 声明一条命令意图，触发 workspace-write 的 ask 决策。
func (t *denyProbeTool) DescribeIntent(json.RawMessage) (Intent, error) {
	return Intent{Command: []string{"program"}}, nil
}

// Execute 记录调用次数；用户拒绝时必须保持为零。
func (t *denyProbeTool) Execute(context.Context, json.RawMessage) (harness.ToolResult, error) {
	t.calls++
	return harness.ToolResult{}, nil
}

// denyApprover 恒定返回用户拒绝。
type denyApprover struct{}

// Approve 模拟用户在审批提示处选择拒绝。
func (denyApprover) Approve(context.Context, policy.Request) (ApprovalScope, error) {
	return ApprovalDenied, nil
}

// TestUserDeniedRecordsTerminalState 验证用户拒绝在 journal 中是终态 denied，
// 不会停留在非终态被误判为 unknown（验收期缺陷的回归测试）。
func TestUserDeniedRecordsTerminalState(t *testing.T) {
	root := t.TempDir()
	store, err := checkpoint.NewStore(filepath.Join(root, ".xcode-state", "checkpoints"), root)
	if err != nil {
		t.Fatal(err)
	}
	tool := &denyProbeTool{}
	registry, err := harness.NewRegistry(tool)
	if err != nil {
		t.Fatal(err)
	}
	engine, err := policy.NewEngine(policy.ModeWorkspaceWrite)
	if err != nil {
		t.Fatal(err)
	}
	journalPath := filepath.Join(root, ".xcode-state", "journal", "run-deny.jsonl")
	journal, err := OpenJournal(journalPath)
	if err != nil {
		t.Fatal(err)
	}
	defer journal.Close()

	runner, err := New(registry, engine, denyApprover{}, store, nil)
	if err != nil {
		t.Fatal(err)
	}
	runner.SetJournal(journal)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	call := harness.ToolCall{ID: "deny-call", Name: "exec", Arguments: []byte(`{}`)}
	if _, err := runner.Execute(ctx, call); err == nil {
		t.Fatal("denial was ignored")
	}
	if tool.calls != 0 {
		t.Fatalf("denied call executed %d times", tool.calls)
	}

	entries, err := ReadJournalFile(journalPath)
	if err != nil {
		t.Fatal(err)
	}
	last := entries[len(entries)-1]
	if last.State != StateDenied || last.ToolCallID != "deny-call" {
		t.Fatalf("last entry = %#v, want terminal denied", last)
	}
	if unknown := UnknownCalls(entries); len(unknown) != 0 {
		t.Fatalf("user denial must not be unknown, got %#v", unknown)
	}
}
