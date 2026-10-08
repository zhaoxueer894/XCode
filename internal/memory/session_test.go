package memory

// 本文件验证 L1 会话笔记的落盘读取与 L2 项目记忆的加载边界。

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestSessionStoreRoundTrip 验证笔记追加后可以原样读回。
func TestSessionStoreRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "memory", "session-notes.json")
	store := NewSessionStore(path)
	if err := store.RecordHandoff("[handoff] 第一次交接：读取了 calc.go。"); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordHandoff("[handoff] 第二次交接：修复完成。"); err != nil {
		t.Fatal(err)
	}
	notes, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(notes) != 2 || notes[0].Kind != "handoff" || notes[1].Kind != "handoff" {
		t.Fatalf("notes = %#v", notes)
	}
	if notes[0].CreatedAt.IsZero() {
		t.Fatal("notes must carry creation time")
	}
	if !strings.Contains(notes[1].Content, "修复完成") {
		t.Fatalf("content = %q", notes[1].Content)
	}
	// Windows 的 FileMode.Perm 不表示 ACL，权限断言仅在非 Windows 执行（与既有先例一致）。
	if runtime.GOOS != "windows" {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("notes file mode = %v, want 0600", info.Mode().Perm())
		}
	}
}

// TestSessionStoreListMissingFile 验证笔记文件不存在时返回空而不是错误。
func TestSessionStoreListMissingFile(t *testing.T) {
	store := NewSessionStore(filepath.Join(t.TempDir(), "absent.json"))
	notes, err := store.List()
	if err != nil || notes != nil {
		t.Fatalf("List() = %#v, %v; want nil, nil", notes, err)
	}
}

// TestLoadProjectMemory 验证 L2 记忆的读取与缺省行为。
func TestLoadProjectMemory(t *testing.T) {
	root := t.TempDir()
	missing, err := LoadProjectMemory(root)
	if err != nil || missing != "" {
		t.Fatalf("LoadProjectMemory(missing) = %q, %v; want empty", missing, err)
	}
	memoryPath := ProjectMemoryPath(root)
	if err := os.MkdirAll(filepath.Dir(memoryPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(memoryPath, []byte("# 项目记忆\n构建：go build ./...\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadProjectMemory(root)
	if err != nil || !strings.Contains(loaded, "go build") {
		t.Fatalf("LoadProjectMemory = %q, %v", loaded, err)
	}
}
