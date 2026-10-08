//go:build darwin || linux || (windows && (amd64 || arm64))

package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"xcode/internal/artifactstore"
	"xcode/internal/executor"
	"xcode/internal/sandbox"
)

// helperArgs 让测试二进制充当确定性命令，无需 Shell 或外部语言运行时。
func helperArgs(t *testing.T, action, path string) []string {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return []string{executable, "-test.run=^TestProcessHelper$", "--", "xcode-helper", action, path}
}

// TestProcessHelper 只在独立子进程收到专用参数时执行夹具动作。
func TestProcessHelper(t *testing.T) {
	index := -1
	for i, arg := range os.Args {
		if arg == "xcode-helper" {
			index = i
			break
		}
	}
	if index < 0 {
		return
	}
	if len(os.Args) < index+3 {
		os.Exit(2)
	}
	action, path := os.Args[index+1], os.Args[index+2]
	switch action {
	case "output":
		fmt.Print(strings.Repeat("x", maxExecStreamBytes+100))
		fmt.Fprint(os.Stderr, "stderr-evidence")
	case "environment":
		fmt.Print(os.Getenv("XCODE_TEST_SECRET"))
	case "child":
		time.Sleep(3 * time.Second)
		if err := os.WriteFile(path, []byte("escaped"), 0o600); err != nil {
			os.Exit(3)
		}
	case "parent":
		executable, err := os.Executable()
		if err != nil {
			os.Exit(4)
		}
		child := exec.Command(executable, "-test.run=^TestProcessHelper$", "--", "xcode-helper", "child", path)
		if err := child.Start(); err != nil {
			os.Exit(5)
		}
		fmt.Print("child-started")
		// 父进程故意等待，由被测运行时终止整个进程集合。
		_ = child.Wait()
	default:
		os.Exit(6)
	}
	os.Exit(0)
}

// TestHostOutputLimit 验证输出被截断但实际字节计数和 stderr 仍保留。
func TestHostOutputLimit(t *testing.T) {
	result := runHostCommand(context.Background(), t.TempDir(), helperArgs(t, "output", ""), 10*time.Second)
	if result.startErr != nil {
		t.Fatal(result.startErr)
	}
	if result.exitCode != 0 || len(result.stdout) != maxExecStreamBytes || result.stdoutBytes != maxExecStreamBytes+100 || !result.stdoutTruncated || result.stderr != "stderr-evidence" {
		t.Fatalf("exit=%d bytes=%d retained=%d truncated=%t stderr=%q", result.exitCode, result.stdoutBytes, len(result.stdout), result.stdoutTruncated, result.stderr)
	}
}

// TestHostDoesNotInheritSecret 验证宿主中不在白名单的变量不会传给命令。
func TestHostDoesNotInheritSecret(t *testing.T) {
	t.Setenv("XCODE_TEST_SECRET", "must-not-leak")
	result := runHostCommand(context.Background(), t.TempDir(), helperArgs(t, "environment", ""), 10*time.Second)
	if result.startErr != nil || result.exitCode != 0 || result.stdout != "" {
		t.Fatalf("unexpected command result: %+v", result)
	}
}

// TestTimeoutKillsDescendant 通过后代延迟写文件检查进程集合是否在超时时被终止。
func TestTimeoutKillsDescendant(t *testing.T) {
	root := t.TempDir()
	marker := filepath.Join(root, "escaped.txt")
	result := runHostCommand(context.Background(), root, helperArgs(t, "parent", marker), time.Second)
	if result.startErr != nil {
		t.Fatal(result.startErr)
	}
	if !result.timedOut || !strings.Contains(result.stdout, "child-started") {
		t.Fatalf("timeout fixture did not start: %+v", result)
	}
	time.Sleep(3500 * time.Millisecond)
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("descendant survived timeout: %v", err)
	}
}

// TestExecIntentSkipsHostResolutionWithSandbox 验证容器后端下意图声明不在宿主解析程序：
// 容器内才存在的命令（宿主上无 ls）可以进入策略层，由容器自行解析。
func TestExecIntentSkipsHostResolutionWithSandbox(t *testing.T) {
	root := t.TempDir()
	store := artifactstore.NewMemoryStore()
	hostSet, err := NewReadOnlySet(context.Background(), root, store)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(map[string]any{"command": []string{"definitely-missing-program", "arg"}})
	if err != nil {
		t.Fatal(err)
	}

	// host 路径：宿主解析失败直接报错（原有行为不变）。
	hostExec := hostSet.WriteTools()[1]
	if _, err := hostExec.(executor.IntentDescriber).DescribeIntent(raw); err == nil {
		t.Fatal("host path should fail on unresolvable program")
	}

	// 容器路径：跳过宿主解析，意图声明成功。
	hostSet.SetSandbox(sandbox.DockerBackend{Image: "alpine:3.20"})
	if _, err := hostExec.(executor.IntentDescriber).DescribeIntent(raw); err != nil {
		t.Fatalf("sandbox path should skip host resolution: %v", err)
	}
}
