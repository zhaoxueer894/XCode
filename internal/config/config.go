package config

// 本文件实现 XCode 的配置文件加载与写入：项目级 .xcode/config 和全局 ~/.xcode/config，
// 使用简单 key=value 格式。模型供应商的密钥、端点、模型名全部收敛在此文件，
// 优先级为 CLI flag > 项目配置 > 全局配置 > 进程环境变量 > 内置默认值。

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
	Provider        string        // 保存模型提供方：demo、bailian 或 deepseek。
	Mode            string        // 保存能力档位：plan、workspace-write 或 full-access。
	ContextStrategy string        // 保存上下文策略：none、sliding、handoff 或 compaction。
	ContextBudget   int           // 保存上下文预算（估算 token）。
	MaxSteps        int           // 保存最大模型迭代数。
	MaxTokens       int           // 保存 Token 预算（0 = 不限制）。
	Sandbox         string        // 保存沙箱后端：host、docker 或 docker-wsl。
	SandboxImage    string        // 保存 Docker 沙箱镜像名。
	Timeout         time.Duration // 保存整轮超时时长。

	// ProviderSet 表示 provider 是否出现在任一配置文件中；用于区分"未配置"与"显式选择 demo"。
	ProviderSet bool

	// 供应商凭据与端点：模型配置的唯一文件入口（项目级覆盖全局级）。
	// Key 只建议写全局 ~/.xcode/config，避免随项目目录提交。
	BailianAPIKey    string // bailian-api-key：通义千问（百炼）密钥。
	BailianBaseURL   string // bailian-base-url：百炼 OpenAI 兼容端点。
	BailianModel     string // bailian-model：百炼模型名（默认 qwen-plus）。
	DeepseekAPIKey   string // deepseek-api-key：DeepSeek 密钥。
	DeepseekBaseURL  string // deepseek-base-url：DeepSeek 端点（默认 https://api.deepseek.com）。
	DeepseekModel    string // deepseek-model：DeepSeek 模型名（默认 deepseek-flash）。
	TypesafeAPIKey   string // typesafe-api-key：jev 分类后端（TypeSafe systemone）密钥。
	TypesafeEndpoint string // typesafe-endpoint：jev 分类服务端点。
	TypesafeModel    string // typesafe-model：jev 分类模型名。
	GlmAPIKey        string // glm-api-key：智谱 GLM 密钥（常作跨模型 reviewer）。
	GlmBaseURL       string // glm-base-url：GLM OpenAI 兼容端点（默认 https://open.bigmodel.cn/api/paas/v4）。
	GlmModel         string // glm-model：GLM 模型名（默认 glm-4.6）。
	ReviewerProvider string // reviewer-provider：完成门禁审查模型供应商；空 = 未配置（降级为同模型并警告）。
	ReviewerModel    string // reviewer-model：审查模型名；覆盖该供应商默认模型。
	Orchestrate      string // orchestrate：编排模式 single / single-review / pipeline；空 = 全工具智能体。

	// 类型化 shadow 分类器的独立模型配置（与主模型解耦）。
	TypedClassifierBackend  string // 保存分类后端：local（聊天模型）或 jev（TypeSafe systemone）；空值视为 local。
	TypedClassifierEndpoint string // 保存 local 分类器的 OpenAI 兼容端点；空值表示复用主模型。
	TypedClassifierModel    string // 保存 local 分类器的模型名；空值表示复用主模型。
}

// Defaults 返回内置默认值，作为最低优先级。
func Defaults() Config {
	return Config{
		Provider:        "demo",
		Mode:            "workspace-write",
		ContextStrategy: "bounded",
		ContextBudget:   6000,
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
		cfg.ProviderSet = true
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
	case "bailian-api-key", "bailian_api_key":
		cfg.BailianAPIKey = value
	case "bailian-base-url", "bailian_base_url":
		cfg.BailianBaseURL = value
	case "bailian-model", "bailian_model":
		cfg.BailianModel = value
	case "deepseek-api-key", "deepseek_api_key":
		cfg.DeepseekAPIKey = value
	case "deepseek-base-url", "deepseek_base_url":
		cfg.DeepseekBaseURL = value
	case "deepseek-model", "deepseek_model":
		cfg.DeepseekModel = value
	case "typesafe-api-key", "typesafe_api_key":
		cfg.TypesafeAPIKey = value
	case "typesafe-endpoint", "typesafe_endpoint":
		cfg.TypesafeEndpoint = value
	case "typesafe-model", "typesafe_model":
		cfg.TypesafeModel = value
	case "glm-api-key", "glm_api_key":
		cfg.GlmAPIKey = value
	case "glm-base-url", "glm_base_url":
		cfg.GlmBaseURL = value
	case "glm-model", "glm_model":
		cfg.GlmModel = value
	case "reviewer-provider", "reviewer_provider":
		cfg.ReviewerProvider = value
	case "reviewer-model", "reviewer_model":
		cfg.ReviewerModel = value
	case "orchestrate":
		cfg.Orchestrate = value
	case "typed-classifier":
		cfg.TypedClassifierBackend = value
	case "typed-classifier-endpoint":
		cfg.TypedClassifierEndpoint = value
	case "typed-classifier-model":
		cfg.TypedClassifierModel = value
	}
}

// GlobalPath 返回全局配置文件路径 ~/.xcode/config。
func GlobalPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".xcode", "config"), nil
}

// Upsert 在 key=value 配置文件中设置一个键：已存在则原位替换，否则追加到文件末尾。
// 文件或目录不存在时创建；注释行和未知行原样保留。供首次运行向导写入 provider 使用。
func Upsert(path, key, value string) error {
	var lines []string
	data, err := os.ReadFile(path)
	if err == nil {
		for _, line := range strings.Split(strings.TrimRight(string(data), "\r\n"), "\n") {
			trimmed := strings.TrimSpace(line)
			if name, _, ok := strings.Cut(trimmed, "="); ok && strings.TrimSpace(name) == key {
				lines = append(lines, key+"="+value)
				key = "" // 后续同名键也一并归一，只保留第一处
				continue
			}
			lines = append(lines, strings.TrimRight(line, "\r"))
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("read %s: %w", path, err)
	}
	if key != "" {
		lines = append(lines, key+"="+value)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}
	output := strings.Join(lines, "\n") + "\n"
	// 0600：配置文件可能随后写入 API Key，默认仅当前用户可读。
	if err := os.WriteFile(path, []byte(output), 0o600); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}
