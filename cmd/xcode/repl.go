package main

// 本文件实现交互式 REPL（Read-Eval-Print Loop）：多轮对话的用户界面。
// 用户在 `xcode> ` 提示符输入任务或斜杠命令；每轮通过 Session.Turn 执行；
// Ctrl+C 中断当前轮但不退出，连续两次 Ctrl+C 退出程序。

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"time"

	"xcode/internal/harness"
	"xcode/internal/policy"
)

// replConfig 保存 REPL 需要的装配信息（由 main.go 传入）。
type replConfig struct {
	Provider  string
	Model     string
	Mode      string
	Workspace string
	Timeout   time.Duration
	Engine    *policy.Engine // /trust 命令需要向策略引擎添加规则。
}

// sessionPath 返回会话持久化文件路径。
func sessionPath(workspace string) string {
	return filepath.Join(workspace, ".xcode-state", "sessions", "session-latest.json")
}

// runInteractive 实现 REPL 主循环。
func runInteractive(baseCtx context.Context, session *harness.Session, cfg replConfig) error {
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024) // 支持 1MB 输入

	interruptCount := 0

	fmt.Printf("xcode | %s/%s | %s | %s\n", cfg.Provider, cfg.Model, cfg.Mode, cfg.Workspace)
	fmt.Println("输入任务开始对话；/help 查看命令；Ctrl+C 中断当前轮。")

	for {
		fmt.Print("xcode> ")
		if !scanner.Scan() {
			break // EOF (Ctrl+D)
		}
		input := strings.TrimSpace(scanner.Text())
		if input == "" {
			continue
		}

		// 斜杠命令不进入模型。
		if strings.HasPrefix(input, "/") {
			if exit := handleSlash(input, session, cfg); exit {
				break
			}
			continue
		}

		// 每轮使用独立的 signal context：Ctrl+C 取消本轮，不影响下一轮。
		sigCtx, stop := signal.NotifyContext(baseCtx, os.Interrupt)
		turnCtx, cancel := context.WithTimeout(sigCtx, cfg.Timeout)

		result, err := session.Turn(turnCtx, input)
		cancel()
		stop()

		if err != nil {
			if sigCtx.Err() != nil {
				interruptCount++
				if interruptCount >= 2 {
					fmt.Println("\n[exiting]")
					break
				}
				fmt.Println("\n[interrupted]（再按一次 Ctrl+C 退出）")
				continue
			}
			fmt.Printf("error: %v\n\n", err)
			continue
		}
		interruptCount = 0 // 正常完成后重置中断计数

		// 显示模型回复 + 轮次统计。
		fmt.Println(result.Answer)
		fmt.Printf("[turn %d · %d steps · %d tokens · session %d tokens]\n\n",
			session.TurnCount(), result.Steps, result.Usage.TotalTokens, session.TotalUsage().TotalTokens)

		// 每轮结束后保存会话状态（崩溃恢复）。
		if err := session.Save(sessionPath(cfg.Workspace)); err != nil {
			fmt.Fprintf(os.Stderr, "warning: save session: %v\n", err)
		}
	}

	// 退出前保存。
	if err := session.Save(sessionPath(cfg.Workspace)); err != nil {
		fmt.Fprintf(os.Stderr, "warning: save session on exit: %v\n", err)
	}
	fmt.Println("bye.")
	return nil
}

// handleSlash 处理斜杠命令；返回 true 表示退出 REPL。
func handleSlash(input string, session *harness.Session, cfg replConfig) bool {
	parts := strings.Fields(input)
	cmd := strings.ToLower(parts[0])

	switch cmd {
	case "/exit", "/quit", "/q":
		return true

	case "/clear":
		session.Clear()
		fmt.Println("✓ 会话已清空（项目记忆保留）")
		return false

	case "/status":
		note := session.WorkingNote()
		fmt.Printf("Provider: %s | Model: %s | Mode: %s\n", cfg.Provider, cfg.Model, cfg.Mode)
		fmt.Printf("轮次: %d | 累计 tokens: %d\n", session.TurnCount(), session.TotalUsage().TotalTokens)
		if note.Objective != "" {
			fmt.Printf("目标: %s\n", note.Objective)
		}
		if len(note.FilesModified) > 0 {
			fmt.Printf("已修改: %s\n", strings.Join(note.FilesModified, ", "))
		}
		if note.TestStatus != "" {
			fmt.Printf("测试: %s (%s)\n", note.TestStatus, note.TestCommand)
		}
		return false

	case "/help", "/h":
		fmt.Println("  /exit   退出")
		fmt.Println("  /clear  清空会话历史")
		fmt.Println("  /status 显示当前状态")
		fmt.Println("  /compact 压缩对话历史（保留工作笔记）")
		fmt.Println("  /trust <cmd> 信任命令前缀（本会话免审批）")
		fmt.Println("  /help   显示此帮助")
		return false

	case "/compact":
		session.Compact()
		fmt.Println("✓ 对话已压缩（工作笔记保留）")
		return false

	case "/trust":
		if len(parts) < 2 {
			fmt.Println("用法: /trust <命令前缀>（如 /trust go test）")
			return false
		}
		if cfg.Engine == nil {
			fmt.Println("error: 策略引擎不可用")
			return false
		}
		prefix := parts[1:]
		cfg.Engine.TrustCommand(prefix)
		fmt.Printf("✓ 已信任命令: %s（本会话内免审批）\n", strings.Join(prefix, " "))
		return false

	default:
		fmt.Printf("未知命令 %s（/help 查看可用命令）\n", cmd)
		return false
	}
}

// offerResume 检查是否有上次会话，提示用户是否恢复。
func offerResume(session *harness.Session, workspace string) {
	state, err := harness.LoadSessionState(sessionPath(workspace))
	if err != nil || state == nil || len(state.Messages) == 0 {
		return // 无上次会话或读取失败
	}
	age := time.Since(state.LastActive).Round(time.Minute)
	fmt.Printf("发现上次会话（%s 前，%d 轮，%d tokens）。恢复？[Y/n] ",
		age, state.TurnCount, state.TotalUsage.TotalTokens)

	reader := bufio.NewReader(os.Stdin)
	answer, _ := reader.ReadString('\n')
	answer = strings.ToLower(strings.TrimSpace(answer))

	if answer != "n" && answer != "no" {
		session.RestoreFrom(state)
		fmt.Printf("✓ 已恢复（最后目标：%s）\n", truncateString(state.WorkingNote.Objective, 60))
	} else {
		fmt.Println("开始新会话。")
	}
}

// truncateString 截断字符串到指定字符数。
func truncateString(s string, n int) string {
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n]) + "…"
}
