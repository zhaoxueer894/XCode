package main

// 负责eval子命令的参数解析与依赖组装，不复制Agent Loop
// 本文件只做三件事：解析flag、装配依赖（Catalog/Model/Registry/Agent）、把控制权交给eval.Runner；
// 真正的Agent Loop仍由harness.Agent.Run提供，避免评测路径与线上路径出现两份实现而产生偏差

import (
	"context"
	"crypto/sha256"
	"flag" // flag是go标准库中的命令行参数解析包
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"xcode/internal/artifactstore" // 把本轮 Eval 的完整工具结果保存在 Run 输出目录。
	"xcode/internal/checkpoint"    // 使只读评测也复用统一管线依赖。
	"xcode/internal/contextmgr"    // 投影管理器与纪元。
	"xcode/internal/eval"
	"xcode/internal/executor" // 与普通 CLI 共用策略执行入口。
	"xcode/internal/harness"
	"xcode/internal/memory" // L1 会话笔记落盘。
	"xcode/internal/model"  // 按 Task 构造脚本化 Demo 模型。
	"xcode/internal/policy" // 评测使用 workspace-write 并自动审批。
	"xcode/internal/tools"
)

// runEval: 执行eval子命令，让每个Trial复用现有harness.Agent.Run
// ctx  : 由main注入，负责向下传递中断信号（Ctrl+C）
// args : 已去掉子命令名的剩余参数，例如 --provider bailian --trials 5
// 返回error表示本次评测未成功；只要存在失败Trial也会返回error，方便CI用退出码判断
func runEval(ctx context.Context, args []string) error {
	// ========== 0. 动词分发：baseline / compare / regress ==========
	if len(args) > 0 {
		switch args[0] {
		case "baseline":
			return runEvalBaseline(args[1:])
		case "compare":
			return runEvalCompare(args[1:])
		case "regress":
			return runEvalRegress(args[1:])
		}
	}
	// ========== 1. 解析命令行参数 ==========
	// 创建并返回一个FlagSet（一组命令行参数的注册器和解析器）
	// ContinueOnError: 解析失败时返回error交给调用方处理，而不是直接os.Exit
	flags := flag.NewFlagSet("xcode eval", flag.ContinueOnError)
	// 固定任务集（tasks.json）路径；固定任务集是评测结果可复现的前提
	catalogPath := flags.String("catalog", "testdata/eval/tasks.json", "path to eval task catalog")
	// 产物目录（trace + report）；留空则按runID自动生成
	outputDir := flags.String("output", "", "eval output directory")
	// 模型提供方：demo为离线假模型（无需网络），bailian走真实API
	provider := flags.String("provider", "demo", "model provider: demo or bailian")
	// 每个Task重复运行的次数；重复多次才能观察到模型输出的不稳定性
	trials := flags.Int("trials", 3, "number of trials per task")
	// 上下文策略与预算，进入 Config Hash，使 A/B 对照可被身份区分。
	contextStrategy := flags.String("context-strategy", "none", "context strategy: none, sliding, handoff or compaction")
	contextBudget := flags.Int("context-budget", 6000, "estimated-token budget before compaction")
	// 运行标签，进入 Config Hash 并展示在报告中。
	label := flags.String("label", "", "run label shown in reports and comparisons")
	// 排除 delegate 工具用于 A/B 对照。
	noDelegate := flags.Bool("no-delegate", false, "exclude delegate tool (single-agent A/B comparison)")
	if err := flags.Parse(args); err != nil {
		return err
	}
	// eval的输入完全来自catalog，因此不接受额外的位置参数（prompt），避免与run子命令混用
	if flags.NArg() != 0 {
		return fmt.Errorf("xcode eval does not accept a prompt")
	}

	// ========== 2. 装配评测依赖 ==========
	// 读取并校验任务集；catalogHash是tasks.json内容的SHA-256，
	// 落盘到报告后可用于验证"两次运行跑的是同一份任务集"
	catalog, catalogHash, err := eval.LoadCatalog(*catalogPath)
	if err != nil {
		return err
	}
	// 复用run子命令的模型构造逻辑，保证评测与实际运行使用同一套Model实现
	selectedModel, err := buildModel(*provider)
	if err != nil {
		return err
	}

	// runID使用UTC纳秒时间戳：既保证多次运行的产物目录不冲突，又能按名称直接按时间排序
	runID := time.Now().UTC().Format("20060102T150405.000000000Z")
	if *outputDir == "" {
		*outputDir = filepath.Join(".eval", "runs", runID)
	}
	// 本轮 Eval 的完整工具结果统一保存在当前 Run 输出目录下。
	artifacts, err := artifactstore.NewFileStore(filepath.Join(*outputDir, "artifacts"))
	if err != nil {
		return err
	}
	// 记录真实模型名：demo提供方没有远端模型；bailian则读环境变量（默认defaultBailianModel）
	modelName := "demo"
	providerEndpoint := ""
	if strings.EqualFold(*provider, "bailian") {
		modelName = envOrDefault("DASHSCOPE_MODEL", defaultBailianModel)
		providerEndpoint = envOrDefault("DASHSCOPE_BASE_URL", defaultBailianBaseURL)
	}
	harnessVersion, err := executableHarnessVersion()
	if err != nil {
		return err
	}

	// config是本次运行的"身份信息"，会随report一起落盘，供事后复现与跨版本对比
	config := eval.Config{
		HarnessVersion:      harnessVersion,
		Provider:            strings.ToLower(strings.TrimSpace(*provider)),
		Model:               modelName,
		ProviderEndpoint:    providerEndpoint,
		Label:               strings.TrimSpace(*label),
		Trials:              *trials,
		ContextStrategy:     strings.ToLower(strings.TrimSpace(*contextStrategy)),
		ContextBudgetTokens: *contextBudget,
		CatalogHash:         catalogHash,
	}
	// execute是交给Runner的回调，职责边界很清楚：
	//   Runner负责调度（准备工作区、超时控制、计时、写trace、汇总指标）
	//   这里只负责"把一次Agent跑完并返回答案"
	// 每个Trial都新建ReadFile/Registry/Agent，避免Trial之间共享状态而互相污染
	execute := func(
		trialContext context.Context, // 已叠加Task超时（TimeoutSeconds）的子context
		workspace string, // Runner为本Trial准备好的独立临时工作区
		task eval.Task, // 当前Task定义（prompt、max_steps等）
		sink harness.EventSink, // Runner提供的事件收集器，用于写trace并计算指标
	) (string, error) {
		// 工具的根目录限定在本Trial的工作区内，防止Agent读到工作区之外的文件
		// 每个 Trial 建立自己的工作区索引和 ReadSet，Artifact 共享本轮 Run 的 Store
		// 每个 Trial 使用自己的纪元与投影管理器；策略与预算来自 Config。
		epoch := contextmgr.NewEpoch()
		readOnlyTools, err := tools.NewReadOnlySetWithEpoch(trialContext, workspace, artifacts, epoch.Current)
		if err != nil {
			return "", err
		}
		manager, mgrErr := contextmgr.NewManager(
			contextmgr.Strategy(strings.ToLower(strings.TrimSpace(*contextStrategy))),
			*contextBudget,
			epoch,
		)
		if mgrErr != nil {
			return "", mgrErr
		}
		manager.SetNoteRecorder(memory.NewSessionStore(
			filepath.Join(workspace, ".xcode-state", "memory", "session-notes.json")))
		if !strings.EqualFold(*provider, "demo") {
			selected := selectedModel
			manager.SetSummarizer(func(ctx context.Context, oldText string) (string, error) {
				response, err := selected.Generate(ctx, []harness.Message{
					{Role: "user", Content: "请把下面这段 Agent 工作历史总结成不超过 200 字的要点，保留目标、已改文件、验证结果、失败与下一步：\n" + oldText},
				}, nil, harness.ModelEventSinkFunc(func(harness.ModelEvent) {}))
				if err != nil {
					return "", err
				}
				return response.Content, nil
			})
		}
		// 注册 delegate 工具（--no-delegate 时跳过，用于 A/B 对照）。
		var registryRef *harness.Registry
		var engineRef *policy.Engine
		var checkpointsRef *checkpoint.Store
		var toolList []harness.Tool
		baseToolList := append(readOnlyTools.Tools(), readOnlyTools.WriteTools()...)
		if !*noDelegate {
			delegate := harness.NewDelegate(
				func(ctx context.Context, role, prompt string, maxSteps int, subSink harness.EventSink) (string, error) {
					subExecutor, err := executor.New(registryRef, engineRef, nil, checkpointsRef, subSink)
					if err != nil {
						return "", err
					}
					subModel := selectedModel
					if strings.EqualFold(*provider, "demo") {
						subModel = model.NewScriptedDemo(scriptSteps(task.DelegateScripts[role]))
					}
					subAgent := harness.NewAgent(subModel, subExecutor, maxSteps, subSink)
					return subAgent.Run(ctx, prompt)
				},
				workspace,
			)
			delegate.SetEventSink(sink)
			toolList = append(baseToolList, delegate)
		} else {
			toolList = baseToolList
		}
		registry, err := harness.NewRegistry(toolList...)
		if err != nil {
			return "", err
		}
		// 关键点：直接复用线上的Agent实现，不在eval里另写一份循环
		// 评测使用 workspace-write：工作区内写入放行，命令执行经自动审批。
		engine, err := policy.NewEngine(policy.ModeWorkspaceWrite)
		if err != nil {
			return "", err
		}
		checkpoints, err := checkpoint.NewStore(filepath.Join(workspace, ".xcode-state", "checkpoints"), workspace)
		if err != nil {
			return "", err
		}
		secure, err := executor.New(registry, engine, evalAutoApprover{}, checkpoints, sink)
		if err != nil {
			return "", err
		}
		registryRef = registry
		engineRef = engine
		checkpointsRef = checkpoints
		// Demo Provider 按 Task 脚本构造模型；真实 Provider 沿用共享客户端。
		trialModel := selectedModel
		if strings.EqualFold(*provider, "demo") {
			trialModel = model.NewScriptedDemo(scriptSteps(task.DemoScript))
		}
		agent := harness.NewAgent(trialModel, secure, task.MaxSteps, sink)
		agent.SetContextManager(manager)
		return agent.Run(trialContext, task.Prompt)
	}

	// ========== 3. 执行评测并输出结果 ==========
	// NewRunner会校验outputDir/runID/config的完整性，参数不合法时提前失败
	runner, err := eval.NewRunner(*outputDir, runID, config, execute)
	if err != nil {
		return err
	}
	// 进度写入stderr，使终端可以实时显示，同时让stdout继续只承载最终报告信息。
	fmt.Fprintf(
		os.Stderr,
		"[eval] run=%s provider=%s model=%s tasks=%d trials=%d total=%d output=%s\n",
		runID,
		config.Provider,
		config.Model,
		len(catalog.Tasks),
		config.Trials,
		len(catalog.Tasks)*config.Trials,
		*outputDir,
	)
	runner.SetProgressSink(func(event eval.ProgressEvent) {
		writeEvalProgress(os.Stderr, event)
	})
	// 顺序执行所有 Task × Trial：逐个Trial写trace，最后汇总成report
	report, err := runner.Run(ctx, catalog)
	if err != nil {
		return err
	}
	// 把结构化report渲染成report.md等产物写入outputDir
	if err := eval.WriteReport(*outputDir, report); err != nil {
		return err
	}

	// 终端只打印最关键的两行：报告位置 + 通过率概览，详细信息去看产物文件
	fmt.Fprintf(os.Stdout, "eval report: %s\n", filepath.Join(*outputDir, "report.md"))
	fmt.Fprintf(
		os.Stdout,
		"passed trials: %d/%d evaluated; task failures: %d; run errors: %d; average task success: %.2f%%\n",
		report.RunSummary.PassedTrials,
		report.RunSummary.EvaluatedTrials,
		report.RunSummary.FailedTrials,
		report.RunSummary.ErroredTrials,
		report.RunSummary.AverageTaskSuccessRate*100,
	)
	// Task 失败或运行错误都会让命令返回非零退出码，但报告会分别保存两种数量。
	if report.RunSummary.FailedTrials > 0 || report.RunSummary.ErroredTrials > 0 {
		return fmt.Errorf(
			"eval completed with %d task failures and %d run errors",
			report.RunSummary.FailedTrials,
			report.RunSummary.ErroredTrials,
		)
	}
	return nil
}

// writeEvalProgress 把结构化 Trial 进度渲染成一行终端文本，不输出模型回答或工具内容。
func writeEvalProgress(writer io.Writer, event eval.ProgressEvent) {
	width := len(fmt.Sprintf("%d", event.Total))
	prefix := fmt.Sprintf("[%0*d/%d]", width, event.Position, event.Total)

	switch event.Kind {
	case eval.ProgressTrialStarted:
		fmt.Fprintf(writer, "%s START task=%s trial=%d\n", prefix, event.TaskID, event.Trial)
	case eval.ProgressTrialCompleted:
		status := "ERROR"
		switch event.Result.Outcome {
		case eval.TrialPassed:
			status = "PASS"
		case eval.TrialFailed:
			status = "FAIL"
		}
		fmt.Fprintf(
			writer,
			"%s %s task=%s trial=%d elapsed=%dms steps=%d tools=%d tokens=%d",
			prefix,
			status,
			event.TaskID,
			event.Trial,
			event.Result.Metrics.WallTimeMillis,
			event.Result.Metrics.Steps,
			event.Result.Metrics.ToolCalls,
			event.Result.Metrics.TotalTokens,
		)
		if event.Result.Error != "" {
			fmt.Fprintf(writer, " error=%q", compactProgressError(event.Result.Error))
		}
		fmt.Fprintln(writer)
	case eval.ProgressTrialAborted:
		fmt.Fprintf(
			writer,
			"%s ABORT task=%s trial=%d error=%q\n",
			prefix,
			event.TaskID,
			event.Trial,
			compactProgressError(event.Error),
		)
	}
}

// compactProgressError 把错误压缩成单行并限制长度，避免一条进度信息占满终端。
func compactProgressError(message string) string {
	message = strings.Join(strings.Fields(message), " ")
	const maxRunes = 160
	runes := []rune(message)
	if len(runes) <= maxRunes {
		return message
	}
	return string(runes[:maxRunes-1]) + "…"
}

// executableHarnessVersion 计算当前 xcode 可执行文件的 SHA-256，使代码变化进入 Eval 配置身份。
func executableHarnessVersion() (string, error) {
	executablePath, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("locate xcode executable: %w", err)
	}
	executable, err := os.Open(executablePath)
	if err != nil {
		return "", fmt.Errorf("open xcode executable: %w", err)
	}
	defer executable.Close()

	hasher := sha256.New()
	if _, err := io.Copy(hasher, executable); err != nil {
		return "", fmt.Errorf("hash xcode executable: %w", err)
	}
	return fmt.Sprintf("s3-eval-v0+sha256:%x", hasher.Sum(nil)), nil
}

// evalAutoApprover 在隔离的一次性 Trial 工作区内自动批准 ask 决策，使评测不需要交互终端。
// 安全边界：Trial 工作区是临时目录，命令仍受 exec 工具的工作目录与环境白名单约束；
// 该类型只用于评测装配，不得被交互式 CLI 使用。
type evalAutoApprover struct{}

// Approve 把评测中的 ask 决策一律按"仅本次"批准，审批事件照常进入 Trace。
func (evalAutoApprover) Approve(context.Context, policy.Request) (executor.ApprovalScope, error) {
	return executor.ApprovalOnce, nil
}

// scriptSteps 把评测目录中的 DemoStep 转换为脚本化模型的输入类型。
func scriptSteps(steps []eval.DemoStep) []model.ScriptStep {
	converted := make([]model.ScriptStep, 0, len(steps))
	for _, step := range steps {
		converted = append(converted, model.ScriptStep{Tool: step.Tool, Arguments: string(step.Arguments)})
	}
	return converted
}
