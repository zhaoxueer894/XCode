// 保存不直接放进模型上下文的完整工具结果
package artifactstore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

const MaxArtifactBytes = 16 << 20 // 单个Artifact大小上限16MiB

// 一份结果的身份和大小
type Artifact struct {
	ID          string // sha256:<64位十六进制>
	ContentHash string
	SizeBytes   int
}

// 定义Store这个接口：方法集包含Put和Get
// 任何类型只要实现了Put和Get，就自动是Store
// Go约定，任何可能阻塞的IO方法，第一个参数都必须是context.Context
type Store interface {
	Put(context.Context, []byte) (Artifact, error) // 内容写入Artifact，返回Artifact
	Get(context.Context, string) ([]byte, error)   // 从Artifact 读取，返回[]byte任意长度字节切片
}

// ============= 内存Store ===============
// 用于把Artifact保存到内存中的store
type MemoryStore struct {
	mu     sync.RWMutex      // 保护values，允许多个读取并阻止读写冲突
	values map[string][]byte // 保存Artifact ID到完整内容的映射
}

// 创建一个空的内存Store：返回指向MemoryStore的指针
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{values: make(map[string][]byte)}
}

// Put：保存完整内容，相同内容复用同一个Artifact ID
func (s *MemoryStore) Put(ctx context.Context, content []byte) (Artifact, error) {
	if err := ctx.Err(); err != nil {
		return Artifact{}, err
	}
	artifact, err := describe(content)
	if err != nil {
		return Artifact{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, exists := s.values[artifact.ID]; !exists {
		s.values[artifact.ID] = append([]byte(nil), content...)
	}
	return artifact, nil
}

// Get: 返回Artifact的独立副本
func (s *MemoryStore) Get(ctx context.Context, id string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if _, err := artifactHash(id); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	content, exists := s.values[id]
	if !exists {
		return nil, fmt.Errorf("artifact %q not found", id)
	}
	return append([]byte(nil), content...), nil
}

// ============= 磁盘Store ===============
type FileStore struct {
	root string // 解析后的Artifact根目录绝对路径
	mu   sync.Mutex
}

// 创建根目录，并固定使用解析符号链接后的真实路径
func NewFileStore(root string) (*FileStore, error) {
	if strings.TrimSpace(root) == "" {
		return nil, fmt.Errorf("artifact store root is required")
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve artifact store root: %w", err)
	}
	if err := os.MkdirAll(abs, 0o700); err != nil {
		return nil, fmt.Errorf("create artifact store root: %w", err)
	}
	realRoot, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return nil, fmt.Errorf("resolve artifact store symlinks: %w", err)
	}
	return &FileStore{root: realRoot}, nil
}

// Put 按内容哈希写入权限为 0600 的文件；相同内容直接复用已有文件。
func (s *FileStore) Put(ctx context.Context, content []byte) (Artifact, error) {
	if err := ctx.Err(); err != nil {
		return Artifact{}, err
	}
	artifact, err := describe(content)
	if err != nil {
		return Artifact{}, err
	}
	hash := artifact.ContentHash
	directory := filepath.Join(s.root, "sha256", hash[:2])
	target := filepath.Join(directory, hash+".artifact")

	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return Artifact{}, fmt.Errorf("create artifact directory: %w", err)
	}
	if _, err := os.Stat(target); err == nil {
		return artifact, nil
	} else if !os.IsNotExist(err) {
		return Artifact{}, fmt.Errorf("inspect artifact: %w", err)
	}

	file, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return Artifact{}, fmt.Errorf("create artifact: %w", err)
	}
	removePartial := true
	defer func() {
		_ = file.Close()
		if removePartial {
			_ = os.Remove(target)
		}
	}()
	written, err := file.Write(content)
	if err != nil {
		return Artifact{}, fmt.Errorf("write artifact: %w", err)
	}
	if written != len(content) {
		return Artifact{}, io.ErrShortWrite
	}
	if err := file.Sync(); err != nil {
		return Artifact{}, fmt.Errorf("sync artifact: %w", err)
	}
	if err := file.Close(); err != nil {
		return Artifact{}, fmt.Errorf("close artifact: %w", err)
	}
	removePartial = false
	return artifact, nil
}

// Get 校验 Artifact ID 后读取对应完整内容，并再次检查大小上限。
func (s *FileStore) Get(ctx context.Context, id string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	hash, err := artifactHash(id)
	if err != nil {
		return nil, err
	}
	target := filepath.Join(s.root, "sha256", hash[:2], hash+".artifact")
	file, err := os.Open(target)
	if err != nil {
		return nil, fmt.Errorf("open artifact %q: %w", id, err)
	}
	defer file.Close()
	content, err := io.ReadAll(io.LimitReader(file, MaxArtifactBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read artifact %q: %w", id, err)
	}
	if len(content) > MaxArtifactBytes {
		return nil, fmt.Errorf("artifact %q exceeds %d bytes", id, MaxArtifactBytes)
	}
	return content, nil
}

// describe: 计算内容身份，返回填充了身份和大小信息的Artifact
func describe(content []byte) (Artifact, error) {
	if len(content) > MaxArtifactBytes {
		return Artifact{}, fmt.Errorf("artifact exceeds %d bytes", MaxArtifactBytes)
	}
	digest := sha256.Sum256(content)
	hash := hex.EncodeToString(digest[:])
	return Artifact{ID: "sha256:" + hash, ContentHash: hash, SizeBytes: len(content)}, nil
}

// artifactHash: 校验ID格式并返回可以安全用于文件名的哈希部分
func artifactHash(id string) (string, error) {
	prefix, hash, ok := strings.Cut(id, ":")
	if !ok || prefix != "sha256" || len(hash) != sha256.Size*2 {
		return "", fmt.Errorf("invalid artifact id %q", id)
	}
	if _, err := hex.DecodeString(hash); err != nil {
		return "", fmt.Errorf("invalid artifact id %q: %w", id, err)
	}
	return hash, nil
}
