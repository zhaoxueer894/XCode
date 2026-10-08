package contextmgr

// 本文件验证投影管理器：估算、纪元推进、预算内不压缩、三种压缩策略的行为。

import (
	"context"
	"strings"
	"testing"

	"xcode/internal/harness"
)

// makeHistory 构造一条超过预算的长历史：首条任务 + 若干工具消息 + 最近消息。
func makeHistory(count int) []harness.Message {
	messages := []harness.Message{{Role: "user", Content: "任务：修复示例仓库里的计算错误并运行测试。"}}
	for i := 0; i < count; i++ {
		messages = append(messages,
			harness.Message{Role: "assistant", ToolCalls: []harness.ToolCall{{ID: "c", Name: "read_file"}}},
			harness.Message{Role: "tool", ToolCallID: "c", Content: strings.Repeat("内容行 ", 120)},
		)
	}
	return messages
}

// TestEstimateTokens 验证估算器的确定性和非 ASCII 权重。
func TestEstimateTokens(t *testing.T) {
	if got := EstimateTokens("abcd"); got != 1 {
		t.Fatalf("abcd tokens = %d, want 1", got)
	}
	if got := EstimateTokens("中"); got != 1 {
		t.Fatalf("single CJK token = %d, want 1", got)
	}
	// 200 个 ASCII 约折 50 token，少于 100 个汉字的 100 token。
	if EstimateTokens(strings.Repeat("x", 200)) >= EstimateTokens(strings.Repeat("中", 100)) {
		t.Fatal("200 ascii should estimate fewer tokens than 100 cjk")
	}
}

// TestEpochAdvances 验证纪元从 1 开始、每次压缩恰好递增一次。
func TestEpochAdvances(t *testing.T) {
	epoch := NewEpoch()
	if epoch.Current() != 1 {
		t.Fatalf("initial epoch = %d, want 1", epoch.Current())
	}
	if epoch.Advance() != 2 || epoch.Current() != 2 {
		t.Fatalf("advance = %d, current = %d, want 2", epoch.Advance(), epoch.Current())
	}
}

// TestProjectWithinBudgetDoesNotCompact 验证预算内或短历史不压缩、不改切片。
func TestProjectWithinBudgetDoesNotCompact(t *testing.T) {
	manager, err := NewManager(StrategySliding, 12000, nil)
	if err != nil {
		t.Fatal(err)
	}
	history := makeHistory(2)
	before := append([]harness.Message{}, history...)
	projection := manager.Project(context.Background(), history)
	if projection.Compacted || projection.Epoch != 1 {
		t.Fatalf("projection = %#v, want un-compacted at epoch 1", projection)
	}
	if &history[0] != &projection.Messages[0] {
		t.Fatal("un-compacted projection should reuse the same slice")
	}
	if len(before) != len(history) {
		t.Fatal("Project must not modify the input slice")
	}
}

// TestSlidingReplacesOldMessages 验证滑动裁剪保留首尾、替换中间并推进纪元。
func TestSlidingReplacesOldMessages(t *testing.T) {
	manager, err := NewManager(StrategySliding, 200, nil)
	if err != nil {
		t.Fatal(err)
	}
	history := makeHistory(6)
	projection := manager.Project(context.Background(), history)
	if !projection.Compacted || projection.Kind != "sliding" || projection.Epoch != 2 {
		t.Fatalf("projection kind=%q epoch=%d compacted=%v", projection.Kind, projection.Epoch, projection.Compacted)
	}
	if projection.Messages[0].Content != history[0].Content {
		t.Fatal("first task message must be preserved")
	}
	if !strings.Contains(projection.Messages[1].Content, placeholderTag) {
		t.Fatalf("middle replacement = %q, want placeholder", projection.Messages[1].Content)
	}
	if len(projection.Messages) != keepRecentMessages+2 {
		t.Fatalf("projected length = %d, want %d", len(projection.Messages), keepRecentMessages+2)
	}
	if projection.Tokens >= EstimateMessagesTokens(history) {
		t.Fatal("projection should estimate fewer tokens than full history")
	}
}

// TestHandoffResetsAndRecordsNote 验证交接重置的摘要结构与 L1 笔记落盘。
func TestHandoffResetsAndRecordsNote(t *testing.T) {
	recorder := &fakeNoteRecorder{}
	manager, err := NewManager(StrategyHandoff, 200, nil)
	if err != nil {
		t.Fatal(err)
	}
	manager.SetNoteRecorder(recorder)
	history := makeHistory(4)
	history[2].Content = "[resource=calc.go#L1-L10 artifact_id=sha256:abc123 content_hash=abc] path=calc.go\nreturn left - right"

	projection := manager.Project(context.Background(), history)
	if !projection.Compacted || projection.Kind != "handoff" || len(projection.Messages) != 1 {
		t.Fatalf("projection = %#v", projection)
	}
	note := projection.Messages[0].Content
	for _, want := range []string{"[handoff]", "工具调用", "calc.go#L1-L10", "artifact=sha256:abc123", "不要重复执行", "直接给出最终答案"} {
		if !strings.Contains(note, want) {
			t.Fatalf("note missing %q:\n%s", want, note)
		}
	}
	if recorder.calls != 1 {
		t.Fatalf("note recorder calls = %d, want 1", recorder.calls)
	}
}

// TestCompactionUsesSummarizer 验证 compaction 优先使用模型摘要，失败时回退占位符。
func TestCompactionUsesSummarizer(t *testing.T) {
	manager, err := NewManager(StrategyCompaction, 200, nil)
	if err != nil {
		t.Fatal(err)
	}
	manager.SetSummarizer(func(_ context.Context, _ string) (string, error) {
		return "目标已明确；calc.go 已从减法修正为加法；go test 通过。", nil
	})
	projection := manager.Project(context.Background(), makeHistory(6))
	if projection.Kind != "compaction" {
		t.Fatalf("kind = %q, want compaction", projection.Kind)
	}
	if !strings.Contains(projection.Messages[1].Content, "calc.go 已从减法修正为加法") {
		t.Fatalf("replacement = %q, want model summary", projection.Messages[1].Content)
	}

	failing, err := NewManager(StrategyCompaction, 200, nil)
	if err != nil {
		t.Fatal(err)
	}
	failing.SetSummarizer(func(_ context.Context, _ string) (string, error) {
		return "", context.DeadlineExceeded
	})
	fallback := failing.Project(context.Background(), makeHistory(6))
	if !strings.Contains(fallback.Messages[1].Content, placeholderTag) {
		t.Fatalf("fallback = %q, want placeholder", fallback.Messages[1].Content)
	}
}

// TestNewManagerRejectsUnknownStrategy 验证非法策略名被拒绝。
func TestNewManagerRejectsUnknownStrategy(t *testing.T) {
	if _, err := NewManager("bogus", 100, nil); err == nil {
		t.Fatal("expected error for unknown strategy")
	}
}

// fakeNoteRecorder 记录 handoff 笔记写入次数的假实现。
type fakeNoteRecorder struct {
	calls int
}

// RecordHandoff 完成一次笔记记录并计数。
func (r *fakeNoteRecorder) RecordHandoff(string) error {
	r.calls++
	return nil
}
