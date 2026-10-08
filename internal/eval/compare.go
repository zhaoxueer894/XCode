package eval

// 本文件实现基线对比：加载两份报告、逐任务计算差值、按保守规则判定并渲染 Markdown。

import (
	"encoding/json" // 解码基线与当前报告。
	"fmt"           // 构造错误与对比文本。
	"math"          // 计算相对变化与浮动比较。
	"os"            // 读取报告文件。
	"path/filepath" // 组装基线目录路径。
	"strings"       // 渲染 Markdown 表格。
)

// compareThreshold 保存判定 improved/regressed 的关键指标相对变化阈值。
const compareThreshold = 0.10

// Verdict 表示一个任务在对比中的判定结果。
type Verdict string

const (
	VerdictImproved  Verdict = "improved"  // 成功率上升，或持平且关键指标明显改善。
	VerdictRegressed Verdict = "regressed" // 成功率下降，或持平但关键指标明显恶化；含任务缺失。
	VerdictUnchanged Verdict = "unchanged" // 无显著变化或信号不足。
	VerdictNew       Verdict = "new"       // 基线中不存在的新任务。
	VerdictMissing   Verdict = "missing"   // 基线中存在但当前缺失，按回归处理。
)

// TaskComparison 保存单个任务在两次运行间的对比结果。
type TaskComparison struct {
	TaskID               string  `json:"task_id"`                // 保存任务编号。
	Verdict              Verdict `json:"verdict"`                // 保存判定结果。
	BaseSuccessRate      float64 `json:"base_success_rate"`      // 保存基线成功率；缺省为 0。
	CurrentSuccessRate   float64 `json:"current_success_rate"`   // 保存当前成功率；缺省为 0。
	SuccessRateDelta     float64 `json:"success_rate_delta"`     // 保存成功率差值（当前减基线）。
	BaseSteps            float64 `json:"base_steps"`             // 保存基线平均 Step。
	CurrentSteps         float64 `json:"current_steps"`          // 保存当前平均 Step。
	StepsDelta           float64 `json:"steps_delta"`            // 保存 Step 差值。
	BaseTokens           int     `json:"base_tokens"`            // 保存基线 Token 总量。
	CurrentTokens        int     `json:"current_tokens"`         // 保存当前 Token 总量。
	TokensDelta          int     `json:"tokens_delta"`           // 保存 Token 差值。
	BaseContextTokens    float64 `json:"base_context_tokens"`    // 保存基线上下文峰值均值。
	CurrentContextTokens float64 `json:"current_context_tokens"` // 保存当前上下文峰值均值。
	ContextTokensDelta   float64 `json:"context_tokens_delta"`   // 保存上下文峰值差值。
}

// Comparison 保存整次对比的结果与两份报告的配置摘要。
type Comparison struct {
	BaseRunID    string           `json:"base_run_id"`    // 保存基线运行编号。
	CurrentRunID string           `json:"current_run_id"` // 保存当前运行编号。
	ConfigDiff   []string         `json:"config_diff"`    // 保存两份配置不同的字段说明。
	Tasks        []TaskComparison `json:"tasks"`          // 保存逐任务对比结果。
	Regressed    int              `json:"regressed"`      // 保存判定为回归的任务数。
	Improved     int              `json:"improved"`       // 保存判定为改善的任务数。
}

// LoadReport 从磁盘读取一份评测报告。
func LoadReport(path string) (Report, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Report{}, fmt.Errorf("read report: %w", err)
	}
	var report Report
	if err := json.Unmarshal(data, &report); err != nil {
		return Report{}, fmt.Errorf("decode report %s: %w", path, err)
	}
	return report, nil
}

// BaselinePath 返回命名基线的报告路径；evalRoot 通常是 .eval。
func BaselinePath(evalRoot, label string) string {
	return filepath.Join(evalRoot, "baselines", label, "report.json")
}

// CompareReports 对比基线与当前报告，输出逐任务判定。
func CompareReports(base, current Report) Comparison {
	comparison := Comparison{BaseRunID: base.RunID, CurrentRunID: current.RunID}
	comparison.ConfigDiff = configDiff(base.Config, current.Config)

	baseTasks := taskSummaryIndex(base.TaskSummaries)
	currentTasks := taskSummaryIndex(current.TaskSummaries)

	// 先按当前报告的任务顺序对比，再补上基线独有任务（判 missing）。
	for _, current := range current.TaskSummaries {
		baseSummary, exists := baseTasks[current.TaskID]
		if !exists {
			comparison.Tasks = append(comparison.Tasks, TaskComparison{
				TaskID: current.TaskID, Verdict: VerdictNew,
				CurrentSuccessRate:   current.SuccessRate,
				CurrentSteps:         current.AverageSteps,
				CurrentTokens:        current.TotalTokens,
				CurrentContextTokens: current.AverageContextTokens,
			})
			continue
		}
		comparison.Tasks = append(comparison.Tasks, compareTask(baseSummary, current))
	}
	for _, baseSummary := range base.TaskSummaries {
		if _, exists := currentTasks[baseSummary.TaskID]; !exists {
			comparison.Tasks = append(comparison.Tasks, TaskComparison{
				TaskID: baseSummary.TaskID, Verdict: VerdictMissing,
				BaseSuccessRate:   baseSummary.SuccessRate,
				BaseSteps:         baseSummary.AverageSteps,
				BaseTokens:        baseSummary.TotalTokens,
				BaseContextTokens: baseSummary.AverageContextTokens,
			})
		}
	}
	for _, task := range comparison.Tasks {
		switch task.Verdict {
		case VerdictRegressed, VerdictMissing:
			comparison.Regressed++
		case VerdictImproved:
			comparison.Improved++
		}
	}
	return comparison
}

// compareTask 对比单个任务的两份汇总，按保守规则给出判定。
func compareTask(base, current TaskSummary) TaskComparison {
	row := TaskComparison{
		TaskID:               current.TaskID,
		BaseSuccessRate:      base.SuccessRate,
		CurrentSuccessRate:   current.SuccessRate,
		SuccessRateDelta:     current.SuccessRate - base.SuccessRate,
		BaseSteps:            base.AverageSteps,
		CurrentSteps:         current.AverageSteps,
		StepsDelta:           current.AverageSteps - base.AverageSteps,
		BaseTokens:           base.TotalTokens,
		CurrentTokens:        current.TotalTokens,
		TokensDelta:          current.TotalTokens - base.TotalTokens,
		BaseContextTokens:    base.AverageContextTokens,
		CurrentContextTokens: current.AverageContextTokens,
		ContextTokensDelta:   current.AverageContextTokens - base.AverageContextTokens,
	}
	switch {
	case current.SuccessRate > base.SuccessRate:
		row.Verdict = VerdictImproved
	case current.SuccessRate < base.SuccessRate:
		row.Verdict = VerdictRegressed
	default:
		// 成功率持平：只在双方都有有效判定且指标变化超阈值时判定。
		if base.EvaluatedTrials == 0 || current.EvaluatedTrials == 0 {
			row.Verdict = VerdictUnchanged
			break
		}
		if worse(float64(base.TotalTokens), float64(current.TotalTokens), compareThreshold) ||
			worse(base.AverageSteps, current.AverageSteps, compareThreshold) ||
			worse(base.AverageContextTokens, current.AverageContextTokens, compareThreshold) {
			row.Verdict = VerdictRegressed
			break
		}
		if better(float64(base.TotalTokens), float64(current.TotalTokens), compareThreshold) ||
			better(base.AverageSteps, current.AverageSteps, compareThreshold) ||
			better(base.AverageContextTokens, current.AverageContextTokens, compareThreshold) {
			row.Verdict = VerdictImproved
			break
		}
		row.Verdict = VerdictUnchanged
	}
	return row
}

// worse 判断当前值相对基值恶化超过阈值（越大越差的指标）。
func worse(base, current float64, threshold float64) bool {
	if base <= 0 {
		return current > 0 && relativeChange(base, current) > threshold
	}
	return (current-base)/base > threshold
}

// better 判断当前值相对基值改善超过阈值。
func better(base, current float64, threshold float64) bool {
	if base <= 0 {
		return false
	}
	return (base-current)/base > threshold
}

// relativeChange 计算从基值到当前值的相对变化幅度；基值为 0 时按当前值是否为 0 判断。
func relativeChange(base, current float64) float64 {
	if base == 0 {
		if current == 0 {
			return 0
		}
		return math.Inf(1)
	}
	return math.Abs(current-base) / base
}

// taskSummaryIndex 把任务汇总转成按 ID 索引的映射。
func taskSummaryIndex(summaries []TaskSummary) map[string]TaskSummary {
	index := make(map[string]TaskSummary, len(summaries))
	for _, summary := range summaries {
		index[summary.TaskID] = summary
	}
	return index
}

// configDiff 列出两份配置的差异字段，进入对比报告头部。
func configDiff(base, current Config) []string {
	var diff []string
	if base.Provider != current.Provider {
		diff = append(diff, fmt.Sprintf("provider: %s -> %s", base.Provider, current.Provider))
	}
	if base.Model != current.Model {
		diff = append(diff, fmt.Sprintf("model: %s -> %s", base.Model, current.Model))
	}
	if base.Trials != current.Trials {
		diff = append(diff, fmt.Sprintf("trials: %d -> %d", base.Trials, current.Trials))
	}
	if base.HarnessVersion != current.HarnessVersion {
		diff = append(diff, fmt.Sprintf("harness: %s -> %s", base.HarnessVersion, current.HarnessVersion))
	}
	baseStrategy := base.ContextStrategy
	if baseStrategy == "" {
		baseStrategy = "none"
	}
	currentStrategy := current.ContextStrategy
	if currentStrategy == "" {
		currentStrategy = "none"
	}
	if baseStrategy != currentStrategy {
		diff = append(diff, fmt.Sprintf("context strategy: %s -> %s", baseStrategy, currentStrategy))
	}
	if base.ContextBudgetTokens != current.ContextBudgetTokens {
		diff = append(diff, fmt.Sprintf("context budget: %d -> %d", base.ContextBudgetTokens, current.ContextBudgetTokens))
	}
	if len(diff) == 0 {
		diff = append(diff, "no config differences")
	}
	return diff
}

// WriteComparisonMarkdown 把对比结果渲染成 Markdown 文本。
func WriteComparisonMarkdown(comparison Comparison) string {
	var builder strings.Builder
	fmt.Fprintf(&builder, "# Eval Comparison\n\n")
	fmt.Fprintf(&builder, "- Baseline run: `%s`\n", comparison.BaseRunID)
	fmt.Fprintf(&builder, "- Current run: `%s`\n", comparison.CurrentRunID)
	fmt.Fprintf(&builder, "- Improved / regressed: `%d / %d`\n\n", comparison.Improved, comparison.Regressed)
	fmt.Fprintln(&builder, "## Config differences")
	fmt.Fprintln(&builder)
	for _, line := range comparison.ConfigDiff {
		fmt.Fprintf(&builder, "- %s\n", line)
	}
	fmt.Fprintln(&builder)
	fmt.Fprintln(&builder, "## Task comparison")
	fmt.Fprintln(&builder)
	fmt.Fprintln(&builder, "| Task | Verdict | Success | Steps | Tokens | Avg Ctx |")
	fmt.Fprintln(&builder, "|---|---|---:|---:|---:|---:|")
	for _, task := range comparison.Tasks {
		fmt.Fprintf(&builder, "| %s | %s | %.0f%% -> %.0f%% | %.2f -> %.2f | %d -> %d | %.0f -> %.0f |\n",
			task.TaskID, task.Verdict,
			task.BaseSuccessRate*100, task.CurrentSuccessRate*100,
			task.BaseSteps, task.CurrentSteps,
			task.BaseTokens, task.CurrentTokens,
			task.BaseContextTokens, task.CurrentContextTokens)
	}
	return builder.String()
}
