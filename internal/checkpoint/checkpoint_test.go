package checkpoint

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestWorkspaceRestore 恢复修改、删除和新增文件，并保留明确排除的 dotenv。
func TestWorkspaceRestore(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	for _, path := range []string{"changed.txt", "deleted.txt", ".env"} {
		if err := os.WriteFile(filepath.Join(root, path), []byte("old"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	store, err := NewStore(filepath.Join(root, ".xcode-state", "checkpoints"), root)
	if err != nil {
		t.Fatal(err)
	}
	entry, err := store.CaptureWorkspace(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"changed.txt", "new.txt", ".env"} {
		if err := os.WriteFile(filepath.Join(root, path), []byte("new"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Remove(filepath.Join(root, "deleted.txt")); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Restore(ctx, entry.ID); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"changed.txt", "deleted.txt"} {
		data, err := os.ReadFile(filepath.Join(root, path))
		if err != nil || string(data) != "old" {
			t.Fatalf("%s: %q %v", path, data, err)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "new.txt")); !os.IsNotExist(err) {
		t.Fatalf("new file remains: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(root, ".env"))
	if err != nil || string(data) != "new" {
		t.Fatalf("excluded file changed: %q %v", data, err)
	}
}

func TestWorkspaceRestoresLinkMetadataAndNeverReadsOutsideTarget(t *testing.T) {
	root, outside := t.TempDir(), filepath.Join(t.TempDir(), "private.txt")
	if err := os.WriteFile(outside, []byte("PRIVATE-OUTSIDE-CONTENT"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "node_modules", ".bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "regular.txt"), []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	links := map[string]string{"node_modules/.bin/tool": "../tool.js", "outside": outside, "dangling": "missing-file"}
	for name, target := range links {
		if err := os.Symlink(target, filepath.Join(root, filepath.FromSlash(name))); err != nil {
			t.Skipf("symlink unavailable: %v", err)
		}
	}
	store, err := NewStore(filepath.Join(root, ".xcode-state", "checkpoints"), root)
	if err != nil {
		t.Fatal(err)
	}
	entry, err := store.CaptureWorkspace(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range entry.Files {
		b, err := store.ReadBlob(file)
		if err != nil || strings.Contains(string(b), "PRIVATE-OUTSIDE-CONTENT") {
			t.Fatalf("followed link: %q %v", b, err)
		}
	}
	if err := os.Remove(filepath.Join(root, "dangling")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "dangling"), []byte("replacement"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(root, "regular.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "regular.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "new-link")); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Restore(context.Background(), entry.ID); err != nil {
		t.Fatal(err)
	}
	for name, target := range links {
		got, err := os.Readlink(filepath.Join(root, filepath.FromSlash(name)))
		// os.Root.Symlink 在部分 Windows 环境会把相对目标规范化为反斜杠分隔；
		// 两种写法语义等价，按规范化形式比较。
		if err != nil || filepath.ToSlash(got) != filepath.ToSlash(target) {
			t.Fatalf("link %s = %s %v", name, got, err)
		}
	}
	if b, err := os.ReadFile(filepath.Join(root, "regular.txt")); err != nil || string(b) != "original" {
		t.Fatalf("file: %q %v", b, err)
	}
	if _, err := os.Lstat(filepath.Join(root, "new-link")); !os.IsNotExist(err) {
		t.Fatalf("new link remains: %v", err)
	}
	if b, err := os.ReadFile(outside); err != nil || string(b) != "PRIVATE-OUTSIDE-CONTENT" {
		t.Fatalf("outside target changed: %q %v", b, err)
	}
}

// TestRestoreRejectsCorruptionAndOtherWorkspace 验证恢复前检查 blob 完整性与工作区身份。
func TestRestoreRejectsCorruptionAndOtherWorkspace(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "note.txt"), []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	storage := t.TempDir()
	store, err := NewStore(storage, root)
	if err != nil {
		t.Fatal(err)
	}
	entry, err := store.Capture(ctx, "note.txt")
	if err != nil {
		t.Fatal(err)
	}
	other, err := NewStore(storage, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := other.Restore(ctx, entry.ID); err == nil {
		t.Fatal("accepted another workspace")
	}
	if err := os.WriteFile(filepath.Join(storage, "blobs", entry.ID), []byte("bad"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Restore(ctx, entry.ID); err == nil {
		t.Fatal("accepted corrupted blob")
	}
	data, err := os.ReadFile(filepath.Join(root, "note.txt"))
	if err != nil || string(data) != "old" {
		t.Fatalf("corruption modified file: %q %v", data, err)
	}
}

// TestCheckpointRejectsSymlinkEscape 验证链接不能把恢复或捕获范围带出工作区。
func TestCheckpointRejectsSymlinkEscape(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "link")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	store, err := NewStore(filepath.Join(root, ".xcode-state", "checkpoints"), root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Capture(context.Background(), "link/outside.txt"); err == nil {
		t.Fatal("accepted symlink escape")
	}
}
