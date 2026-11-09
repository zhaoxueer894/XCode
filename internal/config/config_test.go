package config

import (
	"os"
	"path/filepath"
	"testing"
)

// TestLoadParsesProviderCredentialKeys 验证供应商键的解析与项目级覆盖全局级。
func TestLoadParsesProviderCredentialKeys(t *testing.T) {
	global := t.TempDir()
	t.Setenv("USERPROFILE", global)
	t.Setenv("HOME", global)
	_ = os.MkdirAll(filepath.Join(global, ".xcode"), 0o700)
	_ = os.WriteFile(filepath.Join(global, ".xcode", "config"), []byte(
		"provider=bailian\nbailian-api-key=sk-global\ndeepseek-model=deepseek-chat\ntypesafe-endpoint=https://typesafe.example\n"), 0o600)

	project := t.TempDir()
	_ = os.MkdirAll(filepath.Join(project, ".xcode"), 0o700)
	_ = os.WriteFile(filepath.Join(project, ".xcode", "config"), []byte("bailian-api-key=sk-project\n"), 0o600)

	merged, err := Load(project)
	if err != nil {
		t.Fatal(err)
	}
	if !merged.ProviderSet || merged.Provider != "bailian" {
		t.Fatalf("provider = %q (set=%v), want bailian", merged.Provider, merged.ProviderSet)
	}
	if merged.BailianAPIKey != "sk-project" {
		t.Fatalf("bailian key = %q, want project-level override", merged.BailianAPIKey)
	}
	if merged.DeepseekModel != "deepseek-chat" {
		t.Fatalf("deepseek model = %q, want global value", merged.DeepseekModel)
	}
	if merged.TypesafeEndpoint != "https://typesafe.example" {
		t.Fatalf("typesafe endpoint = %q", merged.TypesafeEndpoint)
	}
}

// TestLoadWithoutProviderLeavesFlagUnset 验证未配置 provider 时 ProviderSet 为 false。
func TestLoadWithoutProviderLeavesFlagUnset(t *testing.T) {
	t.Setenv("USERPROFILE", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	merged, err := Load(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if merged.ProviderSet {
		t.Fatal("ProviderSet = true without any provider key")
	}
	if merged.Provider != "demo" {
		t.Fatalf("default provider = %q, want demo", merged.Provider)
	}
}

// TestUpsertPreservesCommentsAndOtherKeys 验证写入不破坏注释与未知行。
func TestUpsertPreservesCommentsAndOtherKeys(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config")
	original := "# 我的注释\nmode=plan\nprovider=demo\n"
	if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Upsert(path, "provider", "bailian"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	got := string(data)
	for _, want := range []string{"# 我的注释", "mode=plan", "provider=bailian"} {
		if !containsLine(got, want) {
			t.Errorf("config lost line %q; got:\n%s", want, got)
		}
	}
	if containsLine(got, "provider=demo") {
		t.Errorf("stale provider line kept:\n%s", got)
	}
}

func containsLine(text, line string) bool {
	for _, candidate := range splitLines(text) {
		if candidate == line {
			return true
		}
	}
	return false
}

func splitLines(text string) []string {
	var lines []string
	start := 0
	for index := 0; index < len(text); index++ {
		if text[index] == '\n' {
			lines = append(lines, text[start:index])
			start = index + 1
		}
	}
	if start < len(text) {
		lines = append(lines, text[start:])
	}
	return lines
}
