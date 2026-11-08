package harness

import (
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
