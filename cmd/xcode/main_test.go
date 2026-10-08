package main

// 本文件验证 CLI 配置加载与 Provider 选择，不会调用真实百炼服务。

import (
	"os"            // 创建测试专用 .env 并检查进程环境变量。
	"path/filepath" // 拼接临时目录中的配置路径。
	"runtime"       // Windows 不使用 Unix 权限位表达 ACL。
	"strings"       // 检查错误信息是否说明具体配置问题。
	"testing"       // Go 标准测试框架。

	"xcode/internal/harness" // 构造用于验证极简进度渲染的事件。
)

// TestLoadDotEnvLoadsOnlyAllowedValues 验证 .env 只加载白名单配置并支持引号。
func TestLoadDotEnvLoadsOnlyAllowedValues(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".env")
	content := strings.Join([]string{
		"# test configuration",
		"DASHSCOPE_API_KEY='test-key'",
		"DASHSCOPE_BASE_URL=https://example.com/compatible-mode/v1",
		`DASHSCOPE_MODEL="qwen-test"`,
		"UNRELATED_VALUE=must-not-be-loaded",
	}, "\n")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	t.Setenv("DASHSCOPE_API_KEY", "old-key")
	t.Setenv("DASHSCOPE_BASE_URL", "old-url")
	t.Setenv("DASHSCOPE_MODEL", "old-model")
	t.Setenv("UNRELATED_VALUE", "unchanged")
	if err := loadDotEnv(path); err != nil {
		t.Fatal(err)
	}

	if got := os.Getenv("DASHSCOPE_API_KEY"); got != "test-key" {
		t.Fatalf("API key = %q, want test-key", got)
	}
	if got := os.Getenv("DASHSCOPE_BASE_URL"); got != "https://example.com/compatible-mode/v1" {
		t.Fatalf("base URL = %q", got)
	}
	if got := os.Getenv("DASHSCOPE_MODEL"); got != "qwen-test" {
		t.Fatalf("model = %q, want qwen-test", got)
	}
	if got := os.Getenv("UNRELATED_VALUE"); got != "unchanged" {
		t.Fatalf("unrelated environment value changed to %q", got)
	}
}

// TestLoadDotEnvAllowsMissingFile 验证没有 .env 时仍可使用离线 Demo 模式。
func TestLoadDotEnvAllowsMissingFile(t *testing.T) {
	if err := loadDotEnv(filepath.Join(t.TempDir(), "missing.env")); err != nil {
		t.Fatalf("missing .env returned error: %v", err)
	}
}

// TestLoadDotEnvRejectsOpenPermissions 验证可能泄露密钥的宽松文件权限会被拒绝。
func TestLoadDotEnvRejectsOpenPermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows permissions are represented by ACLs instead of Unix mode bits")
	}
	path := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(path, []byte("DASHSCOPE_API_KEY=test-key\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}

	err := loadDotEnv(path)
	if err == nil || !strings.Contains(err.Error(), "chmod 600") {
		t.Fatalf("error = %v, want chmod 600 guidance", err)
	}
}

// TestLoadDotEnvRejectsMalformedAllowedValue 验证空密钥不会悄悄覆盖有效配置。
func TestLoadDotEnvRejectsMalformedAllowedValue(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(path, []byte("DASHSCOPE_API_KEY=\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	err := loadDotEnv(path)
	if err == nil || !strings.Contains(err.Error(), "must not be empty") {
		t.Fatalf("error = %v, want empty-value error", err)
	}
}

// TestBuildModelSelectsSupportedProvider 验证 CLI 可选择 Demo 和配置完整的百炼 Provider。
func TestBuildModelSelectsSupportedProvider(t *testing.T) {
	if _, err := buildModel("demo"); err != nil {
		t.Fatalf("build demo model: %v", err)
	}

	t.Setenv("DASHSCOPE_API_KEY", "test-key")
	t.Setenv("DASHSCOPE_BASE_URL", "https://example.com/compatible-mode/v1")
	t.Setenv("DASHSCOPE_MODEL", "qwen-test")
	if _, err := buildModel("bailian"); err != nil {
		t.Fatalf("build bailian model: %v", err)
	}

	if _, err := buildModel("unknown"); err == nil {
		t.Fatal("unknown provider was accepted")
	}
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
	if _, err := buildModel("bailian"); err != nil {
		errText = err.Error()
	}
	if !strings.Contains(errText, "DASHSCOPE_API_KEY") {
		t.Fatalf("error = %q, want missing-key guidance", errText)
	}
}
