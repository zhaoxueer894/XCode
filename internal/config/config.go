package config

// 本文件实现 XCode 的配置文件加载：项目级 .xcode/config 和全局 ~/.xcode/config，
// 使用简单 key=value 格式（与 .env 相同解析模式），优先级为
// CLI flag > 项目配置 > 全局配置 > 内置默认值。

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Config 保存从配置文件加载的设置；零值字段由调用方用 flag 默认值兜底。
type Config struct {
	Provider        string        // 保存模型提供方：demo 或 bailian。
	Mode            string        // 保存能力档位：plan、workspace-write 或 full-access。
	ContextStrategy string        // 保存上下文策略：none、sliding、handoff 或 compaction。
	ContextBudget   int           // 保存上下文预算（估算 token）。
	MaxSteps        int           // 保存最大模型迭代数。
	MaxTokens       int           // 保存 Token 预算（0 = 不限制）。
	Sandbox         string        // 保存沙箱后端：host、docker 或 docker-wsl。
	SandboxImage    string        // 保存 Docker 沙箱镜像名。
	Timeout         time.Duration // 保存整轮超时时长。

	// 类型化 shadow 分类器的独立模型配置（与主模型解耦）。
	TypedClassifierBackend   string // 保存分类后端：local（聊天模型）或 jev（TypeSafe systemone）；空值视为 local。
	TypedClassifierEndpoint  string // 保存 local 分类器的 OpenAI 兼容端点；空值表示复用主模型。
	TypedClassifierModel     string // 保存 local 分类器的模型名；空值表示复用主模型。
	TypedClassifierKeyEnvVar string // 保存 local 分类器 Key 的环境变量名；空值回退 DASHSCOPE_API_KEY。
}

// Defaults 返回内置默认值，作为最低优先级。
func Defaults() Config {
	return Config{
		Provider:        "demo",
		Mode:            "workspace-write",
		ContextStrategy: "none",
		ContextBudget:   12000,
		MaxSteps:        8,
		MaxTokens:       0,
		Sandbox:         "host",
		SandboxImage:    "alpine:3.20",
		Timeout:         2 * time.Minute,
	}
}

// Load 读取全局和项目配置并合并（项目覆盖全局），返回合并后的配置。
// 任何配置文件不存在都不视为错误（配置是可选的）。
func Load(projectRoot string) (Config, error) {
	merged := Defaults()

	// 全局配置：~/.xcode/config
	home, err := os.UserHomeDir()
	if err == nil {
		if err := mergeFile(&merged, filepath.Join(home, ".xcode", "config")); err != nil {
			return merged, fmt.Errorf("global config: %w", err)
		}
	}

	// 项目配置：<root>/.xcode/config（覆盖全局）
	if err := mergeFile(&merged, filepath.Join(projectRoot, ".xcode", "config")); err != nil {
		return merged, fmt.Errorf("project config: %w", err)
	}

	return merged, nil
}

// mergeFile 把一个 key=value 文件合并到已有配置；文件不存在静默跳过。
func mergeFile(cfg *Config, path string) error {
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("open %s: %w", path, err)
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for lineNumber := 1; scanner.Scan(); lineNumber++ {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			return fmt.Errorf("%s:%d: expected key=value", path, lineNumber)
		}
		applyValue(cfg, strings.TrimSpace(key), strings.TrimSpace(value))
	}
	return scanner.Err()
}

// applyValue 把一个键值对写入配置结构；未知键静默忽略（向后兼容）。
func applyValue(cfg *Config, key, value string) {
	switch strings.ToLower(key) {
	case "provider":
		cfg.Provider = value
	case "mode":
		cfg.Mode = value
	case "context-strategy", "context_strategy":
		cfg.ContextStrategy = value
	case "context-budget", "context_budget":
		if n, err := strconv.Atoi(value); err == nil {
			cfg.ContextBudget = n
		}
	case "max-steps", "max_steps":
		if n, err := strconv.Atoi(value); err == nil {
			cfg.MaxSteps = n
		}
	case "max-tokens", "max_tokens":
		if n, err := strconv.Atoi(value); err == nil {
			cfg.MaxTokens = n
		}
	case "sandbox":
		cfg.Sandbox = value
	case "sandbox-image", "sandbox_image":
		cfg.SandboxImage = value
	case "timeout":
		if d, err := time.ParseDuration(value); err == nil {
			cfg.Timeout = d
		}
	case "typed-classifier":
		cfg.TypedClassifierBackend = value
	case "typed-classifier-endpoint":
		cfg.TypedClassifierEndpoint = value
	case "typed-classifier-model":
		cfg.TypedClassifierModel = value
	case "typed-classifier-api-key-env":
		cfg.TypedClassifierKeyEnvVar = value
	}
}
