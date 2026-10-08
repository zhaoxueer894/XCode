package checkpoint

import (
	"context"
	"crypto/rand"
	"fmt"
	"io/fs"
	"os"
	"sort"
	"time"

	"xcode/internal/workspace"
)

const maxWorkspaceBytes = 64 << 20 // 命令前快照最多保存 64 MiB 文件内容，超限拒绝执行。

// CaptureWorkspace 在任意命令之前保存工作区文件集合；无法完整保存时不允许启动命令。
// .git、密钥与 Harness 状态目录始终排除；工作区外和网络副作用不在文件快照恢复范围内。
func (s *Store) CaptureWorkspace(ctx context.Context) (Entry, error) {
	paths, err := s.workspaceFiles(ctx)
	if err != nil {
		return Entry{}, err
	}
	entry := Entry{Time: time.Now().UTC(), Path: ".", Workspace: s.workspaceID(), WholeWorkspace: true}
	for _, path := range paths {
		file, err := s.Capture(ctx, path)
		if err != nil {
			return Entry{}, err
		}
		if !file.Existed {
			return Entry{}, fmt.Errorf("workspace changed during checkpoint: %s", path)
		}
		entry.SizeBytes += file.SizeBytes
		if entry.SizeBytes > maxWorkspaceBytes {
			return Entry{}, fmt.Errorf("workspace exceeds %d-byte checkpoint limit", maxWorkspaceBytes)
		}
		entry.Files = append(entry.Files, file)
	}
	nonce := make([]byte, 16)
	_, _ = rand.Read(nonce)
	entry.ID = fmt.Sprintf("cp-%s-%x", entry.Time.Format("20060102T150405.000000000"), nonce)
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.appendManifest(entry); err != nil {
		return Entry{}, err
	}
	return entry, nil
}

// workspaceFiles 列出快照范围内全部普通文件，包括普通 Git 忽略文件，拒绝不支持的文件类型。
func (s *Store) workspaceFiles(ctx context.Context) ([]string, error) {
	view, err := workspace.Open(s.workspaceRoot)
	if err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(s.workspaceRoot)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	var paths []string
	err = fs.WalkDir(root.FS(), ".", func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if view.IsHardExcluded(path) {
			if entry.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("workspace checkpoint does not support non-regular file %q", path)
		}
		if len(paths) >= 20_000 {
			return fmt.Errorf("workspace checkpoint exceeds 20000 files")
		}
		paths = append(paths, path)
		return nil
	})
	sort.Strings(paths)
	return paths, err
}

// restoreWorkspace 恢复命令前的文件集合并移除范围内新增文件；空目录保留，多文件恢复不是事务。
func (s *Store) restoreWorkspace(ctx context.Context, entry Entry) error {
	current, err := s.workspaceFiles(ctx)
	if err != nil {
		return err
	}
	// 先校验所有旧内容，避免因后面的 blob 损坏导致前面文件已被恢复。
	for _, file := range entry.Files {
		if err := ctx.Err(); err != nil {
			return err
		}
		if _, err := s.readBlob(file); err != nil {
			return err
		}
	}
	original := make(map[string]bool, len(entry.Files))
	for _, file := range entry.Files {
		original[file.Path] = true
		if err := s.restoreFile(ctx, file); err != nil {
			return fmt.Errorf("restore %q (workspace may be partially restored): %w", file.Path, err)
		}
	}
	for _, path := range current {
		if original[path] {
			continue
		}
		absent := Entry{ID: entry.ID, Workspace: s.workspaceID(), Path: path}
		if err := s.restoreFile(ctx, absent); err != nil {
			return fmt.Errorf("remove newly created %q: %w", path, err)
		}
	}
	return nil
}
