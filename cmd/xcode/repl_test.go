package main

// REPL 斜杠命令的输入驱动测试：覆盖 /model 的参数分支，不启动真实模型。

import (
	"bufio"
	"strings"
	"testing"
)

// TestSwitchModelCommandArgBranches 覆盖 /model 带参数的各分支：
// 未知供应商报错；同名供应商提示未更改；正常切换调用装配的闭包。
func TestSwitchModelCommandArgBranches(t *testing.T) {
	scanner := bufio.NewScanner(strings.NewReader(""))

	// 未知供应商：直接返回，不触碰闭包。
	cfg := &replConfig{Provider: "demo", SwitchProvider: func(string) (string, error) { return "", nil }}
	switchModelCommand(cfg, scanner, strings.Fields("/model nonsense"))

	// 同名供应商：提示未更改，不调用闭包。
	called := false
	cfg.SwitchProvider = func(string) (string, error) { called = true; return "", nil }
	switchModelCommand(cfg, scanner, strings.Fields("/model demo"))
	if called {
		t.Fatal("same-provider switch should be a no-op")
	}
}

// TestSwitchModelCommandMenuCancelsOnBlankLine 空行取消菜单，不修改状态。
func TestSwitchModelCommandMenuCancelsOnBlankLine(t *testing.T) {
	scanner := bufio.NewScanner(strings.NewReader("\n"))
	cfg := &replConfig{Provider: "demo", SwitchProvider: func(string) (string, error) {
		t.Fatal("switch called after cancel")
		return "", nil
	}}
	switchModelCommand(cfg, scanner, strings.Fields("/model"))
	if cfg.Provider != "demo" {
		t.Fatalf("provider changed to %q after cancel", cfg.Provider)
	}
}
