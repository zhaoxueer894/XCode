package faulteval

// 本文件实现故障评测的辅助运行设施：验证命令执行、报告落盘与最小 Trace Replay。

import (
	"context"       // 验证命令与重放运行。
	"encoding/json" // Trace 解析与报告编码。
	"fmt"           // 构造错误。
	"os"            // 报告落盘与工作区。
	"os/exec"       // 运行 verify_command。
	"path/filepath" // 产物路径。
	"strings"       // Trace 行过滤。

	"xcode/internal/eventstore" // 从 Trace 提取任务与配置。
)

// execCommand 构造一次在指定工作区运行的验证命令；不经过 Shell。
func execCommand(ctx context.Context, workspace string, argv []string) *exec.Cmd {
	command := exec.CommandContext(ctx, argv[0], argv[1:]...)
	command.Dir = workspace
	return command
}

// WriteFaultReport 把报告写成机器可读 JSON 与人类可读 Markdown。
func WriteFaultReport(outputDir string, report FaultReport) error {
	encoded, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return fmt.Errorf("encode fault report: %w", err)
	}
	encoded = append(encoded, '\n')
	if err := os.WriteFile(filepath.Join(outputDir, "report.json"), encoded, 0o600); err != nil {
		return fmt.Errorf("write JSON report: %w", err)
	}

	var markdown strings.Builder
	fmt.Fprintln(&markdown, "# XCode Fault Eval Report")
	fmt.Fprintln(&markdown)
	fmt.Fprintf(&markdown, "- Run ID: `%s`\n", report.RunID)
	fmt.Fprintf(&markdown, "- Provider / model: `%s` / `%s`\n", report.Config.Provider, report.Config.Model)
	fmt.Fprintf(&markdown, "- Trials per strategy: `%d`\n", report.Config.Trials)
	fmt.Fprintf(&markdown, "- Context budget: `%d`\n", report.Config.ContextBudget)
	fmt.Fprintf(&markdown, "- Shadow decider (Jev-pattern, self-implemented): `%t`\n", report.Config.TypedShadow)
	fmt.Fprintln(&markdown)
	fmt.Fprintln(&markdown, "## 策略对照")
	fmt.Fprintln(&markdown)
	fmt.Fprintln(&markdown, "| 策略 | 成功率 | 恢复成功率 | 提前完成率 | 事实保留率 | 输入 Token | 重复副作用 |")
	fmt.Fprintln(&markdown, "|---|---:|---:|---:|---:|---:|---:|")
	for _, summary := range report.Strategies {
		fmt.Fprintf(&markdown, "| %s | %.0f%% | %.0f%% | %.0f%% | %.0f%% | %d | %d |\n",
			summary.Strategy, summary.SuccessRate*100, summary.RecoverySuccessRate*100,
			summary.PrematureRate*100, summary.FactRetentionRate*100,
			summary.TotalInputTokens, summary.TotalDuplicates)
	}
	fmt.Fprintln(&markdown)
	fmt.Fprintln(&markdown, "## Trial 明细")
	fmt.Fprintln(&markdown)
	fmt.Fprintln(&markdown, "| 任务 | 策略 | Trial | 结果 | 恢复 | Token | 重复副作用 | 备注 |")
	fmt.Fprintln(&markdown, "|---|---|---:|---|---:|---:|---:|---|")
	for _, result := range report.Results {
		fmt.Fprintf(&markdown, "| %s | %s | %d | %s | %t | %d | %d | %s |\n",
			result.TaskID, result.Strategy, result.Trial, result.Outcome,
			result.Metrics.Recovered, result.Metrics.InputTokens,
			result.Metrics.DuplicateSideEffects, firstLine(result.Error))
	}
	fmt.Fprintln(&markdown)
	fmt.Fprintln(&markdown, "## 备注")
	for _, note := range report.Notes {
		fmt.Fprintf(&markdown, "- %s\n", note)
	}
	if err := os.WriteFile(filepath.Join(outputDir, "report.md"), []byte(markdown.String()), 0o600); err != nil {
		return fmt.Errorf("write markdown report: %w", err)
	}
	return nil
}

// firstLine 取文本首行并截断，用于报告表格里的失败原因。
func firstLine(text string) string {
	if text == "" {
		return ""
	}
	line := text
	if index := strings.IndexAny(text, "\r\n"); index >= 0 {
		line = text[:index]
	}
	runes := []rune(line)
	if len(runes) > 120 {
		return string(runes[:119]) + "…"
	}
	return line
}

// ReplayRequest 保存一次最小 Trace Replay 的输入：从失败 Trace 提取任务与配置，
// 重建干净工作区后以同一策略重跑一次，生成 fresh trace 并重新判定结果。
type ReplayRequest struct {
	TracePath   string // 保存源 Trace 的 JSONL 路径。
	CatalogPath string // 保存任务目录路径；按 Trace 中的 TaskID 定位任务。
	OutputDir   string // 保存 fresh trace 的输出目录。
}

// ReplayResult 保存重放结果：新 Trial 的结果与使用的任务/配置快照。
type ReplayResult struct {
	SourceTaskID string      `json:"source_task_id"` // 保存源 Trace 的任务 ID。
	SourceTrial  int         `json:"source_trial"`   // 保存源 Trace 的 Trial 序号。
	Strategy     Strategy    `json:"strategy"`       // 保存从 Trace 提取的策略（由 trace 文件名或任务配置推断）。
	TaskID       string      `json:"task_id"`        // 保存重放的任务 ID（应与源一致）。
	Result       TrialResult `json:"result"`         // 保存重放 Trial 的完整结果。
	Note         string      `json:"note,omitempty"` // 保存重放说明。
}

// Replay 执行最小 Trace Replay：解析 Trace 头部身份 → 定位任务 → 全新工作区重跑 → 报告。
// 重放使用的策略从 Trace 文件名（<task>-<strategy>-trial-NN.jsonl）推断，保证与源运行同策略。
func (r *StackRunner) Replay(ctx context.Context, catalog FaultCatalog, request ReplayRequest) (ReplayResult, error) {
	outcome := ReplayResult{}
	// 第一步：解析源 Trace 的身份行（第一行即含 run/task/trial/config 身份）。
	raw, err := os.ReadFile(request.TracePath)
	if err != nil {
		return outcome, fmt.Errorf("read source trace: %w", err)
	}
	var first eventstore.TraceRecord
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		if err := json.Unmarshal([]byte(line), &first); err != nil {
			return outcome, fmt.Errorf("decode source trace: %w", err)
		}
		break
	}
	outcome.SourceTaskID = first.TaskID
	outcome.SourceTrial = first.Trial
	outcome.Strategy = strategyFromTraceName(request.TracePath, first.TaskID)

	// 第二步：在目录中定位任务。
	var task *FaultTask
	for index := range catalog.Tasks {
		if catalog.Tasks[index].ID == first.TaskID {
			task = &catalog.Tasks[index]
		}
	}
	if task == nil {
		return outcome, fmt.Errorf("task %q from trace not found in catalog %s", first.TaskID, request.CatalogPath)
	}
	outcome.TaskID = task.ID

	// 第三步：以全新工作区重跑同一策略一次；结果写入独立输出目录（trace 文件名互不冲突）。
	if err := os.MkdirAll(filepath.Join(request.OutputDir, "traces"), 0o700); err != nil {
		return outcome, fmt.Errorf("create replay output: %w", err)
	}
	replayRunner := &StackRunner{
		outputDir: request.OutputDir,
		runID:     r.runID + "-replay",
		config:    r.config,
		build:     r.build,
		progress:  r.progress,
	}
	result, runErr := replayRunner.runTrial(ctx, *task, 1, outcome.Strategy, r.config.CatalogHash)
	if runErr != nil {
		return outcome, fmt.Errorf("replay run: %w", runErr)
	}
	outcome.Result = result
	outcome.Note = "fresh workspace replay of the same task, fault point and strategy; outcome re-graded from the new run"
	return outcome, nil
}

// strategyFromTraceName 从 Trace 文件名推断策略；文件名约定为 <task>-<strategy>-trial-NN.jsonl。
func strategyFromTraceName(path, taskID string) Strategy {
	base := filepath.Base(path)
	trimmed := strings.TrimPrefix(strings.TrimSuffix(base, filepath.Ext(base)), taskID+"-")
	trimmed = strings.TrimSuffix(trimmed, "-trial-01")
	switch Strategy(trimmed) {
	case StrategyDurable:
		return StrategyDurable
	case StrategyDecisionGated:
		return StrategyDecisionGated
	default:
		return StrategyReference
	}
}
