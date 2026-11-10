package harness

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
)

type runtimeProgressKey struct{}

// This is observed tool activity, not a correctness verdict or a Git diff.
// Shell edits are deliberately not inferred from command strings.
type runtimeProgress struct {
	ProductionEdits int
	TestEdits       int
	CallsSinceEdit  int
	// ExecOKAfterEdit counts successful commands (exit code 0) observed after
	// the last production edit. A new production edit resets it: earlier green
	// runs did not see the newest change.
	ExecOKAfterEdit int
}

// WithRunProgress 把一个 run 级共享进度账本放入 ctx，返回新的 ctx。
// 主智能体与全部子智能体必须读写同一本账：探索门禁与运行时提示据此判定
// "这个 run 是否有人改过生产代码"，而不是"某个智能体自己改没改"——否则
// 晚期出生的子智能体会因自己的空白账本在出生瞬间被门禁收走工具。
// CLI 在启动时调用一次；未注入时 Agent.Run 退回私有账本（单智能体等价）。
func WithRunProgress(ctx context.Context) context.Context {
	if _, ok := ctx.Value(runtimeProgressKey{}).(*runtimeProgress); ok {
		return ctx
	}
	return context.WithValue(ctx, runtimeProgressKey{}, &runtimeProgress{})
}

// runtimeProgressFromContext 返回 ctx 中的共享账本；不存在时返回新账本。
func runtimeProgressFromContext(ctx context.Context) *runtimeProgress {
	if progress, ok := ctx.Value(runtimeProgressKey{}).(*runtimeProgress); ok {
		return progress
	}
	return &runtimeProgress{}
}

func (p *runtimeProgress) observe(call ToolCall, result ToolResult, err error) {
	p.CallsSinceEdit++
	if err != nil {
		return
	}
	if call.Name == "exec" {
		if result.ExitCode == 0 && p.ProductionEdits > 0 {
			p.ExecOKAfterEdit++
		}
		return
	}
	if call.Name != "apply_patch" {
		return
	}
	var args struct {
		Path string `json:"path"`
	}
	if json.Unmarshal(call.Arguments, &args) != nil || args.Path == "" {
		return
	}
	name := strings.ToLower(filepath.ToSlash(args.Path))
	base := filepath.Base(name)
	test := strings.HasPrefix(name, "tests/") || strings.HasPrefix(name, "test/") || strings.Contains(name, "/tests/") || strings.Contains(name, "/test/") || strings.HasSuffix(name, "_test.go") || strings.HasPrefix(base, "test_") || strings.Contains(base, ".test.") || strings.Contains(base, ".spec.") || base == "conftest.py"
	if test {
		p.TestEdits++
		return
	}
	if strings.HasPrefix(name, "docs/") || strings.HasSuffix(name, ".md") || strings.HasSuffix(name, ".rst") || strings.HasSuffix(name, ".txt") {
		return
	}
	p.ProductionEdits++
	p.CallsSinceEdit = 0
	p.ExecOKAfterEdit = 0
}
