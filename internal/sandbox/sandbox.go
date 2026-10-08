package sandbox

// 本文件定义命令执行环境的抽象：Backend 接口、输入输出结构和 Docker 可用性探测。
// Policy 决定"该不该执行"，Backend 决定"在哪里执行、能碰到什么"。

import (
	"context" // 为探测命令传递取消。
	"fmt"     // 构造探测错误。
	"os/exec" // 调用 docker CLI。
	"time"    // 为探测设置超时。
)

// Spec 保存一次沙箱执行的完整输入。
type Spec struct {
	Command []string      // 保存完整命令参数数组；首项是程序名，永不经过 Shell。
	Workdir string        // 保存宿主侧工作目录的绝对路径；docker 后端把它挂载为 /workspace。
	Timeout time.Duration // 保存命令级超时；到期后整个执行环境被强制终止。
}

// Outcome 保存一次沙箱执行的结果；字段与宿主执行路径的口径一致。
type Outcome struct {
	StartErr        error         // 保存无法启动时的错误；启动成功后为 nil。
	ExitCode        int           // 保存命令退出码；被超时终止时为 -1。
	TimedOut        bool          // 表示是否因超时被强制终止。
	Duration        time.Duration // 保存实际运行时长。
	Stdout          string        // 保存受上限约束的标准输出。
	Stderr          string        // 保存受上限约束的标准错误。
	StdoutBytes     int           // 保存标准输出的实际字节数。
	StderrBytes     int           // 保存标准错误的实际字节数。
	StdoutTruncated bool          // 表示标准输出是否被上限截断。
	StderrTruncated bool          // 表示标准错误是否被上限截断。
}

// Backend 表示一种命令执行环境；host 路径不经过本接口，见 tools/exec.go 的分流说明。
type Backend interface {
	Name() string                               // 返回后端名称，进入 Trace 与错误信息。
	Run(ctx context.Context, spec Spec) Outcome // 在受限环境中执行命令并返回统一结果。
}

// ErrDaemonUnavailable 表示 docker CLI 不可用或守护进程未运行。
var ErrDaemonUnavailable = fmt.Errorf("docker daemon is unavailable")

// ProbeDocker 检查 docker CLI 存在且守护进程可达；失败时返回 ErrDaemonUnavailable 包装错误。
// viaWSL 为 true 时经 wsl.exe -e docker 探测 WSL 内的 Docker CE（无 Docker Desktop 的机器）。
func ProbeDocker(ctx context.Context, viaWSL bool) error {
	prefix := DockerBackend{ViaWSL: viaWSL}.cliPrefix()
	probeContext, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	args := append(append([]string{}, prefix[1:]...), "version", "--format", "{{.Server.Version}}")
	output, err := exec.CommandContext(probeContext, prefix[0], args...).Output()
	if err != nil {
		return fmt.Errorf("%w: %v", ErrDaemonUnavailable, err)
	}
	if len(output) == 0 {
		return fmt.Errorf("%w: empty server version", ErrDaemonUnavailable)
	}
	return nil
}
