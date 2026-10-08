package main

// 本文件实现契约文件加载：把用户编写的 JSON 契约解析并校验成
// harness.TaskContract，供完成门禁在模型声明完成时使用。

import (
	"encoding/json" // 解析契约 JSON。
	"fmt"           // 构造校验错误。
	"os"            // 读取契约文件。

	"xcode/internal/harness"   // 复用 TaskContract 与 VerifyCommand 类型。
	"xcode/internal/workspace" // 计算工作区内容指纹。
)

// loadContract 读取并校验契约文件；任何结构性缺失都在启动时报错，而不是运行中途失败。
func loadContract(path string) (*harness.TaskContract, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read contract %s: %w", path, err)
	}
	var contract harness.TaskContract
	if err := json.Unmarshal(data, &contract); err != nil {
		return nil, fmt.Errorf("decode contract %s: %w", path, err)
	}
	if err := validateContract(&contract); err != nil {
		return nil, fmt.Errorf("invalid contract %s: %w", path, err)
	}
	return &contract, nil
}

// validateContract 校验契约的必备字段：目标非空、至少一条验证命令、每条命令是至少一个元素的参数数组。
func validateContract(contract *harness.TaskContract) error {
	if contract == nil {
		return fmt.Errorf("contract is required")
	}
	if len([]rune(contract.Goal)) == 0 {
		return fmt.Errorf("goal is required")
	}
	if len(contract.VerifyCommands) == 0 {
		return fmt.Errorf("at least one verify_command is required")
	}
	for i, verify := range contract.VerifyCommands {
		if len(verify.Command) == 0 {
			return fmt.Errorf("verify_commands[%d] (%q) requires a non-empty command array", i, verify.Name)
		}
	}
	return nil
}

// wireWorkspaceFingerprint 返回供完成门禁使用的工作区指纹函数；根目录在装配期固定。
// 每次门禁评估时重新计算指纹，使验证证据与工作区版本绑定。
func wireWorkspaceFingerprint(root string) func() (string, error) {
	return func() (string, error) {
		return workspace.Fingerprint(root)
	}
}
