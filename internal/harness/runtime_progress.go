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

// WithRunProgress stores a run-level shared progress ledger in ctx. The
// orchestrator and every sub-agent must share one ledger: gates and runtime
// hints judge "has anyone edited production code in this run", not "did this
// particular agent edit" — otherwise late-spawned sub-agents are crippled at
// birth by their empty private ledger. Injected once by the CLI; without it
// Agent.Run falls back to a private ledger (single-agent equivalent).
func WithRunProgress(ctx context.Context) context.Context {
	if _, ok := ctx.Value(runtimeProgressKey{}).(*runtimeProgress); ok {
		return ctx
	}
	return context.WithValue(ctx, runtimeProgressKey{}, &runtimeProgress{})
}

// runtimeProgressFromContext returns the shared ledger, or a fresh one.
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
