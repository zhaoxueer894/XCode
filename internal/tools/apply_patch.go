package tools

// 本文件实现唯一的文件写入工具：以精确匹配替换为核心的 apply_patch。

import (
	"context"       // 在写入前响应取消或超时。
	"crypto/sha256" // 计算写入前后文件内容哈希。
	"encoding/hex"  // 把哈希转换成十六进制字符串。
	"encoding/json" // 接收模型提供的 JSON 参数。
	"fmt"           // 构造参数、路径与匹配错误。
	"io"            // 限制单个源文件的读取上限。
	"os"            // 读取、写入与删除工作区文件。
	"strings"       // 执行精确文本匹配与替换。
	"unicode/utf8"  // 拒绝对非 UTF-8 文件做文本替换。

	"xcode/internal/executor"  // 声明执行前意图，进入统一管线。
	"xcode/internal/harness"   // 返回 ToolSpec 与 ToolResult。
	"xcode/internal/workspace" // 用受根目录约束的原子写入完成修改。
)

// ApplyPatch 在工作区内创建、修改或删除单个 UTF-8 文本文件。
type ApplyPatch struct {
	runtime *readOnlyRuntime // 保存共享 Workspace、Index、ReadSet 和结果控制器。
}

// applyPatchArgs 保存 apply_patch 的全部参数；operation 决定其余字段的取值要求。
type applyPatchArgs struct {
	Path                string `json:"path"`                            // 保存目标文件的工作区相对路径。
	Operation           string `json:"operation"`                       // 保存操作类型：create、update 或 delete。
	Content             string `json:"content,omitempty"`               // create 时保存新文件的完整内容。
	OldString           string `json:"old_string,omitempty"`            // update 时保存要被替换的精确文本。
	NewString           string `json:"new_string,omitempty"`            // update 时保存替换后的文本。
	ReplaceAll          bool   `json:"replace_all,omitempty"`           // update 时允许替换全部出现位置；默认要求唯一匹配。
	ExpectedContentHash string `json:"expected_content_hash,omitempty"` // 可选：写入前文件必须匹配的 SHA-256，防止基于过期内容修改。
}

// Spec 返回 apply_patch 的名称、用途和参数约束。
func (t *ApplyPatch) Spec() harness.ToolSpec {
	return harness.ToolSpec{
		Name:        "apply_patch",
		Description: "Create, update (exact string replacement) or delete one UTF-8 text file inside the workspace",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path":                  map[string]any{"type": "string"},
				"operation":             map[string]any{"type": "string", "enum": []string{"create", "update", "delete"}},
				"content":               map[string]any{"type": "string"},
				"old_string":            map[string]any{"type": "string"},
				"new_string":            map[string]any{"type": "string"},
				"replace_all":           map[string]any{"type": "boolean"},
				"expected_content_hash": map[string]any{"type": "string"},
			},
			"required":             []string{"path", "operation"},
			"additionalProperties": false,
		},
	}
}

// DescribeIntent 在执行前向统一 Executor 声明：本调用会修改哪个工作区文件。
func (t *ApplyPatch) DescribeIntent(raw json.RawMessage) (executor.Intent, error) {
	var args applyPatchArgs
	if err := decodeArguments(raw, &args); err != nil {
		return executor.Intent{}, err
	}
	if err := validatePatchArgs(args); err != nil {
		return executor.Intent{}, err
	}
	_, relative, err := t.runtime.workspace.ResolveForWrite(args.Path)
	if err != nil {
		return executor.Intent{}, err
	}
	if t.runtime.workspace.IsHardExcluded(relative) {
		return executor.Intent{}, fmt.Errorf("path %q is excluded from model tool access", args.Path)
	}
	args.Path = relative
	normalized, err := json.Marshal(args)
	if err != nil {
		return executor.Intent{}, err
	}
	return executor.Intent{Mutating: true, Paths: []string{relative}, Arguments: normalized}, nil
}

// Execute 校验参数与边界后执行写入，并在成功后刷新索引与 ReadSet。
func (t *ApplyPatch) Execute(ctx context.Context, raw json.RawMessage) (harness.ToolResult, error) {
	if err := ctx.Err(); err != nil {
		return harness.ToolResult{}, err
	}
	var args applyPatchArgs
	if err := decodeArguments(raw, &args); err != nil {
		return harness.ToolResult{}, err
	}
	if err := validatePatchArgs(args); err != nil {
		return harness.ToolResult{}, err
	}
	_, relative, err := t.runtime.workspace.ResolveForWrite(args.Path)
	if err != nil {
		return harness.ToolResult{}, err
	}
	if t.runtime.workspace.IsHardExcluded(relative) {
		return harness.ToolResult{}, fmt.Errorf("path %q is excluded from model tool access", args.Path)
	}

	root, err := os.OpenRoot(t.runtime.workspace.Root())
	if err != nil {
		return harness.ToolResult{}, err
	}
	defer root.Close()
	var summary string
	switch args.Operation {
	case "create":
		summary, err = t.create(ctx, root, relative, args)
	case "update":
		summary, err = t.update(ctx, root, relative, args)
	case "delete":
		summary, err = t.remove(ctx, root, relative, args)
	default:
		return harness.ToolResult{}, fmt.Errorf("operation must be create, update or delete")
	}
	if err != nil {
		return harness.ToolResult{}, err
	}

	// 写入成功后立即失效旧的索引记录与读取记录，避免模型继续使用过期内容。
	if err := t.runtime.invalidate(ctx, relative); err != nil {
		summary += "\nWARNING: file changed, but index refresh failed: " + err.Error()
	}
	resource := "apply_patch:" + relative
	return t.runtime.results.build(ctx, resource, "", "", []byte(summary))
}

// create 创建一个不存在的新文件，并按需创建工作区内的父目录。
func (t *ApplyPatch) create(ctx context.Context, root *os.Root, relative string, args applyPatchArgs) (string, error) {
	if args.OldString != "" || args.NewString != "" {
		return "", fmt.Errorf("create does not accept old_string or new_string")
	}
	if _, err := root.Lstat(relative); err == nil {
		return "", fmt.Errorf("path %q already exists; use update instead", relative)
	} else if !os.IsNotExist(err) {
		return "", fmt.Errorf("inspect %q: %w", relative, err)
	}
	if !utf8.ValidString(args.Content) {
		return "", fmt.Errorf("content must be valid UTF-8 text")
	}
	if err := workspace.WriteFile(ctx, root, relative, []byte(args.Content), 0o644, true); err != nil {
		return "", fmt.Errorf("create %q: %w", relative, err)
	}
	digest := sha256.Sum256([]byte(args.Content))
	return fmt.Sprintf(
		"path=%s operation=create bytes=%d new_hash=%s",
		relative, len(args.Content), hex.EncodeToString(digest[:]),
	), nil
}

// update 用精确文本匹配替换文件内容；默认要求 old_string 恰好出现一次。
func (t *ApplyPatch) update(ctx context.Context, root *os.Root, relative string, args applyPatchArgs) (string, error) {
	if args.OldString == "" {
		return "", fmt.Errorf("update requires a non-empty old_string")
	}
	if args.OldString == args.NewString {
		return "", fmt.Errorf("old_string and new_string are identical")
	}
	info, err := root.Lstat(relative)
	if err != nil {
		return "", fmt.Errorf("inspect %q: %w", relative, err)
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("path %q is not a regular file", relative)
	}
	file, err := root.Open(relative)
	if err != nil {
		return "", fmt.Errorf("open %q: %w", relative, err)
	}
	data, err := io.ReadAll(io.LimitReader(file, maxReadFileBytes+1))
	closeErr := file.Close()
	if err != nil {
		return "", fmt.Errorf("read %q: %w", relative, err)
	}
	if closeErr != nil {
		return "", fmt.Errorf("close %q: %w", relative, closeErr)
	}
	if len(data) > maxReadFileBytes {
		return "", fmt.Errorf("file %q exceeds %d bytes", relative, maxReadFileBytes)
	}
	if !utf8.Valid(data) {
		return "", fmt.Errorf("file %q is not valid UTF-8 text", relative)
	}

	oldDigest := sha256.Sum256(data)
	oldHash := hex.EncodeToString(oldDigest[:])
	// 基线哈希不匹配说明模型基于过期内容提出修改，必须先重新读取。
	if args.ExpectedContentHash != "" && args.ExpectedContentHash != oldHash {
		return "", fmt.Errorf(
			"file %q content hash %s does not match expected %s; re-read the file before patching",
			relative, oldHash, args.ExpectedContentHash,
		)
	}
	occurrences := strings.Count(string(data), args.OldString)
	if occurrences == 0 {
		return "", fmt.Errorf("old_string not found in %q", relative)
	}
	if occurrences > 1 && !args.ReplaceAll {
		return "", fmt.Errorf(
			"old_string appears %d times in %q; provide a longer unique match or set replace_all",
			occurrences, relative,
		)
	}
	updated := strings.Replace(string(data), args.OldString, args.NewString, -1)
	if len(updated) > maxReadFileBytes {
		return "", fmt.Errorf("updated file exceeds %d bytes", maxReadFileBytes)
	}
	if err := workspace.WriteFile(ctx, root, relative, []byte(updated), info.Mode().Perm(), false); err != nil {
		return "", fmt.Errorf("write %q: %w", relative, err)
	}
	newDigest := sha256.Sum256([]byte(updated))
	return fmt.Sprintf(
		"path=%s operation=update replaced=%d bytes=%d->%d old_hash=%s new_hash=%s",
		relative, occurrences, len(data), len(updated), oldHash, hex.EncodeToString(newDigest[:]),
	), nil
}

// remove 删除一个已存在的普通文件。
func (t *ApplyPatch) remove(ctx context.Context, root *os.Root, relative string, args applyPatchArgs) (string, error) {
	if args.Content != "" || args.OldString != "" || args.NewString != "" {
		return "", fmt.Errorf("delete does not accept content, old_string or new_string")
	}
	info, err := root.Lstat(relative)
	if err != nil {
		return "", fmt.Errorf("inspect %q: %w", relative, err)
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("path %q is not a regular file", relative)
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if args.ExpectedContentHash != "" {
		file, err := root.Open(relative)
		if err != nil {
			return "", err
		}
		data, err := io.ReadAll(io.LimitReader(file, maxReadFileBytes+1))
		closeErr := file.Close()
		if err != nil {
			return "", err
		}
		if closeErr != nil {
			return "", closeErr
		}
		digest := sha256.Sum256(data)
		if len(data) > maxReadFileBytes || hex.EncodeToString(digest[:]) != args.ExpectedContentHash {
			return "", fmt.Errorf("content hash does not match expected; re-read before deleting")
		}
	}
	if err := root.Remove(relative); err != nil {
		return "", fmt.Errorf("delete %q: %w", relative, err)
	}
	return fmt.Sprintf("path=%s operation=delete bytes=%d", relative, info.Size()), nil
}

// validatePatchArgs 在策略和快照之前拒绝无效操作及无意义的字段组合。
func validatePatchArgs(args applyPatchArgs) error {
	if strings.TrimSpace(args.Path) == "" {
		return fmt.Errorf("path is required")
	}
	if len(args.Content) > maxReadFileBytes {
		return fmt.Errorf("content exceeds %d bytes", maxReadFileBytes)
	}
	if args.ExpectedContentHash != "" {
		decoded, err := hex.DecodeString(args.ExpectedContentHash)
		if err != nil || len(decoded) != sha256.Size {
			return fmt.Errorf("expected_content_hash must be a SHA-256 hex digest")
		}
	}
	switch args.Operation {
	case "create":
		if args.OldString != "" || args.NewString != "" || args.ReplaceAll || args.ExpectedContentHash != "" {
			return fmt.Errorf("create accepts only path, operation and content")
		}
	case "update":
		if args.Content != "" || args.OldString == "" || args.OldString == args.NewString {
			return fmt.Errorf("update requires distinct old_string and new_string, without content")
		}
	case "delete":
		if args.Content != "" || args.OldString != "" || args.NewString != "" || args.ReplaceAll {
			return fmt.Errorf("delete does not accept replacement fields")
		}
	default:
		return fmt.Errorf("operation must be create, update or delete")
	}
	return nil
}
