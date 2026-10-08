package main

// 契约文件加载测试：覆盖合法契约与三类结构性缺失。

import (
	"os"            // 写临时契约文件。
	"path/filepath" // 拼接临时路径。
	"testing"       // Go 标准测试框架。
)

// TestLoadContractValidation 覆盖契约文件的加载与校验。
func TestLoadContractValidation(t *testing.T) {
	dir := t.TempDir()
	write := func(name, content string) string {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}

	valid := write("valid.json", `{
		"goal": "修复 add",
		"acceptance": ["Add(2,3) 返回 5"],
		"verify_commands": [{"name": "go test", "command": ["go", "test", "./..."]}],
		"review": true
	}`)
	contract, err := loadContract(valid)
	if err != nil {
		t.Fatalf("valid contract rejected: %v", err)
	}
	if contract.Goal != "修复 add" || len(contract.VerifyCommands) != 1 || !contract.Review {
		t.Fatalf("decoded contract = %+v", contract)
	}
	if contract.VerifyCommands[0].Command[0] != "go" {
		t.Fatalf("verify command = %+v", contract.VerifyCommands[0])
	}

	if _, err := loadContract(write("no_goal.json", `{"verify_commands":[{"name":"t","command":["go"]}]}`)); err == nil {
		t.Fatal("contract without goal must be rejected")
	}
	if _, err := loadContract(write("no_commands.json", `{"goal":"x"}`)); err == nil {
		t.Fatal("contract without verify commands must be rejected")
	}
	if _, err := loadContract(write("empty_command.json", `{"goal":"x","verify_commands":[{"name":"t","command":[]}]}`)); err == nil {
		t.Fatal("contract with empty command array must be rejected")
	}
	if _, err := loadContract(filepath.Join(dir, "absent.json")); err == nil {
		t.Fatal("missing contract file must be rejected")
	}
}
