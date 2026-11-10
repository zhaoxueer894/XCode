package main

// xcode install —— 可选的用户级 PATH 安装。
// 把当前可执行文件复制到用户目录（Windows: %LOCALAPPDATA%\Programs\xcode，
// Unix: ~/.local/bin），Windows 下同时把该目录写入用户 PATH 注册表。
// 完全可选：不安装也始终可以用 bin/xcode.exe 直接启动。
// 卸载 = 删除目标文件；Windows 再从用户 PATH 移除该目录即可。

import (
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// runInstall 实现 install 子命令：展示目标路径、交互确认、复制并接通 PATH。
func runInstall(args []string) error {
	flags := flag.NewFlagSet("xcode install", flag.ContinueOnError)
	assumeYes := flags.Bool("yes", false, "skip the confirmation prompt (for scripts)")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("xcode install does not accept positional arguments")
	}

	self, err := os.Executable()
	if err != nil {
		return err
	}
	if resolved, resolveErr := filepath.EvalSymlinks(self); resolveErr == nil {
		self = resolved
	}
	targetDir, err := installTargetDir()
	if err != nil {
		return err
	}
	target := filepath.Join(targetDir, filepath.Base(self))

	fmt.Printf("安装位置：%s\n", target)
	if !*assumeYes {
		if !stdinIsTerminal() {
			return fmt.Errorf("install needs an interactive confirmation; re-run with --yes")
		}
		fmt.Print("确认安装到该位置？[Y/n] ")
		answer, readErr := readAnswer()
		if readErr != nil || isNoAnswer(answer) {
			fmt.Println("已取消。")
			return nil
		}
	}

	if err := os.MkdirAll(targetDir, 0o700); err != nil {
		return fmt.Errorf("create install dir: %w", err)
	}
	if err := copyExecutable(self, target); err != nil {
		return err
	}
	if runtime.GOOS == "windows" {
		if err := ensureUserPath(targetDir); err != nil {
			fmt.Fprintf(os.Stderr, "warning: PATH 更新失败（%v）；请手动把 %s 加入用户 PATH。\n", err, targetDir)
		}
	} else if !pathListContains(targetDir) {
		fmt.Printf("[notice] %s 不在当前 PATH 中；在 shell 配置里加一行 export PATH=\"%s:$PATH\"。\n",
			targetDir, targetDir)
	}
	fmt.Printf("✓ 已安装。新开一个终端，运行 xcode 启动（卸载：删除该文件%s）。\n", windowsPathHint())
	return nil
}

func windowsPathHint() string {
	if runtime.GOOS == "windows" {
		return "，并从用户 PATH 移除安装目录"
	}
	return ""
}

// installTargetDir 返回用户级安装目录，不触碰系统目录。
func installTargetDir() (string, error) {
	if runtime.GOOS == "windows" {
		localAppData := os.Getenv("LOCALAPPDATA")
		if strings.TrimSpace(localAppData) == "" {
			return "", fmt.Errorf("LOCALAPPDATA is not set; install manually to a PATH directory")
		}
		return filepath.Join(localAppData, "Programs", "xcode"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".local", "bin"), nil
}

// copyExecutable 复制自身到目标路径并保留可执行权限。
func copyExecutable(source, target string) error {
	sourceFile, err := os.Open(source)
	if err != nil {
		return fmt.Errorf("open %s: %w", source, err)
	}
	defer sourceFile.Close()
	info, err := sourceFile.Stat()
	if err != nil {
		return err
	}
	targetFile, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, info.Mode().Perm()|0o200)
	if err != nil {
		return fmt.Errorf("write %s: %w", target, err)
	}
	defer targetFile.Close()
	if _, err := io.Copy(targetFile, sourceFile); err != nil {
		return fmt.Errorf("copy to %s: %w", target, err)
	}
	return nil
}

// pathListContains 判断目录是否已在当前进程的 PATH 中。
func pathListContains(dir string) bool {
	for _, entry := range filepath.SplitList(os.Getenv("PATH")) {
		if strings.EqualFold(filepath.Clean(entry), filepath.Clean(dir)) {
			return true
		}
	}
	return false
}

// ensureUserPath 在 Windows 上把目录追加到用户级 PATH（HKCU\Environment）。
// 通过 reg.exe 读写注册表，避免引入依赖；广播变更需要重启终端才对新进程生效。
func ensureUserPath(dir string) error {
	if pathListContains(dir) {
		return nil // 已在当前 PATH 中（常见于安装过一次后又重装）
	}
	query, err := exec.Command("reg", "query", `HKCU\Environment`, "/v", "Path").Output()
	if err != nil {
		// 用户 PATH 尚不存在：直接新建。
		return exec.Command("reg", "add", `HKCU\Environment`, "/v", "Path",
			"/t", "REG_EXPAND_SZ", "/d", dir, "/f").Run()
	}
	if strings.Contains(string(query), dir) {
		return nil // 注册表已包含，只是当前终端没刷新
	}
	old, ok := extractRegStringValue(string(query))
	if !ok {
		return fmt.Errorf("unexpected reg query output")
	}
	newPath := dir
	if strings.TrimSpace(old) != "" {
		newPath = old + ";" + dir
	}
	return exec.Command("reg", "add", `HKCU\Environment`, "/v", "Path",
		"/t", "REG_EXPAND_SZ", "/d", newPath, "/f").Run()
}

// extractRegStringValue 从 reg query 输出中取出 "名称 REG_*_SZ 值" 行的值部分。
func extractRegStringValue(queryOutput string) (string, bool) {
	for _, line := range strings.Split(queryOutput, "\n") {
		line = strings.TrimRight(line, "\r")
		index := strings.Index(line, "REG_")
		if index < 0 {
			continue
		}
		// 类型标记（REG_SZ / REG_EXPAND_SZ 等）之后的全部内容即值，可能含空格。
		rest := line[index:]
		if space := strings.IndexAny(rest, " \t"); space >= 0 {
			return strings.TrimSpace(rest[space+1:]), true
		}
	}
	return "", false
}
