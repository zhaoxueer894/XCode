package harness_test

// 本文件验证运行控制：doom-loop 检测、Token 预算、停止原因与旧入口 Run 的委托行为。

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"xcode/internal/harness"
)

// TestRunWithOptionsReportsCompletedStop 验证正常结束时 RunResult 携带答案、停止原因、步数与累计用量。
func TestRunWithOptionsReportsCompletedStop(t *testing.T) {
	registry, err := harness.NewRegistry()
	if err != nil {
		t.Fatal(err)
	}
	answerModel := modelFunc(func(
		_ context.Context,
		_ []harness.Message,
		_ []harness.ToolSpec,
		_ harness.ModelEventSink,
	) (harness.ModelResponse, error) {
		return harness.ModelResponse{
			Content:      "done",
			Usage:        harness.Usage{InputTokens: 3, OutputTokens: 5, TotalTokens: 8},
			FinishReason: "stop",
		}, nil
	})
	agent := harness.NewAgent(answerModel, registry, 4, nil)

	result, err := agent.RunWithOptions(context.Background(), "hello", harness.RunOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if result.Answer != "done" || result.StopReason != harness.StopCompleted ||
		result.Steps != 1 || result.Usage.TotalTokens != 8 {
		t.Fatalf("result = %#v", result)
	}

	// 旧入口 Run 只返回答案，行为必须与 RunWithOptions 完全一致。
	answer, err := agent.Run(context.Background(), "hello")
	if err != nil || answer != "done" {
		t.Fatalf("Run answer = %q err = %v", answer, err)
	}
}

// TestRunWithOptionsStopsDoomLoop 验证连续相同调用在第三次触发停止，且第三次不再执行。
func TestRunWithOptionsStopsDoomLoop(t *testing.T) {
	registry, err := harness.NewRegistry(staticTool{name: "repeat", output: "again"})
	if err != nil {
		t.Fatal(err)
	}
	var events []harness.Event
	agent := harness.NewAgent(repeatingModel{}, registry, 10, harness.EventSinkFunc(func(event harness.Event) {
		events = append(events, event)
	}))

	result, err := agent.RunWithOptions(context.Background(), "loop", harness.RunOptions{})
	if !errors.Is(err, harness.ErrDoomLoop) {
		t.Fatalf("error = %v, want ErrDoomLoop", err)
	}
	if result.StopReason != harness.StopDoomLoop {
		t.Fatalf("stop reason = %q, want doom_loop", result.StopReason)
	}

	started := 0
	for _, event := range events {
		if event.Kind == harness.ToolStarted {
			started++
		}
	}
	if started != 2 {
		t.Fatalf("tool.started count = %d, want 2 (third call must not execute)", started)
	}
	last := events[len(events)-1]
	if last.Kind != harness.RunFailed || last.StopReason != harness.StopDoomLoop {
		t.Fatalf("last event = %#v, want run.failed with doom_loop", last)
	}
}

// TestRunWithOptionsStopsWhenBudgetExhausted 验证预算在下一次模型调用前触发，已开始的调用不再追加。
func TestRunWithOptionsStopsWhenBudgetExhausted(t *testing.T) {
	registry, err := harness.NewRegistry(staticTool{name: "repeat", output: "again"})
	if err != nil {
		t.Fatal(err)
	}
	modelCalls := 0
	budgetModel := modelFunc(func(
		_ context.Context,
		_ []harness.Message,
		_ []harness.ToolSpec,
		_ harness.ModelEventSink,
	) (harness.ModelResponse, error) {
		modelCalls++
		return harness.ModelResponse{
			ToolCalls: []harness.ToolCall{{
				ID:        "budget-call",
				Name:      "repeat",
				Arguments: json.RawMessage(`{}`),
			}},
			Usage:        harness.Usage{TotalTokens: 100},
			FinishReason: "tool_calls",
		}, nil
	})
	agent := harness.NewAgent(budgetModel, registry, 10, nil)

	result, err := agent.RunWithOptions(context.Background(), "budget", harness.RunOptions{MaxTotalTokens: 150})
	if !errors.Is(err, harness.ErrBudget) {
		t.Fatalf("error = %v, want ErrBudget", err)
	}
	if result.StopReason != harness.StopBudget {
		t.Fatalf("stop reason = %q, want budget", result.StopReason)
	}
	if result.Usage.TotalTokens != 200 {
		t.Fatalf("usage = %d, want 200", result.Usage.TotalTokens)
	}
	if modelCalls != 2 {
		t.Fatalf("model calls = %d, want 2", modelCalls)
	}
}

// TestRunWithOptionsReportsMaxStepsStop 验证步数耗尽时返回哨兵错误并携带 max_steps 停止原因。
func TestRunWithOptionsReportsMaxStepsStop(t *testing.T) {
	registry, err := harness.NewRegistry(staticTool{name: "repeat", output: "again"})
	if err != nil {
		t.Fatal(err)
	}
	agent := harness.NewAgent(repeatingModel{}, registry, 2, nil)

	result, err := agent.RunWithOptions(context.Background(), "loop", harness.RunOptions{})
	if !errors.Is(err, harness.ErrMaxSteps) {
		t.Fatalf("error = %v, want ErrMaxSteps", err)
	}
	if result.StopReason != harness.StopMaxSteps {
		t.Fatalf("stop reason = %q, want max_steps", result.StopReason)
	}
}

// fakeProjector 返回固定投影并记录调用，验证 Agent 与 ContextManager 的接线。
type fakeProjector struct {
	calls    int
	messages []harness.Message
}

// Project 返回只含最后一条消息的压缩投影，并记录传入的完整历史。
// 保留最后一条使模型能看到最近的工具结果，模拟真实压缩策略的保留窗口语义。
func (f *fakeProjector) Project(_ context.Context, messages []harness.Message) harness.Projection {
	f.calls++
	f.messages = messages
	return harness.Projection{
		Messages:  []harness.Message{messages[len(messages)-1]},
		Tokens:    42,
		Epoch:     2,
		Compacted: true,
		Kind:      "sliding",
	}
}

// TestAgentUsesProjectionAndEmitsContextEvents 验证模型收到投影、完整历史不被修改、
// context.built 与 context.compacted 事件携带正确字段。
func TestAgentUsesProjectionAndEmitsContextEvents(t *testing.T) {
	registry, err := harness.NewRegistry(staticTool{name: "repeat", output: "again"})
	if err != nil {
		t.Fatal(err)
	}
	var received []harness.Message
	projector := &fakeProjector{}
	loopModel := modelFunc(func(
		_ context.Context,
		messages []harness.Message,
		_ []harness.ToolSpec,
		_ harness.ModelEventSink,
	) (harness.ModelResponse, error) {
		received = append(received, messages...)
		if len(messages) > 0 && messages[len(messages)-1].Role == "tool" {
			return harness.ModelResponse{Content: "done", FinishReason: "stop"}, nil
		}
		return harness.ModelResponse{
			ToolCalls:    []harness.ToolCall{{ID: "c1", Name: "repeat", Arguments: json.RawMessage(`{}`)}},
			FinishReason: "tool_calls",
		}, nil
	})
	var events []harness.Event
	agent := harness.NewAgent(loopModel, registry, 4, harness.EventSinkFunc(func(event harness.Event) {
		events = append(events, event)
	}))
	agent.SetContextManager(projector)

	answer, err := agent.Run(context.Background(), "task")
	if err != nil || answer != "done" {
		t.Fatalf("answer = %q err = %v", answer, err)
	}
	if projector.calls != 2 {
		t.Fatalf("projector calls = %d, want 2", projector.calls)
	}
	// 两次调用各收到一条投影消息：第一次是任务，第二次是压缩窗口保留的工具结果。
	if len(received) != 2 || received[0].Role != "user" || received[1].Role != "tool" {
		t.Fatalf("model received = %#v", received)
	}
	// 传给投影的完整历史包含 3 条消息（user、assistant、tool），且未被修改。
	if len(projector.messages) != 3 {
		t.Fatalf("full history length = %d, want 3", len(projector.messages))
	}
	var built, compacted bool
	for _, event := range events {
		switch event.Kind {
		case harness.ContextBuilt:
			built = event.ContextTokens == 42 && event.ContextEpoch == 2
		case harness.ContextCompacted:
			compacted = event.Data == "sliding"
		}
	}
	if !built || !compacted {
		t.Fatalf("context events: built=%v compacted=%v", built, compacted)
	}
}
