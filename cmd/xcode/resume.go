package main

// 本文件实现恢复流程（--resume）：从轮次恢复日志、ToolCall journal
// 与快照 manifest 重建上次被中断的轮次，完成副作用对账后从最后一个安全边界继续，
// 不重新提交完整用户任务。非幂等的 unknown 调用在此进入人工确认清单，绝不自动重放。

import (
	"bufio"         // 读取人工确认清单的终端输入。
	"context"       // 恢复轮沿用整次运行的取消与超时信号。
	"encoding/json" // 把恢复后的会话状态写回 session-latest.json。
	"fmt"           // 输出恢复摘要与最终答案。
	"os"            // 创建目录与写回会话状态文件。
	"path/filepath" // 定位恢复日志、journal 与会话状态文件。
	"strings"       // 拼接确认清单的展示文本。
	"time"          // 记录恢复发生时间。

	"xcode/internal/checkpoint" // 提供快照 manifest 的读取入口。
	"xcode/internal/executor"   // 读取 ToolCall journal 状态。
	"xcode/internal/harness"    // 复用 RunTurn、RunOptions 与 SessionState。
	"xcode/internal/recovery"   // 恢复分析与副作用对账。
)

// turnLogPath 返回工作区的轮次恢复日志路径；main 与恢复流程共用同一份文件。
func turnLogPath(workspace string) string {
	return filepath.Join(workspace, ".xcode-state", "recovery", "turn-latest.jsonl")
}

// runResume 执行一次完整的恢复：分析 → 人工确认 → 继续轮次 → 写回会话状态。
// agent 已在 main 中完成全部装配（模型、统一执行管线、快照仓库与恢复日志）。
func runResume(ctx context.Context, agent *harness.Agent, workspace string, checkpoints *checkpoint.Store, prelude string, options harness.RunOptions) error {
	// 第一步：收集三类证据源。
	records, err := harness.ReadTurnLog(turnLogPath(workspace))
	if err != nil {
		return err
	}
	journalEntries, err := readAllJournalEntries(workspace)
	if err != nil {
		return err
	}
	manifest, err := checkpoints.List(ctx)
	if err != nil {
		return fmt.Errorf("list checkpoint manifest: %w", err)
	}

	// 第二步：重建消息历史并对全部未完成调用对账。
	plan, err := recovery.Analyze(recovery.Input{
		Records:     records,
		Journal:     journalEntries,
		Checkpoints: manifest,
		Store:       checkpoints,
		Workspace:   workspace,
	})
	if err != nil {
		return err
	}
	if plan.Completed {
		fmt.Println("[resume] no unfinished turn; nothing to recover")
		return nil
	}

	// 第三步：打印恢复摘要与每个未完成调用的对账结论。
	fmt.Printf("[resume] run=%s turn=%s resumed=%t\n", plan.RunID, plan.TurnID, plan.Resumed)
	fmt.Printf("[resume] rebuilt %d message(s) from the recovery log\n", len(plan.Messages))
	fmt.Printf("[resume] user input: %s\n", compactLine(plan.UserInput, 120))
	for _, verdict := range plan.Verdicts {
		fmt.Printf("[resume] %s (%s): %s — %s\n", verdict.Call.Name, shortCallID(verdict.Call.ID), verdict.Verdict, verdict.Reason)
	}

	// 第四步：冲突即放弃自动恢复；人工对账前不动工作区。
	for _, verdict := range plan.Verdicts {
		if verdict.Verdict == recovery.VerdictConflict {
			return fmt.Errorf("recovery conflict on %s call %s: %s; resolve manually, then rerun --resume",
				verdict.Call.Name, shortCallID(verdict.Call.ID), verdict.Reason)
		}
	}

	// 第五步：非幂等 unknown 调用逐条人工确认；只有"跳过"一个安全方向，没有重放选项。
	reader := bufio.NewReader(os.Stdin)
	for _, verdict := range plan.Verdicts {
		if verdict.Verdict != recovery.VerdictConfirm {
			continue
		}
		fmt.Printf("[resume] confirm %s call %s: %s\n    [s]kip and continue, or [a]bort recovery? ",
			verdict.Call.Name, shortCallID(verdict.Call.ID), recovery.ExecCommandText(verdict.Call.Arguments))
		answer, readErr := reader.ReadString('\n')
		if readErr != nil {
			return fmt.Errorf("read confirmation: %w", readErr)
		}
		switch strings.ToLower(strings.TrimSpace(answer)) {
		case "s", "skip":
			plan.ResolveConfirm(verdict.Call.ID, fmt.Sprintf(
				"[recovery] %s %q was interrupted and its side effects are unknown; it was NOT replayed. Inspect the workspace and continue.",
				verdict.Call.Name, recovery.ExecCommandText(verdict.Call.Arguments)))
		default:
			return fmt.Errorf("recovery aborted by user; the unfinished turn is kept for the next --resume")
		}
	}

	// 第六步：拼装完整消息历史：L2 前导 + 已保存会话历史 + 重建的本轮消息。
	var messages []harness.Message
	if strings.TrimSpace(prelude) != "" {
		messages = append(messages, harness.Message{Role: "user", Content: "[project-memory]\n" + prelude})
	}
	state, err := harness.LoadSessionState(sessionPath(workspace))
	if err != nil {
		return err
	}
	if state != nil {
		messages = append(messages, state.Messages...)
	}
	messages = append(messages, plan.Messages...)

	// 第七步：从最后安全边界继续轮次；Pending 由 Agent 在恢复预处理中补执行。
	options.ResumePending = plan.Pending
	fmt.Printf("[resume] continuing turn with %d pending call(s), max steps %d\n", len(plan.Pending), options.MaxSteps)
	result, runErr := agent.RunTurn(ctx, messages, options)

	// 第八步：无论恢复轮成功与否，都把最新消息历史写回会话状态，保证 Transcript 不丢。
	if saveErr := saveResumedSession(workspace, state, result); saveErr != nil {
		fmt.Fprintf(os.Stderr, "warning: save resumed session: %v\n", saveErr)
	}
	if runErr != nil {
		return runErr
	}
	fmt.Fprintf(os.Stderr, "[resume] stop reason: %s\n", result.StopReason)
	fmt.Println(result.Answer)
	return nil
}

// readAllJournalEntries 合并读取工作区 journal 目录下全部运行文件的条目。
func readAllJournalEntries(workspace string) ([]executor.JournalEntry, error) {
	files, err := executor.ListJournalFiles(filepath.Join(workspace, ".xcode-state", "journal"))
	if err != nil {
		return nil, err
	}
	var entries []executor.JournalEntry
	for _, path := range files {
		fileEntries, err := executor.ReadJournalFile(path)
		if err != nil {
			return nil, err
		}
		entries = append(entries, fileEntries...)
	}
	return entries, nil
}

// saveResumedSession 把恢复轮的结果合并进会话状态并写回 session-latest.json。
// 恢复计为一轮；工作笔记沿用中断前的内容，Token 用量累加恢复轮的消耗。
func saveResumedSession(workspace string, previous *harness.SessionState, result harness.RunResult) error {
	state := harness.SessionState{
		Messages:   result.Messages,
		TurnCount:  1,
		StartedAt:  time.Now().UTC(),
		LastActive: time.Now().UTC(),
	}
	if previous != nil {
		state.Messages = result.Messages
		state.WorkingNote = previous.WorkingNote
		state.TurnCount = previous.TurnCount + 1
		state.StartedAt = previous.StartedAt
		state.TotalUsage = harness.Usage{
			InputTokens:  previous.TotalUsage.InputTokens + result.Usage.InputTokens,
			OutputTokens: previous.TotalUsage.OutputTokens + result.Usage.OutputTokens,
			TotalTokens:  previous.TotalUsage.TotalTokens + result.Usage.TotalTokens,
		}
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("encode resumed session: %w", err)
	}
	path := sessionPath(workspace)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create session directory: %w", err)
	}
	return os.WriteFile(path, data, 0o600)
}

// shortCallID 压缩工具调用 ID 便于终端展示；ID 通常很长且只有尾段区分度高。
func shortCallID(id string) string {
	if len(id) <= 12 {
		return id
	}
	return id[:6] + "…" + id[len(id)-4:]
}
