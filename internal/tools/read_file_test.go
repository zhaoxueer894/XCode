// package tools_test 只通过公开 API 验证 read_file 的功能和安全边界。
package tools_test

import (
	"context"       // 构造正常和已取消的工具调用上下文。
	"encoding/json" // 构造与模型 ToolCall 一致的 JSON 参数。
	"errors"        // 判断取消错误是否保留 context.Canceled。
	"fmt"           // 构造测试用的编号行内容。
	"os"            // 创建普通文件、大文件和符号链接。
	"path/filepath" // 以跨平台方式创建测试路径。
	"strings"       // 检查错误说明和截断标记。
	"testing"       // Go 标准测试框架。

	"xcode/internal/harness" // 校验结构化 ToolResult 字段。
	"xcode/internal/tools"   // 使用待验收的 read_file 实现。
)

// executePath 把路径编码成真实工具参数，减少每个测试的重复代码。
func executePath(t *testing.T, readFile *tools.ReadFile, path string) (harness.ToolResult, error) {
	t.Helper()
	raw, err := json.Marshal(map[string]string{"path": path})
	if err != nil {
		t.Fatal(err)
	}
	return readFile.Execute(context.Background(), raw)
}

// TestReadFileReadsWorkspaceFile 验证工作区内普通文件能够被读取，并返回结构化虚拟化结果。
func TestReadFileReadsWorkspaceFile(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "note.txt"), []byte("inside workspace"), 0o600); err != nil {
		t.Fatal(err)
	}
	readFile, err := tools.NewReadFile(root)
	if err != nil {
		t.Fatal(err)
	}
	result, err := executePath(t, readFile, "note.txt")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Observation, "inside workspace") {
		t.Fatalf("observation does not contain file content: %q", result.Observation)
	}
	if result.Resource != "note.txt#L1-L1" {
		t.Fatalf("resource = %q, want note.txt#L1-L1", result.Resource)
	}
	if !strings.HasPrefix(result.ArtifactID, "sha256:") {
		t.Fatalf("artifact id = %q, want sha256 prefix", result.ArtifactID)
	}
	if result.ContentHash == "" || result.OriginalBytes == 0 || result.VisibleBytes == 0 {
		t.Fatalf("tool result metadata is incomplete: %#v", result)
	}
	if result.Truncated || result.RepeatedRead {
		t.Fatalf("small first read should not be truncated or repeated: %#v", result)
	}
}

// TestReadFileReadsLineRange 验证 start_line 和 end_line 只返回指定的包含边界行。
func TestReadFileReadsLineRange(t *testing.T) {
	root := t.TempDir()
	content := "line-1\nline-2\nline-3\nline-4\n"
	if err := os.WriteFile(filepath.Join(root, "list.txt"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	readFile, err := tools.NewReadFile(root)
	if err != nil {
		t.Fatal(err)
	}
	result, err := readFile.Execute(context.Background(), json.RawMessage(`{"path":"list.txt","start_line":2,"end_line":3}`))
	if err != nil {
		t.Fatal(err)
	}
	if result.Resource != "list.txt#L2-L3" {
		t.Fatalf("resource = %q, want list.txt#L2-L3", result.Resource)
	}
	if !strings.Contains(result.Observation, "line-2\nline-3") {
		t.Fatalf("observation missing selected lines: %q", result.Observation)
	}
	if strings.Contains(result.Observation, "line-1") || strings.Contains(result.Observation, "line-4") {
		t.Fatalf("observation leaked lines outside the range: %q", result.Observation)
	}
}

// TestReadFileDetectsRepeatedRead 验证同一范围、同一内容的第二次读取被标记为重复。
func TestReadFileDetectsRepeatedRead(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "note.txt"), []byte("same content"), 0o600); err != nil {
		t.Fatal(err)
	}
	readFile, err := tools.NewReadFile(root)
	if err != nil {
		t.Fatal(err)
	}
	first, err := executePath(t, readFile, "note.txt")
	if err != nil {
		t.Fatal(err)
	}
	if first.RepeatedRead {
		t.Fatalf("first read should not be repeated: %#v", first)
	}
	second, err := executePath(t, readFile, "note.txt")
	if err != nil {
		t.Fatal(err)
	}
	if !second.RepeatedRead {
		t.Fatalf("second identical read was not marked repeated: %#v", second)
	}
	if second.VisibleBytes != 0 {
		t.Fatalf("repeated read visible bytes = %d, want 0", second.VisibleBytes)
	}
	if !strings.Contains(second.Observation, "repeated_read=true") {
		t.Fatalf("repeated observation missing marker: %q", second.Observation)
	}
}

// TestReadFileRefreshesAfterContentChange 验证文件内容变化后同一范围重新提供正文。
func TestReadFileRefreshesAfterContentChange(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "note.txt")
	if err := os.WriteFile(target, []byte("version one"), 0o600); err != nil {
		t.Fatal(err)
	}
	readFile, err := tools.NewReadFile(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := executePath(t, readFile, "note.txt"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("version two"), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := executePath(t, readFile, "note.txt")
	if err != nil {
		t.Fatal(err)
	}
	if result.RepeatedRead {
		t.Fatalf("changed content was wrongly treated as repeated: %#v", result)
	}
	if !strings.Contains(result.Observation, "version two") {
		t.Fatalf("observation missing refreshed content: %q", result.Observation)
	}
}

// TestReadFileRejectsWorkspaceEscape 验证显式 ../ 不能越过工作区边界。
func TestReadFileRejectsWorkspaceEscape(t *testing.T) {
	readFile, err := tools.NewReadFile(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	_, err = executePath(t, readFile, "../secret.txt")
	if err == nil || !strings.Contains(err.Error(), "escapes workspace") {
		t.Fatalf("error = %v, want workspace escape rejection", err)
	}
}

// TestReadFileRejectsSymlinkEscape 验证工作区内链接不能指向外部文件。
func TestReadFileRejectsSymlinkEscape(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	secret := filepath.Join(outside, "secret.txt")
	if err := os.WriteFile(secret, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secret, filepath.Join(root, "link.txt")); err != nil {
		t.Skipf("symlink creation is unavailable: %v", err)
	}
	readFile, err := tools.NewReadFile(root)
	if err != nil {
		t.Fatal(err)
	}
	_, err = executePath(t, readFile, "link.txt")
	if err == nil || !strings.Contains(err.Error(), "escapes workspace") {
		t.Fatalf("error = %v, want symlink escape rejection", err)
	}
}

// TestReadFileRejectsHardExcludedFiles 验证 .env 密钥文件不会进入模型工具结果。
func TestReadFileRejectsHardExcludedFiles(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, ".env"), []byte("DASHSCOPE_API_KEY=fake"), 0o600); err != nil {
		t.Fatal(err)
	}
	readFile, err := tools.NewReadFile(root)
	if err != nil {
		t.Fatal(err)
	}
	_, err = executePath(t, readFile, ".env")
	if err == nil || !strings.Contains(err.Error(), "excluded") {
		t.Fatalf("error = %v, want hard exclusion rejection", err)
	}
	if err != nil && strings.Contains(err.Error(), "fake") {
		t.Fatalf("error leaked secret content: %v", err)
	}
}

// TestReadFileValidatesArguments 验证无效 JSON、空路径和非法行范围得到明确错误。
func TestReadFileValidatesArguments(t *testing.T) {
	readFile, err := tools.NewReadFile(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := readFile.Execute(context.Background(), json.RawMessage(`{"path":`)); err == nil || !strings.Contains(err.Error(), "decode arguments") {
		t.Fatalf("invalid JSON error = %v", err)
	}
	if _, err := readFile.Execute(context.Background(), json.RawMessage(`{}`)); err == nil || !strings.Contains(err.Error(), "path or artifact_id is required") {
		t.Fatalf("empty path error = %v", err)
	}
	if _, err := readFile.Execute(context.Background(), json.RawMessage(`{"path":"a.txt","unknown":true}`)); err == nil || !strings.Contains(err.Error(), "decode arguments") {
		t.Fatalf("unknown field error = %v", err)
	}
	if _, err := readFile.Execute(context.Background(), json.RawMessage(`{"path":"a.txt","start_line":5,"end_line":2}`)); err == nil || !strings.Contains(err.Error(), "end_line") {
		t.Fatalf("reversed range error = %v", err)
	}
}

// TestReadFileHonorsCanceledContext 验证取消发生后不再访问文件系统。
func TestReadFileHonorsCanceledContext(t *testing.T) {
	readFile, err := tools.NewReadFile(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = readFile.Execute(ctx, json.RawMessage(`{"path":"anything"}`))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
}

// TestReadFileVirtualizesLargeOutput 验证超过 8 KiB 的结果只返回受限预览并保留完整字节数。
func TestReadFileVirtualizesLargeOutput(t *testing.T) {
	root := t.TempDir()
	large := strings.Repeat("x", (16<<10)) + "\n"
	if err := os.WriteFile(filepath.Join(root, "large.txt"), []byte(large), 0o600); err != nil {
		t.Fatal(err)
	}
	readFile, err := tools.NewReadFile(root)
	if err != nil {
		t.Fatal(err)
	}
	result, err := executePath(t, readFile, "large.txt")
	if err != nil {
		t.Fatal(err)
	}
	if !result.Truncated {
		t.Fatalf("large output was not truncated: %#v", result)
	}
	if result.VisibleBytes > 8<<10 {
		t.Fatalf("visible bytes = %d, want at most %d", result.VisibleBytes, 8<<10)
	}
	if result.OriginalBytes <= result.VisibleBytes {
		t.Fatalf("original bytes = %d should exceed visible bytes = %d", result.OriginalBytes, result.VisibleBytes)
	}
	if !strings.Contains(result.Observation, "[preview truncated") {
		t.Fatalf("observation missing truncation marker: %q", result.Observation)
	}
}

// TestReadFileFetchesArtifactLines 验证 artifact_id 回取：从已存完整结果按行取回，不读磁盘。
func TestReadFileFetchesArtifactLines(t *testing.T) {
	root := t.TempDir()
	content := ""
	for i := 1; i <= 12; i++ {
		content += fmt.Sprintf("artifact-line-%02d\n", i)
	}
	if err := os.WriteFile(filepath.Join(root, "data.txt"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	readFile, err := tools.NewReadFile(root)
	if err != nil {
		t.Fatal(err)
	}
	full, err := executePath(t, readFile, "data.txt")
	if err != nil {
		t.Fatal(err)
	}
	if full.ArtifactID == "" {
		t.Fatal("expected artifact id from full read")
	}

	// 完整结果的 Artifact 第 1 行是 path= 头部，文件正文从第 2 行开始；
	// 因此回取第 4 到 6 行对应文件的第 3 到 5 行。
	raw, err := json.Marshal(map[string]any{"artifact_id": full.ArtifactID, "start_line": 4, "end_line": 6})
	if err != nil {
		t.Fatal(err)
	}
	result, err := readFile.Execute(context.Background(), raw)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(result.Resource, "artifact:") || !strings.Contains(result.Resource, "#L4-L6") {
		t.Fatalf("resource = %q", result.Resource)
	}
	if !strings.Contains(result.Observation, "artifact-line-03") || !strings.Contains(result.Observation, "artifact-line-05") {
		t.Fatalf("observation = %q", result.Observation)
	}
	if strings.Contains(result.Observation, "artifact-line-06") {
		t.Fatalf("observation should not contain line 6: %q", result.Observation)
	}
	if result.RepeatedRead {
		t.Fatal("artifact fetch must never be flagged as repeated read")
	}

	missing, err := json.Marshal(map[string]string{"artifact_id": "sha256:deadbeef"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := readFile.Execute(context.Background(), missing); err == nil || !strings.Contains(err.Error(), "fetch artifact") {
		t.Fatalf("missing artifact error = %v", err)
	}
}
