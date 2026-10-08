package executor_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"xcode/internal/artifactstore"
	"xcode/internal/checkpoint"
	"xcode/internal/executor"
	"xcode/internal/harness"
	"xcode/internal/policy"
	"xcode/internal/tools"
)

// fixture 创建真实工具管线，所有文件都限制在测试临时目录。
func fixture(t *testing.T, mode policy.Mode) (string, *executor.Executor, *checkpoint.Store, *[]harness.Event) {
	t.Helper()
	root := t.TempDir()
	set, err := tools.NewReadOnlySet(context.Background(), root, artifactstore.NewMemoryStore())
	if err != nil {
		t.Fatal(err)
	}
	registry, err := harness.NewRegistry(append(set.Tools(), set.WriteTools()...)...)
	if err != nil {
		t.Fatal(err)
	}
	engine, err := policy.NewEngine(mode)
	if err != nil {
		t.Fatal(err)
	}
	store, err := checkpoint.NewStore(filepath.Join(root, ".xcode-state", "checkpoints"), root)
	if err != nil {
		t.Fatal(err)
	}
	events := []harness.Event{}
	// toolcall.state 事件由 journal 专项测试覆盖，不进入管线顺序断言。
	runner, err := executor.New(registry, engine, nil, store, harness.EventSinkFunc(func(e harness.Event) {
		if e.Kind == harness.ToolCallState {
			return
		}
		events = append(events, e)
	}))
	if err != nil {
		t.Fatal(err)
	}
	return root, runner, store, &events
}

// invoke 把测试参数编码成模型实际提交的 ToolCall。
func invoke(runner *executor.Executor, name string, args any) (harness.ToolResult, error) {
	raw, err := json.Marshal(args)
	if err != nil {
		return harness.ToolResult{}, err
	}
	return runner.Execute(context.Background(), harness.ToolCall{ID: "test-call", Name: name, Arguments: raw})
}

// TestPlanRejectsBeforeCheckpoint 确认策略拒绝不会创建目标文件或写前快照。
func TestPlanRejectsBeforeCheckpoint(t *testing.T) {
	root, runner, store, events := fixture(t, policy.ModePlan)
	_, err := invoke(runner, "apply_patch", map[string]any{"path": "new.txt", "operation": "create", "content": "hello"})
	if err == nil || !strings.Contains(err.Error(), "policy denied") {
		t.Fatalf("expected policy denial, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "new.txt")); !os.IsNotExist(err) {
		t.Fatalf("target exists: %v", err)
	}
	entries, err := store.List(context.Background())
	if err != nil || len(entries) != 0 {
		t.Fatalf("unexpected checkpoints: %v %v", entries, err)
	}
	if len(*events) != 2 || (*events)[1].Kind != harness.PolicyDecided {
		t.Fatalf("events: %#v", *events)
	}
}

// TestPatchRefreshAndRestore 验证创建、读取去重、修改后重新提供正文，以及快照恢复。
func TestPatchRefreshAndRestore(t *testing.T) {
	root, runner, store, events := fixture(t, policy.ModeWorkspaceWrite)
	if _, err := invoke(runner, "apply_patch", map[string]any{"path": "note.txt", "operation": "create", "content": "before"}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		result, err := invoke(runner, "read_file", map[string]any{"path": "note.txt"})
		if err != nil || result.RepeatedRead != (i == 1) {
			t.Fatalf("read %d: %#v %v", i, result, err)
		}
	}
	if _, err := invoke(runner, "apply_patch", map[string]any{"path": "note.txt", "operation": "update", "old_string": "before", "new_string": "after"}); err != nil {
		t.Fatal(err)
	}
	result, err := invoke(runner, "read_file", map[string]any{"path": "note.txt"})
	if err != nil || result.RepeatedRead || !strings.Contains(result.Observation, "after") {
		t.Fatalf("stale read: %#v %v", result, err)
	}
	entries, err := store.List(context.Background())
	if err != nil || len(entries) != 2 {
		t.Fatalf("checkpoints: %#v %v", entries, err)
	}
	if _, err := store.Restore(context.Background(), entries[1].ID); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(root, "note.txt"))
	if err != nil || string(data) != "before" {
		t.Fatalf("restore: %q %v", data, err)
	}
	if _, err := store.Restore(context.Background(), entries[0].ID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "note.txt")); !os.IsNotExist(err) {
		t.Fatalf("created file remains: %v", err)
	}
	for _, event := range *events {
		if event.Time.IsZero() || event.ToolCallID == "" {
			t.Fatalf("missing event identity: %#v", event)
		}
	}
}

// TestWriteRejectsEscapeAndStaleHash 确认非法路径和过期内容不会被工具覆盖。
func TestWriteRejectsEscapeAndStaleHash(t *testing.T) {
	root, runner, _, _ := fixture(t, policy.ModeWorkspaceWrite)
	for _, path := range []string{"../outside.txt", ".env", ".xcode-state/secret.txt"} {
		if _, err := invoke(runner, "apply_patch", map[string]any{"path": path, "operation": "create", "content": "bad"}); err == nil {
			t.Fatalf("accepted %q", path)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "note.txt"), []byte("current"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := invoke(runner, "apply_patch", map[string]any{"path": "note.txt", "operation": "update", "old_string": "current", "new_string": "bad", "expected_content_hash": strings.Repeat("0", 64)})
	if err == nil {
		t.Fatal("accepted stale hash")
	}
	data, err := os.ReadFile(filepath.Join(root, "note.txt"))
	if err != nil || string(data) != "current" {
		t.Fatalf("file changed: %q %v", data, err)
	}
}

// scriptedTool 只记录执行次数，用于独立验证审批和快照阶段的先后关系。
type scriptedTool struct {
	command []string // 保存当前测试调用的命令事实。
	calls   int      // 保存真正执行的次数。
}

func (s *scriptedTool) Spec() harness.ToolSpec { return harness.ToolSpec{Name: "exec"} }
func (s *scriptedTool) DescribeIntent(json.RawMessage) (executor.Intent, error) {
	return executor.Intent{Mutating: true, Command: s.command}, nil
}
func (s *scriptedTool) Execute(context.Context, json.RawMessage) (harness.ToolResult, error) {
	s.calls++
	return harness.ToolResult{}, nil
}

// approvalFunc 把测试函数适配为可控审批器。
type approvalFunc func(context.Context, policy.Request) (executor.ApprovalScope, error)

func (f approvalFunc) Approve(ctx context.Context, request policy.Request) (executor.ApprovalScope, error) {
	return f(ctx, request)
}

// TestApprovalBeforeCommandSnapshot 验证拒绝不会产生副作用，session 授权不扩大到新参数。
func TestApprovalBeforeCommandSnapshot(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	store, err := checkpoint.NewStore(filepath.Join(root, ".xcode-state", "checkpoints"), root)
	if err != nil {
		t.Fatal(err)
	}
	tool := &scriptedTool{command: []string{"program", "first"}}
	registry, err := harness.NewRegistry(tool)
	if err != nil {
		t.Fatal(err)
	}
	engine, err := policy.NewEngine(policy.ModeWorkspaceWrite)
	if err != nil {
		t.Fatal(err)
	}
	scope, approvals := executor.ApprovalDenied, 0
	approver := approvalFunc(func(context.Context, policy.Request) (executor.ApprovalScope, error) { approvals++; return scope, nil })
	runner, err := executor.New(registry, engine, approver, store, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := invoke(runner, "exec", map[string]any{}); err == nil {
		t.Fatal("denial was ignored")
	}
	entries, err := store.List(ctx)
	if err != nil || len(entries) != 0 || tool.calls != 0 {
		t.Fatalf("denied call had side effects: %v", err)
	}
	scope = executor.ApprovalSession
	for i := 0; i < 2; i++ {
		if _, err := invoke(runner, "exec", map[string]any{}); err != nil {
			t.Fatal(err)
		}
	}
	if approvals != 2 || tool.calls != 2 {
		t.Fatalf("approvals=%d executions=%d", approvals, tool.calls)
	}
	tool.command = []string{"program", "different"}
	scope = executor.ApprovalDenied
	if _, err := invoke(runner, "exec", map[string]any{}); err == nil {
		t.Fatal("new arguments inherited authorization")
	}
	entries, err = store.List(ctx)
	if err != nil || len(entries) != 2 || !entries[0].WholeWorkspace || !entries[1].WholeWorkspace {
		t.Fatalf("missing command snapshots: %#v %v", entries, err)
	}
}
