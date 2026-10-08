package sandbox

// 本文件实现 Docker 容器后端：一次性容器、断网、资源限额、工作区挂载与超时清理。
// 支持两种调用桥：直接 docker CLI；或 Windows 上经 wsl -e docker 调用 WSL 内的 Docker CE，
// 后者让没有 Docker Desktop 的机器也能使用容器沙箱。

import (
	"bytes"        // 限制输出累积。
	"context"      // 传递取消并在超时后清理容器。
	"crypto/rand"  // 生成容器随机名后缀。
	"encoding/hex" // 把随机字节转为名字。
	"fmt"          // 拼接 docker 参数与错误。
	"os/exec"      // 调用 docker CLI（或经 wsl.exe 桥接）。
	"strings"      // 翻译 Windows 盘符路径为 WSL 挂载路径。
	"time"         // 计时与 docker kill 超时。
)

const (
	maxStreamBytes = 2 << 20 // stdout 与 stderr 各自最多保留 2 MiB。
	defaultMemory  = "512m"  // 单个容器的默认内存上限。
	defaultPids    = "128"   // 单个容器的默认进程数上限。
)

// DockerBackend 在一次性容器内执行命令；工作区以读写方式挂载为 /workspace。
type DockerBackend struct {
	Image  string // 保存执行使用的镜像名，例如 alpine:3.20 或 golang:1.26。
	ViaWSL bool   // 表示经 wsl.exe -e docker 调用 WSL 内的 Docker CE；Windows 无 Desktop 时的桥接方式。
}

// Name 返回后端标识，进入 Trace 与错误信息。
func (d DockerBackend) Name() string { return "docker" }

// cliPrefix 返回调用 docker CLI 的命令前缀；WSL 桥接时通过 wsl.exe 转发。
func (d DockerBackend) cliPrefix() []string {
	if d.ViaWSL {
		return []string{"wsl", "-e", "docker"}
	}
	return []string{"docker"}
}

// mountPath 返回挂载源路径；WSL 桥接时把 Windows 盘符路径翻译为 /mnt/<盘>/ 形式。
func (d DockerBackend) mountPath(workspace string) string {
	if !d.ViaWSL {
		return workspace
	}
	return toWSLPath(workspace)
}

// toWSLPath 把 Windows 绝对路径翻译为 WSL 内的挂载路径；已是斜杠开头则原样返回。
func toWSLPath(windowsPath string) string {
	normalized := strings.ReplaceAll(windowsPath, "\\", "/")
	if strings.HasPrefix(normalized, "/") {
		return normalized
	}
	if len(normalized) >= 2 && normalized[1] == ':' {
		return "/mnt/" + strings.ToLower(normalized[:1]) + normalized[2:]
	}
	return normalized
}

// dockerArgs 组装 docker run 的完整参数数组；容器名用于超时后的强制清理。
func dockerArgs(name, workspace, image string, command []string) []string {
	args := []string{
		"run", "--rm",
		"--name", name,
		"--network", "none",
		"--pids-limit", defaultPids,
		"--memory", defaultMemory,
		"-v", workspace + ":/workspace",
		"-w", "/workspace",
		image,
	}
	return append(args, command...)
}

// Run 在容器内执行命令：超时或取消时先终止 docker 客户端，再强制清理容器本身。
func (d DockerBackend) Run(ctx context.Context, spec Spec) Outcome {
	if d.Image == "" {
		return Outcome{StartErr: fmt.Errorf("docker backend requires an image")}
	}
	suffix := make([]byte, 4)
	if _, err := rand.Read(suffix); err != nil {
		return Outcome{StartErr: fmt.Errorf("generate container name: %w", err)}
	}
	name := "xcode-" + hex.EncodeToString(suffix)

	prefix := d.cliPrefix()
	runArgs := append(append([]string{}, prefix[1:]...), dockerArgs(name, d.mountPath(spec.Workdir), d.Image, spec.Command)...)

	runContext, cancel := context.WithTimeout(ctx, spec.Timeout)
	defer cancel()
	command := exec.CommandContext(runContext, prefix[0], runArgs...)
	var stdout, stderr limitedBuffer
	stdout.limit = maxStreamBytes
	stderr.limit = maxStreamBytes
	command.Stdout = &stdout
	command.Stderr = &stderr

	started := time.Now()
	if err := command.Start(); err != nil {
		return Outcome{StartErr: fmt.Errorf("start docker: %w", err)}
	}
	waitErr := command.Wait()
	// docker run 客户端被终止不代表容器已停止；对具名容器补一次强制清理。
	if runContext.Err() != nil {
		cleanupContext, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		cleanupArgs := append(append([]string{}, prefix[1:]...), "kill", name)
		_ = exec.CommandContext(cleanupContext, prefix[0], cleanupArgs...).Run()
		cleanupCancel()
	}
	return Outcome{
		ExitCode:        exitCodeFrom(waitErr),
		TimedOut:        runContext.Err() == context.DeadlineExceeded,
		Duration:        time.Since(started),
		Stdout:          stdout.buffer.String(),
		Stderr:          stderr.buffer.String(),
		StdoutBytes:     stdout.written,
		StderrBytes:     stderr.written,
		StdoutTruncated: stdout.truncated,
		StderrTruncated: stderr.truncated,
	}
}

// exitCodeFrom 从 Wait 错误中提取退出码；无法提取时返回 -1。
func exitCodeFrom(err error) int {
	if err == nil {
		return 0
	}
	if exitError, ok := err.(*exec.ExitError); ok {
		return exitError.ExitCode()
	}
	return -1
}

// limitedBuffer 最多累积 limit 字节，超限后继续计数但不保留内容。
type limitedBuffer struct {
	buffer    bytes.Buffer // 保存实际保留的输出内容。
	limit     int          // 保存最多保留的字节数；小于等于 0 表示无限制。
	written   int          // 保存累计写入的字节数，可能大于 buffer 长度。
	truncated bool         // 表示是否发生过截断。
}

// Write 实现 io.Writer；超限后只累计计数。
func (b *limitedBuffer) Write(data []byte) (int, error) {
	b.written += len(data)
	if b.buffer.Len() >= b.limit {
		b.truncated = true
		return len(data), nil
	}
	remaining := b.limit - b.buffer.Len()
	if len(data) > remaining {
		b.truncated = true
		data = data[:remaining]
	}
	return b.buffer.Write(data)
}
