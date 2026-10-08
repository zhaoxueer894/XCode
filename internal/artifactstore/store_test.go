// package artifactstore_test 验证内容寻址 Artifact Store 的保存、读取和安全边界。
package artifactstore_test

import (
	"context"       // 构造正常与已取消的调用上下文。
	"errors"        // 判断取消错误是否保留 context.Canceled。
	"os"            // 检查 Artifact 文件与目录权限。
	"path/filepath" // 拼接 Artifact 的落盘路径。
	"runtime"       // Windows 文件权限由 ACL 表达，不能断言 Unix mode。
	"strings"       // 检查错误信息与 Artifact ID 格式。
	"testing"       // Go 标准测试框架。

	"xcode/internal/artifactstore" // 使用待验收的 Store 实现。
)

// TestMemoryStorePutGetRoundTrip 验证内存 Store 能按 ID 取回相同内容且互不影响。
func TestMemoryStorePutGetRoundTrip(t *testing.T) {
	store := artifactstore.NewMemoryStore()
	content := []byte("hello artifact")
	artifact, err := store.Put(context.Background(), content)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(artifact.ID, "sha256:") || artifact.SizeBytes != len(content) {
		t.Fatalf("artifact = %#v", artifact)
	}
	// 相同内容再次保存应复用同一个 ID。
	again, err := store.Put(context.Background(), content)
	if err != nil || again.ID != artifact.ID {
		t.Fatalf("dedup failed: %#v, err=%v", again, err)
	}
	got, err := store.Get(context.Background(), artifact.ID)
	if err != nil || string(got) != string(content) {
		t.Fatalf("get = %q, err=%v", got, err)
	}
	// 修改返回副本不能污染 Store 内部数据。
	got[0] = 'X'
	fresh, err := store.Get(context.Background(), artifact.ID)
	if err != nil || string(fresh) != string(content) {
		t.Fatalf("store content was mutated: %q", fresh)
	}
}

// TestMemoryStoreRejectsInvalidID 验证格式不正确的 Artifact ID 得到明确错误。
func TestMemoryStoreRejectsInvalidID(t *testing.T) {
	store := artifactstore.NewMemoryStore()
	for _, id := range []string{"", "sha256:short", "md5:" + strings.Repeat("a", 64), strings.Repeat("a", 64)} {
		if _, err := store.Get(context.Background(), id); err == nil || !strings.Contains(err.Error(), "invalid artifact id") {
			t.Fatalf("id %q error = %v, want invalid artifact id", id, err)
		}
	}
}

// TestFileStorePersistsWithRestrictedPermissions 验证磁盘 Store 的路径布局和 0600 权限。
func TestFileStorePersistsWithRestrictedPermissions(t *testing.T) {
	root := t.TempDir()
	store, err := artifactstore.NewFileStore(filepath.Join(root, "artifacts"))
	if err != nil {
		t.Fatal(err)
	}
	artifact, err := store.Put(context.Background(), []byte("disk artifact"))
	if err != nil {
		t.Fatal(err)
	}
	hash := artifact.ContentHash
	target := filepath.Join(root, "artifacts", "sha256", hash[:2], hash+".artifact")
	info, err := os.Stat(target)
	if err != nil {
		t.Fatalf("artifact file missing: %v", err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("artifact permissions = %v, want 0600", info.Mode().Perm())
	}
	got, err := store.Get(context.Background(), artifact.ID)
	if err != nil || string(got) != "disk artifact" {
		t.Fatalf("get = %q, err=%v", got, err)
	}
}

// TestFileStoreRejectsMissingArtifactAndEmptyRoot 验证失败路径返回明确错误。
func TestFileStoreRejectsMissingArtifactAndEmptyRoot(t *testing.T) {
	if _, err := artifactstore.NewFileStore("  "); err == nil {
		t.Fatal("empty root was accepted")
	}
	store, err := artifactstore.NewFileStore(filepath.Join(t.TempDir(), "artifacts"))
	if err != nil {
		t.Fatal(err)
	}
	missing := "sha256:" + strings.Repeat("ab", 32)
	if _, err := store.Get(context.Background(), missing); err == nil {
		t.Fatal("missing artifact was returned")
	}
}

// TestStoreHonorsCanceledContext 验证取消发生后不再执行保存或读取。
func TestStoreHonorsCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	memory := artifactstore.NewMemoryStore()
	if _, err := memory.Put(ctx, []byte("data")); !errors.Is(err, context.Canceled) {
		t.Fatalf("put error = %v, want context.Canceled", err)
	}
	if _, err := memory.Get(ctx, "sha256:"+strings.Repeat("ab", 32)); !errors.Is(err, context.Canceled) {
		t.Fatalf("get error = %v, want context.Canceled", err)
	}
}
