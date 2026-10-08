package tools

// 本文件把工作区索引中的文件和代码摘要提供给模型。

import (
	"context"       // 在产生结果前响应取消。
	"encoding/json" // 接收模型提供的 JSON 参数。
	"fmt"           // 生成稳定的文件摘要行。
	"path/filepath" // 比较规范相对路径层级。
	"sort"          // 对符号和依赖摘要排序。
	"strings"       // 拼接符号、依赖和多行输出。

	"xcode/internal/harness"   // 返回 list_files 工具声明和结果。
	"xcode/internal/workspace" // 使用工作区索引的 FileRecord。
)

const maxListFiles = 1000 // 单次最多列出 1000 个文件，防止列表无限增长。

// ListFiles 从一次 Run 的工作区索引中列出文件。
type ListFiles struct {
	runtime *readOnlyRuntime // 保存共享 Workspace、Index 和结果控制器。
}

// Spec 返回列表路径、深度、数量和详细摘要参数。
func (t *ListFiles) Spec() harness.ToolSpec {
	return harness.ToolSpec{
		Name:        "list_files",
		Description: "List indexed workspace files with optional language, hash, Go symbols, and imports",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path":            map[string]any{"type": "string"},
				"max_depth":       map[string]any{"type": "integer", "minimum": 0, "maximum": 32},
				"limit":           map[string]any{"type": "integer", "minimum": 1, "maximum": maxListFiles},
				"include_details": map[string]any{"type": "boolean"},
			},
			"additionalProperties": false,
		},
	}
}

// Execute 过滤工作区索引，并把完整列表保存为 Artifact、预览返回给模型。
func (t *ListFiles) Execute(ctx context.Context, raw json.RawMessage) (harness.ToolResult, error) {
	if err := ctx.Err(); err != nil {
		return harness.ToolResult{}, err
	}
	if t.runtime.index == nil {
		return harness.ToolResult{}, fmt.Errorf("workspace index unavailable after a change; start a new run")
	}
	var args struct {
		Path           string `json:"path"`            // 保存列表起点；空值表示工作区根目录。
		MaxDepth       *int   `json:"max_depth"`       // 保存起点以下允许展示的目录深度；nil 表示使用默认值 4。
		Limit          int    `json:"limit"`           // 保存最多返回的文件数量。
		IncludeDetails bool   `json:"include_details"` // 表示是否展示索引中的语言、哈希、符号和依赖。
	}
	if err := decodeArguments(raw, &args); err != nil {
		return harness.ToolResult{}, err
	}
	if strings.TrimSpace(args.Path) == "" {
		args.Path = "."
	}
	if args.MaxDepth == nil {
		defaultDepth := 4
		args.MaxDepth = &defaultDepth
	}
	if *args.MaxDepth < 0 || *args.MaxDepth > 32 {
		return harness.ToolResult{}, fmt.Errorf("max_depth must be between 0 and 32")
	}
	if args.Limit == 0 {
		args.Limit = 200
	}
	if args.Limit < 1 || args.Limit > maxListFiles {
		return harness.ToolResult{}, fmt.Errorf("limit must be between 1 and %d", maxListFiles)
	}

	_, base, info, err := t.runtime.workspace.ResolveExisting(args.Path)
	if err != nil {
		return harness.ToolResult{}, err
	}
	if t.runtime.workspace.IsDiscoveryIgnored(base, info.IsDir()) {
		return harness.ToolResult{}, fmt.Errorf("path %q is outside the default discovery view", args.Path)
	}

	records := make([]workspace.FileRecord, 0)
	for _, record := range t.runtime.index.Files() {
		if !withinListDepth(record.Path, base, info.IsDir(), *args.MaxDepth) {
			continue
		}
		records = append(records, record)
		if len(records) == args.Limit {
			break
		}
	}
	lines := []string{fmt.Sprintf(
		"indexed_files=%d shown=%d languages=%s",
		len(t.runtime.index.Files()),
		len(records),
		t.runtime.index.LanguageSummary(),
	)}
	for _, record := range records {
		if !args.IncludeDetails {
			lines = append(lines, record.Path)
			continue
		}
		lines = append(lines, fmt.Sprintf(
			"%s | language=%s bytes=%d sha256=%s symbols=%s imports=%s",
			record.Path,
			record.Language,
			record.SizeBytes,
			record.ContentHash,
			joinSummary(record.Symbols),
			joinSummary(record.Dependencies),
		))
	}
	content := []byte(strings.Join(lines, "\n"))
	resource := "list_files:" + base
	return t.runtime.results.build(ctx, resource, "", "", content)
}

// withinListDepth 判断文件是否位于起点内并且没有超过允许目录深度。
func withinListDepth(filePath, base string, baseIsDirectory bool, maxDepth int) bool {
	if !baseIsDirectory {
		return filePath == base
	}
	if base == "." {
		return strings.Count(filePath, "/") <= maxDepth
	}
	prefix := strings.TrimSuffix(filepath.ToSlash(base), "/") + "/"
	if !strings.HasPrefix(filePath, prefix) {
		return false
	}
	remainder := strings.TrimPrefix(filePath, prefix)
	return strings.Count(remainder, "/") <= maxDepth
}

// joinSummary 把空摘要显示为短横线，非空摘要按逗号连接。
func joinSummary(values []string) string {
	if len(values) == 0 {
		return "-"
	}
	copyOfValues := append([]string(nil), values...)
	sort.Strings(copyOfValues)
	return strings.Join(copyOfValues, ",")
}
