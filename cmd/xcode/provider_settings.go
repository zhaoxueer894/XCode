package main

// 本文件实现供应商配置的统一取值链：config 文件键 > 进程环境变量 > 内置默认值。
// 模型密钥、端点、模型名的唯一文件入口是 ~/.xcode/config（项目 .xcode/config 覆盖全局），
// 环境变量仅作为 CI 等无配置文件场景的注入兜底。

import (
	"os"
	"strings"

	"xcode/internal/config"
	"xcode/internal/model/bailian"
	"xcode/internal/model/deepseek"
)

const (
	defaultDeepseekBaseURL = "https://api.deepseek.com"
	defaultDeepseekModel   = "deepseek-flash"
)

// pick 返回第一个非空（去空白后）候选值，调用方按优先级顺序传入。
func pick(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

// bailianSettings 构建 bailian 客户端配置。
func bailianSettings(cfg config.Config, retries int) bailian.Config {
	return bailian.Config{
		APIKey:         pick(cfg.BailianAPIKey, os.Getenv("DASHSCOPE_API_KEY")),
		BaseURL:        pick(cfg.BailianBaseURL, os.Getenv("DASHSCOPE_BASE_URL"), defaultBailianBaseURL),
		Model:          pick(cfg.BailianModel, os.Getenv("DASHSCOPE_MODEL"), defaultBailianModel),
		MaxRetries:     retries,
		DisableRetries: retries == 0,
	}
}

// deepseekSettings 构建 deepseek 客户端配置。
func deepseekSettings(cfg config.Config, thinking, effort string, retries, outputTokens int) deepseek.Config {
	settings := deepseek.Config{
		APIKey:          pick(cfg.DeepseekAPIKey, os.Getenv("DEEPSEEK_API_KEY")),
		BaseURL:         pick(cfg.DeepseekBaseURL, os.Getenv("DEEPSEEK_BASE_URL"), defaultDeepseekBaseURL),
		Model:           pick(cfg.DeepseekModel, os.Getenv("DEEPSEEK_MODEL"), defaultDeepseekModel),
		Thinking:        thinking,
		ReasoningEffort: effort,
		MaxOutputTokens: outputTokens,
		MaxRetries:      retries,
		DisableRetries:  retries == 0,
	}
	if outputTokens == 0 {
		settings.MaxOutputTokens = 8192
	}
	return settings
}

// providerKeyConfigured 判断某供应商的 API Key 在配置文件或环境变量中是否可用。
func providerKeyConfigured(cfg config.Config, provider string) bool {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "bailian":
		return pick(cfg.BailianAPIKey, os.Getenv("DASHSCOPE_API_KEY")) != ""
	case "deepseek":
		return pick(cfg.DeepseekAPIKey, os.Getenv("DEEPSEEK_API_KEY")) != ""
	}
	return false
}

// detectConfiguredRealProvider 返回第一个已配置 Key 的真实供应商（bailian 优先）；都没有则返回空串。
func detectConfiguredRealProvider(cfg config.Config) string {
	for _, provider := range []string{"bailian", "deepseek"} {
		if providerKeyConfigured(cfg, provider) {
			return provider
		}
	}
	return ""
}

// providerKeyHint 返回某供应商 API Key 的配置写法说明，用于向导和报错指引。
func providerKeyHint(provider string) string {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "bailian":
		return "bailian-api-key=sk-..."
	case "deepseek":
		return "deepseek-api-key=sk-..."
	}
	return ""
}

// typesafeSettings 解析 jev 分类后端凭据：config 键优先，环境变量兜底。
func typesafeSettings(cfg config.Config) (key, endpoint, model string) {
	return pick(cfg.TypesafeAPIKey, os.Getenv("TYPESAFE_API_KEY")),
		pick(cfg.TypesafeEndpoint, os.Getenv("TYPESAFE_ENDPOINT")),
		pick(cfg.TypesafeModel, os.Getenv("TYPESAFE_MODEL"))
}

// classifierKey 解析 local 分类后端 Key：bailian 配置键优先，环境变量兜底。
func classifierKey(cfg config.Config) string {
	return pick(cfg.BailianAPIKey, os.Getenv("DASHSCOPE_API_KEY"))
}

// displayModelName 返回某供应商当前生效的模型名，供 REPL 状态栏展示。
func displayModelName(cfg config.Config, provider string) string {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "bailian":
		return pick(cfg.BailianModel, os.Getenv("DASHSCOPE_MODEL"), defaultBailianModel)
	case "deepseek":
		return pick(cfg.DeepseekModel, os.Getenv("DEEPSEEK_MODEL"), defaultDeepseekModel)
	}
	return "demo"
}
