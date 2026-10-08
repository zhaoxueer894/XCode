package policy_test

// 本文件验证规则加载校验、匹配语义与三段优先级判定链。

import (
	"os"
	"path/filepath"
	"testing"

	"xcode/internal/policy"
)

// writeRules 把规则 JSON 写入临时文件并返回路径。
func writeRules(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "policy.json")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestLoadRulesFileValidates 验证规则文件的校验规则与缺省行为。
func TestLoadRulesFileValidates(t *testing.T) {
	missing, err := policy.LoadRulesFile(filepath.Join(t.TempDir(), "absent.json"))
	if err != nil || missing != nil {
		t.Fatalf("missing file = %#v, %v; want nil, nil", missing, err)
	}

	cases := []struct {
		name      string
		content   string
		wantError string
	}{
		{"bad decision", `{"rules":[{"id":"a","tool":"exec","decision":"maybe"}]}`, "invalid decision"},
		{"duplicate id", `{"rules":[{"id":"a","tool":"exec","decision":"allow"},{"id":"a","tool":"exec","decision":"deny"}]}`, "duplicate"},
		{"no matcher", `{"rules":[{"id":"a","decision":"deny"}]}`, "requires tool"},
		{"empty command element", `{"rules":[{"id":"a","command_prefix":["git",""],"decision":"allow"}]}`, "empty element"},
		{"unknown field", `{"rules":[{"id":"a","tool":"exec","decision":"allow","bogus":1}]}`, "decode"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			_, err := policy.LoadRulesFile(writeRules(t, testCase.content))
			if err == nil || !contains(err.Error(), testCase.wantError) {
				t.Fatalf("error = %v, want substring %q", err, testCase.wantError)
			}
		})
	}
}

// contains 是避免引入 strings 依赖的最小子串判断。
func contains(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}

// TestRuleMatching 验证命令前缀逐元素精确匹配、路径前缀与首条命中顺序。
func TestRuleMatching(t *testing.T) {
	ruleSet, err := policy.LoadRulesFile(writeRules(t, `{
		"rules": [
			{"id":"git","tool":"exec","command_prefix":["git","status"],"decision":"allow"},
			{"id":"internal","tool":"apply_patch","path_prefix":"internal","decision":"ask"},
			{"id":"any-exec","tool":"exec","decision":"ask"}
		]
	}`))
	if err != nil {
		t.Fatal(err)
	}

	git := ruleSet.Match(policy.Request{Tool: "exec", Command: []string{"git", "status", "--short"}})
	if git == nil || git.ID != "git" {
		t.Fatalf("git match = %#v", git)
	}
	// 前缀逐元素比较：git push 不命中 git status 规则，落入 any-exec。
	other := ruleSet.Match(policy.Request{Tool: "exec", Command: []string{"git", "push"}})
	if other == nil || other.ID != "any-exec" {
		t.Fatalf("git push should fall to any-exec, got %#v", other)
	}
	// 路径前缀匹配：internal/ 下的写入命中，根文件不命中。
	inside := ruleSet.Match(policy.Request{Tool: "apply_patch", Paths: []string{"internal/harness/agent.go"}})
	if inside == nil || inside.ID != "internal" {
		t.Fatalf("internal path match = %#v", inside)
	}
	if outside := ruleSet.Match(policy.Request{Tool: "apply_patch", Paths: []string{"README.md"}}); outside != nil {
		t.Fatalf("README.md should not match internal rule, got %#v", outside)
	}
	// 工具不匹配。
	if miss := ruleSet.Match(policy.Request{Tool: "read_file", Paths: []string{"internal/x.go"}}); miss != nil {
		t.Fatalf("read_file should not match apply_patch rule, got %#v", miss)
	}

	// 裸名首元素按程序基名匹配：exec 意图中的命令是 PATH 解析后的绝对路径。
	gitPath := filepath.Join("D:", "software", "Git", "cmd", "git.exe")
	resolved := ruleSet.Match(policy.Request{Tool: "exec", Command: []string{gitPath, "status"}})
	if resolved == nil || resolved.ID != "git" {
		t.Fatalf("resolved path should match bare-name rule, got %#v", resolved)
	}
	// 基名不同则不命中。
	pingPath := filepath.Join("D:", "tools", "ping.exe")
	pingCommand := []string{pingPath, "status"}
	if miss := ruleSet.Match(policy.Request{Tool: "exec", Command: pingCommand}); miss == nil || miss.ID != "any-exec" {
		t.Fatalf("different base name should fall through, got %#v", miss)
	}
}

// TestDecidePrecedence 验证三段优先级：plan 硬安全、用户规则、档位默认。
func TestDecidePrecedence(t *testing.T) {
	ruleSet, err := policy.LoadRulesFile(writeRules(t, `{
		"rules": [
			{"id":"allow-git-status","tool":"exec","command_prefix":["git","status"],"decision":"allow"},
			{"id":"deny-rm","tool":"exec","command_prefix":["rm"],"decision":"deny"}
		]
	}`))
	if err != nil {
		t.Fatal(err)
	}
	newEngine := func(t *testing.T, mode policy.Mode) *policy.Engine {
		t.Helper()
		engine, err := policy.NewEngine(mode)
		if err != nil {
			t.Fatal(err)
		}
		engine.SetRules(ruleSet)
		return engine
	}
	gitStatus := policy.Request{Tool: "exec", Command: []string{"git", "status"}}
	rm := policy.Request{Tool: "exec", Command: []string{"rm", "-rf", "x"}}
	push := policy.Request{Tool: "exec", Command: []string{"git", "push"}}

	// 用户 allow 规则免除 workspace-write 的默认询问。
	if got := newEngine(t, policy.ModeWorkspaceWrite).Decide(gitStatus); got != policy.Allow {
		t.Fatalf("rule allow = %v", got)
	}
	// 未命中规则回落档位默认 ask。
	if got := newEngine(t, policy.ModeWorkspaceWrite).Decide(push); got != policy.Ask {
		t.Fatalf("mode default = %v", got)
	}
	// 用户 deny 规则在 full-access 下依然生效（规则先于档位）。
	if got := newEngine(t, policy.ModeFullAccess).Decide(rm); got != policy.Deny {
		t.Fatalf("full-access deny rule = %v", got)
	}
	// plan 硬安全压过用户 allow 规则。
	if got := newEngine(t, policy.ModePlan).Decide(gitStatus); got != policy.Deny {
		t.Fatalf("plan override = %v", got)
	}
}
