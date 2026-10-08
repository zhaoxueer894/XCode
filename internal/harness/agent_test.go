// package harness_test 从外部使用公开 API，避免测试依赖包内私有实现。
package harness_test

import (
	"context"       // 构造正常、取消等不同运行上下文。
	"encoding/json" // 验证 Event 对外输出的 JSON 字段名。
	"errors"        // 判断最大步数和取消错误是否保留原始类型。
	"os"            // 在隔离测试目录中创建待读取文件。
	"path/filepath" // 以跨平台方式拼接测试文件路径。
	"reflect"       // 比较完整事件顺序。
	"strings"       // 检查最终答案是否包含真实工具结果。
	"testing"       // Go 标准测试框架。

	"xcode/internal/harness" // 使用待验收的 Agent、Registry 和事件类型。
	"xcode/internal/model"   // 使用离线 Demo Model 验证两轮闭环。
	"xcode/internal/tools"   // 创建受工作区限制的 read_file 工具。
)

// staticTool 是测试专用工具，可以由测试指定名称和固定输出。
type staticTool struct {
	name   string // Registry 用来注册和查找工具的名称。
	output string // Execute 每次返回的固定 Observation。
}

// Spec 返回最小工具声明，让 staticTool 满足 harness.Tool 接口。
func (t staticTool) Spec() harness.ToolSpec {
	return harness.ToolSpec{Name: t.name}
}

// Execute 返回固定结果；参数和 Context 在这些 Registry 测试中不影响行为。
func (t staticTool) Execute(context.Context, json.RawMessage) (harness.ToolResult, error) {
	return harness.ToolResult{Observation: t.output}, nil
}

// repeatingModel 每轮都请求同一个工具，用来触发 Agent 的最大步数保护。
type repeatingModel struct{}

// Generate 始终生成工具调用，因此 Agent 不会主动返回最终答案。
func (repeatingModel) Generate(
	_ context.Context,
	_ []harness.Message,
	_ []harness.ToolSpec,
	sink harness.ModelEventSink,
) (harness.ModelResponse, error) {
	if sink != nil {
		sink.EmitModelEvent(harness.ModelEvent{
			Kind:           harness.ModelToolCallDelta,
			ToolCallIndex:  0,
			ToolCallID:     "repeat-call",
			ToolName:       "repeat",
			ArgumentsDelta: `{}`,
		})
		sink.EmitModelEvent(harness.ModelEvent{Kind: harness.ModelFinished, FinishReason: "tool_calls"})
	}
	return harness.ModelResponse{ToolCalls: []harness.ToolCall{{
		ID:        "repeat-call",
		Name:      "repeat",
		Arguments: json.RawMessage(`{}`),
	}}, FinishReason: "tool_calls"}, nil
}

// TestAgentReadsWorkspaceFileAndEmitsLifecycle 验证完整的“模型 → 工具 → 模型”闭环。
func TestAgentReadsWorkspaceFileAndEmitsLifecycle(t *testing.T) {
	// 每个测试使用独立临时工作区，结束后由 Go 自动清理。
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("hello harness"), 0o600); err != nil {
		t.Fatal(err)
	}

	readFile, err := tools.NewReadFile(root)
	if err != nil {
		t.Fatal(err)
	}
	registry, err := harness.NewRegistry(readFile)
	if err != nil {
		t.Fatal(err)
	}

	// 收集事件，既验证最终答案，也验证 Harness 是否报告了完整生命周期。
	var events []harness.Event
	agent := harness.NewAgent(model.Demo{}, registry, 4, harness.EventSinkFunc(func(event harness.Event) {
		events = append(events, event)
	}))
	answer, err := agent.Run(context.Background(), "读取 README.md")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(answer, "hello harness") {
		t.Fatalf("answer does not contain file content: %q", answer)
	}

	gotKinds := make([]harness.EventKind, 0, len(events))
	for _, event := range events {
		gotKinds = append(gotKinds, event.Kind)
		if event.Time.IsZero() {
			t.Fatalf("event %q has zero timestamp", event.Kind)
		}
	}
	wantKinds := []harness.EventKind{
		harness.RunStarted,
		harness.ModelToolCallDeltaEmitted,
		harness.ModelFinishedEmitted,
		harness.ModelCompleted,
		harness.ToolStarted,
		harness.ToolCompleted,
		harness.ModelTextDeltaEmitted,
		harness.ModelFinishedEmitted,
		harness.ModelCompleted,
		harness.RunCompleted,
	}
	if !reflect.DeepEqual(gotKinds, wantKinds) {
		t.Fatalf("event kinds = %v, want %v", gotKinds, wantKinds)
	}
	if events[1].ToolCallIndex == nil || *events[1].ToolCallIndex != 0 {
		t.Fatalf("tool-call delta index = %#v, want 0", events[1].ToolCallIndex)
	}
	if events[1].ToolCallID != "call-1" || events[3].FinishReason != "tool_calls" {
		t.Fatalf("stream event metadata was not preserved: %#v", events)
	}
}

// TestEventJSONUsesStableFieldNames 验证 CLI 事件使用协议约定的小写字段名。
func TestEventJSONUsesStableFieldNames(t *testing.T) {
	toolCallIndex := 0
	encoded, err := json.Marshal(harness.Event{
		Kind:          harness.ModelToolCallDeltaEmitted,
		Step:          2,
		Tool:          "read_file",
		ToolCallID:    "call-1",
		ToolCallIndex: &toolCallIndex,
		Data:          "preview",
		InputTokens:   10,
		OutputTokens:  2,
		TotalTokens:   12,
		FinishReason:  "tool_calls",
		Error:         "boom",
	})
	if err != nil {
		t.Fatal(err)
	}

	var fields map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &fields); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{
		"time",
		"kind",
		"step",
		"tool",
		"tool_call_id",
		"tool_call_index",
		"data",
		"input_tokens",
		"output_tokens",
		"total_tokens",
		"finish_reason",
		"error",
	} {
		if _, ok := fields[name]; !ok {
			t.Errorf("JSON event missing %q field: %s", name, encoded)
		}
	}
	for _, unexpected := range []string{"Time", "Kind", "Step", "Tool", "Data", "Error"} {
		if _, ok := fields[unexpected]; ok {
			t.Errorf("JSON event contains Go field name %q: %s", unexpected, encoded)
		}
	}
}

// TestAgentForwardsUsageAndFinishReason 验证模型统计与停止原因进入统一事件。
func TestAgentForwardsUsageAndFinishReason(t *testing.T) {
	registry, err := harness.NewRegistry()
	if err != nil {
		t.Fatal(err)
	}
	model := modelFunc(func(_ context.Context, _ []harness.Message, _ []harness.ToolSpec, sink harness.ModelEventSink) (harness.ModelResponse, error) {
		usage := harness.Usage{InputTokens: 7, OutputTokens: 3, TotalTokens: 10}
		sink.EmitModelEvent(harness.ModelEvent{Kind: harness.ModelTextDelta, TextDelta: "done"})
		sink.EmitModelEvent(harness.ModelEvent{Kind: harness.ModelUsage, Usage: usage})
		sink.EmitModelEvent(harness.ModelEvent{Kind: harness.ModelFinished, FinishReason: "stop"})
		return harness.ModelResponse{Content: "done", Usage: usage, FinishReason: "stop"}, nil
	})

	var events []harness.Event
	agent := harness.NewAgent(model, registry, 1, harness.EventSinkFunc(func(event harness.Event) {
		events = append(events, event)
	}))
	answer, err := agent.Run(context.Background(), "finish")
	if err != nil {
		t.Fatal(err)
	}
	if answer != "done" {
		t.Fatalf("answer = %q, want done", answer)
	}
	if len(events) != 6 {
		t.Fatalf("events = %#v", events)
	}
	if events[2].Kind != harness.ModelUsageEmitted || events[2].TotalTokens != 10 {
		t.Fatalf("usage event = %#v", events[2])
	}
	if events[4].Kind != harness.ModelCompleted || events[4].FinishReason != "stop" || events[4].InputTokens != 7 {
		t.Fatalf("completed event = %#v", events[4])
	}
}

// modelFunc 让测试函数直接实现 harness.Model，避免额外的假模型结构体。
type modelFunc func(context.Context, []harness.Message, []harness.ToolSpec, harness.ModelEventSink) (harness.ModelResponse, error)

// Generate 把模型调用转发给测试提供的函数。
func (f modelFunc) Generate(ctx context.Context, messages []harness.Message, tools []harness.ToolSpec, sink harness.ModelEventSink) (harness.ModelResponse, error) {
	return f(ctx, messages, tools, sink)
}

// TestRegistryRejectsInvalidNamesAndSortsSpecs 验证注册表拒绝歧义并稳定排序。
func TestRegistryRejectsInvalidNamesAndSortsSpecs(t *testing.T) {
	if _, err := harness.NewRegistry(staticTool{}); err == nil {
		t.Fatal("empty tool name was accepted")
	}
	if _, err := harness.NewRegistry(staticTool{name: "same"}, staticTool{name: "same"}); err == nil {
		t.Fatal("duplicate tool names were accepted")
	}

	registry, err := harness.NewRegistry(staticTool{name: "zeta"}, staticTool{name: "alpha"})
	if err != nil {
		t.Fatal(err)
	}
	specs := registry.Specs()
	if len(specs) != 2 || specs[0].Name != "alpha" || specs[1].Name != "zeta" {
		t.Fatalf("spec order = %#v", specs)
	}
	if _, err := registry.Execute(context.Background(), harness.ToolCall{Name: "missing"}); err == nil {
		t.Fatal("unknown tool was executed")
	}
}

// TestAgentStopsAtMaxSteps 验证错误模型不能让 Agent 无限循环。
func TestAgentStopsAtMaxSteps(t *testing.T) {
	registry, err := harness.NewRegistry(staticTool{name: "repeat", output: "again"})
	if err != nil {
		t.Fatal(err)
	}
	var events []harness.Event
	agent := harness.NewAgent(repeatingModel{}, registry, 2, harness.EventSinkFunc(func(event harness.Event) {
		events = append(events, event)
	}))
	_, err = agent.Run(context.Background(), "loop")
	if !errors.Is(err, harness.ErrMaxSteps) {
		t.Fatalf("error = %v, want ErrMaxSteps", err)
	}
	if len(events) == 0 || events[len(events)-1].Kind != harness.RunFailed {
		t.Fatalf("last event = %#v, want run.failed", events)
	}
}

// TestAgentHonorsCanceledContext 验证运行开始后会立即响应调用方取消。
func TestAgentHonorsCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	registry, err := harness.NewRegistry()
	if err != nil {
		t.Fatal(err)
	}
	agent := harness.NewAgent(model.Demo{}, registry, 1, nil)
	_, err = agent.Run(ctx, "读取 README.md")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
}
