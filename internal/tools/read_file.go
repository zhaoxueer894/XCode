package tools

// 提供Agent可调用的工具

import (
	"context"       // 在读取文件前响应取消或超时。
	"crypto/sha256" // 计算完整源文件内容哈希。
	"encoding/hex"  // 把源文件哈希转换成十六进制字符串。
	"encoding/json" // 接收模型提供的 JSON 参数。
	"fmt"           // 构造路径、范围和文件内容错误。
	"io"            // 限制单次源文件读取上限。
	"os"            // 打开通过工作区边界检查的文件。
	"strings"       // 按换行符拆分文本文件。
	"unicode/utf8"  // 拒绝明显的非 UTF-8 文件。

	"xcode/internal/harness" // 返回 ToolSpec 与 ToolResult。
)

const (
	maxRangeLines    = 1000    // 一次 read_file 最多选择 1000 行，鼓励模型缩小读取范围。
	maxReadFileBytes = 8 << 20 // 单个源文件最多读取 8 MiB，为结果标题保留 Artifact 大小余量。
)

// ReadFile 使用共享只读运行环境读取工作区内的 UTF-8 文本文件。
type ReadFile struct {
	runtime *readOnlyRuntime // 保存 Workspace、Artifact Store 和 ReadSet。
}

// Spec 返回 read_file 的名称、用途和可选行范围参数。
func (t *ReadFile) Spec() harness.ToolSpec {
	return harness.ToolSpec{
		Name:        "read_file",
		Description: "Read a UTF-8 text file or an inclusive line range inside the workspace",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path":        map[string]any{"type": "string"},
				"artifact_id": map[string]any{"type": "string"},
				"start_line":  map[string]any{"type": "integer", "minimum": 1},
				"end_line":    map[string]any{"type": "integer", "minimum": 1},
			},
			"required":             []string{},
			"additionalProperties": false,
		},
	}
}

// Execute 校验路径和行范围，保存完整范围结果，并返回受限模型预览。
func (t *ReadFile) Execute(ctx context.Context, raw json.RawMessage) (harness.ToolResult, error) {
	if err := ctx.Err(); err != nil {
		return harness.ToolResult{}, err
	}
	var args struct {
		Path       string `json:"path"`        // 保存模型要求读取的工作区相对路径；artifact_id 模式下可留空。
		ArtifactID string `json:"artifact_id"` // 保存可选的 Artifact 编号；设置后从 Artifact Store 回取而不读磁盘。
		StartLine  int    `json:"start_line"`  // 保存包含边界的起始行；0 表示使用第 1 行。
		EndLine    int    `json:"end_line"`    // 保存包含边界的结束行；0 表示最多向后 1000 行。
	}
	if err := decodeArguments(raw, &args); err != nil {
		return harness.ToolResult{}, err
	}
	if strings.TrimSpace(args.Path) == "" && strings.TrimSpace(args.ArtifactID) == "" {
		return harness.ToolResult{}, fmt.Errorf("path or artifact_id is required")
	}
	if strings.TrimSpace(args.ArtifactID) != "" {
		return t.readArtifact(ctx, args.ArtifactID, args.StartLine, args.EndLine)
	}
	if args.StartLine < 0 || args.EndLine < 0 {
		return harness.ToolResult{}, fmt.Errorf("line numbers must be positive")
	}
	if args.StartLine == 0 {
		args.StartLine = 1
	}
	if args.EndLine > 0 && args.EndLine < args.StartLine {
		return harness.ToolResult{}, fmt.Errorf("end_line must be greater than or equal to start_line")
	}

	target, relative, info, err := t.runtime.workspace.ResolveExisting(args.Path)
	if err != nil {
		return harness.ToolResult{}, err
	}
	if info.IsDir() {
		return harness.ToolResult{}, fmt.Errorf("path %q is a directory", args.Path)
	}
	if !info.Mode().IsRegular() {
		return harness.ToolResult{}, fmt.Errorf("path %q is not a regular file", args.Path)
	}
	if t.runtime.workspace.IsHardExcluded(relative) {
		return harness.ToolResult{}, fmt.Errorf("path %q is excluded from model tool results", args.Path)
	}

	file, err := os.Open(target)
	if err != nil {
		return harness.ToolResult{}, fmt.Errorf("open %q: %w", args.Path, err)
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxReadFileBytes+1))
	if err != nil {
		return harness.ToolResult{}, fmt.Errorf("read %q: %w", args.Path, err)
	}
	if len(data) > maxReadFileBytes {
		return harness.ToolResult{}, fmt.Errorf("file %q exceeds %d bytes; use search_text to narrow the target", args.Path, maxReadFileBytes)
	}
	if !utf8.Valid(data) {
		return harness.ToolResult{}, fmt.Errorf("file %q is not valid UTF-8 text", args.Path)
	}

	lines := strings.Split(string(data), "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	totalLines := len(lines)
	digest := sha256.Sum256(data)
	fileHash := hex.EncodeToString(digest[:])
	if totalLines == 0 {
		if args.StartLine != 1 {
			return harness.ToolResult{}, fmt.Errorf("start_line %d exceeds empty file", args.StartLine)
		}
		resource := relative + "#L0-L0"
		content := []byte(fmt.Sprintf("path=%s lines=0-0/0\n", relative))
		return t.runtime.results.build(ctx, resource, resource, fileHash, content)
	}
	if args.StartLine > totalLines {
		return harness.ToolResult{}, fmt.Errorf("start_line %d exceeds file line count %d", args.StartLine, totalLines)
	}

	actualEnd := args.EndLine
	if actualEnd == 0 || actualEnd > totalLines {
		actualEnd = totalLines
	}
	maximumEnd := args.StartLine + maxRangeLines - 1
	if actualEnd > maximumEnd {
		actualEnd = maximumEnd
	}
	selected := ""
	if totalLines > 0 {
		selected = strings.Join(lines[args.StartLine-1:actualEnd], "\n")
	}
	content := []byte(fmt.Sprintf(
		"path=%s lines=%d-%d/%d\n%s",
		relative,
		args.StartLine,
		actualEnd,
		totalLines,
		selected,
	))
	resource := fmt.Sprintf("%s#L%d-L%d", relative, args.StartLine, actualEnd)
	return t.runtime.results.build(ctx, resource, resource, fileHash, content)
}

// readArtifact 从 Artifact Store 回取一份不可变结果并按行裁剪；
// Artifact 内容寻址不可变，因此本路径不经过 ReadSet，也不会重复读取磁盘。
func (t *ReadFile) readArtifact(ctx context.Context, artifactID string, startLine, endLine int) (harness.ToolResult, error) {
	if startLine < 0 || endLine < 0 {
		return harness.ToolResult{}, fmt.Errorf("line numbers must be positive")
	}
	if startLine == 0 {
		startLine = 1
	}
	if endLine > 0 && endLine < startLine {
		return harness.ToolResult{}, fmt.Errorf("end_line must be greater than or equal to start_line")
	}
	data, err := t.runtime.results.store.Get(ctx, artifactID)
	if err != nil {
		return harness.ToolResult{}, fmt.Errorf("fetch artifact %s: %w", artifactID, err)
	}
	lines := strings.Split(string(data), "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	totalLines := len(lines)
	if startLine > totalLines {
		return harness.ToolResult{}, fmt.Errorf("start_line %d exceeds artifact line count %d", startLine, totalLines)
	}
	actualEnd := endLine
	if actualEnd == 0 || actualEnd > totalLines {
		actualEnd = totalLines
	}
	if actualEnd-startLine+1 > maxRangeLines {
		actualEnd = startLine + maxRangeLines - 1
	}
	selected := strings.Join(lines[startLine-1:actualEnd], "\n")
	resource := fmt.Sprintf("artifact:%s#L%d-L%d", artifactID, startLine, actualEnd)
	content := []byte(fmt.Sprintf(
		"artifact=%s lines=%d-%d/%d\n%s",
		artifactID,
		startLine,
		actualEnd,
		totalLines,
		selected,
	))
	// readKey 传空：Artifact 不可变，重复回取永远安全，不需要 ReadSet 参与判定。
	return t.runtime.results.build(ctx, resource, "", "", content)
}
