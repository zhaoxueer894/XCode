package faulteval

// 本文件实现故障评测的三策略运行器。一次 Trial = 同一任务 × 同一故障点 × 同一 Trial 序号
// 在三种策略下的成对运行；phase1 在故障点被截断（取消 context 并放弃内存状态，磁盘上的
// TurnLog、journal 与工作区改动保留），phase2 按策略语义恢复：
//   reference：不恢复——清空对话从零重跑同一任务（复现 Anthropic 公开的 Compaction +
//     自维护进展要点模式；上下文策略同为 compaction，保证与 durable 只差"恢复"一个变量）；
//   durable：从 TurnLog + journal + 快照重建未完成轮次并续跑；
//   decision-gated：同 durable，但上下文压缩换为决策门控策略（可另挂 shadow 决策器——Jev 模式的自研实现）。
// 故障注入是"截断式"：硬崩溃的 unknown 窗口由 磁盘夹具单元测试覆盖，不在本评测重复。
// 全部指标从事件流收集，报告数字可从 Trace 重算。

import (
	"context"       // 阶段运行与故障取消。
	"encoding/json" // 副作用键解析。
	"errors"        // 识别 context.Canceled。
	"fmt"           // 构造错误与进度文本。
	"os"            // Trial 工作区创建。
	"path/filepath" // 产物路径。
	"sort"          // 副作用键稳定排序。
	"strings"       // 副作用键归一与事实保留判定。
	"time"          // 计时与时间戳。

	"xcode/internal/checkpoint" // 读取快照 manifest 与基线 blob。
	"xcode/internal/eventstore" // Trial 事件写 Trace。
	"xcode/internal/executor"   // 读取 journal 目录的全部状态条目。
	"xcode/internal/harness"    // Agent、事件与门禁契约。
	"xcode/internal/recovery"   // 未完成轮次重建与副作用对账。
)

// StackFunc 由调用方（CLI 装配层）提供：为一次阶段运行组装完整 Agent 栈。
// strategy 决定上下文策略与是否挂恢复日志；skipSteps 供 phase2 推进离线脚本进度。
type StackFunc func(ctx context.Context, workspace string, task FaultTask, sink harness.EventSink, strategy Strategy, skipSteps int) (*Stack, error)

// Stack 保存一次阶段运行的已装配组件与恢复所需的磁盘证据位置。
type Stack struct {
	Agent       *harness.Agent         // 保存已挂投影管理器的 Agent；门禁契约经 RunOptions 注入。
	TurnLogPath string                 // 保存轮次恢复日志路径；reference 策略为空字符串。
	JournalDir  string                 // 保存 journal 目录；恢复时读取其中全部 run-*.jsonl。
	Store       *checkpoint.Store      // 保存快照仓库；恢复分析读取 manifest 与基线 blob。
	Contract    *harness.TaskContract  // 保存完成门禁契约；task.Gate 为 false 时为 nil。
	Fingerprint func() (string, error) // 保存工作区指纹函数；门禁证据与工作区版本绑定时使用。
}

// RunOutcome 汇总一次阶段运行的产出。
type RunOutcome struct {
	Answer   string // 保存模型最终答案；失败阶段为空。
	Err      error  // 保存阶段错误；被故障截断的阶段以 context.Canceled 结束。
	ToolEnds int    // 保存本阶段 tool.completed 事件数；phase2 用它推进脚本进度。
}

// StackRunner 保存故障评测运行器的输出位置、配置与装配回调。
type StackRunner struct {
	outputDir string      // 保存当前 Run 输出目录。
	runID     string      // 保存 Run 唯一编号。
	config    FaultConfig // 保存运行配置。
	build     StackFunc   // 保存调用方的栈装配回调。
	progress  func(string)
}

// NewStackRunner 校验参数并创建故障评测运行器。
func NewStackRunner(outputDir, runID string, config FaultConfig, build StackFunc) (*StackRunner, error) {
	if strings.TrimSpace(outputDir) == "" || strings.TrimSpace(runID) == "" {
		return nil, fmt.Errorf("fault eval output directory and run id are required")
	}
	if config.Trials <= 0 {
		return nil, fmt.Errorf("fault eval trials must be positive")
	}
	if strings.TrimSpace(config.HarnessVersion) == "" || strings.TrimSpace(config.Provider) == "" ||
		strings.TrimSpace(config.Model) == "" || strings.TrimSpace(config.CatalogHash) == "" {
		return nil, fmt.Errorf("fault eval config requires harness version, provider, model and catalog hash")
	}
	if build == nil {
		return nil, fmt.Errorf("fault eval stack builder is required")
	}
	config.SchemaVersion = 1
	return &StackRunner{outputDir: outputDir, runID: runID, config: config, build: build}, nil
}

// SetProgress 设置进度输出函数；nil 安静。
func (r *StackRunner) SetProgress(progress func(string)) { r.progress = progress }

func (r *StackRunner) emitProgress(line string) {
	if r.progress != nil {
		r.progress(line)
	}
}

// FaultMetrics 保存一次 Trial 在一种策略下的全部指标。
type FaultMetrics struct {
	Steps                int   `json:"steps"`                  // 保存两阶段累计的模型迭代数（事件最大 step）。
	ToolCalls            int   `json:"tool_calls"`             // 保存两阶段累计 tool.started 数。
	InputTokens          int   `json:"input_tokens"`           // 保存两阶段累计输入 Token（含子智能体）。
	OutputTokens         int   `json:"output_tokens"`          // 保存两阶段累计输出 Token。
	TotalTokens          int   `json:"total_tokens"`           // 保存两阶段累计总 Token。
	WallTimeMillis       int64 `json:"wall_time_millis"`       // 保存 Trial 总耗时毫秒。
	MaxContextTokens     int   `json:"max_context_tokens"`     // 保存投影 token 峰值。
	Compactions          int   `json:"compactions"`            // 保存压缩/门控投影次数。
	Recovered            bool  `json:"recovered"`              // 表示 phase2 是否完成续跑。
	RestartedFromScratch bool  `json:"restarted_from_scratch"` // 表示 reference 是否整轮重跑。
	DuplicateSideEffects int   `json:"duplicate_side_effects"` // 保存超出"每键一次"的副作用调用数。
	GateBlocks           int   `json:"gate_blocks"`            // 保存完成门禁拦截次数（提前完成率分子）。
	FactRetentionPassed  bool  `json:"fact_retention_passed"`  // 表示关键事实是否全部出现在最终答案。
}

// TrialResult 保存一个策略下一次 Trial 的结果。
type TrialResult struct {
	TaskID       string       `json:"task_id"`                 // 保存任务 ID。
	FactEligible bool         `json:"fact_eligible"`           // 表示任务声明了关键事实（事实保留率的分母）。
	GateEligible bool         `json:"gate_eligible"`           // 表示任务挂载了完成门禁（提前完成率的分母）。
	Strategy     Strategy     `json:"strategy"`                // 保存策略。
	Trial        int          `json:"trial"`                   // 保存 Trial 序号。
	Outcome      string       `json:"outcome"`                 // 保存 passed / failed / error。
	Answer       string       `json:"answer,omitempty"`        // 保存最终答案。
	Error        string       `json:"error,omitempty"`         // 保存失败原因。
	TracePath    string       `json:"trace_path"`              // 保存 Trace 相对路径。
	RecoveryNote string       `json:"recovery_note,omitempty"` // 保存恢复摘要。
	Metrics      FaultMetrics `json:"metrics"`                 // 保存全部指标。
}

// StrategySummary 保存一个策略在全部任务上的汇总对照。
type StrategySummary struct {
	Strategy            Strategy `json:"strategy"`                     // 保存策略名。
	Trials              int      `json:"trials"`                       // 保存 Trial 总数。
	Passed              int      `json:"passed"`                       // 保存通过数。
	Failed              int      `json:"failed"`                       // 保存任务失败数。
	Errored             int      `json:"errored"`                      // 保存运行错误数。
	SuccessRate         float64  `json:"success_rate"`                 // 保存任务成功率。
	RecoverySuccessRate float64  `json:"recovery_success_rate"`        // 保存恢复成功率（reference 以重跑代替，记 0）。
	PrematureRate       float64  `json:"premature_rate"`               // 保存出现门禁拦截的 Trial 比例。
	FactRetentionRate   float64  `json:"fact_retention_rate"`          // 保存关键事实保留率。
	TotalInputTokens    int      `json:"total_input_tokens"`           // 保存输入 Token 总数。
	AverageInputTokens  float64  `json:"average_input_tokens"`         // 保存平均每 Trial 输入 Token。
	TotalDuplicates     int      `json:"total_duplicate_side_effects"` // 保存重复副作用总数。
}

// FaultReport 保存一次故障评测的完整报告；全部数字可由 Trace 重算。
type FaultReport struct {
	SchemaVersion int               `json:"schema_version"`  // 保存报告格式版本号（1）。
	RunID         string            `json:"run_id"`          // 保存 Run 编号。
	GeneratedAt   time.Time         `json:"generated_at"`    // 保存生成时间。
	ConfigHash    string            `json:"config_hash"`     // 保存配置哈希。
	Config        FaultConfig       `json:"config"`          // 保存完整配置。
	Results       []TrialResult     `json:"results"`         // 保存全部 Trial 结果。
	Strategies    []StrategySummary `json:"strategies"`      // 保存按策略汇总的对照数据。
	Notes         []string          `json:"notes,omitempty"` // 保存诚实性备注。
}

// Run 执行全部任务 × Trial × 三策略的成对评测并产出报告。
func (r *StackRunner) Run(ctx context.Context, catalog FaultCatalog) (FaultReport, error) {
	if err := catalog.MatchesRunHash(r.config.CatalogHash); err != nil {
		return FaultReport{}, err
	}
	configHash, err := hashFaultConfig(r.config)
	if err != nil {
		return FaultReport{}, err
	}
	if err := os.MkdirAll(filepath.Join(r.outputDir, "traces"), 0o700); err != nil {
		return FaultReport{}, fmt.Errorf("create fault eval output: %w", err)
	}
	report := FaultReport{
		SchemaVersion: 1,
		RunID:         r.runID,
		GeneratedAt:   time.Now().UTC(),
		ConfigHash:    configHash,
		Config:        r.config,
		Notes: []string{
			"fault injection truncates the run at a tool boundary and keeps on-disk state; hard-crash unknown windows are covered by recovery unit tests",
			"reference does not resume: it re-runs the whole task in a fresh conversation on the same workspace (public compaction + progress-note pattern)",
			"recovery auto-skips non-idempotent unknown calls with a deterministic note; replay is never automatic",
		},
	}
	if r.config.TypedShadow {
		report.Notes = append(report.Notes, "typed shadow decider recorded alongside decision-gated runs; its decisions are never applied (Jev-pattern, self-implemented; TypeSafe Jev product not integrated)")
	}

	strategies := []Strategy{StrategyReference, StrategyDurable, StrategyDecisionGated}
	for _, task := range catalog.Tasks {
		for trial := 1; trial <= r.config.Trials; trial++ {
			for _, strategy := range strategies {
				if err := ctx.Err(); err != nil {
					return FaultReport{}, err
				}
				r.emitProgress(fmt.Sprintf("[fault] task=%s trial=%d strategy=%s start", task.ID, trial, strategy))
				result, runErr := r.runTrial(ctx, task, trial, strategy, configHash)
				if runErr != nil {
					return FaultReport{}, fmt.Errorf("task %s trial %d strategy %s: %w", task.ID, trial, strategy, runErr)
				}
				report.Results = append(report.Results, result)
				r.emitProgress(fmt.Sprintf("[fault] task=%s trial=%d strategy=%s %s tokens=%d dup=%d recovered=%t",
					task.ID, trial, strategy, result.Outcome, result.Metrics.InputTokens,
					result.Metrics.DuplicateSideEffects, result.Metrics.Recovered))
			}
		}
	}
	report.Strategies = summarizeStrategies(report.Results)
	return report, nil
}

// runTrial 在隔离工作区中完成一种策略的一次 Trial。
func (r *StackRunner) runTrial(ctx context.Context, task FaultTask, trial int, strategy Strategy, configHash string) (TrialResult, error) {
	workspace, err := os.MkdirTemp("", "xcode-fault-"+task.ID+"-")
	if err != nil {
		return TrialResult{}, fmt.Errorf("create workspace: %w", err)
	}
	defer os.RemoveAll(workspace)
	if err := materializeFaultTask(workspace, task); err != nil {
		return TrialResult{}, err
	}

	traceName := fmt.Sprintf("%s-%s-trial-%02d.jsonl", task.ID, strategy, trial)
	traceRel := filepath.ToSlash(filepath.Join("traces", traceName))
	traceFile, err := os.OpenFile(filepath.Join(r.outputDir, traceRel), os.O_CREATE|os.O_EXCL|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return TrialResult{}, fmt.Errorf("create trace: %w", err)
	}
	defer traceFile.Close()
	recorder := eventstore.NewJSONLRecorder(traceFile, eventstore.TraceContext{
		RunID:      r.runID,
		SessionID:  fmt.Sprintf("%s/%s/%s/trial-%02d", r.runID, task.ID, strategy, trial),
		TurnID:     fmt.Sprintf("%s/%s/%s/trial-%02d/turn-1", r.runID, task.ID, strategy, trial),
		TaskID:     task.ID,
		Trial:      trial,
		ConfigHash: configHash,
		Provider:   r.config.Provider,
		Model:      r.config.Model,
	})
	var events []harness.Event
	sink := harness.EventSinkFunc(func(event harness.Event) {
		events = append(events, event)
		recorder.Emit(event)
	})

	result := TrialResult{TaskID: task.ID, Strategy: strategy, Trial: trial, TracePath: traceRel}
	started := time.Now()

	// phase1：到故障点截断（无故障任务只跑一个阶段）。
	phase1, phase1Err := r.runPhase(ctx, workspace, task, strategy, sink, 0, true)
	interrupted := phase1Err != nil && errors.Is(phase1Err, context.Canceled)
	// 故障截断以外的 phase1 错误（如 API 限流）必须如实呈现——否则会被 grade 错误掩盖。
	if phase1Err != nil && !interrupted {
		result.Outcome = "failed"
		result.Error = "phase1: " + phase1Err.Error()
		result.Metrics.WallTimeMillis = time.Since(started).Milliseconds()
		collectFaultMetrics(&result, events, task)
		return result, nil
	}

	switch {
	case task.FaultAfterTools == 0:
		result.Answer = phase1.Answer
		result.Metrics.Recovered = phase1Err == nil
	case strategy == StrategyReference:
		// 公开模式：不恢复。同一工作区、全新对话从零重跑。
		phase2, phase2Err := r.runPhase(ctx, workspace, task, strategy, sink, 0, false)
		result.Answer = phase2.Answer
		result.Metrics.RestartedFromScratch = true
		result.Metrics.Recovered = phase2Err == nil
		mergePhaseError(&result, interrupted, phase1Err, phase2Err)
	case strategy == StrategyDurable || strategy == StrategyDecisionGated:
		// 可恢复模式：从磁盘证据重建未完成轮次并续跑。
		plan, note, rebuildErr := r.rebuild(ctx, workspace, task, strategy, sink)
		if rebuildErr != nil {
			result.Outcome = "error"
			result.Error = rebuildErr.Error()
			result.Metrics.WallTimeMillis = time.Since(started).Milliseconds()
			return result, nil
		}
		result.RecoveryNote = note
		phase2, phase2Err := r.runPhaseResume(ctx, workspace, task, strategy, sink, phase1.ToolEnds, plan)
		result.Answer = phase2.Answer
		result.Metrics.Recovered = phase2Err == nil
		mergePhaseError(&result, interrupted, phase1Err, phase2Err)
	default:
		return TrialResult{}, fmt.Errorf("unknown strategy %q", strategy)
	}

	result.Metrics.WallTimeMillis = time.Since(started).Milliseconds()
	collectFaultMetrics(&result, events, task)

	gradeErr := r.grade(ctx, workspace, task, result.Answer)
	if gradeErr != nil {
		if result.Outcome == "" {
			result.Outcome = "failed"
		}
		result.Error = gradeErr.Error()
	}
	if result.Outcome == "" {
		result.Outcome = "passed"
	}
	return result, nil
}

// mergePhaseError 把阶段错误折算成 Trial 结果：预期内的故障截断不算失败。
func mergePhaseError(result *TrialResult, interrupted bool, phase1Err, phase2Err error) {
	if phase2Err != nil && !errors.Is(phase2Err, context.Canceled) {
		if result.Outcome == "" {
			result.Outcome = "failed"
		}
		if result.Error == "" {
			result.Error = "phase2: " + phase2Err.Error()
		}
		return
	}
	if interrupted && result.Error == "" {
		result.Error = "phase1 truncated by fault injection; recovered"
	}
}

// runPhase 运行一个阶段；计数器用闭包维护，副作用事件到达阈值后取消本阶段 context。
func (r *StackRunner) runPhase(ctx context.Context, workspace string, task FaultTask, strategy Strategy,
	sink harness.EventSink, skipSteps int, armFault bool) (RunOutcome, error) {

	phaseCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	ends := 0
	phaseSink := harness.EventSinkFunc(func(event harness.Event) {
		sink.Emit(event)
		if event.Kind == harness.ToolCompleted {
			ends++
			if armFault && task.FaultAfterTools > 0 && ends >= task.FaultAfterTools {
				cancel()
			}
		}
	})
	stack, err := r.build(phaseCtx, workspace, task, phaseSink, strategy, skipSteps)
	if err != nil {
		return RunOutcome{}, err
	}
	options := harness.RunOptions{MaxSteps: task.MaxSteps}
	if stack.Contract != nil {
		options.Contract = stack.Contract
		options.WorkspaceFingerprint = stack.Fingerprint
	}
	result, runErr := stack.Agent.RunWithOptions(phaseCtx, task.Prompt, options)
	return RunOutcome{Answer: result.Answer, Err: runErr, ToolEnds: ends}, runErr
}

// runPhaseResume 以恢复续跑方式运行 phase2：重建的消息 + ResumePending 预处理；不注入故障。
func (r *StackRunner) runPhaseResume(ctx context.Context, workspace string, task FaultTask, strategy Strategy,
	sink harness.EventSink, skipSteps int, plan *recovery.Plan) (RunOutcome, error) {

	stack, err := r.build(ctx, workspace, task, sink, strategy, skipSteps)
	if err != nil {
		return RunOutcome{}, err
	}
	options := harness.RunOptions{MaxSteps: task.MaxSteps, ResumePending: plan.Pending}
	if stack.Contract != nil {
		options.Contract = stack.Contract
		options.WorkspaceFingerprint = stack.Fingerprint
	}
	result, runErr := stack.Agent.RunTurn(ctx, plan.Messages, options)
	return RunOutcome{Answer: result.Answer, Err: runErr}, runErr
}

// rebuild 从磁盘证据重建未完成轮次；确认清单在评测中自动跳过（绝不自动重放），
// 冲突判定直接判运行错误——恢复器拒绝在证据不足时自动恢复。
func (r *StackRunner) rebuild(ctx context.Context, workspace string, task FaultTask, strategy Strategy,
	sink harness.EventSink) (*recovery.Plan, string, error) {

	stack, err := r.build(ctx, workspace, task, sink, strategy, 0)
	if err != nil {
		return nil, "", err
	}
	if strings.TrimSpace(stack.TurnLogPath) == "" {
		return nil, "", fmt.Errorf("strategy %s requires a turn log", strategy)
	}
	records, err := harness.ReadTurnLog(stack.TurnLogPath)
	if err != nil {
		return nil, "", err
	}
	journalEntries, err := readAllJournal(stack.JournalDir)
	if err != nil {
		return nil, "", err
	}
	manifest, err := stack.Store.List(ctx)
	if err != nil {
		return nil, "", fmt.Errorf("list checkpoint manifest: %w", err)
	}
	plan, err := recovery.Analyze(recovery.Input{
		Records:     records,
		Journal:     journalEntries,
		Checkpoints: manifest,
		Store:       stack.Store,
		Workspace:   workspace,
	})
	if err != nil {
		return nil, "", err
	}
	if plan.Completed {
		return plan, "no unfinished turn", nil
	}
	for _, verdict := range plan.Verdicts {
		if verdict.Verdict == recovery.VerdictConfirm {
			plan.ResolveConfirm(verdict.Call.ID, fmt.Sprintf(
				"[recovery] %s %q was interrupted and its side effects are unknown; it was NOT replayed (auto-skipped in evaluation). Inspect the workspace and continue.",
				verdict.Call.Name, recovery.ExecCommandText(verdict.Call.Arguments)))
		}
		if verdict.Verdict == recovery.VerdictConflict {
			return nil, "", fmt.Errorf("recovery conflict on %s call %s: %s", verdict.Call.Name, verdict.Call.ID, verdict.Reason)
		}
	}
	return plan, fmt.Sprintf("rebuilt=%d pending=%d", len(plan.Messages), len(plan.Pending)), nil
}

// readAllJournal 合并读取 journal 目录下全部运行文件的条目。
func readAllJournal(directory string) ([]executor.JournalEntry, error) {
	files, err := executor.ListJournalFiles(directory)
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

// collectFaultMetrics 从事件流计算全部指标；同样的 Trace 永远算出同样的数字。
func collectFaultMetrics(result *TrialResult, events []harness.Event, task FaultTask) {
	m := &result.Metrics
	result.FactEligible = len(task.CriticalFacts) > 0
	result.GateEligible = task.Gate
	sideEffectCounts := map[string]int{}
	for _, event := range events {
		switch event.Kind {
		case harness.ModelCompleted:
			m.InputTokens += event.InputTokens
			m.OutputTokens += event.OutputTokens
			m.TotalTokens += event.TotalTokens
			if event.Step > m.Steps {
				m.Steps = event.Step
			}
		case harness.ToolStarted:
			// 完成门禁自己发起的验证调用（gate-verify-*）属于 Harness 开销而非模型行为，
			// 不计入工具调用数与重复副作用统计。
			if strings.HasPrefix(event.ToolCallID, "gate-") {
				continue
			}
			m.ToolCalls++
			if event.Tool == "apply_patch" || event.Tool == "exec" {
				key := event.Tool + ":" + sideEffectKey(event.Tool, event.Data)
				sideEffectCounts[key]++
			}
		case harness.ContextBuilt:
			if event.ContextTokens > m.MaxContextTokens {
				m.MaxContextTokens = event.ContextTokens
			}
		case harness.ContextCompacted:
			m.Compactions++
		case harness.CompletionGateBlocked:
			m.GateBlocks++
		}
	}
	for _, count := range sideEffectCounts {
		if count > 1 {
			m.DuplicateSideEffects += count - 1
		}
	}
	if len(task.CriticalFacts) > 0 {
		m.FactRetentionPassed = true
		for _, fact := range task.CriticalFacts {
			if !strings.Contains(result.Answer, fact) {
				m.FactRetentionPassed = false
				break
			}
		}
	}
}

// sideEffectKey 从工具参数 JSON 中提取副作用键：apply_patch 用目标路径，exec 用完整命令。
func sideEffectKey(tool, arguments string) string {
	var parsed struct {
		Path    string   `json:"path"`
		Command []string `json:"command"`
	}
	if err := json.Unmarshal([]byte(arguments), &parsed); err != nil {
		return arguments
	}
	if tool == "apply_patch" && parsed.Path != "" {
		return strings.TrimSpace(parsed.Path)
	}
	if len(parsed.Command) > 0 {
		return strings.Join(parsed.Command, " ")
	}
	return arguments
}

// grade 执行环境结果验证与关键事实判定；返回 error 表示 Trial 判失败。
func (r *StackRunner) grade(ctx context.Context, workspace string, task FaultTask, answer string) error {
	if len(task.VerifyCommand) > 0 {
		verifyCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
		defer cancel()
		command := execCommand(verifyCtx, workspace, task.VerifyCommand)
		if err := command.Run(); err != nil {
			return fmt.Errorf("verify_command failed: %w", err)
		}
	}
	if len(task.CriticalFacts) > 0 {
		for _, fact := range task.CriticalFacts {
			if !strings.Contains(answer, fact) {
				return fmt.Errorf("critical fact %q missing from final answer", fact)
			}
		}
	}
	if strings.TrimSpace(answer) == "" {
		return fmt.Errorf("empty final answer")
	}
	return nil
}

// summarizeStrategies 按策略聚合全部 Trial 结果。
func summarizeStrategies(results []TrialResult) []StrategySummary {
	grouped := map[Strategy][]TrialResult{}
	order := []Strategy{}
	for _, result := range results {
		if _, exists := grouped[result.Strategy]; !exists {
			order = append(order, result.Strategy)
		}
		grouped[result.Strategy] = append(grouped[result.Strategy], result)
	}
	sort.Slice(order, func(i, j int) bool { return order[i] < order[j] })
	summaries := make([]StrategySummary, 0, len(order))
	for _, strategy := range order {
		trials := grouped[strategy]
		summary := StrategySummary{Strategy: strategy, Trials: len(trials)}
		factEligible, gateEligible := 0, 0
		for _, trial := range trials {
			switch trial.Outcome {
			case "passed":
				summary.Passed++
			case "failed":
				summary.Failed++
			default:
				summary.Errored++
			}
			if trial.Metrics.Recovered {
				summary.RecoverySuccessRate++
			}
			if trial.FactEligible {
				factEligible++
				if trial.Metrics.FactRetentionPassed {
					summary.FactRetentionRate++
				}
			}
			if trial.GateEligible {
				gateEligible++
				if trial.Metrics.GateBlocks > 0 {
					summary.PrematureRate++
				}
			}
			summary.TotalInputTokens += trial.Metrics.InputTokens
			summary.TotalDuplicates += trial.Metrics.DuplicateSideEffects
		}
		evaluated := summary.Passed + summary.Failed
		if evaluated > 0 {
			summary.SuccessRate = float64(summary.Passed) / float64(evaluated)
		}
		summary.RecoverySuccessRate /= float64(len(trials))
		if factEligible > 0 {
			summary.FactRetentionRate /= float64(factEligible)
		}
		if gateEligible > 0 {
			summary.PrematureRate /= float64(gateEligible)
		}
		if len(trials) > 0 {
			summary.AverageInputTokens = float64(summary.TotalInputTokens) / float64(len(trials))
		}
		summaries = append(summaries, summary)
	}
	return summaries
}
