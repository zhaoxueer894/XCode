package model

// 放 不同模型实现，此阶段先只实现离线demo

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"xcode/internal/harness"
)

type Demo struct{}

// 模型核心逻辑：Generate
func (Demo) Generate(_ context.Context, messages []harness.Message,
	_ []harness.ToolSpec, sink harness.ModelEventSink) (harness.ModelResponse, error) {
	// 把模拟增量发送给 Agent；nil 表示调用方不需要流式事件。
	emit := func(event harness.ModelEvent) {
		if sink != nil {
			sink.EmitModelEvent(event)
		}
	}

	// 取最后一条message，即为最后追加的
	last := messages[len(messages)-1]
	if last.Role == "tool" {
		content := "工具返回:\n" + last.Content
		emit(harness.ModelEvent{Kind: harness.ModelTextDelta, TextDelta: content})
		emit(harness.ModelEvent{Kind: harness.ModelFinished, FinishReason: "stop"})
		return harness.ModelResponse{Content: content, FinishReason: "stop"}, nil
	}

	// 第一次收到用户消息，把最后一个空白分隔字段当成待读取路径
	fields := strings.Fields(last.Content)
	if len(fields) == 0 {
		return harness.ModelResponse{}, fmt.Errorf("empty prompt")
	}
	path := fields[len(fields)-1]
	// json序列化
	args, _ := json.Marshal(map[string]string{"path": path})
	emit(harness.ModelEvent{
		Kind:           harness.ModelToolCallDelta,
		ToolCallIndex:  0,
		ToolCallID:     "call-1",
		ToolName:       "read_file",
		ArgumentsDelta: string(args),
	})
	emit(harness.ModelEvent{Kind: harness.ModelFinished, FinishReason: "tool_calls"})
	return harness.ModelResponse{ToolCalls: []harness.ToolCall{{
		ID:        "call-1",
		Name:      "read_file",
		Arguments: args,
	}}, FinishReason: "tool_calls"}, nil
}
