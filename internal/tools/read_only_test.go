// package tools_test 的本文件验证 list_files、search_text 和共享只读工具集。
package tools_test

import (
	"context"       // 构造工具调用上下文。
	"encoding/json" // 构造与模型 ToolCall 一致的 JSON 参数。
	"os"            // 创建测试工作区文件。
	"path/filepath" // 以跨平台方式拼接测试路径。
	"strings"       // 检查预览内容和错误信息。
	"testing"       // Go 标准测试框架。

	"xcode/internal/artifactstore" // 为共享工具集提供内存 Store。
	"xcode/internal/harness"       // 校验工具声明与结构化结果。
	"xcode/internal/tools"         // 使用待验收的只读工具集。
)

// newFixtureSet 建立一个包含 Go 代码、忽略文件和密钥文件的共享只读工具集。
func newFixtureSet(t *testing.T) (*tools.ReadOnlySet, string) {
	t.Helper()
	root := t.TempDir()
	files := map[string]string{
		".gitignore":     "dist/\ncoverage.out\n",
		"main.go":        "package main\n\nimport \"fmt\"\n\nfunc main() { fmt.Println(\"xcode-marker\") }\n",
		"pkg/util.go":    "package pkg\n\ntype Helper struct{}\n",
		"docs/notes.md":  "notes about xcode-marker",
		"dist/out.js":    "built artifact",
		"coverage.out":   "coverage data with xcode-marker",
		".env":           "DASHSCOPE_API_KEY=fake-secret",
		"deep/a/b/c.txt": "deep file",
	}
	for name, content := range files {
		target := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(target, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	set, err := tools.NewReadOnlySet(context.Background(), root, artifactstore.NewMemoryStore())
	if err != nil {
		t.Fatal(err)
	}
	return set, root
}

// toolByName 从共享工具集中取出指定名称的工具。
func toolByName(t *testing.T, set *tools.ReadOnlySet, name string) harness.Tool {
	t.Helper()
	for _, tool := range set.Tools() {
		if tool.Spec().Name == name {
			return tool
		}
	}
	t.Fatalf("tool %q not found", name)
	return nil
}

// TestReadOnlySetRegistersReadOnlyTools 验证工具集提供四个可注册的只读工具。
func TestReadOnlySetRegistersReadOnlyTools(t *testing.T) {
	set, _ := newFixtureSet(t)
	registry, err := harness.NewRegistry(set.Tools()...)
	if err != nil {
		t.Fatal(err)
	}
	specs := registry.Specs()
	if len(specs) != 4 {
		t.Fatalf("specs = %d, want 4", len(specs))
	}
	names := []string{specs[0].Name, specs[1].Name, specs[2].Name, specs[3].Name}
	want := []string{"list_files", "read_file", "read_skill", "search_text"}
	for index, name := range want {
		if names[index] != name {
			t.Fatalf("sorted tool names = %v, want %v", names, want)
		}
	}
}

// TestListFilesAppliesDefaultFilters 验证列表隐藏忽略文件与密钥文件并展示索引摘要。
func TestListFilesAppliesDefaultFilters(t *testing.T) {
	set, _ := newFixtureSet(t)
	listFiles := toolByName(t, set, "list_files")
	result, err := listFiles.Execute(context.Background(), json.RawMessage(`{"include_details":true}`))
	if err != nil {
		t.Fatal(err)
	}
	for _, hidden := range []string{"dist/out.js", "coverage.out", ".env"} {
		if strings.Contains(result.Observation, hidden) {
			t.Errorf("listing leaked %q: %q", hidden, result.Observation)
		}
	}
	if !strings.Contains(result.Observation, "main.go") || !strings.Contains(result.Observation, "language=Go") {
		t.Fatalf("listing missing indexed detail: %q", result.Observation)
	}
	if !strings.Contains(result.Observation, "symbols=") || !strings.Contains(result.Observation, "Helper") {
		t.Fatalf("listing missing Go symbols: %q", result.Observation)
	}
	if result.ArtifactID == "" {
		t.Fatalf("listing missing artifact id: %#v", result)
	}
}

// TestListFilesHonorsPathAndDepth 验证起点和 max_depth 限制展示范围。
func TestListFilesHonorsPathAndDepth(t *testing.T) {
	set, _ := newFixtureSet(t)
	listFiles := toolByName(t, set, "list_files")
	result, err := listFiles.Execute(context.Background(), json.RawMessage(`{"path":"deep","max_depth":1}`))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(result.Observation, "deep/a/b/c.txt") {
		t.Fatalf("depth limit ignored: %q", result.Observation)
	}
	full, err := listFiles.Execute(context.Background(), json.RawMessage(`{"path":"deep","max_depth":3}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(full.Observation, "deep/a/b/c.txt") {
		t.Fatalf("deep file missing at depth 3: %q", full.Observation)
	}
	if _, err := listFiles.Execute(context.Background(), json.RawMessage(`{"path":"dist"}`)); err == nil {
		t.Fatal("ignored directory was listed")
	}
}

// TestSearchTextFindsMatchesAndRespectsBoundaries 验证搜索命中格式与默认过滤。
func TestSearchTextFindsMatchesAndRespectsBoundaries(t *testing.T) {
	set, _ := newFixtureSet(t)
	searchText := toolByName(t, set, "search_text")
	result, err := searchText.Execute(context.Background(), json.RawMessage(`{"query":"xcode-marker"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Observation, "main.go:") || !strings.Contains(result.Observation, "docs/notes.md:") {
		t.Fatalf("search missed expected files: %q", result.Observation)
	}
	// coverage.out 被 .gitignore 命中，.env 被硬性排除；两者都不得出现在结果中。
	if strings.Contains(result.Observation, "coverage.out") || strings.Contains(result.Observation, "fake-secret") {
		t.Fatalf("search leaked filtered content: %q", result.Observation)
	}
	if !strings.HasPrefix(result.Observation, "[resource=search_text:") {
		t.Fatalf("search result missing metadata header: %q", result.Observation)
	}
}

// TestSearchTextValidatesArguments 验证空 query 和越界 max_results 被拒绝。
func TestSearchTextValidatesArguments(t *testing.T) {
	set, _ := newFixtureSet(t)
	searchText := toolByName(t, set, "search_text")
	if _, err := searchText.Execute(context.Background(), json.RawMessage(`{}`)); err == nil || !strings.Contains(err.Error(), "query is required") {
		t.Fatalf("empty query error = %v", err)
	}
	if _, err := searchText.Execute(context.Background(), json.RawMessage(`{"query":"x","max_results":9999}`)); err == nil || !strings.Contains(err.Error(), "max_results") {
		t.Fatalf("oversized max_results error = %v", err)
	}
}

// TestSearchTextGoFallbackMatchesEngineContract 验证纯 Go 回退产出相同结构的命中。
func TestSearchTextGoFallbackMatchesEngineContract(t *testing.T) {
	// 通过清空 PATH 让 rg 不可用，强制走纯 Go 回退路径。
	t.Setenv("PATH", t.TempDir())
	set, _ := newFixtureSet(t)
	searchText := toolByName(t, set, "search_text")
	result, err := searchText.Execute(context.Background(), json.RawMessage(`{"query":"XCODE-MARKER"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Observation, "engine=go") {
		t.Fatalf("fallback engine missing: %q", result.Observation)
	}
	// 默认不区分大小写，因此大写查询仍应命中小写正文。
	if !strings.Contains(result.Observation, "main.go:") {
		t.Fatalf("fallback search missed match: %q", result.Observation)
	}
}
