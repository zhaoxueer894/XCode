package main

// 本文件实现供应商配置的统一取值链：config 文件键 > 进程环境变量 > 内置默认值。
// 模型密钥、端点、模型名的唯一文件入口是 ~/.xcode/config（项目 .xcode/config 覆盖全局），
// 环境变量仅作为 CI 等无配置文件场景的注入兜底。

import (
	"fmt"
	"os"
	"strings"

	"xcode/internal/config"
	"xcode/internal/harness"
	"xcode/internal/model/bailian"
	"xcode/internal/model/chatcompletions"
	"xcode/internal/model/deepseek"
	"xcode/internal/model/glm"
)

const (
	defaultDeepseekBaseURL = "https://api.deepseek.com"
	defaultDeepseekModel   = "deepseek-flash"
	defaultGlmBaseURL      = "https://open.bigmodel.cn/api/paas/v4"
	defaultGlmModel        = "glm-4.6"
	// defaultReviewerOutputTokens is larger than the developer default because a
	// thinking reviewer must reason before emitting its single GATE-VERDICT line.
	defaultReviewerOutputTokens = 32768
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

// glmSettings builds the glm client config; modelOverride (reviewer-model) wins.
func glmSettings(cfg config.Config, modelOverride string, retries, outputTokens int) glm.Config {
	return glm.Config{
		APIKey:          pick(cfg.GlmAPIKey, os.Getenv("GLM_API_KEY")),
		BaseURL:         pick(cfg.GlmBaseURL, os.Getenv("GLM_BASE_URL"), defaultGlmBaseURL),
		Model:           pick(modelOverride, cfg.GlmModel, os.Getenv("GLM_MODEL"), defaultGlmModel),
		MaxOutputTokens: outputTokens,
		MaxRetries:      retries,
		DisableRetries:  retries == 0,
	}
}

// reviewerOutputBudget returns the per-request output limit for the gate
// reviewer. A thinking reviewer spends this budget on reasoning before it can
// emit its one-line verdict, so a reviewer left at the developer default can
// hit the cap mid-reasoning and never produce a verdict at all. Callers pass
// an explicit value only when they know it is enough.
func reviewerOutputBudget(configured int) int {
	if configured > 0 {
		return configured
	}
	return defaultReviewerOutputTokens
}

// buildReviewerModel builds the cross-model reviewer; a non-empty
// modelOverride replaces the provider default. The returned model is not
// budget-wrapped (the caller wraps it as sub:reviewer).
func buildReviewerModel(cfg config.Config, provider, modelOverride string, retries, outputTokens int) (harness.Model, string, error) {
	outputTokens = reviewerOutputBudget(outputTokens)
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "glm":
		settings := glmSettings(cfg, modelOverride, retries, outputTokens)
		client, err := glm.New(settings)
		return client, settings.Model, err
	case "deepseek":
		settings := deepseekSettings(cfg, "enabled", "high", retries, outputTokens)
		if strings.TrimSpace(modelOverride) != "" {
			settings.Model = strings.TrimSpace(modelOverride)
		}
		client, err := deepseek.New(settings)
		return client, settings.Model, err
	case "bailian":
		settings := bailianSettings(cfg, retries)
		settings.MaxOutputTokens = outputTokens
		if strings.TrimSpace(modelOverride) != "" {
			settings.Model = strings.TrimSpace(modelOverride)
		}
		client, err := bailian.New(settings)
		return client, settings.Model, err
	}
	return nil, "", fmt.Errorf("reviewer-provider must be glm, deepseek or bailian")
}

// modelFromSlot builds a model from a config slot. Known slot names reuse
// their provider wrappers (defaults for thinking etc.); any other name is a
// generic OpenAI-compatible endpoint.
func modelFromSlot(name string, slot config.ModelSlot, thinking, effort string, retries, outputTokens int) (harness.Model, string, error) {
	if strings.TrimSpace(slot.APIKey) == "" {
		return nil, "", fmt.Errorf("model slot %q has no key: set model.%s.key", name, name)
	}
	if outputTokens == 0 {
		outputTokens = 8192
	}
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "deepseek":
		settings := deepseek.Config{APIKey: slot.APIKey, BaseURL: slot.BaseURL, Model: slot.Model,
			Thinking: thinking, ReasoningEffort: effort, MaxOutputTokens: outputTokens,
			MaxRetries: retries, DisableRetries: retries == 0}
		client, err := deepseek.New(settings)
		return client, settings.Model, err
	case "bailian":
		settings := bailian.Config{APIKey: slot.APIKey, BaseURL: slot.BaseURL, Model: slot.Model,
			MaxRetries: retries, DisableRetries: retries == 0}
		client, err := bailian.New(settings)
		return client, settings.Model, err
	case "glm":
		settings := glm.Config{APIKey: slot.APIKey, BaseURL: slot.BaseURL, Model: slot.Model,
			MaxRetries: retries, DisableRetries: retries == 0}
		client, err := glm.New(settings)
		return client, settings.Model, err
	}
	settings := chatcompletions.Config{Provider: name, APIKey: slot.APIKey, BaseURL: slot.BaseURL,
		Model: slot.Model, MaxRetries: retries, DisableRetries: retries == 0}
	client, err := chatcompletions.New(settings)
	return client, settings.Model, err
}

// providerKeyConfigured 判断某供应商的 API Key 在配置文件或环境变量中是否可用。
func providerKeyConfigured(cfg config.Config, provider string) bool {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "bailian":
		return pick(cfg.BailianAPIKey, os.Getenv("DASHSCOPE_API_KEY")) != ""
	case "deepseek":
		return pick(cfg.DeepseekAPIKey, os.Getenv("DEEPSEEK_API_KEY")) != ""
	case "glm":
		return pick(cfg.GlmAPIKey, os.Getenv("GLM_API_KEY")) != ""
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
	case "glm":
		return "glm-api-key=..."
	}
	return ""
}

// typesafeSettings 解析 jev 分类后端凭据：config 键优先，环境变量兜底。
func typesafeSettings(cfg config.Config) (key, endpoint, model string) {
	return pick(cfg.TypesafeAPIKey, os.Getenv("TYPESAFE_API_KEY")),
		pick(cfg.TypesafeEndpoint, os.Getenv("TYPESAFE_ENDPOINT")),
		pick(cfg.TypesafeModel, os.Getenv("TYPESAFE_MODEL"))
}

// classifierKey resolves the dedicated-classifier credential: its own key
// first, then the bailian key (legacy default), then env.
func classifierKey(cfg config.Config) string {
	return pick(cfg.TypedClassifierAPIKey, cfg.BailianAPIKey, os.Getenv("DASHSCOPE_API_KEY"))
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
