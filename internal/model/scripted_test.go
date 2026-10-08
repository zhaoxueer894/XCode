package model_test

// 本文件验证脚本化 Demo 模型按步骤重放工具调用，并在脚本耗尽后输出引用工具结果的总结。

import (
	"context"
	"strings"
	"testing"

	"xcode/internal/harness"
	"xcode/internal/model"
)

// TestScriptedDemoReplaysStepsThenSummarizes 验证脚本按 tool 消息数量推进，耗尽后给出总结答案。
func TestScriptedDemoReplaysStepsThenSummarizes(t *testing.T) {
	demo := model.NewScriptedDemo([]model.ScriptStep{
		{Tool: "read_file", Arguments: `{"path":"calc.go"}`},
		{Tool: "exec", Arguments: `{"command":["go","test","./..."]}`},
	})

	first, err := demo.Generate(
		context.Background(),
		[]harness.Message{{Role: "user", Content: "fix"}},
		nil, nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.ToolCalls) != 1 || first.ToolCalls[0].Name != "read_file" ||
		first.ToolCalls[0].ID != "call-1" || string(first.ToolCalls[0].Arguments) != `{"path":"calc.go"}` ||
		first.FinishReason != "tool_calls" {
		t.Fatalf("first response = %#v", first)
	}

	afterFirst := []harness.Message{
		{Role: "user", Content: "fix"},
		{Role: "assistant", ToolCalls: first.ToolCalls},
		{Role: "tool", Content: "package calc", ToolCallID: "call-1"},
	}
	second, err := demo.Generate(context.Background(), afterFirst, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(second.ToolCalls) != 1 || second.ToolCalls[0].Name != "exec" || second.ToolCalls[0].ID != "call-2" {
		t.Fatalf("second response = %#v", second)
	}

	afterSecond := append(append([]harness.Message{}, afterFirst...),
		harness.Message{Role: "assistant", ToolCalls: second.ToolCalls},
		harness.Message{Role: "tool", Content: "all tests passed", ToolCallID: "call-2"},
	)
	final, err := demo.Generate(context.Background(), afterSecond, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(final.ToolCalls) != 0 || final.FinishReason != "stop" {
		t.Fatalf("final response = %#v", final)
	}
	if !strings.Contains(final.Content, "已按脚本完成 2 个工具步骤") ||
		!strings.Contains(final.Content, "all tests passed") {
		t.Fatalf("final content = %q", final.Content)
	}
}

// TestScriptedDemoEmptyScriptSummarizesImmediately 验证空脚本直接给出零步骤总结。
func TestScriptedDemoEmptyScriptSummarizesImmediately(t *testing.T) {
	demo := model.NewScriptedDemo(nil)
	response, err := demo.Generate(
		context.Background(),
		[]harness.Message{{Role: "user", Content: "nothing"}},
		nil, nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(response.ToolCalls) != 0 || !strings.Contains(response.Content, "0 个工具步骤") {
		t.Fatalf("response = %#v", response)
	}
}
