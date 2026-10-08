package contextmgr

// 决策门控压缩测试：单元拆分、规则判定、投影组装、shadow 对照与类型化决策器回退。

import (
	"context" // shadow 分类调用的取消信号。
	"errors"  // 构造分类失败。
	"strings" // 断言投影内容。
	"testing" // Go 标准测试框架。

	"xcode/internal/harness" // 消息协议。
)

// msgs 是构造消息历史的便捷函数。
func msgs(messages ...harness.Message) []harness.Message { return messages }

// readPair 构造一次只读工具调用的证据对。
func readPair(id, path, output string) []harness.Message {
	return []harness.Message{
		{Role: "assistant", ToolCalls: []harness.ToolCall{{ID: id, Name: "read_file", Arguments: []byte(`{"path":"` + path + `"}`)}}},
		{Role: "tool", Content: output, ToolCallID: id},
	}
}

// newGateManager 创建 decision-gated 管理器；预算设小值以便测试稳定触发。
func newGateManager() *Manager {
	manager, _ := NewManager(StrategyDecisionGated, 1, NewEpoch())
	return manager
}

// TestSplitUnitsPairsToolCalls 验证 assistant 工具调用与其全部回应构成完整单元。
func TestSplitUnitsPairsToolCalls(t *testing.T) {
	messages := msgs(
		harness.Message{Role: "user", Content: "goal"},
		harness.Message{Role: "assistant", ToolCalls: []harness.ToolCall{
			{ID: "a", Name: "read_file"}, {ID: "b", Name: "list_files"},
		}},
		harness.Message{Role: "tool", ToolCallID: "b", Content: "listing"},
		harness.Message{Role: "tool", ToolCallID: "a", Content: "content"},
		harness.Message{Role: "assistant", Content: "文本"},
	)
	units := splitUnits(messages)
	if len(units) != 3 {
		t.Fatalf("units = %d, want 3 (goal, paired calls, prose)", len(units))
	}
	if len(units[1].Messages) != 3 {
		t.Fatalf("paired unit must keep assistant + 2 tool messages, got %d", len(units[1].Messages))
	}
	if units[1].Messages[1].ToolCallID != "b" || units[1].Messages[2].ToolCallID != "a" {
		t.Fatal("tool responses must stay with their unit regardless of order")
	}
}

// TestRuleDeciderKeepsFixedEvidence 验证固定保留项：目标、最近窗口、错误与验证证据。
func TestRuleDeciderKeepsFixedEvidence(t *testing.T) {
	messages := msgs(
		harness.Message{Role: "user", Content: "goal"},
	)
	// 8 个只读对 + 1 个错误对 + 1 个验证对：错误与验证在窗口外也必须保留。
	for i := 0; i < 8; i++ {
		messages = append(messages, readPair(string(rune('a'+i)), "f.md", "[resource=f.md] ok")...)
	}
	messages = append(messages,
		harness.Message{Role: "assistant", ToolCalls: []harness.ToolCall{{ID: "err", Name: "exec"}}},
		harness.Message{Role: "tool", ToolCallID: "err", Content: "ERROR: old_string not found"},
	)
	messages = append(messages,
		harness.Message{Role: "assistant", ToolCalls: []harness.ToolCall{{ID: "ver", Name: "exec"}}},
		harness.Message{Role: "tool", ToolCallID: "ver", Content: "command=go test exit_code=0"},
	)
	manager := newGateManager()
	projection := manager.Project(context.Background(), messages)
	if !projection.Compacted || projection.Kind != string(StrategyDecisionGated) {
		t.Fatalf("kind = %q, want decision-gated", projection.Kind)
	}
	decisions := manager.LastDecisions()
	if len(decisions) != 11 {
		t.Fatalf("decisions = %d units, want 11", len(decisions))
	}
	if decisions[0].Decision != DecisionKeep {
		t.Fatalf("goal must be kept, got %s", decisions[0].Decision)
	}
	if decisions[9].Decision != DecisionKeep || decisions[10].Decision != DecisionKeep {
		t.Fatalf("error and verification units must be kept: %s / %s", decisions[9].Decision, decisions[10].Decision)
	}
	if decisions[1].Decision != DecisionDrop {
		t.Fatalf("old read-only unit must drop, got %s", decisions[1].Decision)
	}
	// 投影里必须有错误与验证证据，且带决策汇总说明。
	joined := renderGateProjection(projection.Messages)
	if !strings.Contains(joined, "ERROR: old_string not found") {
		t.Fatal("unresolved error must survive projection")
	}
	if !strings.Contains(joined, "exit_code=0") {
		t.Fatal("verification evidence must survive projection")
	}
	if !strings.Contains(joined, "[decision-gated]") {
		t.Fatal("projection must carry the decision summary")
	}
}

// renderGateProjection 拼接投影内容便于断言。
func renderGateProjection(messages []harness.Message) string {
	var builder strings.Builder
	for _, message := range messages {
		builder.WriteString(message.Content)
		for _, call := range message.ToolCalls {
			builder.WriteString(string(call.Arguments))
		}
	}
	return builder.String()
}

// TestDecisionGateDropsAndTruncates 验证 drop 移出单元、truncate 保留结构但压缩内容。
func TestDecisionGateDropsAndTruncates(t *testing.T) {
	long := strings.Repeat("占位文本", 100) // ~400 非重复字符的散文。
	messages := msgs(
		harness.Message{Role: "user", Content: "goal"},
		harness.Message{Role: "assistant", Content: long},
	)
	messages = append(messages, readPair("r1", "old.md", "[resource=old.md] old")...)
	messages = append(messages, readPair("r2", "new.md", "[resource=new.md] new")...)
	messages = append(messages,
		harness.Message{Role: "assistant", Content: "最新结论"},
		harness.Message{Role: "user", Content: "继续"},
		harness.Message{Role: "user", Content: "再继续"},
		harness.Message{Role: "user", Content: "收尾"},
	)
	manager := newGateManager()
	projection := manager.Project(context.Background(), messages)
	joined := renderGateProjection(projection.Messages)
	if strings.Contains(joined, "[resource=old.md] old") {
		t.Fatal("dropped read unit must be absent from projection")
	}
	if !strings.Contains(joined, "最新结论") {
		t.Fatal("recent messages must be kept")
	}
	if len([]rune(long)) <= 400 && strings.Contains(joined, long) {
		t.Fatal("old prose must be truncated")
	}
	if projection.Epoch == 0 {
		t.Fatal("projection must carry a new epoch")
	}
}

// TestTypedShadowRecordsButNeverApplies 验证 shadow 决策只记录不生效，分歧被如实记录。
func TestTypedShadowRecordsButNeverApplies(t *testing.T) {
	messages := msgs(
		harness.Message{Role: "user", Content: "goal"},
	)
	messages = append(messages, readPair("r1", "old.md", "[resource=old.md] old")...)
	messages = append(messages, readPair("r2", "new.md", "[resource=new.md] new")...)
	for i := 0; i < 6; i++ {
		messages = append(messages, harness.Message{Role: "user", Content: "继续"})
	}
	manager := newGateManager()
	// shadow 一律判 keep（与规则判 drop 相反），但投影仍按规则执行。
	manager.SetShadowDecider(stubDecider{decision: DecisionKeep})
	projection := manager.Project(context.Background(), messages)
	joined := renderGateProjection(projection.Messages)
	if strings.Contains(joined, "[resource=old.md] old") {
		t.Fatal("shadow decisions must never be applied")
	}
	records := manager.LastShadow()
	if len(records) == 0 {
		t.Fatal("shadow records must be collected")
	}
	for _, record := range records {
		if record.Shadow != DecisionKeep || record.Applied != DecisionDrop && record.Applied != DecisionKeep {
			t.Fatalf("unexpected shadow record %+v", record)
		}
		if record.Shadow != record.Applied && record.Agree {
			t.Fatal("disagreement must not be marked as agreement")
		}
	}
}

// stubDecider 是测试用的固定判定决策器。
type stubDecider struct{ decision Decision }

// Decide 对全部单元返回同一判定。
func (s stubDecider) Decide(units []Unit) []UnitDecision {
	decisions := make([]UnitDecision, 0, len(units))
	for index := range units {
		decisions = append(decisions, UnitDecision{Index: index, Decision: s.decision, Reason: "stub"})
	}
	return decisions
}

// TestTypedModelDeciderFallsBackOnErrorAndLowConfidence 验证分类失败或低置信度时回退规则判定。
func TestTypedModelDeciderFallsBackOnErrorAndLowConfidence(t *testing.T) {
	units := []Unit{{Messages: msgs(harness.Message{Role: "user", Content: "x"})}}

	failing := NewTypedModelDecider(func(context.Context, []Unit) ([]UnitDecision, error) {
		return nil, errors.New("provider down")
	}, 0.6)
	if decisions := failing.Decide(units); decisions[0].Decision != DecisionKeep {
		t.Fatal("classify error must fall back to rule decision (keep for goal)")
	}
	if !strings.Contains(failing.LastAttempt, "provider down") {
		t.Fatalf("fallback reason = %q", failing.LastAttempt)
	}

	lowConfidence := NewTypedModelDecider(func(context.Context, []Unit) ([]UnitDecision, error) {
		return []UnitDecision{{Index: 0, Decision: DecisionDrop, Reason: "low_confidence"}}, nil
	}, 0.6)
	if decisions := lowConfidence.Decide(units); decisions[0].Decision != DecisionKeep {
		t.Fatal("low confidence must fall back to rule decision")
	}

	healthy := NewTypedModelDecider(func(context.Context, []Unit) ([]UnitDecision, error) {
		return []UnitDecision{{Index: 0, Decision: DecisionDrop, Reason: "sure"}}, nil
	}, 0.6)
	if decisions := healthy.Decide(units); decisions[0].Decision != DecisionDrop {
		t.Fatal("healthy classification must be used")
	}
}
