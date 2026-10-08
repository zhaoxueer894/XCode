package tools

// 本文件实现 read_skill 只读工具：按名称读取 .xcode/skills/ 下的技能正文，
// 完整内容进入 Artifact Store，模型只接收受限预览（复用虚拟化管线）。

import (
	"context"       // 在读取前响应取消。
	"encoding/json" // 接收模型提供的 JSON 参数。
	"fmt"           // 构造参数错误。
	"strings"       // 清理名称参数的空白。

	"xcode/internal/harness" // 返回 ToolSpec 与 ToolResult。
	"xcode/internal/memory"  // 技能目录的扫描与读取。
)

// ReadSkill 按名称读取技能正文并虚拟化返回。
type ReadSkill struct {
	runtime *readOnlyRuntime // 保存共享 Workspace 与结果控制器。
}

// Spec 返回 read_skill 的名称、用途和参数约束。
func (t *ReadSkill) Spec() harness.ToolSpec {
	return harness.ToolSpec{
		Name:        "read_skill",
		Description: "Read one skill instructions file from .xcode/skills by its name",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name": map[string]any{"type": "string"},
			},
			"required":             []string{"name"},
			"additionalProperties": false,
		},
	}
}

// Execute 校验名称后读取技能正文，完整结果经虚拟化管线返回。
func (t *ReadSkill) Execute(ctx context.Context, raw json.RawMessage) (harness.ToolResult, error) {
	if err := ctx.Err(); err != nil {
		return harness.ToolResult{}, err
	}
	var args struct {
		Name string `json:"name"` // 保存要读取的技能名称。
	}
	if err := decodeArguments(raw, &args); err != nil {
		return harness.ToolResult{}, err
	}
	if strings.TrimSpace(args.Name) == "" {
		return harness.ToolResult{}, fmt.Errorf("name is required")
	}
	body, err := memory.ReadSkillBody(t.runtime.workspace.Root(), args.Name)
	if err != nil {
		return harness.ToolResult{}, err
	}
	resource := "skill:" + args.Name
	content := []byte("skill=" + args.Name + "\n" + body)
	// readKey 为空：技能正文是静态指引，重复读取由 ReadSet 之外的自然行为决定。
	return t.runtime.results.build(ctx, resource, "", "", content)
}
