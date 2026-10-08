package main

// 本文件实现 xcode policy check 子命令：不执行任何动作，只打印一次判定的完整链路。

import (
	"encoding/json" // 解析调用参数 JSON。
	"flag"          // 子命令参数解析。
	"fmt"           // 输出判定链路。
	"path/filepath" // 组装默认规则路径。
	"strings"       // 清理参数空白。

	"xcode/internal/policy"
)

// runPolicyCheck 实现 policy 子命令；--tool 与 --arguments 必填，输出命中的规则或档位默认。
func runPolicyCheck(args []string) error {
	// 允许 xcode policy check ... 与 xcode policy ... 两种写法，先消费可选的 check 子动词。
	if len(args) > 0 && args[0] == "check" {
		args = args[1:]
	}
	flags := flag.NewFlagSet("xcode policy", flag.ContinueOnError)
	workspaceFlag := flags.String("workspace", ".", "workspace root")
	policyPath := flags.String("policy", "", "policy rules file (default <workspace>/.xcode/policy.json)")
	mode := flags.String("mode", string(policy.ModeWorkspaceWrite), "capability mode: plan, workspace-write or full-access")
	tool := flags.String("tool", "", "tool name to evaluate, e.g. exec or apply_patch")
	arguments := flags.String("arguments", "{}", "tool arguments as JSON")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("xcode policy does not accept positional arguments")
	}
	if strings.TrimSpace(*tool) == "" {
		return fmt.Errorf("--tool is required")
	}

	rulePath := *policyPath
	if rulePath == "" {
		rulePath = filepath.Join(*workspaceFlag, ".xcode", "policy.json")
	}
	ruleSet, err := policy.LoadRulesFile(rulePath)
	if err != nil {
		return err
	}
	engine, err := policy.NewEngine(policy.Mode(*mode))
	if err != nil {
		return err
	}
	engine.SetRules(ruleSet)

	var parsed map[string]any
	if err := json.Unmarshal([]byte(*arguments), &parsed); err != nil {
		return fmt.Errorf("--arguments must be a JSON object: %w", err)
	}
	request := policy.Request{Tool: strings.TrimSpace(*tool)}
	if request.Tool == "exec" {
		if command, ok := parsed["command"].([]any); ok {
			for _, element := range command {
				if text, ok := element.(string); ok {
					request.Command = append(request.Command, text)
				}
			}
		}
	}
	if path, ok := parsed["path"].(string); ok && path != "" {
		request.Paths = []string{path}
		request.Mutating = request.Tool == "apply_patch"
	}

	rule, matched := engine.MatchRule(request)
	decision := engine.Decide(request)
	fmt.Printf("mode=%s\n", engine.Mode())
	if matched {
		fmt.Printf("rule=%s decision=%s\n", rule.ID, rule.Decision)
	} else {
		fmt.Printf("rule=<none> (mode default)\n")
	}
	fmt.Printf("decision=%s\n", decision)
	if matched && decision != rule.Decision {
		fmt.Printf("note: builtin safety overrides rule decision %s\n", rule.Decision)
	}
	if !matched && decision == policy.Ask {
		fmt.Printf("hint: add an allow rule in %s to skip approval for this exact action\n", rulePath)
	}
	return nil
}
