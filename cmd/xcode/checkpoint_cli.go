package main

// 本文件实现写前快照的终端交互：审批提示、快照列表与恢复子命令。

import (
	"context"       // 为快照列表与恢复提供取消上下文。
	"flag"          // 解析 checkpoints 与 restore 子命令参数。
	"fmt"           // 输出快照列表、恢复结果与审批提示。
	"os"            // 访问标准输入输出。
	"os/signal"     // 将恢复期间的 Ctrl+C 转为取消。
	"path/filepath" // 按指定工作区定位默认快照目录。
	"strings"       // 规范化用户的审批答复。

	"xcode/internal/checkpoint" // 打开写前快照仓库。
	"xcode/internal/executor"   // 审批范围类型。
	"xcode/internal/policy"     // 审批请求事实。
)

// terminalApprover 在终端向用户展示 ask 请求并读取答复。
type terminalApprover struct {
	// 审批时临时打开控制台，避免把管道内容误当成用户同意。
}

// newTerminalApprover 创建从标准输入读取答复的审批器。
func newTerminalApprover() *terminalApprover {
	return &terminalApprover{}
}

// Approve 展示请求内容并把用户答复映射成审批范围；输入结束（如管道 EOF）视为拒绝。
func (a *terminalApprover) Approve(ctx context.Context, request policy.Request) (executor.ApprovalScope, error) {
	if err := ctx.Err(); err != nil {
		return executor.ApprovalDenied, err
	}
	info, err := os.Stdin.Stat()
	if err != nil || info.Mode()&os.ModeCharDevice == 0 {
		return executor.ApprovalDenied, nil
	}
	fmt.Fprintf(os.Stderr, "[approval] %s wants to run argv=%q in %q\n", request.Tool, request.Command, request.Workdir)
	fmt.Fprint(os.Stderr, "[approval] allow? y=once / s=session (same argv+directory) / N=deny: ")
	line, err := readApprovalLine(ctx)
	if err != nil {
		return executor.ApprovalDenied, err
	}
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return executor.ApprovalOnce, nil
	case "s", "session":
		return executor.ApprovalSession, nil
	default:
		return executor.ApprovalDenied, nil
	}
}

// runCheckpoints 实现 xcode checkpoints 子命令：按时间顺序列出全部写前快照。
func runCheckpoints(args []string) error {
	flags := flag.NewFlagSet("xcode checkpoints", flag.ContinueOnError)
	dir := flags.String("checkpoint-dir", "", "directory for pre-write checkpoints")
	workspaceFlag := flags.String("workspace", ".", "workspace root")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *dir == "" {
		*dir = filepath.Join(*workspaceFlag, ".xcode-state", "checkpoints")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	store, err := checkpoint.NewStore(*dir, *workspaceFlag)
	if err != nil {
		return err
	}
	entries, err := store.List(ctx)
	if err != nil {
		return err
	}
	if len(entries) == 0 {
		fmt.Println("no checkpoints recorded")
		return nil
	}
	for _, entry := range entries {
		state := "existed"
		if entry.WholeWorkspace {
			state = "workspace"
		} else if !entry.Existed {
			state = "absent"
		}
		fmt.Printf(
			"%s  %s  %s  %s  %dB  %s\n",
			entry.ID,
			entry.Time.Format("2006-01-02T15:04:05Z"),
			entry.Path,
			state,
			entry.SizeBytes,
			entry.ContentHash,
		)
	}
	return nil
}

// runRestore 实现 xcode restore 子命令：把指定快照恢复到写入前状态。
func runRestore(args []string) error {
	flags := flag.NewFlagSet("xcode restore", flag.ContinueOnError)
	dir := flags.String("checkpoint-dir", "", "directory for pre-write checkpoints")
	workspaceFlag := flags.String("workspace", ".", "workspace root")
	id := flags.String("id", "", "checkpoint id to restore (from xcode checkpoints)")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*id) == "" {
		return fmt.Errorf("restore requires --id; run xcode checkpoints to list ids")
	}
	if *dir == "" {
		*dir = filepath.Join(*workspaceFlag, ".xcode-state", "checkpoints")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	store, err := checkpoint.NewStore(*dir, *workspaceFlag)
	if err != nil {
		return err
	}
	entry, err := store.Restore(ctx, *id)
	if err != nil {
		return err
	}
	action := "restored previous content of"
	if entry.WholeWorkspace {
		action = "restored workspace files from"
	} else if !entry.Existed {
		action = "removed newly created file"
	}
	fmt.Printf("%s %s (checkpoint %s)\n", action, entry.Path, entry.ID)
	return nil
}
