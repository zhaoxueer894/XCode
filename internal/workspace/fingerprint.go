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
// 只含被 Git 跟踪意义上的文件：跳过硬性排除路径与 .gitignore 命中路径——
// 候选补丁与验收只关心被跟踪文件，忽略目录（node_modules、本地缓存）可能
// 有数万文件。超大文件按"总大小+头部哈希"入指纹，链接按链接文本入指纹。
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
		// Git 忽略的路径不入指纹：候选补丁与验收只关心被跟踪文件，忽略目录
		// （node_modules、本地缓存等）可能包含数万文件，整读会让门禁不可用。
		if view.IsDiscoveryIgnored(path, entry.IsDir()) {
			if entry.IsDir() && view.CanSkipIgnoredDirectories() {
				return fs.SkipDir
			}
			if entry.IsDir() {
				return nil
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
		if info.Mode()&os.ModeSymlink != 0 {
			// 链接按链接文本入指纹（与写前快照同语义），不读取目标内容。
			target, linkErr := osRoot.Readlink(path)
			if linkErr != nil {
				return fmt.Errorf("readlink %q for fingerprint: %w", path, linkErr)
			}
			digest := sha256.Sum256([]byte("link:" + target))
			lines = append(lines, path+":L"+hex.EncodeToString(digest[:]))
			return nil
		}
		if !info.Mode().IsRegular() {
			// 既非普通文件也非符号链接的条目（Windows 重解析点等）：按元数据入指纹。
			// 条目的出现/消失或大小变化仍会改变指纹；读不到内容时指纹不因此失败。
			lines = append(lines, fmt.Sprintf("%s:%d:mode%#o", path, info.Size(), info.Mode()))
			return nil
		}
		file, err := osRoot.Open(path)
		if err != nil {
			return fmt.Errorf("open %q for fingerprint: %w", path, err)
		}
		defer file.Close()
		digest := sha256.New()
		// 超大文件按"总大小 + 前 8MiB 内容哈希"入指纹：任何现实的编辑都会改变
		// 文件大小或头部内容，证据绑定依然成立；对齐 GB 级测试夹具整读纯属浪费。
		if _, err := io.Copy(digest, io.LimitReader(file, maxFingerprintFileBytes)); err != nil {
			return fmt.Errorf("read %q for fingerprint: %w", path, err)
		}
		lines = append(lines, fmt.Sprintf("%s:%d:%s", path, info.Size(), hex.EncodeToString(digest.Sum(nil))))
		return nil
	})
	if walkErr != nil {
		return "", walkErr
	}
	sort.Strings(lines)
	overall := sha256.Sum256([]byte(strings.Join(lines, "\n")))
	return hex.EncodeToString(overall[:]), nil
}
