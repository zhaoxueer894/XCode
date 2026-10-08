package recovery

// 本文件实现 apply_patch 的崩溃对账：用三方证据判定一次中断的补丁的真实效果。
//   证据一：tool.begin 记录里保存的补丁参数（路径、操作类型、替换文本）。
//   证据二：checkpoint manifest 中该文件最后一次写前快照（基线哈希与基线内容）。
//   证据三：工作区中该文件的当前实际内容哈希。
// 判定结论：applied（补丁已生效，不重放）、retry（补丁未生效，可安全重试）、
// conflict（与两种预期都不符，放弃自动恢复转人工处理）。

import (
	"crypto/sha256" // 计算期望内容与当前内容的哈希做比对。
	"encoding/hex"  // 把哈希转换成十六进制文本。
	"encoding/json" // 解析 tool.begin 保存的补丁参数。
	"fmt"           // 构造判定说明与错误。
	"io"            // 限制对账读取的文件大小。
	"os"            // 读取工作区文件的当前内容。
	"path/filepath" // 规范化相对路径并拒绝越界。
	"strings"       // 按精确匹配重构补丁应产生的结果内容。

	"xcode/internal/checkpoint" // 读取基线快照的元数据与内容。
	"xcode/internal/harness"    // 复用 ToolCall 类型。
)

// maxReconcileFileBytes 限制对账读取的单个文件大小；与 apply_patch 的写入上限保持同一量级。
const maxReconcileFileBytes = 8 << 20

// patchArgs 是对账关心的补丁参数子集；字段与 tools 包的 applyPatchArgs 保持同名同义。
type patchArgs struct {
	Path       string `json:"path"`                  // 保存目标文件的工作区相对路径。
	Operation  string `json:"operation"`             // 保存操作类型：create、update、delete。
	Content    string `json:"content,omitempty"`     // create 时保存新文件的完整内容。
	OldString  string `json:"old_string,omitempty"`  // update 时保存要被替换的精确文本。
	NewString  string `json:"new_string,omitempty"`  // update 时保存替换后的文本。
	ReplaceAll bool   `json:"replace_all,omitempty"` // 与原调用的替换全部出现位置语义一致。
}

// reconcilePatch 判定一次中断的 apply_patch 的真实效果；无法给出可靠结论时返回 conflict。
func reconcilePatch(in Input, call harness.ToolCall) (Verdict, string, error) {
	var args patchArgs
	if err := json.Unmarshal(call.Arguments, &args); err != nil {
		return VerdictConflict, "", fmt.Errorf("decode patch arguments: %w", err)
	}
	path, err := cleanRelative(args.Path)
	if err != nil {
		return VerdictConflict, "", fmt.Errorf("patch path: %w", err)
	}
	exists, currentHash, err := readFileState(in.Workspace, path)
	if err != nil {
		return VerdictConflict, "", err
	}
	base, hasBase := latestCheckpoint(in.Checkpoints, path)

	switch args.Operation {
	case "create":
		switch {
		case !exists:
			return VerdictRetry, "target file still absent; create never took effect", nil
		case currentHash == contentHash([]byte(args.Content)):
			return VerdictApplied, "target file content matches the requested new file", nil
		default:
			return VerdictConflict, "target file exists with different content; manual decision required", nil
		}
	case "delete":
		switch {
		case !exists:
			return VerdictApplied, "target file is already gone; delete took effect", nil
		case hasBase && base.Existed && base.ContentHash == currentHash:
			return VerdictRetry, "target file still matches its pre-patch baseline; delete never took effect", nil
		default:
			return VerdictConflict, "target file changed after the baseline; manual decision required", nil
		}
	case "update":
		return reconcileUpdate(in, args, path, exists, currentHash, base, hasBase)
	default:
		return VerdictConflict, "", fmt.Errorf("unknown patch operation %q", args.Operation)
	}
}

// reconcileUpdate 处理 update 操作：用基线内容重构补丁应有的结果，再与当前文件比对。
func reconcileUpdate(
	in Input,
	args patchArgs,
	path string,
	exists bool,
	currentHash string,
	base checkpoint.Entry,
	hasBase bool,
) (Verdict, string, error) {
	switch {
	case !exists:
		return VerdictConflict, "target file is gone but update requires it to exist; manual decision required", nil
	case !hasBase || !base.Existed:
		// 执行管线保证 update 前必有快照（核心不变量 5）；缺失说明状态目录被破坏。
		return VerdictConflict, "no pre-patch baseline checkpoint found; cannot verify the patch", nil
	case base.ContentHash == currentHash:
		return VerdictRetry, "target file still matches its pre-patch baseline; update never took effect", nil
	}
	content, err := in.Store.ReadBlob(base)
	if err != nil {
		return VerdictConflict, "", fmt.Errorf("read baseline blob: %w", err)
	}
	occurrences := strings.Count(string(content), args.OldString)
	if occurrences == 0 || (occurrences > 1 && !args.ReplaceAll) {
		// 原调用当时就会以参数错误返回，不产生副作用；重试只是复现同一个错误。
		return VerdictRetry, "original call would have failed validation without side effects; safe to retry", nil
	}
	expected := strings.Replace(string(content), args.OldString, args.NewString, -1)
	if currentHash == contentHash([]byte(expected)) {
		return VerdictApplied, "target file content matches the patched result", nil
	}
	return VerdictConflict, "target file matches neither baseline nor patched result; manual decision required", nil
}

// latestCheckpoint 返回该文件在 manifest 中最后一次的单文件快照；没有时返回 false。
func latestCheckpoint(entries []checkpoint.Entry, path string) (checkpoint.Entry, bool) {
	for i := len(entries) - 1; i >= 0; i-- {
		entry := entries[i]
		if !entry.WholeWorkspace && entry.Path == path {
			return entry, true
		}
	}
	return checkpoint.Entry{}, false
}

// readFileState 返回文件是否存在与其完整内容的 SHA-256；读取失败视为对账证据不足。
func readFileState(workspace, relative string) (bool, string, error) {
	root, err := os.OpenRoot(workspace)
	if err != nil {
		return false, "", fmt.Errorf("open workspace: %w", err)
	}
	defer root.Close()
	info, statErr := root.Lstat(relative)
	if os.IsNotExist(statErr) {
		return false, "", nil
	}
	if statErr != nil {
		return false, "", fmt.Errorf("inspect %q: %w", relative, statErr)
	}
	if !info.Mode().IsRegular() {
		return false, "", fmt.Errorf("reconcile target %q is not a regular file", relative)
	}
	file, err := root.Open(relative)
	if err != nil {
		return false, "", fmt.Errorf("open %q: %w", relative, err)
	}
	defer file.Close()
	digest := sha256.New()
	size, err := io.Copy(digest, io.LimitReader(file, maxReconcileFileBytes+1))
	if err != nil {
		return false, "", fmt.Errorf("read %q: %w", relative, err)
	}
	if size > maxReconcileFileBytes {
		return false, "", fmt.Errorf("reconcile target %q exceeds %d bytes", relative, maxReconcileFileBytes)
	}
	return true, hex.EncodeToString(digest.Sum(nil)), nil
}

// contentHash 返回内容的 SHA-256 十六进制文本。
func contentHash(content []byte) string {
	digest := sha256.Sum256(content)
	return hex.EncodeToString(digest[:])
}

// cleanRelative 规范化补丁参数中的相对路径，拒绝绝对路径与父目录逃逸。
func cleanRelative(input string) (string, error) {
	if strings.TrimSpace(input) == "" {
		return "", fmt.Errorf("path is required")
	}
	clean := filepath.Clean(filepath.FromSlash(input))
	if filepath.IsAbs(clean) || filepath.VolumeName(clean) != "" || clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path escapes workspace")
	}
	return filepath.ToSlash(clean), nil
}
