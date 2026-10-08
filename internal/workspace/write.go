package workspace

import (
	"context"
	"crypto/rand"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// WriteFile 把完整内容先写入同目录临时文件，再替换目标，避免截断旧文件或透写硬链接。
// createOnly 使用独占创建，禁止覆盖同名文件；root 在实际 I/O 时约束路径逃逸。
func WriteFile(ctx context.Context, root *os.Root, relative string, content []byte, mode os.FileMode, createOnly bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := root.MkdirAll(filepath.Dir(relative), 0o755); err != nil {
		return err
	}
	name := relative
	if !createOnly {
		name = filepath.Join(filepath.Dir(relative), fmt.Sprintf(".xcode-write-%x", randomBytes()))
	}
	file, err := root.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	committed := false
	defer func() {
		_ = file.Close()
		if !committed {
			_ = root.Remove(name)
		}
	}()
	if n, err := file.Write(content); err != nil {
		return err
	} else if n != len(content) {
		return io.ErrShortWrite
	}
	if err := file.Chmod(mode.Perm()); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if !createOnly {
		if err := root.Rename(name, relative); err != nil {
			return err
		}
	}
	committed = true
	return nil
}

// randomBytes 返回用于临时文件名的随机身份；crypto/rand.Read 在熵源失败时终止进程。
func randomBytes() []byte {
	value := make([]byte, 16)
	_, _ = rand.Read(value)
	return value
}
