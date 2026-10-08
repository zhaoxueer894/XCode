// package workspace_test 验证工作区边界、忽略规则、索引和 ReadSet 的行为。
package workspace_test

import (
	"context"       // 构造正常与已取消的索引上下文。
	"os"            // 创建测试文件、目录和符号链接。
	"path/filepath" // 以跨平台方式拼接测试路径。
	"strings"       // 检查错误信息。
	"testing"       // Go 标准测试框架。

	"xcode/internal/workspace" // 使用待验收的工作区实现。
)

// writeFile 在测试工作区中创建一个带父目录的小文件。
func writeFile(t *testing.T, root, relative, content string) {
	t.Helper()
	target := filepath.Join(root, filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestResolveExistingRejectsEscapes 验证 ../ 和符号链接都不能越过工作区。
func TestResolveExistingRejectsEscapes(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	writeFile(t, outside, "secret.txt", "secret")
	if err := os.Symlink(filepath.Join(outside, "secret.txt"), filepath.Join(root, "link.txt")); err != nil {
		t.Skipf("symlink creation is unavailable: %v", err)
	}
	view, err := workspace.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := view.ResolveExisting("../secret.txt"); err == nil || !strings.Contains(err.Error(), "escapes workspace") {
		t.Fatalf("dotdot error = %v", err)
	}
	if _, _, _, err := view.ResolveExisting("link.txt"); err == nil || !strings.Contains(err.Error(), "escapes workspace") {
		t.Fatalf("symlink error = %v", err)
	}
}

// TestHardExclusionsProtectInternalData 验证内部目录和 dotenv 文件始终被排除。
func TestHardExclusionsProtectInternalData(t *testing.T) {
	view, err := workspace.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{".git/config", ".eval/runs/x", ".xcode-state/artifacts/a", ".env", "config/.env.local"} {
		if !view.IsHardExcluded(path) {
			t.Errorf("%q should be hard excluded", path)
		}
	}
	for _, path := range []string{"README.md", ".env.example", "internal/harness/types.go"} {
		if view.IsHardExcluded(path) {
			t.Errorf("%q should not be hard excluded", path)
		}
	}
}

// TestGitignoreRulesControlDiscovery 验证注释、目录规则、通配符和 ! 重新包含。
func TestGitignoreRulesControlDiscovery(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, ".gitignore", "# comment\ndist/\n*.log\n!keep.log\n/build\ndocs/**/tmp\n")
	view, err := workspace.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		path        string
		isDirectory bool
		ignored     bool
	}{
		{"dist", true, true},
		{"dist/app.js", false, true},
		{"error.log", false, true},
		{"keep.log", false, false},
		{"build", false, true},
		{"nested/build", false, false},
		{"docs/a/b/tmp", false, true},
		{"main.go", false, false},
	}
	for _, testCase := range cases {
		if got := view.IsDiscoveryIgnored(testCase.path, testCase.isDirectory); got != testCase.ignored {
			t.Errorf("IsDiscoveryIgnored(%q) = %v, want %v", testCase.path, got, testCase.ignored)
		}
	}
	// 存在 ! 规则时不允许整目录跳过，否则 keep.log 可能被错误隐藏。
	if view.CanSkipIgnoredDirectories() {
		t.Error("negated rules must disable directory skipping")
	}
}

// TestBuildIndexRecordsGoSymbolsAndSkipsIgnored 验证索引内容、排序和默认过滤。
func TestBuildIndexRecordsGoSymbolsAndSkipsIgnored(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, ".gitignore", "dist/\n")
	writeFile(t, root, "main.go", "package main\n\nimport \"fmt\"\n\ntype Greeter struct{}\n\nfunc (g *Greeter) Hello() { fmt.Println(\"hi\") }\n\nfunc main() {}\n")
	writeFile(t, root, "README.md", "# demo")
	writeFile(t, root, "dist/out.js", "ignored")
	writeFile(t, root, ".env", "SECRET=1")

	view, err := workspace.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	index, err := workspace.BuildIndex(context.Background(), view)
	if err != nil {
		t.Fatal(err)
	}
	files := index.Files()
	paths := make([]string, 0, len(files))
	for _, record := range files {
		paths = append(paths, record.Path)
	}
	for _, hidden := range []string{"dist/out.js", ".env"} {
		for _, path := range paths {
			if path == hidden {
				t.Errorf("ignored file %q entered the index", hidden)
			}
		}
	}
	record, ok := index.File("main.go")
	if !ok {
		t.Fatalf("main.go missing from index; got %v", paths)
	}
	if record.Language != "Go" || record.ContentHash == "" || record.SizeBytes == 0 {
		t.Fatalf("record = %#v", record)
	}
	wantSymbols := map[string]bool{"Greeter": false, "Greeter.Hello": false, "main": false}
	for _, symbol := range record.Symbols {
		if _, tracked := wantSymbols[symbol]; tracked {
			wantSymbols[symbol] = true
		}
	}
	for symbol, seen := range wantSymbols {
		if !seen {
			t.Errorf("symbol %q missing from %v", symbol, record.Symbols)
		}
	}
	if len(record.Dependencies) != 1 || record.Dependencies[0] != "fmt" {
		t.Errorf("dependencies = %v, want [fmt]", record.Dependencies)
	}
	if summary := index.LanguageSummary(); !strings.Contains(summary, "Go=1") {
		t.Errorf("language summary = %q", summary)
	}
}

// TestBuildIndexHonorsCanceledContext 验证索引建立立即响应取消。
func TestBuildIndexHonorsCanceledContext(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "a.txt", "a")
	view, err := workspace.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := workspace.BuildIndex(ctx, view); err == nil {
		t.Fatal("canceled index build succeeded")
	}
}

// TestReadSetObserveDetectsRepeats 验证同资源同内容同 Epoch 才判定为重复。
func TestReadSetObserveDetectsRepeats(t *testing.T) {
	reads := workspace.NewReadSet()
	record := workspace.ReadRecord{Resource: "a.txt#L1-L2", ContentHash: "hash-1", VisibleEpoch: 1}
	if reads.Observe(record) {
		t.Fatal("first observation was marked repeated")
	}
	if !reads.Observe(record) {
		t.Fatal("identical observation was not marked repeated")
	}
	changed := record
	changed.ContentHash = "hash-2"
	if reads.Observe(changed) {
		t.Fatal("changed content was marked repeated")
	}
	newEpoch := changed
	newEpoch.VisibleEpoch = 2
	if reads.Observe(newEpoch) {
		t.Fatal("new epoch was marked repeated")
	}
}
