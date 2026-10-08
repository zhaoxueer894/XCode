package main

// 本文件实现 eval-fault 子命令：长任务故障评测的参数解析与栈装配。
// 装配层不复用 eval.go 的 execute 回调（那是"一次跑完"语义），而是提供 StackFunc：
// 运行器按策略与阶段多次调用它，phase2 依赖 Stack 携带的恢复证据路径。
// 策略→上下文策略的映射：reference/durable 用 compaction（隔离"恢复"这一个变量），
// decision-gated 用 decision-gated 压缩并可挂 shadow 决策器（Jev 模式的自研实现）。

import (
	"context"       // 阶段运行。
	"encoding/json" // 类型化分类输出解析。
	"flag"          // 参数解析。
	"fmt"           // 输出与错误。
	"os"            // 产物目录。
	"path/filepath" // 路径拼接。
	"strings"       // 策略名归一。
	"time"          // RunID 时间戳。

	"xcode/internal/artifactstore" // Trial 工件统一落盘。
	"xcode/internal/checkpoint"    // 写前快照仓库。
	"xcode/internal/config"        // 分类器模型配置（与主模型解耦）。
	"xcode/internal/contextmgr"    // 投影管理器与决策器。
	"xcode/internal/executor"      // 统一执行管线与 journal。
	"xcode/internal/faulteval"     // 被装配的故障评测运行器。
	"xcode/internal/harness"       // Agent 与栈类型。
	"xcode/internal/memory"        // L1 会话笔记。
	"xcode/internal/model"         // 脚本化 Demo。
	"xcode/internal/model/jev"     // TypeSafe Jev systemone 客户端（真接入，调用前需用户允许）。
	"xcode/internal/policy"        // workspace-write 档位。
	"xcode/internal/tools"         // 工具集。
	"xcode/internal/workspace"     // 门禁指纹。
)

// runFaultEval 是 eval-fault 子命令入口；replay 动词转交最小 Trace Replay。
func runFaultEval(ctx context.Context, args []string) error {
	if len(args) > 0 && args[0] == "replay" {
		return runFaultReplay(ctx, args[1:])
	}
	flags := flag.NewFlagSet("xcode eval-fault", flag.ContinueOnError)
	// 先读配置文件（.xcode/config），分类器模型配置作为 flag 默认值；flag 显式传入时覆盖。
	appConfig, configErr := config.Load(".")
	if configErr != nil {
		fmt.Fprintf(os.Stderr, "warning: load config: %v\n", configErr)
	}
	catalogPath := flags.String("catalog", "testdata/fault/tasks.json", "path to fault task catalog")
	outputDir := flags.String("output", "", "output directory (default .eval/fault/<runID>)")
	provider := flags.String("provider", "demo", "model provider: demo or bailian")
	trials := flags.Int("trials", 3, "trials per task per strategy (spec requires >= 3)")
	label := flags.String("label", "", "run label")
	contextBudget := flags.Int("context-budget", 1500, "estimated-token budget before compaction")
	typedShadow := flags.Bool("typed-shadow", false, "record typed shadow decisions alongside decision-gated runs (Jev-pattern, self-implemented)")
	typedClassifierDefault := appConfig.TypedClassifierBackend
	if strings.TrimSpace(typedClassifierDefault) == "" {
		typedClassifierDefault = "local"
	}
	typedClassifier := flags.String("typed-classifier", typedClassifierDefault, "typed shadow classifier backend: local (chat LLM) or jev (TypeSafe systemone; requires TYPESAFE_API_KEY and user approval for real calls)")
	typedClassifierEndpoint := flags.String("typed-classifier-endpoint", appConfig.TypedClassifierEndpoint, "local classifier OpenAI-compatible endpoint (config: typed-classifier-endpoint; empty = reuse main model)")
	typedClassifierModel := flags.String("typed-classifier-model", appConfig.TypedClassifierModel, "local classifier model name (config: typed-classifier-model; empty = reuse main model)")
	typedClassifierKeyEnv := flags.String("typed-classifier-api-key-env", appConfig.TypedClassifierKeyEnvVar, "env var name holding the local classifier API key (config: typed-classifier-api-key-env; empty = DASHSCOPE_API_KEY)")
	typedApply := flags.Bool("typed-apply", false, "let the typed classifier APPLY its compaction decisions (effective decider with rule fallback); makes real classifier calls")
	classifierKeyEnv := strings.TrimSpace(*typedClassifierKeyEnv)
	if classifierKeyEnv == "" {
		classifierKeyEnv = "DASHSCOPE_API_KEY"
	}
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("xcode eval-fault does not accept a prompt")
	}

	catalog, catalogHash, err := faulteval.LoadFaultCatalog(*catalogPath)
	if err != nil {
		return err
	}
	selectedModel, err := buildModel(*provider)
	if err != nil {
		return err
	}
	runID := time.Now().UTC().Format("20060102T150405.000000000Z")
	if strings.TrimSpace(*outputDir) == "" {
		*outputDir = filepath.Join(".eval", "fault", runID)
	}
	artifacts, err := artifactstore.NewFileStore(filepath.Join(*outputDir, "artifacts"))
	if err != nil {
		return err
	}
	modelName, providerEndpoint := "demo", ""
	if strings.EqualFold(*provider, "bailian") {
		modelName = envOrDefault("DASHSCOPE_MODEL", defaultBailianModel)
		providerEndpoint = envOrDefault("DASHSCOPE_BASE_URL", defaultBailianBaseURL)
	}
	harnessVersion, err := executableHarnessVersion()
	if err != nil {
		return err
	}
	config := faulteval.FaultConfig{
		HarnessVersion:          harnessVersion,
		Provider:                strings.ToLower(strings.TrimSpace(*provider)),
		Model:                   modelName,
		ProviderEndpoint:        providerEndpoint,
		Trials:                  *trials,
		ContextBudget:           *contextBudget,
		Label:                   strings.TrimSpace(*label),
		TypedShadow:             *typedShadow,
		TypedClassifier:         strings.ToLower(strings.TrimSpace(*typedClassifier)),
		TypedClassifierEndpoint: strings.TrimSpace(*typedClassifierEndpoint),
		TypedClassifierModel:    strings.TrimSpace(*typedClassifierModel),
		TypedApplied:            *typedApply,
		CatalogHash:             catalogHash,
	}

	// 栈装配回调：运行器每阶段调用一次；策略决定上下文策略与恢复日志的挂载。
	build := func(ctx context.Context, ws string, task faulteval.FaultTask, sink harness.EventSink,
		strategy faulteval.Strategy, skipSteps int) (*faulteval.Stack, error) {
		return buildFaultStack(ctx, ws, task, sink, strategy, skipSteps, selectedModel, *provider, artifacts, config, classifierKeyEnv)
	}
	runner, err := faulteval.NewStackRunner(*outputDir, runID, config, build)
	if err != nil {
		return err
	}
	runner.SetProgress(func(line string) { fmt.Fprintln(os.Stderr, line) })
	fmt.Fprintf(os.Stderr, "[eval-fault] run=%s provider=%s model=%s tasks=%d trials=%d strategies=3 output=%s\n",
		runID, config.Provider, config.Model, len(catalog.Tasks), config.Trials, *outputDir)

	report, err := runner.Run(ctx, catalog)
	if err != nil {
		return err
	}
	if err := faulteval.WriteFaultReport(*outputDir, report); err != nil {
		return err
	}
	fmt.Fprintf(os.Stdout, "fault eval report: %s\n", filepath.Join(*outputDir, "report.md"))
	for _, summary := range report.Strategies {
		fmt.Fprintf(os.Stdout, "%s: success=%.0f%% recovery=%.0f%% premature=%.0f%% facts=%.0f%% input_tokens=%d duplicates=%d\n",
			summary.Strategy, summary.SuccessRate*100, summary.RecoverySuccessRate*100,
			summary.PrematureRate*100, summary.FactRetentionRate*100,
			summary.TotalInputTokens, summary.TotalDuplicates)
	}
	return nil
}

// buildFaultStack 按策略装配一次阶段运行的完整栈；与 eval.go 的 execute 平行但语义不同。
func buildFaultStack(ctx context.Context, ws string, task faulteval.FaultTask, sink harness.EventSink,
	strategy faulteval.Strategy, skipSteps int, selectedModel harness.Model, provider string,
	artifacts *artifactstore.FileStore, config faulteval.FaultConfig, classifierKeyEnv string) (*faulteval.Stack, error) {

	contextStrategy := contextmgr.StrategyCompaction
	if strategy == faulteval.StrategyDecisionGated {
		contextStrategy = contextmgr.StrategyDecisionGated
	}
	epoch := contextmgr.NewEpoch()
	readOnlyTools, err := tools.NewReadOnlySetWithEpoch(ctx, ws, artifacts, epoch.Current)
	if err != nil {
		return nil, err
	}
	manager, mgrErr := contextmgr.NewManager(contextStrategy, config.ContextBudget, epoch)
	if mgrErr != nil {
		return nil, mgrErr
	}
	manager.SetNoteRecorder(memory.NewSessionStore(filepath.Join(ws, ".xcode-state", "memory", "session-notes.json")))
	if !strings.EqualFold(provider, "demo") {
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
		// reference 策略对应公开模式：在任务说明后附加自维护进展要点提示。
		if strategy == faulteval.StrategyReference {
			task.Prompt += "\n[harness-note] 这是可能被中断的长任务：请在回复中随进度维护简短进展要点，便于上下文压缩后继续。"
		}
	}

	// shadow 分类后端（local/jev）统一走 mountTypedShadow——与主命令路径共用同一接线。
	// local 分类器支持独立端点+模型（配置解耦），demo 主模型下也能采集真实 LLM 分类基线；
	// jev 后端按约定：接线完成即可跑，是否发起真实调用由用户决定。
	// 挂载条件:shadow(只对照)或 applied(拍板)任一开启;applied 时分类器为生效决策器。
	if (config.TypedShadow || config.TypedApplied) && strategy == faulteval.StrategyDecisionGated {
		if err := mountTypedShadow(manager, config.TypedClassifier, provider, selectedModel, config.TypedClassifierEndpoint, config.TypedClassifierModel, classifierKeyEnv, config.TypedApplied); err != nil {
			return nil, err
		}
	}

	registry, err := harness.NewRegistry(append(readOnlyTools.Tools(), readOnlyTools.WriteTools()...)...)
	if err != nil {
		return nil, err
	}
	engine, err := policy.NewEngine(policy.ModeWorkspaceWrite)
	if err != nil {
		return nil, err
	}
	checkpoints, err := checkpoint.NewStore(filepath.Join(ws, ".xcode-state", "checkpoints"), ws)
	if err != nil {
		return nil, err
	}
	secure, err := executor.New(registry, engine, evalAutoApprover{}, checkpoints, sink)
	if err != nil {
		return nil, err
	}
	journalDir := filepath.Join(ws, ".xcode-state", "journal")
	journal, err := executor.OpenJournal(filepath.Join(journalDir,
		"phase-"+time.Now().UTC().Format("150405.000000000")+".jsonl"))
	if err != nil {
		return nil, err
	}
	secure.SetJournal(journal)

	trialModel := selectedModel
	if strings.EqualFold(provider, "demo") {
		demo := model.NewScriptedDemo(faultScriptSteps(task.DemoScript))
		demo.Advance(skipSteps)
		trialModel = demo
	}
	agent := harness.NewAgent(trialModel, secure, task.MaxSteps, sink)
	agent.SetContextManager(manager)

	stack := &faulteval.Stack{
		Agent:       agent,
		TurnLogPath: filepath.Join(ws, ".xcode-state", "recovery", "turn-latest.jsonl"),
		JournalDir:  journalDir,
		Store:       checkpoints,
	}
	if strategy != faulteval.StrategyReference {
		turnLog, logErr := harness.NewTurnLog(stack.TurnLogPath)
		if logErr != nil {
			return nil, logErr
		}
		agent.SetTurnLog(turnLog)
	}
	if task.Gate {
		contract := &harness.TaskContract{
			Goal: task.Prompt,
			VerifyCommands: []harness.VerifyCommand{{
				Name:    "verify",
				Command: task.VerifyCommand,
			}},
		}
		stack.Contract = contract
		stack.Fingerprint = func() (string, error) { return workspace.Fingerprint(ws) }
	}
	return stack, nil
}

// runFaultReplay 实现 eval-fault replay 动词：从源 Trace 重放同一任务与策略。
func runFaultReplay(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("xcode eval-fault replay", flag.ContinueOnError)
	tracePath := flags.String("trace", "", "source trial trace (JSONL)")
	catalogPath := flags.String("catalog", "testdata/fault/tasks.json", "path to fault task catalog")
	outputDir := flags.String("output", "", "replay output directory (default .eval/fault/replay-<ts>)")
	provider := flags.String("provider", "demo", "model provider: demo or bailian")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*tracePath) == "" {
		return fmt.Errorf("replay requires --trace")
	}
	catalog, catalogHash, err := faulteval.LoadFaultCatalog(*catalogPath)
	if err != nil {
		return err
	}
	appConfig, configErr := config.Load(".")
	if configErr != nil {
		fmt.Fprintf(os.Stderr, "warning: load config: %v\n", configErr)
	}
	selectedModel, err := buildModel(*provider)
	if err != nil {
		return err
	}
	if strings.TrimSpace(*outputDir) == "" {
		*outputDir = filepath.Join(".eval", "fault", "replay-"+time.Now().UTC().Format("20060102T150405.000000000Z"))
	}
	artifacts, err := artifactstore.NewFileStore(filepath.Join(*outputDir, "artifacts"))
	if err != nil {
		return err
	}
	harnessVersion, err := executableHarnessVersion()
	if err != nil {
		return err
	}
	config := faulteval.FaultConfig{
		HarnessVersion: harnessVersion,
		Provider:       strings.ToLower(strings.TrimSpace(*provider)),
		Model:          "demo",
		Trials:         1,
		ContextBudget:  1500,
		CatalogHash:    catalogHash,
	}
	if strings.EqualFold(*provider, "bailian") {
		config.Model = envOrDefault("DASHSCOPE_MODEL", defaultBailianModel)
		config.ProviderEndpoint = envOrDefault("DASHSCOPE_BASE_URL", defaultBailianBaseURL)
	}
	replayKeyEnv := strings.TrimSpace(appConfig.TypedClassifierKeyEnvVar)
	if replayKeyEnv == "" {
		replayKeyEnv = "DASHSCOPE_API_KEY"
	}
	build := func(ctx context.Context, ws string, task faulteval.FaultTask, sink harness.EventSink,
		strategy faulteval.Strategy, skipSteps int) (*faulteval.Stack, error) {
		return buildFaultStack(ctx, ws, task, sink, strategy, skipSteps, selectedModel, *provider, artifacts, config, replayKeyEnv)
	}
	runner, err := faulteval.NewStackRunner(*outputDir, "replay-"+time.Now().UTC().Format("20060102T150405.000000000Z"), config, build)
	if err != nil {
		return err
	}
	result, err := runner.Replay(ctx, catalog, faulteval.ReplayRequest{
		TracePath:   *tracePath,
		CatalogPath: *catalogPath,
		OutputDir:   *outputDir,
	})
	if err != nil {
		return err
	}
	encoded, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return err
	}
	fmt.Println(string(encoded))
	return nil
}

// faultScriptSteps 把故障任务目录中的脚本步骤转换为脚本化模型输入。
func faultScriptSteps(steps []faulteval.FaultStep) []model.ScriptStep {
	converted := make([]model.ScriptStep, 0, len(steps))
	for _, step := range steps {
		converted = append(converted, model.ScriptStep{Tool: step.Tool, Arguments: string(step.Arguments)})
	}
	return converted
}

// buildTypedClassify 构造 shadow 决策器的类型化分类调用（Jev 模式的自研实现）：把证据单元渲染给模型，
// 要求其输出 JSON 判定数组；解析失败、单元数不符或低置信度都由 TypedModelDecider 回退规则。
func buildTypedClassify(classifier harness.Model) contextmgr.ClassificationFunc {
	return func(ctx context.Context, units []contextmgr.Unit) ([]contextmgr.UnitDecision, error) {
		var prompt strings.Builder
		prompt.WriteString("你是上下文压缩决策器。对每个证据单元给出 keep/truncate/drop 判定。\n")
		prompt.WriteString("固定保留：任务目标、未解决错误、验证证据、最近对话。只读工具结果可 drop。\n")
		for index, unit := range units {
			fmt.Fprintf(&prompt, "\n[单元 %d]\n", index)
			for _, message := range unit.Messages {
				fmt.Fprintf(&prompt, "(%s) %s\n", message.Role, truncateUnitText(message.Content, 200))
			}
		}
		prompt.WriteString("\n只输出 JSON 数组，每项形如 {\"index\":0,\"decision\":\"keep\",\"reason\":\"...\",\"confidence\":0.9}\n")
		response, err := classifier.Generate(ctx, []harness.Message{
			{Role: "user", Content: prompt.String()},
		}, nil, harness.ModelEventSinkFunc(func(harness.ModelEvent) {}))
		if err != nil {
			return nil, err
		}
		start := strings.Index(response.Content, "[")
		end := strings.LastIndex(response.Content, "]")
		if start < 0 || end <= start {
			return nil, fmt.Errorf("classifier output has no JSON array")
		}
		var decisions []struct {
			Index      int     `json:"index"`
			Decision   string  `json:"decision"`
			Reason     string  `json:"reason"`
			Confidence float64 `json:"confidence"`
		}
		if err := json.Unmarshal([]byte(response.Content[start:end+1]), &decisions); err != nil {
			return nil, fmt.Errorf("decode classifier decisions: %w", err)
		}
		results := make([]contextmgr.UnitDecision, 0, len(decisions))
		for _, decision := range decisions {
			reason := decision.Reason
			if decision.Confidence > 0 && decision.Confidence < 0.6 {
				reason = "low_confidence"
			}
			results = append(results, contextmgr.UnitDecision{
				Index:    decision.Index,
				Decision: contextmgr.Decision(strings.ToLower(strings.TrimSpace(decision.Decision))),
				Reason:   reason,
			})
		}
		return results, nil
	}
}

// truncateUnitText 截断文本供分类提示使用。
func truncateUnitText(value string, limit int) string {
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit-1]) + "…"
}

// buildJevSystemOneClassify 构造基于 TypeSafe systemone 端点的类型化分类后端：
// 把证据单元渲染为 state，每个单元生成一个 Choice 问题，把带校准概率的答案映射为判定。
// 调用失败、缺答案或选项非法都返回 error，由 TypedModelDecider 整体回退规则判定；
// 低置信度（<0.6）按约定标记 low_confidence 触发回退。
func buildJevSystemOneClassify(client *jev.Client) contextmgr.ClassificationFunc {
	return func(ctx context.Context, units []contextmgr.Unit) ([]contextmgr.UnitDecision, error) {
		state := make(map[string]string, len(units))
		questions := make(map[string]jev.Question, len(units))
		for index, unit := range units {
			var body strings.Builder
			for _, message := range unit.Messages {
				fmt.Fprintf(&body, "(%s) %s\n", message.Role, truncateUnitText(message.Content, 200))
			}
			id := fmt.Sprintf("unit-%d", index)
			state[id] = body.String()
			questions[id] = jev.Question{
				Type:         jev.QuestionChoice,
				Instructions: "Context compaction decision for this evidence unit in a coding-agent transcript.",
				Criteria: map[string]string{
					"keep":     "current goal, unresolved error, verification evidence (exit_code), or recent conversation",
					"truncate": "old prose or write-tool pair outside the recent window",
					"drop":     "read-only tool pair whose content is externalized in artifacts",
				},
			}
		}
		answers, err := client.SystemOne(ctx, state, questions)
		if err != nil {
			return nil, err
		}
		results := make([]contextmgr.UnitDecision, 0, len(units))
		for index := range units {
			id := fmt.Sprintf("unit-%d", index)
			answer, ok := answers[id]
			if !ok {
				return nil, fmt.Errorf("jev: missing answer for %s", id)
			}
			decision := strings.ToLower(strings.TrimSpace(answer.Choice))
			switch decision {
			case "keep", "truncate", "drop":
			default:
				return nil, fmt.Errorf("jev: unknown decision %q for %s", answer.Choice, id)
			}
			reason := fmt.Sprintf("jev confidence=%.2f", answer.Confidence)
			if answer.Confidence > 0 && answer.Confidence < 0.6 {
				reason = "low_confidence"
			}
			results = append(results, contextmgr.UnitDecision{
				Index:    index,
				Decision: contextmgr.Decision(decision),
				Reason:   reason,
			})
		}
		return results, nil
	}
}
