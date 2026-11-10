package main

// 本文件验证 CLI 配置取值链与 Provider 选择，不会调用真实百炼服务。

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"xcode/internal/config"  // 配置结构与 Upsert 写入。
	"xcode/internal/harness" // 构造用于验证极简进度渲染的事件。
)

// TestBuildModelSelectsSupportedProvider 验证 CLI 可选择 Demo 和配置完整的百炼 Provider。
func TestBuildModelSelectsSupportedProvider(t *testing.T) {
	if _, err := buildModel(config.Config{}, "demo"); err != nil {
		t.Fatalf("build demo model: %v", err)
	}

	t.Setenv("DASHSCOPE_API_KEY", "test-key")
	t.Setenv("DASHSCOPE_BASE_URL", "https://example.com/compatible-mode/v1")
	t.Setenv("DASHSCOPE_MODEL", "qwen-test")
	if _, err := buildModel(config.Config{}, "bailian"); err != nil {
		t.Fatalf("build bailian model: %v", err)
	}

	if _, err := buildModel(config.Config{}, "unknown"); err == nil {
		t.Fatal("unknown provider was accepted")
	}
}

// TestBailianSettingsResolutionChain 验证取值链：配置文件键 > 环境变量 > 内置默认。
func TestBailianSettingsResolutionChain(t *testing.T) {
	t.Setenv("DASHSCOPE_API_KEY", "env-key")
	t.Setenv("DASHSCOPE_BASE_URL", "https://env.example/v1")
	t.Setenv("DASHSCOPE_MODEL", "env-model")

	fromEnv := bailianSettings(config.Config{}, 2)
	if fromEnv.APIKey != "env-key" || fromEnv.BaseURL != "https://env.example/v1" || fromEnv.Model != "env-model" {
		t.Fatalf("env fallback settings = %+v", fromEnv)
	}

	fromConfig := bailianSettings(config.Config{
		BailianAPIKey: "cfg-key", BailianBaseURL: "https://cfg.example/v1", BailianModel: "cfg-model",
	}, 2)
	if fromConfig.APIKey != "cfg-key" || fromConfig.BaseURL != "https://cfg.example/v1" || fromConfig.Model != "cfg-model" {
		t.Fatalf("config override settings = %+v", fromConfig)
	}

	t.Setenv("DASHSCOPE_API_KEY", "")
	t.Setenv("DASHSCOPE_BASE_URL", "")
	t.Setenv("DASHSCOPE_MODEL", "")
	defaults := bailianSettings(config.Config{}, 2)
	if defaults.APIKey != "" || defaults.BaseURL != defaultBailianBaseURL || defaults.Model != defaultBailianModel {
		t.Fatalf("default settings = %+v", defaults)
	}
}

// TestDeepseekSettingsDefaults 验证 DeepSeek 端点、模型与输出上限的内置默认值。
func TestDeepseekSettingsDefaults(t *testing.T) {
	t.Setenv("DEEPSEEK_API_KEY", "")
	t.Setenv("DEEPSEEK_BASE_URL", "")
	t.Setenv("DEEPSEEK_MODEL", "")
	settings := deepseekSettings(config.Config{}, "enabled", "high", 2, 0)
	if settings.APIKey != "" || settings.BaseURL != defaultDeepseekBaseURL || settings.Model != defaultDeepseekModel {
		t.Fatalf("deepseek defaults = %+v", settings)
	}
	if settings.MaxOutputTokens != 8192 {
		t.Fatalf("max output tokens = %d, want 8192", settings.MaxOutputTokens)
	}
}

// TestDetectConfiguredRealProvider 验证矛盾检测的供应商发现顺序。
func TestDetectConfiguredRealProvider(t *testing.T) {
	t.Setenv("DASHSCOPE_API_KEY", "")
	t.Setenv("DEEPSEEK_API_KEY", "")
	if got := detectConfiguredRealProvider(config.Config{}); got != "" {
		t.Fatalf("detected = %q, want empty", got)
	}
	if got := detectConfiguredRealProvider(config.Config{DeepseekAPIKey: "sk-x"}); got != "deepseek" {
		t.Fatalf("detected = %q, want deepseek", got)
	}
	t.Setenv("DASHSCOPE_API_KEY", "env-key")
	if got := detectConfiguredRealProvider(config.Config{DeepseekAPIKey: "sk-x"}); got != "bailian" {
		t.Fatalf("detected = %q, want bailian priority", got)
	}
}

// TestParseWizardChoice 验证向导菜单输入解析。
func TestParseWizardChoice(t *testing.T) {
	cases := []struct {
		input  string
		want   string
		wantOK bool
	}{
		{"1", "bailian", true},
		{" 2 ", "deepseek", true},
		{"3", "demo", true},
		{"deepseek", "deepseek", true},
		{"DEMO", "demo", true},
		{"0", "", false},
		{"4", "", false},
		{"x", "", false},
		{"", "", false},
	}
	for _, testCase := range cases {
		got, ok := parseWizardChoice(testCase.input)
		if ok != testCase.wantOK || got != testCase.want {
			t.Errorf("parseWizardChoice(%q) = (%q, %v), want (%q, %v)",
				testCase.input, got, ok, testCase.want, testCase.wantOK)
		}
	}
}

// TestConfigUpsertReplacesAndAppends 验证向导写入：原位替换、保留注释、追加新键。
func TestConfigUpsertReplacesAndAppends(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "config")
	if err := config.Upsert(path, "provider", "bailian"); err != nil {
		t.Fatal(err)
	}
	if err := config.Upsert(path, "provider", "deepseek"); err != nil {
		t.Fatal(err)
	}
	if err := config.Upsert(path, "bailian-api-key", "sk-new"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	got := string(data)
	wantLines := []string{"provider=deepseek", "bailian-api-key=sk-new"}
	for _, line := range wantLines {
		if !strings.Contains(got, line+"\n") {
			t.Errorf("config missing %q; got:\n%s", line, got)
		}
	}
	if strings.Contains(got, "provider=bailian") {
		t.Errorf("stale provider line kept:\n%s", got)
	}
}

// TestNoticeLegacyDotEnv 验证旧 .env 检测只认已知变量名。
func TestNoticeLegacyDotEnv(t *testing.T) {
	t.Chdir(t.TempDir())
	_ = os.WriteFile(".env", []byte("UNRELATED=1\n"), 0o600)
	noticeLegacyDotEnv() // 不应 panic；无关变量不触发提示（输出无法捕获，仅冒烟）。
	_ = os.WriteFile(".env", []byte("DASHSCOPE_API_KEY=sk-old\n"), 0o600)
	noticeLegacyDotEnv()
}

// TestWriteCompactEventRendersKeyKinds 验证极简进度只渲染关键事件并压缩内容。
func TestWriteCompactEventRendersKeyKinds(t *testing.T) {
	var output strings.Builder
	events := []harness.Event{
		{Kind: harness.RunStarted, Data: "prompt"},
		{Kind: harness.ModelTextDeltaEmitted, Step: 1, Data: "noise"},
		{Kind: harness.ModelToolCallDeltaEmitted, Step: 1, Data: "noise"},
		{Kind: harness.ModelUsageEmitted, Step: 1, TotalTokens: 481},
		{Kind: harness.ModelFinishedEmitted, Step: 1, FinishReason: "tool_calls"},
		{Kind: harness.ModelCompleted, Step: 1, FinishReason: "tool_calls", TotalTokens: 481},
		{Kind: harness.ToolStarted, Step: 1, Tool: "read_file", Data: "{\"path\": \"README.md\"}"},
		{
			Kind: harness.ToolCompleted, Step: 1, Tool: "read_file",
			Resource: "README.md#L1-L2", OriginalBytes: 36599, VisibleBytes: 8192, Truncated: true,
		},
		{
			Kind: harness.ToolCompleted, Step: 2, Tool: "read_file",
			Resource: "README.md#L1-L2", OriginalBytes: 36599, RepeatedRead: true,
		},
		{Kind: harness.ToolCompleted, Step: 2, Tool: "read_file", Data: "ERROR: path escapes workspace"},
		{Kind: harness.RunCompleted, Step: 2, Data: "answer"},
		{Kind: harness.RunFailed, Step: 2, Error: "maximum agent steps reached"},
	}
	for _, event := range events {
		writeCompactEvent(&output, event)
	}

	got := output.String()
	wantLines := []string{
		"[step 1] model finish=tool_calls tokens=481",
		"[step 1] read_file {\"path\": \"README.md\"}",
		"[step 1] read_file ok README.md#L1-L2 36599B→8192B truncated",
		"[step 2] read_file ok README.md#L1-L2 repeated_read",
		"[step 2] read_file error: path escapes workspace",
		"[done] steps=2",
		"[failed] step 2: maximum agent steps reached",
	}
	for _, line := range wantLines {
		if !strings.Contains(got, line+"\n") {
			t.Errorf("compact output missing line %q; got:\n%s", line, got)
		}
	}
	// 流式增量和 usage/finished 属于噪声事件，不应产生任何进度行。
	if strings.Contains(got, "noise") || strings.Count(got, "\n") != len(wantLines) {
		t.Errorf("compact output contains unexpected lines:\n%s", got)
	}
}

// TestCompactLineCollapsesAndTruncates 验证进度文本压成单行并遵守长度上限。
func TestCompactLineCollapsesAndTruncates(t *testing.T) {
	if got := compactLine("a\n  b\tc", 120); got != "a b c" {
		t.Fatalf("collapsed line = %q, want %q", got, "a b c")
	}
	long := strings.Repeat("x", 200)
	got := compactLine(long, 120)
	if len([]rune(got)) != 120 || !strings.HasSuffix(got, "…") {
		t.Fatalf("truncated line length = %d, want 120 ending with ellipsis", len([]rune(got)))
	}
}

// TestBuildModelRequiresBailianKey 验证真实 Provider 缺少密钥时在联网前失败。
func TestBuildModelRequiresBailianKey(t *testing.T) {
	t.Setenv("DASHSCOPE_API_KEY", "")
	errText := ""
	if _, err := buildModel(config.Config{}, "bailian"); err != nil {
		errText = err.Error()
	}
	if !strings.Contains(errText, "bailian-api-key") || !strings.Contains(errText, "DASHSCOPE_API_KEY") {
		t.Fatalf("error = %q, want missing-key guidance", errText)
	}
}
