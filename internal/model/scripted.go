package model

// 本文件实现脚本化 Demo 模型：按评测目录声明的步骤逐步重放工具调用，
// 让离线评测可以零成本、完全确定地走完读取、修改与执行测试的完整闭环。

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"xcode/internal/harness"
)

// ScriptStep 保存脚本化 Demo 要重放的一个工具调用。
type ScriptStep struct {
	Tool      string // 保存本步骤要调用的工具名称，例如 read_file 或 apply_patch。
	Arguments string // 保存本步骤的完整 JSON 参数字符串；内容由评测目录原样提供。
}

// ScriptedDemo 按固定脚本重放工具调用；脚本耗尽后输出总结答案。
// 进度保存在实例字段而不是统计消息历史：模型输入是可能被压缩改写的投影，
// 测试驾驶员的剧本进度必须与投影内容解耦，否则压缩会导致脚本错误重放。
type ScriptedDemo struct {
	steps []ScriptStep // 保存待重放的全部步骤，顺序即执行顺序。
	next  int          // 保存下一个待执行步骤的下标；每次发出工具调用后递增。
}

// NewScriptedDemo 创建一个脚本化 Demo 模型；steps 为空时模型直接给出空脚本总结。
func NewScriptedDemo(steps []ScriptStep) *ScriptedDemo {
	return &ScriptedDemo{steps: steps}
}

// Advance 把脚本进度向前跳过 n 个步骤；故障评测的 phase2 用它对齐
// phase1 已经完成的工具调用，避免恢复续跑时重放已执行过的步骤。
func (m *ScriptedDemo) Advance(n int) {
	if n <= 0 {
		return
	}
	m.next += n
	if m.next > len(m.steps) {
		m.next = len(m.steps)
	}
}

// Generate 完成一次"模型调用"：统计历史里已有的 tool 消息数确定进度，
// 未耗尽脚本时发出下一步工具调用，耗尽后返回引用最后工具结果的总结答案。
func (m *ScriptedDemo) Generate(
	_ context.Context,
	messages []harness.Message,
	_ []harness.ToolSpec,
	sink harness.ModelEventSink,
) (harness.ModelResponse, error) {
	// 与 Demo 模型一致：nil 表示调用方不需要流式事件。
	emit := func(event harness.ModelEvent) {
		if sink != nil {
			sink.EmitModelEvent(event)
		}
	}

	// 剧本进度自持：next 字段不受投影裁剪影响（Agent 对模型的调用是串行的，无并发写）。
	if m.next < len(m.steps) {
		// 还有步骤未执行：取出当前步骤并发出对应的工具调用增量。
		step := m.steps[m.next]
		callID := fmt.Sprintf("call-%d", m.next+1)
		emit(harness.ModelEvent{
			Kind:           harness.ModelToolCallDelta,
			ToolCallIndex:  0,
			ToolCallID:     callID,
			ToolName:       step.Tool,
			ArgumentsDelta: step.Arguments,
		})
		emit(harness.ModelEvent{Kind: harness.ModelFinished, FinishReason: "tool_calls"})
		m.next++
		return harness.ModelResponse{
			ToolCalls: []harness.ToolCall{{
				ID:        callID,
				Name:      step.Tool,
				Arguments: json.RawMessage(step.Arguments),
			}},
			FinishReason: "tool_calls",
		}, nil
	}

	// 脚本已耗尽：给出总结答案，并摘录最后一个工具结果作为证据。
	content := fmt.Sprintf("已按脚本完成 %d 个工具步骤。", len(m.steps))
	for index := len(messages) - 1; index >= 0; index-- {
		if messages[index].Role == "tool" {
			content += "\n最后一个工具结果摘要:\n" + compactScriptText(messages[index].Content, 600)
			break
		}
	}
	emit(harness.ModelEvent{Kind: harness.ModelTextDelta, TextDelta: content})
	emit(harness.ModelEvent{Kind: harness.ModelFinished, FinishReason: "stop"})
	return harness.ModelResponse{Content: content, FinishReason: "stop"}, nil
}

// compactScriptText 把工具结果压缩成最多 maxRunes 个字符的单行摘要，避免答案过长。
// 起脚本答案的摘要长度提高到 600：read_file 结果的元数据头约 280 字符，200 会吞掉正文。
func compactScriptText(value string, maxRunes int) string {
	singleLine := strings.Join(strings.Fields(value), " ")
	runes := []rune(singleLine)
	if len(runes) <= maxRunes {
		return singleLine
	}
	return string(runes[:maxRunes]) + "…"
}
