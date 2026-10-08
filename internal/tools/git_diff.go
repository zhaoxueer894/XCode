package tools

// 本文件实现只读的 git_diff 工具，为写入结果提供可核对的差异证据。

import (
	"context"       // 在启动 git 前响应取消。
	"encoding/json" // 接收模型提供的 JSON 参数。
	"fmt"           // 构造参数与 git 运行错误。
	"strings"       // 校验路径参数并拼接资源名。
	"time"          // 为 git 命令设置固定超时。

	"xcode/internal/harness" // 返回 ToolSpec 与 ToolResult。
)

const gitDiffTimeout = 30 * time.Second // git diff 属于本地只读操作，固定 30 秒超时。

// GitDiff 在工作区根目录运行 git diff，把差异作为虚拟化结果返回。
// 它只读取仓库状态、参数完全固定，因此不声明执行意图，策略上等同只读工具。
type GitDiff struct {
	runtime *readOnlyRuntime // 保存共享 Workspace 和结果控制器。
}

// gitDiffArgs 保存 git_diff 的可选参数。
type gitDiffArgs struct {
	Staged bool   `json:"staged,omitempty"` // true 表示查看已暂存差异（git diff --cached）。
	Path   string `json:"path,omitempty"`   // 限定差异范围的工作区相对路径；空值表示整个仓库。
}

// Spec 返回 git_diff 的名称、用途和参数约束。
func (t *GitDiff) Spec() harness.ToolSpec {
	return harness.ToolSpec{
		Name:        "git_diff",
		Description: "Show unstaged (or staged) git changes of the workspace as evidence for edits",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"staged": map[string]any{"type": "boolean"},
				"path":   map[string]any{"type": "string"},
			},
			"additionalProperties": false,
		},
	}
}

// Execute 以固定参数数组运行 git diff，并把差异输出虚拟化返回。
func (t *GitDiff) Execute(ctx context.Context, raw json.RawMessage) (harness.ToolResult, error) {
	if err := ctx.Err(); err != nil {
		return harness.ToolResult{}, err
	}
	var args gitDiffArgs
	if err := decodeArguments(raw, &args); err != nil {
		return harness.ToolResult{}, err
	}

	// 参数数组完全由本工具构造，模型只能影响 staged 开关和经过边界检查的路径。
	argv := []string{"git", "diff", "--no-color", "--no-ext-diff", "--no-textconv"}
	if args.Staged {
		argv = append(argv, "--cached")
	}
	scope := "."
	if strings.TrimSpace(args.Path) != "" {
		_, relative, err := t.runtime.workspace.ResolveForWrite(args.Path)
		if err != nil {
			return harness.ToolResult{}, err
		}
		if t.runtime.workspace.IsHardExcluded(relative) {
			return harness.ToolResult{}, fmt.Errorf("path %q is excluded from model tool access", args.Path)
		}
		scope = relative
	}

	// 固定排除内部状态和密钥，裸 git diff 同样不允许显示这些内容。
	// pathspec 通配规则需要 Git 解释，因此仅用户路径使用 literal，内部排除单独显式 magic。
	argv = append(argv, "--", ":(literal)"+scope, ":(exclude,icase).git", ":(exclude,icase).eval", ":(exclude,icase).xcode-state", ":(exclude,glob,icase)**/.env", ":(exclude,glob,icase)**/.env.*")
	outcome := runHostCommand(ctx, t.runtime.workspace.Root(), argv, gitDiffTimeout)
	if outcome.startErr != nil {
		return harness.ToolResult{}, fmt.Errorf("start git: %w", outcome.startErr)
	}
	// git 不在仓库中运行等失败通过退出码与 stderr 反馈给模型。
	if outcome.exitCode != 0 {
		return harness.ToolResult{}, fmt.Errorf(
			"git diff failed with exit code %d: %s",
			outcome.exitCode, compactSearchLine(outcome.stderr),
		)
	}
	body := outcome.stdout
	if strings.TrimSpace(body) == "" {
		body = "No changes."
	}
	content := []byte(fmt.Sprintf(
		"git_diff staged=%t scope=%s bytes=%d truncated=%t\n%s",
		args.Staged, scope, outcome.stdoutBytes, outcome.stdoutTruncated, body,
	))
	resource := fmt.Sprintf("git_diff:%s:staged=%t", scope, args.Staged)
	return t.runtime.results.build(ctx, resource, "", "", content)
}
