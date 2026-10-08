// Package workspace 建立只读、可索引并遵守忽略规则的工作区视图。
package workspace

import (
	"fmt"           // 为路径错误补充用户输入信息。
	"os"            // 检查解析后的目标文件类型。
	"path/filepath" // 规范化、拼接并比较真实文件系统路径。
	"runtime"       // 识别 Windows 路径别名和设备名规则。
	"strings"       // 判断相对路径是否以 .. 开头。
)

// Workspace 保存本次 Agent Run 允许读取的真实根目录和忽略规则。
type Workspace struct {
	root    string         // 保存解析符号链接后的工作区绝对根路径。
	ignores *IgnoreMatcher // 保存根目录 .gitignore 解析出的规则。
}

// Open 解析工作区真实根目录并加载忽略规则。
func Open(root string) (*Workspace, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve workspace: %w", err)
	}
	realRoot, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return nil, fmt.Errorf("resolve workspace symlinks: %w", err)
	}
	info, err := os.Stat(realRoot)
	if err != nil {
		return nil, fmt.Errorf("inspect workspace: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("workspace root is not a directory")
	}
	ignores, err := LoadIgnoreMatcher(realRoot)
	if err != nil {
		return nil, err
	}
	return &Workspace{root: realRoot, ignores: ignores}, nil
}

// Root 返回已经解析符号链接的工作区绝对根路径。
func (w *Workspace) Root() string {
	return w.root
}

// ResolveExisting 把用户相对路径转换成工作区内的真实路径和规范相对路径。
func (w *Workspace) ResolveExisting(input string) (string, string, os.FileInfo, error) {
	clean, err := cleanRelative(input)
	if err != nil {
		return "", "", nil, err
	}
	target := filepath.Join(w.root, clean)
	if !isInside(w.root, target) {
		return "", "", nil, fmt.Errorf("path escapes workspace")
	}
	realTarget, err := filepath.EvalSymlinks(target)
	if err != nil {
		return "", "", nil, fmt.Errorf("resolve %q: %w", input, err)
	}
	if !isInside(w.root, realTarget) {
		return "", "", nil, fmt.Errorf("path escapes workspace")
	}
	info, err := os.Stat(realTarget)
	if err != nil {
		return "", "", nil, fmt.Errorf("inspect %q: %w", input, err)
	}
	relative, err := filepath.Rel(w.root, realTarget)
	if err != nil {
		return "", "", nil, fmt.Errorf("make %q relative to workspace: %w", input, err)
	}
	return realTarget, filepath.ToSlash(relative), info, nil
}

// ResolveForWrite 把相对路径转换成可以安全写入的真实路径；目标允许尚不存在。
// 它解析最近已存在祖先目录中的符号链接，拒绝写出工作区，也拒绝对符号链接本身写入。
func (w *Workspace) ResolveForWrite(input string) (string, string, error) {
	clean, err := cleanRelative(input)
	if err != nil {
		return "", "", err
	}
	if clean == "." {
		return "", "", fmt.Errorf("path %q is the workspace root", input)
	}
	target := filepath.Join(w.root, clean)
	if !isInside(w.root, target) {
		return "", "", fmt.Errorf("path escapes workspace")
	}
	// 从目标向上找到最近的已存在祖先目录，解析其中的符号链接。
	ancestor := filepath.Dir(target)
	suffix := filepath.Base(target)
	for {
		if _, statErr := os.Lstat(ancestor); statErr == nil {
			break
		} else if !os.IsNotExist(statErr) {
			return "", "", fmt.Errorf("inspect ancestor: %w", statErr)
		}
		suffix = filepath.Join(filepath.Base(ancestor), suffix)
		parent := filepath.Dir(ancestor)
		if parent == ancestor {
			break
		}
		ancestor = parent
	}
	realAncestor, err := filepath.EvalSymlinks(ancestor)
	if err != nil {
		return "", "", fmt.Errorf("resolve %q: %w", input, err)
	}
	if !isInside(w.root, realAncestor) {
		return "", "", fmt.Errorf("path escapes workspace")
	}
	absolute := filepath.Join(realAncestor, suffix)
	if !isInside(w.root, absolute) {
		return "", "", fmt.Errorf("path escapes workspace")
	}
	// 目标已存在且本身是符号链接时拒绝写入，防止内容被透写到链接指向的位置。
	if info, statErr := os.Lstat(absolute); statErr == nil && info.Mode()&os.ModeSymlink != 0 {
		return "", "", fmt.Errorf("path %q is a symlink; refusing to write through links", input)
	}
	relative, err := filepath.Rel(w.root, absolute)
	if err != nil {
		return "", "", fmt.Errorf("make %q relative to workspace: %w", input, err)
	}
	return absolute, filepath.ToSlash(relative), nil
}

// IsHardExcluded 返回路径是否属于 Harness 内部目录或明确的 dotenv 密钥文件。
func (w *Workspace) IsHardExcluded(relative string) bool {
	relative = strings.ToLower(strings.Trim(filepath.ToSlash(filepath.Clean(filepath.FromSlash(relative))), "/"))
	for _, fixed := range []string{".git", ".eval", ".xcode-state"} {
		if relative == fixed || strings.HasPrefix(relative, fixed+"/") {
			return true
		}
	}
	name := filepath.Base(filepath.FromSlash(relative))
	if name == ".env" || (strings.HasPrefix(name, ".env.") && name != ".env.example") {
		return true
	}
	return false
}

// IsDiscoveryIgnored 返回路径是否应从默认列表、搜索和工作区索引中隐藏。
func (w *Workspace) IsDiscoveryIgnored(relative string, isDirectory bool) bool {
	if w.IsHardExcluded(relative) {
		return true
	}
	return w.ignores.Ignore(relative, isDirectory)
}

// CanSkipIgnoredDirectories 表示遍历器能否安全跳过整个被忽略目录。
func (w *Workspace) CanSkipIgnoredDirectories() bool {
	return w.ignores.CanSkipIgnoredDirectories()
}

// cleanRelative 允许点号表示根目录，并拒绝绝对路径和父目录逃逸。
func cleanRelative(input string) (string, error) {
	if strings.TrimSpace(input) == "" {
		input = "."
	}
	clean := filepath.Clean(filepath.FromSlash(input))
	if !filepath.IsLocal(clean) {
		return "", fmt.Errorf("path escapes workspace")
	}
	if runtime.GOOS == "windows" {
		for _, segment := range strings.Split(clean, string(filepath.Separator)) {
			if segment != "." && (strings.TrimRight(segment, " .") != segment || strings.ContainsAny(segment, ":*?\"<>|")) {
				return "", fmt.Errorf("path contains Windows aliases or invalid characters")
			}
		}
	}
	return clean, nil
}

// isInside 判断目标路径是否等于根目录或位于根目录内部。
func isInside(root, target string) bool {
	relative, err := filepath.Rel(root, target)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}
