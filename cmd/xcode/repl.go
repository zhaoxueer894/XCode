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

	"xcode/internal/config"
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
	// SwitchProvider 由 main 装配：/model 切换时重载配置并重建模型，返回新的展示名。
	SwitchProvider func(provider string) (string, error)
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
			if exit := handleSlash(input, session, &cfg, scanner); exit {
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

// handleSlash 处理斜杠命令；返回 true 表示退出 REPL。cfg 与 scanner 供 /model 的
// 菜单追问读取后续输入行。
func handleSlash(input string, session *harness.Session, cfg *replConfig, scanner *bufio.Scanner) bool {
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
		fmt.Println("  /model  切换模型供应商（菜单选择，写入配置并立即生效）")
		fmt.Println("  /setup  同 /model：重新选择模型")
		fmt.Println("  /compact 压缩对话历史（保留工作笔记）")
		fmt.Println("  /trust <cmd> 信任命令前缀（本会话免审批）")
		fmt.Println("  /help   显示此帮助")
		return false

	case "/model", "/setup":
		switchModelCommand(cfg, scanner, parts)
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

// switchModelCommand 实现 /model 与 /setup：菜单（或直接参数）选择供应商，
// 校验 Key → 写入全局配置（下次启动沿用）→ 热切换当前会话模型。
func switchModelCommand(cfg *replConfig, scanner *bufio.Scanner, parts []string) {
	globalPath, err := config.GlobalPath()
	if err != nil {
		fmt.Println("error: 无法定位全局配置路径")
		return
	}
	choice := ""
	if len(parts) > 1 {
		if provider, ok := parseWizardChoice(parts[1]); ok {
			choice = provider
		} else {
			fmt.Printf("未知 provider %s（可选：%s）\n", parts[1], strings.Join(wizardProviders, " / "))
			return
		}
	} else {
		printProviderMenu("选择要使用的模型（当前: "+cfg.Provider+"）", globalPath)
		for attempt := 0; attempt < 3; attempt++ {
			fmt.Print("输入序号并回车（直接回车取消）：")
			if !scanner.Scan() {
				return
			}
			answer := scanner.Text()
			if strings.TrimSpace(answer) == "" {
				return
			}
			if provider, ok := parseWizardChoice(answer); ok {
				choice = provider
				break
			}
			fmt.Println("无法识别，请输入 1-3。")
		}
	}
	if choice == "" || choice == cfg.Provider {
		if choice == cfg.Provider {
			fmt.Printf("已在使用 %s，未更改。\n", choice)
		}
		return
	}
	// 选真实供应商但 Key 缺失：指引而不是带错误切换。
	if choice != "demo" {
		fresh, loadErr := config.Load(cfg.Workspace)
		if loadErr == nil && !providerKeyConfigured(fresh, choice) {
			fmt.Printf("未找到 %s 的 API Key。请编辑 %s，在 %s 后填入后重试 /model。\n",
				choice, globalPath, providerKeyHint(choice))
			return
		}
	}
	if cfg.SwitchProvider == nil {
		fmt.Println("error: 当前会话不支持运行中切换模型（重启后生效配置写入）")
		return
	}
	modelName, switchErr := cfg.SwitchProvider(choice)
	if switchErr != nil {
		fmt.Printf("error: 切换失败：%v\n", switchErr)
		return
	}
	if err := config.Upsert(globalPath, "provider", choice); err != nil {
		fmt.Printf("warning: 写入配置失败（%v）；本次会话已切换，下次启动需重试 /model。\n", err)
	}
	cfg.Provider, cfg.Model = choice, modelName
	fmt.Printf("✓ 已切换到 %s（下一轮生效；provider=%s 已写入 %s）\n", wizardProviderLabel(choice), choice, globalPath)
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
