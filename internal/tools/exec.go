package tools

// 本文件实现宿主受限的命令执行工具：参数数组、超时、进程组终止、环境白名单与输出上限。

import (
	"bytes"         // 收集受上限约束的 stdout 与 stderr。
	"context"       // 控制命令的取消与超时。
	"encoding/json" // 接收模型提供的 JSON 参数。
	"errors"        // 识别非零退出码。
	"fmt"           // 构造参数与执行错误。
	"os"            // 读取允许透传的宿主环境变量。
	"os/exec"       // 以参数数组启动子进程，不经过 Shell。
	"path/filepath" // 拒绝工作区中的相对可执行程序，必须使用已解析的程序路径。
	"strings"       // 拼接命令展示文字。
	"time"          // 应用命令级超时。

	"xcode/internal/executor" // 声明执行前意图，进入统一管线。
	"xcode/internal/harness"  // 返回 ToolSpec 与 ToolResult。
	"xcode/internal/sandbox"  // 可选的受限命令执行后端。
)

const (
	defaultExecTimeoutSeconds = 60      // 未指定超时时命令最多运行 60 秒。
	maxExecTimeoutSeconds     = 600     // 单条命令允许的最长运行时间。
	maxExecStreamBytes        = 2 << 20 // stdout 与 stderr 各自最多保留 2 MiB。
)

// Exec 在工作区目录内执行一条命令；策略与审批由统一 Executor 在执行前完成。
type Exec struct {
	runtime *readOnlyRuntime // 保存共享 Workspace 和结果控制器。
}

// execArgs 保存 exec 工具的参数；command 是参数数组，永远不经过 Shell 解释。
type execArgs struct {
	Command        []string `json:"command"`                   // 保存完整命令参数数组，第一项是程序名。
	WorkdirRel     string   `json:"workdir,omitempty"`         // 保存工作目录的工作区相对路径；空值表示工作区根目录。
	TimeoutSeconds int      `json:"timeout_seconds,omitempty"` // 保存命令级超时秒数；0 表示使用默认 60 秒。
}

// Spec 返回 exec 的名称、用途和参数约束。
func (t *Exec) Spec() harness.ToolSpec {
	return harness.ToolSpec{
		Name:        "exec",
		Description: "Run one command (argv array, no shell) inside the workspace with timeout and output limits",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"command": map[string]any{
					"type":     "array",
					"items":    map[string]any{"type": "string"},
					"minItems": 1,
				},
				"workdir":         map[string]any{"type": "string"},
				"timeout_seconds": map[string]any{"type": "integer", "minimum": 1, "maximum": maxExecTimeoutSeconds},
			},
			"required":             []string{"command"},
			"additionalProperties": false,
		},
	}
}

// DescribeIntent 声明本调用要执行的完整命令；副作用范围无法预枚举，由策略与审批把关。
func (t *Exec) DescribeIntent(raw json.RawMessage) (executor.Intent, error) {
	var args execArgs
	if err := decodeArguments(raw, &args); err != nil {
		return executor.Intent{}, err
	}
	if len(args.Command) == 0 || strings.TrimSpace(args.Command[0]) == "" {
		return executor.Intent{}, fmt.Errorf("command requires at least a program name")
	}
	if args.TimeoutSeconds < 0 || args.TimeoutSeconds > maxExecTimeoutSeconds {
		return executor.Intent{}, fmt.Errorf("invalid timeout_seconds")
	}
	workdir := t.runtime.workspace.Root()
	if strings.TrimSpace(args.WorkdirRel) != "" {
		resolved, relative, info, err := t.runtime.workspace.ResolveExisting(args.WorkdirRel)
		if err != nil {
			return executor.Intent{}, err
		}
		if !info.IsDir() || t.runtime.workspace.IsHardExcluded(relative) {
			return executor.Intent{}, fmt.Errorf("workdir is not an allowed directory")
		}
		workdir = resolved
		args.WorkdirRel = relative
	}
	// 容器后端下程序由容器内解析（宿主上可能不存在，如 Windows 上的 ls）；
	// 只有 host 路径才在宿主解析为绝对路径，供审批展示与会话授权键使用。
	if t.runtime.sandbox == nil {
		program, err := exec.LookPath(args.Command[0])
		if err != nil {
			return executor.Intent{}, err
		}
		if !filepath.IsAbs(program) {
			return executor.Intent{}, fmt.Errorf("command must resolve to an absolute executable path")
		}
		args.Command[0] = program
	}
	normalized, err := json.Marshal(args)
	if err != nil {
		return executor.Intent{}, err
	}
	return executor.Intent{Mutating: true, Command: args.Command, Workdir: workdir, Arguments: normalized}, nil
}

// Execute 在受限环境中运行命令，并把退出码与受上限约束的输出虚拟化返回。
func (t *Exec) Execute(ctx context.Context, raw json.RawMessage) (harness.ToolResult, error) {
	if err := ctx.Err(); err != nil {
		return harness.ToolResult{}, err
	}
	var args execArgs
	if err := decodeArguments(raw, &args); err != nil {
		return harness.ToolResult{}, err
	}
	if len(args.Command) == 0 || strings.TrimSpace(args.Command[0]) == "" {
		return harness.ToolResult{}, fmt.Errorf("command requires at least a program name")
	}
	if args.TimeoutSeconds == 0 {
		args.TimeoutSeconds = defaultExecTimeoutSeconds
	}
	if args.TimeoutSeconds < 1 || args.TimeoutSeconds > maxExecTimeoutSeconds {
		return harness.ToolResult{}, fmt.Errorf("timeout_seconds must be between 1 and %d", maxExecTimeoutSeconds)
	}
	workdir := t.runtime.workspace.Root()
	workdirRel := "."
	if strings.TrimSpace(args.WorkdirRel) != "" {
		resolved, relative, info, err := t.runtime.workspace.ResolveExisting(args.WorkdirRel)
		if err != nil {
			return harness.ToolResult{}, err
		}
		if !info.IsDir() {
			return harness.ToolResult{}, fmt.Errorf("workdir %q is not a directory", args.WorkdirRel)
		}
		if t.runtime.workspace.IsHardExcluded(relative) {
			return harness.ToolResult{}, fmt.Errorf("workdir %q is excluded from model tool access", args.WorkdirRel)
		}
		workdir = resolved
		workdirRel = relative
	}

	// 执行分流：注入了沙箱后端时走受限环境，否则沿用 host 进程控制路径（原有行为）。
	var outcome execOutcome
	if t.runtime.sandbox != nil {
		outcome = fromSandboxOutcome(t.runtime.sandbox.Run(ctx, sandbox.Spec{
			Command: args.Command,
			Workdir: workdir,
			Timeout: time.Duration(args.TimeoutSeconds) * time.Second,
		}))
	} else {
		outcome = runHostCommand(ctx, workdir, args.Command, time.Duration(args.TimeoutSeconds)*time.Second)
	}
	if outcome.startErr != nil {
		return harness.ToolResult{}, fmt.Errorf("start %q: %w", args.Command[0], outcome.startErr)
	}

	content := []byte(fmt.Sprintf(
		"command=%s workdir=%s exit_code=%d duration_ms=%d timed_out=%t\n--- stdout (%dB truncated=%t) ---\n%s\n--- stderr (%dB truncated=%t) ---\n%s",
		strings.Join(args.Command, " "),
		workdirRel,
		outcome.exitCode,
		outcome.duration.Milliseconds(),
		outcome.timedOut,
		outcome.stdoutBytes, outcome.stdoutTruncated, outcome.stdout,
		outcome.stderrBytes, outcome.stderrTruncated, outcome.stderr,
	))
	// 命令可创建、删除或修改任意工作区文件，必须重建索引。
	if refreshErr := t.runtime.refreshAfterCommand(ctx); refreshErr != nil {
		content = append(content, []byte("\nWARNING: command finished but index refresh failed: "+refreshErr.Error())...)
	}
	resource := "exec:" + strings.Join(args.Command, " ")
	return t.runtime.results.build(ctx, resource, "", "", content)
}

// execOutcome 保存一次命令运行的退出码、输出和运行状态。
type execOutcome struct {
	startErr        error         // 命令无法启动时的错误；启动成功后为 nil。
	exitCode        int           // 命令退出码；被超时终止时为 -1。
	timedOut        bool          // 表示命令是否因超时被整组终止。
	duration        time.Duration // 命令实际运行时间。
	stdout          string        // 受上限约束的标准输出文本。
	stderr          string        // 受上限约束的标准错误文本。
	stdoutBytes     int           // 标准输出的实际字节数。
	stderrBytes     int           // 标准错误的实际字节数。
	stdoutTruncated bool          // 标准输出是否被上限截断。
	stderrTruncated bool          // 标准错误是否被上限截断。
}

// runHostCommand 启动受进程生命周期约束的命令，限制环境和输出，并在取消时终止进程集合。
func runHostCommand(ctx context.Context, workdir string, argv []string, timeout time.Duration) execOutcome {
	commandContext, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	command := exec.CommandContext(commandContext, argv[0], argv[1:]...)
	command.Dir = workdir
	command.Env = allowedEnvironment()
	// 平台后端使用 Unix 进程组或 Windows Job Object 管理后代进程。
	afterStart, cleanup, err := configureProcess(command)
	if err != nil {
		return execOutcome{startErr: err}
	}
	defer cleanup()
	// 进程组被终止后最多再等 2 秒回收资源，防止 Wait 因残留管道读端永久阻塞。
	command.WaitDelay = 2 * time.Second

	var stdout, stderr limitedBuffer
	stdout.limit = maxExecStreamBytes
	stderr.limit = maxExecStreamBytes
	command.Stdout = &stdout
	command.Stderr = &stderr

	started := time.Now()
	if err := command.Start(); err != nil {
		return execOutcome{startErr: err}
	}
	if err := afterStart(); err != nil {
		_ = command.Process.Kill()
		_ = command.Wait()
		return execOutcome{startErr: err}
	}
	waitErr := command.Wait()
	outcome := execOutcome{
		duration:        time.Since(started),
		timedOut:        commandContext.Err() == context.DeadlineExceeded,
		stdout:          stdout.buffer.String(),
		stderr:          stderr.buffer.String(),
		stdoutBytes:     stdout.written,
		stderrBytes:     stderr.written,
		stdoutTruncated: stdout.truncated,
		stderrTruncated: stderr.truncated,
	}
	// 非零退出码是命令的正常结果之一，转换成 exit_code 交给模型判断，不作为工具错误。
	var exitError *exec.ExitError
	switch {
	case waitErr == nil:
		outcome.exitCode = 0
	case errors.As(waitErr, &exitError):
		outcome.exitCode = exitError.ExitCode()
	default:
		outcome.exitCode = -1
	}
	return outcome
}

// allowedEnvironment 只把白名单内的宿主环境变量透传给子进程，避免泄露密钥类变量。
func allowedEnvironment() []string {
	allowed := []string{"PATH", "HOME", "TMPDIR", "LANG", "GOCACHE", "SystemRoot", "TEMP", "TMP", "USERPROFILE", "LOCALAPPDATA", "APPDATA"}
	environment := make([]string, 0, len(allowed))
	for _, name := range allowed {
		if value := os.Getenv(name); value != "" {
			environment = append(environment, name+"="+value)
		}
	}
	return environment
}

// limitedBuffer 是有硬上限的输出缓冲：超出部分被丢弃并记录截断状态。
type limitedBuffer struct {
	buffer    bytes.Buffer // 保存上限以内的输出内容。
	limit     int          // 保存允许保留的最大字节数。
	written   int          // 保存子进程实际产生的总字节数。
	truncated bool         // 表示是否有输出因超限被丢弃。
}

// Write 实现 io.Writer；始终报告全量写入成功，让子进程不因输出超限而收到写错误。
func (b *limitedBuffer) Write(data []byte) (int, error) {
	total := len(data)
	b.written += total
	remaining := b.limit - b.buffer.Len()
	if remaining <= 0 {
		b.truncated = true
		return total, nil
	}
	if total > remaining {
		b.truncated = true
		data = data[:remaining]
	}
	b.buffer.Write(data)
	return total, nil
}

// fromSandboxOutcome 把沙箱后端的统一结果转换成本工具的执行结果结构。
func fromSandboxOutcome(outcome sandbox.Outcome) execOutcome {
	return execOutcome{
		startErr:        outcome.StartErr,
		exitCode:        outcome.ExitCode,
		timedOut:        outcome.TimedOut,
		duration:        outcome.Duration,
		stdout:          outcome.Stdout,
		stderr:          outcome.Stderr,
		stdoutBytes:     outcome.StdoutBytes,
		stderrBytes:     outcome.StderrBytes,
		stdoutTruncated: outcome.StdoutTruncated,
		stderrTruncated: outcome.StderrTruncated,
	}
}
