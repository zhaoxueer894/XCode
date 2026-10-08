package workspace

// 本文件实现工作区内容指纹：对全部非硬性排除的普通文件，
// 计算"相对路径:SHA-256"排序后的整体哈希。任何文件内容或路径集合的变化
// 都会改变指纹，完成门禁用它把验证证据与工作区版本绑定。

import (
	"crypto/sha256" // 计算单文件与整体指纹的两级哈希。
	"encoding/hex"  // 把哈希转换成十六进制文本。
	"fmt"           // 构造读取错误。
	"io"            // 限制单文件哈希的读取大小。
	"io/fs"         // 遍历工作区文件树。
	"os"            // 打开受限根目录与文件。
	"sort"          // 让路径列表顺序稳定。
	"strings"       // 拼接指纹原文。
)

// maxFingerprintFileBytes 限制参与指纹的单个文件大小；与快照、补丁对账的 8 MiB 上限一致。
const maxFingerprintFileBytes = 8 << 20

// Fingerprint 返回工作区当前内容的稳定指纹（十六进制 SHA-256）。
// 遍历边界与写前快照一致：只含普通文件，跳过 .git、.xcode-state 等硬性排除路径；
// Git 忽略但未硬排除的文件参与指纹。出现非普通文件或超限文件时返回错误，
// 由调用方决定门禁如何降级，绝不静默跳过导致指纹失真。
func Fingerprint(root string) (string, error) {
	view, err := Open(root)
	if err != nil {
		return "", fmt.Errorf("open workspace for fingerprint: %w", err)
	}
	osRoot, err := os.OpenRoot(view.Root())
	if err != nil {
		return "", fmt.Errorf("open workspace root for fingerprint: %w", err)
	}
	defer osRoot.Close()

	var lines []string
	walkErr := fs.WalkDir(osRoot.FS(), ".", func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
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
			return fmt.Errorf("inspect %q for fingerprint: %w", path, err)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("workspace fingerprint does not support non-regular file %q", path)
		}
		file, err := osRoot.Open(path)
		if err != nil {
			return fmt.Errorf("open %q for fingerprint: %w", path, err)
		}
		defer file.Close()
		digest := sha256.New()
		size, err := io.Copy(digest, io.LimitReader(file, maxFingerprintFileBytes+1))
		if err != nil {
			return fmt.Errorf("read %q for fingerprint: %w", path, err)
		}
		if size > maxFingerprintFileBytes {
			return fmt.Errorf("file %q exceeds fingerprint limit %d bytes", path, maxFingerprintFileBytes)
		}
		lines = append(lines, path+":"+hex.EncodeToString(digest.Sum(nil)))
		return nil
	})
	if walkErr != nil {
		return "", walkErr
	}
	sort.Strings(lines)
	overall := sha256.Sum256([]byte(strings.Join(lines, "\n")))
	return hex.EncodeToString(overall[:]), nil
}
