// Package checkpoint 在每次修改工作区文件前保存可恢复的旧内容快照。
package checkpoint

import (
	"bufio"                    // 按行读取 manifest.jsonl。
	"context"                  // 在读写快照前响应取消。
	"crypto/rand"              // 为不同进程创建唯一快照编号。
	"crypto/sha256"            // 计算旧内容哈希，作为快照身份的一部分。
	"encoding/hex"             // 把哈希转换成十六进制字符串。
	"encoding/json"            // 编码与解析 manifest 中的 Entry。
	"fmt"                      // 构造路径与快照错误。
	"io"                       // 限制单个文件的快照大小。
	"os"                       // 读取旧文件并写入快照与 manifest。
	"path/filepath"            // 生成快照文件与目标文件的真实路径。
	"strings"                  // 校验路径与 ID 格式。
	"sync"                     // 保证序号与 manifest 追加在并发下一致。
	"time"                     // 记录快照创建时间并生成 ID。
	"xcode/internal/workspace" // 复用硬性排除和受根目录约束的原子写入。
)

const maxCheckpointBytes = 8 << 20 // 单个文件快照上限 8 MiB，与 read_file 的读取上限一致。

// Entry 保存一次写前快照的完整身份；manifest.jsonl 中每行一个 Entry。
type Entry struct {
	Workspace      string    `json:"workspace"`                 // 保存工作区身份哈希，拒绝跨工作区恢复。
	Files          []Entry   `json:"files,omitempty"`           // 工作区快照保存各文件的旧状态；单文件快照为空。
	WholeWorkspace bool      `json:"whole_workspace,omitempty"` // 表示恢复范围为全部非硬性排除的普通文件。
	ID             string    `json:"id"`                        // 保存快照唯一编号，恢复时使用。
	Time           time.Time `json:"time"`                      // 保存快照创建的 UTC 时间。
	Path           string    `json:"path"`                      // 保存被保护文件相对工作区根目录的斜杠路径。
	Existed        bool      `json:"existed"`                   // 表示写入前该文件是否存在；不存在时恢复动作是删除新文件。
	ContentHash    string    `json:"content_hash,omitempty"`    // 保存写入前完整文件内容的 SHA-256。
	SizeBytes      int       `json:"size_bytes"`                // 保存写入前文件的字节数。
	FileMode       uint32    `json:"file_mode,omitempty"`       // 保存写入前文件的权限位，恢复时原样还原。
}

// Store 把快照内容保存到受权限保护的本地目录，并用 manifest 记录快照顺序。
type Store struct {
	mu            sync.Mutex // 保证序号分配与 manifest 追加的原子性。
	workspaceRoot string     // 保存解析符号链接后的工作区绝对根路径。
	root          string     // 保存快照目录的绝对路径。
}

// NewStore 创建快照根目录，并绑定它所保护的工作区。
func NewStore(root, workspaceRoot string) (*Store, error) {
	if strings.TrimSpace(root) == "" || strings.TrimSpace(workspaceRoot) == "" {
		return nil, fmt.Errorf("checkpoint root and workspace root are required")
	}
	view, err := workspace.Open(workspaceRoot)
	if err != nil {
		return nil, err
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	// 创建目录之前解析最近已存在祖先，避免错误配置先在项目源码目录内落盘。
	ancestor, suffix := abs, ""
	for {
		if _, err := os.Lstat(ancestor); err == nil {
			break
		} else if !os.IsNotExist(err) {
			return nil, err
		}
		suffix = filepath.Join(filepath.Base(ancestor), suffix)
		parent := filepath.Dir(ancestor)
		if parent == ancestor {
			return nil, fmt.Errorf("checkpoint root has no existing ancestor")
		}
		ancestor = parent
	}
	realAncestor, err := filepath.EvalSymlinks(ancestor)
	if err != nil {
		return nil, err
	}
	resolved := filepath.Join(realAncestor, suffix)
	if isInside(view.Root(), resolved) {
		relative, err := filepath.Rel(view.Root(), resolved)
		if err != nil {
			return nil, err
		}
		relative = strings.ToLower(filepath.ToSlash(relative))
		if relative != ".xcode-state" && !strings.HasPrefix(relative, ".xcode-state/") {
			return nil, fmt.Errorf("checkpoint store inside workspace must be under .xcode-state")
		}
	}
	if err := os.MkdirAll(resolved, 0o700); err != nil {
		return nil, err
	}
	storage, err := os.OpenRoot(resolved)
	if err != nil {
		return nil, err
	}
	defer storage.Close()
	if err := storage.MkdirAll("blobs", 0o700); err != nil {
		return nil, err
	}
	return &Store{workspaceRoot: view.Root(), root: resolved}, nil
}

// Capture 在写入前保存目标文件的当前状态；文件不存在也会记录，恢复时表示删除。
func (s *Store) Capture(ctx context.Context, relative string) (Entry, error) {
	if err := ctx.Err(); err != nil {
		return Entry{}, err
	}
	clean, err := cleanWorkspacePath(relative)
	if err != nil {
		return Entry{}, err
	}
	view, err := workspace.Open(s.workspaceRoot)
	if err != nil {
		return Entry{}, err
	}
	_, canonical, err := view.ResolveForWrite(clean)
	if err != nil {
		return Entry{}, err
	}
	clean = canonical
	if view.IsHardExcluded(clean) {
		return Entry{}, fmt.Errorf("checkpoint path is excluded")
	}
	root, err := os.OpenRoot(s.workspaceRoot)
	if err != nil {
		return Entry{}, err
	}
	defer root.Close()

	entry := Entry{Time: time.Now().UTC(), Path: clean, Workspace: s.workspaceID()}
	var content []byte
	info, statErr := root.Lstat(clean)
	switch {
	case statErr == nil && info.Mode().IsRegular():
		file, openErr := root.Open(clean)
		if openErr != nil {
			return Entry{}, fmt.Errorf("open %q for checkpoint: %w", clean, openErr)
		}
		content, err = io.ReadAll(io.LimitReader(file, maxCheckpointBytes+1))
		closeErr := file.Close()
		if err != nil {
			return Entry{}, fmt.Errorf("read %q for checkpoint: %w", clean, err)
		}
		if closeErr != nil {
			return Entry{}, fmt.Errorf("close %q after checkpoint read: %w", clean, closeErr)
		}
		if len(content) > maxCheckpointBytes {
			return Entry{}, fmt.Errorf("file %q exceeds checkpoint limit %d bytes", clean, maxCheckpointBytes)
		}
		digest := sha256.Sum256(content)
		entry.Existed = true
		entry.ContentHash = hex.EncodeToString(digest[:])
		entry.SizeBytes = len(content)
		entry.FileMode = uint32(info.Mode().Perm())
	case statErr == nil:
		// 目录、符号链接等非普通文件不允许被写工具修改，也不允许建立快照。
		return Entry{}, fmt.Errorf("path %q is not a regular file", clean)
	case os.IsNotExist(statErr):
		entry.Existed = false
	default:
		return Entry{}, fmt.Errorf("inspect %q for checkpoint: %w", clean, statErr)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	nonce := make([]byte, 16)
	_, _ = rand.Read(nonce)
	entry.ID = fmt.Sprintf("cp-%s-%x", entry.Time.Format("20060102T150405.000000000"), nonce)
	if entry.Existed {
		blobs, err := os.OpenRoot(s.root)
		if err != nil {
			return Entry{}, err
		}
		defer blobs.Close()
		if err := writeExclusive(blobs, filepath.Join("blobs", entry.ID), content); err != nil {
			return Entry{}, fmt.Errorf("write checkpoint blob: %w", err)
		}
	}
	if err := s.appendManifest(entry); err != nil {
		return Entry{}, err
	}
	return entry, nil
}

// Restore 把快照对应的文件恢复到写入前的状态；快照记录"文件不存在"时会删除当前文件。
func (s *Store) Restore(ctx context.Context, id string) (Entry, error) {
	if err := ctx.Err(); err != nil {
		return Entry{}, err
	}
	entries, err := s.List(ctx)
	if err != nil {
		return Entry{}, err
	}
	for index := len(entries) - 1; index >= 0; index-- {
		entry := entries[index]
		if entry.ID != id {
			continue
		}
		if entry.Workspace != s.workspaceID() {
			return Entry{}, fmt.Errorf("checkpoint belongs to a different workspace")
		}
		if entry.WholeWorkspace {
			return entry, s.restoreWorkspace(ctx, entry)
		}
		return entry, s.restoreFile(ctx, entry)

	}
	return Entry{}, fmt.Errorf("checkpoint %q not found", id)
}

// List 按创建顺序返回 manifest 中的全部快照记录。
func (s *Store) List(ctx context.Context) ([]Entry, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	storage, err := os.OpenRoot(s.root)
	if err != nil {
		return nil, err
	}
	defer storage.Close()
	file, err := storage.Open("manifest.jsonl")
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("open checkpoint manifest: %w", err)
	}
	defer file.Close()

	entries := make([]Entry, 0)
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64<<10), 16<<20)
	for scanner.Scan() {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		var entry Entry
		if err := json.Unmarshal(scanner.Bytes(), &entry); err != nil {
			return nil, fmt.Errorf("decode checkpoint manifest: %w", err)
		}
		if err := validateEntry(entry); err != nil {
			return nil, err
		}
		entries = append(entries, entry)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read checkpoint manifest: %w", err)
	}
	return entries, nil
}

// appendManifest 把一条快照记录以 JSONL 追加到 manifest；调用方必须已持有锁。
func (s *Store) appendManifest(entry Entry) error {
	encoded, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("encode checkpoint entry: %w", err)
	}
	if len(encoded)+1 > 16<<20 {
		return fmt.Errorf("checkpoint manifest entry exceeds 16 MiB")
	}
	storage, err := os.OpenRoot(s.root)
	if err != nil {
		return err
	}
	defer storage.Close()
	file, err := storage.OpenFile("manifest.jsonl", os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("open checkpoint manifest: %w", err)
	}
	defer file.Close()
	data := append(encoded, '\n')
	if n, err := file.Write(data); err != nil {
		return fmt.Errorf("append checkpoint manifest: %w", err)
	} else if n != len(data) {
		return io.ErrShortWrite
	}
	if err := file.Sync(); err != nil {
		return err
	}
	return file.Close()
}

// cleanWorkspacePath 规范化工作区相对路径，并拒绝绝对路径与父目录逃逸。
func cleanWorkspacePath(input string) (string, error) {
	if strings.TrimSpace(input) == "" {
		return "", fmt.Errorf("checkpoint path is required")
	}
	clean := filepath.Clean(filepath.FromSlash(input))
	if filepath.IsAbs(clean) || filepath.VolumeName(clean) != "" || clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("checkpoint path escapes workspace")
	}
	return filepath.ToSlash(clean), nil
}

// isInside 判断目标路径是否等于根目录或位于根目录内部。
func isInside(root, target string) bool {
	relative, err := filepath.Rel(root, target)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

// workspaceID 把真实工作区路径转换成身份，避免把另一工作区的快照恢复到当前位置。
func (s *Store) workspaceID() string {
	digest := sha256.Sum256([]byte(s.workspaceRoot))
	return hex.EncodeToString(digest[:])
}

// validateEntry 校验来自磁盘的快照元数据，防止 ID 或路径被篡改后越界。
func validateEntry(entry Entry) error {
	if !strings.HasPrefix(entry.ID, "cp-") || strings.ContainsAny(entry.ID, "/\\:") || entry.ID == "" {
		return fmt.Errorf("invalid checkpoint id")
	}
	if entry.WholeWorkspace {
		if entry.Path != "." || len(entry.Files) > 20_000 {
			return fmt.Errorf("invalid workspace checkpoint")
		}
		total := 0
		seen := make(map[string]bool, len(entry.Files))
		for _, file := range entry.Files {
			if file.WholeWorkspace || file.Workspace != entry.Workspace || !file.Existed || seen[file.Path] {
				return fmt.Errorf("invalid workspace checkpoint member")
			}
			seen[file.Path] = true
			total += file.SizeBytes
			if total > maxWorkspaceBytes {
				return fmt.Errorf("workspace checkpoint exceeds size limit")
			}
			if err := validateEntry(file); err != nil {
				return err
			}
		}
		if total != entry.SizeBytes || total > maxWorkspaceBytes {
			return fmt.Errorf("invalid workspace checkpoint total")
		}
		return nil
	}
	if _, err := cleanWorkspacePath(entry.Path); err != nil {
		return err
	}
	if entry.SizeBytes < 0 || entry.SizeBytes > maxCheckpointBytes || entry.FileMode & ^uint32(0o777) != 0 {
		return fmt.Errorf("invalid checkpoint size or mode")
	}
	if entry.Existed {
		digest, err := hex.DecodeString(entry.ContentHash)
		if err != nil || len(digest) != sha256.Size {
			return fmt.Errorf("invalid checkpoint content hash")
		}
	}
	return nil
}

// restoreFile 校验旧内容的哈希后在受约束的根目录内恢复，拒绝硬性排除路径。
func (s *Store) restoreFile(ctx context.Context, entry Entry) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validateEntry(entry); err != nil {
		return err
	}
	if entry.Workspace != s.workspaceID() {
		return fmt.Errorf("checkpoint workspace mismatch")
	}
	view, err := workspace.Open(s.workspaceRoot)
	if err != nil {
		return err
	}
	if view.IsHardExcluded(entry.Path) {
		return fmt.Errorf("checkpoint path is excluded")
	}
	_, canonical, err := view.ResolveForWrite(entry.Path)
	if err != nil {
		return err
	}
	if view.IsHardExcluded(canonical) {
		return fmt.Errorf("checkpoint resolved path is excluded")
	}
	entry.Path = canonical
	root, err := os.OpenRoot(s.workspaceRoot)
	if err != nil {
		return err
	}
	defer root.Close()
	info, statErr := root.Lstat(entry.Path)
	if statErr == nil && !info.Mode().IsRegular() {
		return fmt.Errorf("restore target is not a regular file")
	}
	if statErr != nil && !os.IsNotExist(statErr) {
		return statErr
	}
	if !entry.Existed {
		err := root.Remove(entry.Path)
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	content, err := s.readBlob(entry)
	if err != nil {
		return err
	}
	return workspace.WriteFile(ctx, root, entry.Path, content, os.FileMode(entry.FileMode), false)
}

// ReadBlob 以导出接口读取并校验快照旧内容；恢复对账用它重构补丁应有的结果。
func (s *Store) ReadBlob(entry Entry) ([]byte, error) {
	return s.readBlob(entry)
}

// readBlob 读取并验证快照内容，校验成功前不会修改工作区。
func (s *Store) readBlob(entry Entry) ([]byte, error) {
	blobs, err := os.OpenRoot(s.root)
	if err != nil {
		return nil, err
	}
	defer blobs.Close()
	file, err := blobs.Open(filepath.Join("blobs", entry.ID))
	if err != nil {
		return nil, err
	}
	content, readErr := io.ReadAll(io.LimitReader(file, maxCheckpointBytes+1))
	closeErr := file.Close()
	if readErr != nil {
		return nil, readErr
	}
	if closeErr != nil {
		return nil, closeErr
	}
	digest := sha256.Sum256(content)
	if len(content) != entry.SizeBytes || hex.EncodeToString(digest[:]) != entry.ContentHash {
		return nil, fmt.Errorf("checkpoint blob integrity check failed")
	}
	return content, nil
}

// writeExclusive 持久化唯一 blob；已有文件绝不覆盖，写入或同步失败会清理不完整文件。
func writeExclusive(root *os.Root, path string, content []byte) error {
	file, err := root.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	complete := false
	defer func() {
		_ = file.Close()
		if !complete {
			_ = root.Remove(path)
		}
	}()
	if n, err := file.Write(content); err != nil {
		return err
	} else if n != len(content) {
		return io.ErrShortWrite
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	complete = true
	return nil
}
