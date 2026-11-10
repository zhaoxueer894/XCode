package main

// 提供用户可以直接运行的命令行程序

import (
	"bufio"         // 逐行扫描旧版 .env 以生成迁移提示。
	"context"       // 控制整次 Agent 运行的取消与超时。
	"encoding/json" // 把结构化 Event 编码成一行 JSON。
	"errors"        // 合并多处延迟关闭的报错。
	"flag"          // 使用标准库解析 --workspace 和 --max-steps 参数。
	"fmt"           // 输出最终答案和错误信息。
	"io"            // 让极简进度渲染函数可以写入任意输出目标。
	"os"            // 访问命令行标准输出、标准错误和退出码。
	"os/signal"     // 把 Ctrl+C 转换为 Context 取消信号。
	"path/filepath" // 生成事件日志的默认路径并创建父目录。
	"strings"       // 把多个位置参数重新拼成完整用户提示。
	"sync"
	"time" // 表示整次运行的超时时间。

	"xcode/internal/artifactstore" // 把普通 CLI 的完整工具结果保存到本地状态目录。
	"xcode/internal/checkpoint"    // 写前快照的保存与恢复。
	"xcode/internal/config"        // 配置文件加载与合并。
	"xcode/internal/contextmgr"    // 模型可见投影与压缩策略。
	"xcode/internal/executor"      // 统一工具执行管线。
	"xcode/internal/harness"       // 组装 Registry、Agent 和 EventSink。
	"xcode/internal/memory"        // L1 会话笔记与 L2 项目记忆。
	"xcode/internal/model"         // 使用不需要 API Key 的 Demo Model。
	"xcode/internal/model/bailian" // 提供阿里云百炼的流式模型适配器。
	"xcode/internal/model/deepseek"
	"xcode/internal/policy"
	"xcode/internal/recovery" // 恢复分析与副作用对账。
	"xcode/internal/sandbox"  // 策略引擎与运行 Mode。
	"xcode/internal/tools"    // 注册安全的 read_file 工具。
)

const (
	defaultBailianBaseURL = "https://dashscope.aliyuncs.com/compatible-mode/v1" // 百炼 OpenAI 兼容接口的基础地址。
	defaultBailianModel   = "qwen-plus"                                         // 默认使用的百炼模型。
)

// main 是 xcode CLI 的程序入口。
func main() { check(runCLI(os.Args[1:])) }

// runCLI returns before main exits, so failure paths flush logs and result files.
func runCLI(args []string) (runErr error) {
	// Batch callers can opt out of ambient configuration and .env loading.
	isolated := false
	for _, arg := range args {
		if arg == "--" {
			break
		}
		if arg == "--isolated" || arg == "--isolated=true" {
			isolated = true
		}
		if arg == "--isolated=false" {
			isolated = false
		}
	}
	// .env 已被统一配置文件取代；检测到旧文件时提示迁移，不再注入进程环境。
	if !isolated {
		noticeLegacyDotEnv()
	}
	// eval 使用独立 FlagSet
	if len(args) > 0 && args[0] == "eval" {
		evalContext, stop := signal.NotifyContext(context.Background(), os.Interrupt)
		defer stop()
		if err := runEval(evalContext, args[1:]); err != nil {
			return err
		}
		return nil
	}

	// 可选的用户级 PATH 安装（不装也始终可以 bin/xcode.exe 直接启动）。
	if len(args) > 0 && args[0] == "install" {
		if err := runInstall(args[1:]); err != nil {
			return err
		}
		return nil
	}

	// 长任务故障评测子命令（三策略对照 + replay）。
	if len(args) > 0 && args[0] == "eval-fault" {
		faultContext, stop := signal.NotifyContext(context.Background(), os.Interrupt)
		defer stop()
		if err := runFaultEval(faultContext, args[1:]); err != nil {
			return err
		}
		return nil
	}

	// 查看与恢复写前快照的独立子命令。
	if len(args) > 0 && args[0] == "checkpoints" {
		if err := runCheckpoints(args[1:]); err != nil {
			return err
		}
		return nil
	}
	if len(args) > 0 && args[0] == "restore" {
		if err := runRestore(args[1:]); err != nil {
			return err
		}
		return nil
	}
	if len(args) > 0 && args[0] == "policy" {
		if err := runPolicyCheck(args[1:]); err != nil {
			return err
		}
		return nil
	}
	if len(args) > 0 && args[0] == "journal" {
		if err := runJournal(args[1:]); err != nil {
			return err
		}
		return nil
	}

	// 预扫描 --workspace 以定位配置文件，配置值作为 flag 默认值。
	presetWorkspace := "."
	for i, arg := range args {
		if arg == "--workspace" && i+1 < len(args) {
			presetWorkspace = args[i+1]
		}
		if strings.HasPrefix(arg, "--workspace=") {
			presetWorkspace = strings.TrimPrefix(arg, "--workspace=")
		}
	}
	appConfig := config.Defaults()
	var configErr error
	if !isolated {
		appConfig, configErr = config.Load(presetWorkspace)
	}
	if configErr != nil {
		fmt.Fprintf(os.Stderr, "warning: load config: %v\n", configErr)
	}

	flags := flag.NewFlagSet("xcode", flag.ContinueOnError)
	flags.BoolVar(&isolated, "isolated", isolated, "ignore user/project config files; use flags and process environment")
	// 定义可选命令行参数；返回的是指针，调用 flag.Parse 后再解引用读取值。
	// 默认值来自配置文件（flag 显式传入时覆盖配置）。
	workspace := flags.String("workspace", presetWorkspace, "workspace root")
	maxSteps := flags.Int("max-steps", appConfig.MaxSteps, "maximum model iterations")
	provider := flags.String("provider", appConfig.Provider, "model provider: demo, bailian or deepseek")
	timeout := flags.Duration("timeout", appConfig.Timeout, "maximum duration of one run")
	artifactDir := flags.String("artifact-dir", ".xcode-state/artifacts", "directory for full tool result artifacts")
	eventLog := flags.String("event-log", "", "JSONL event log path (default .xcode-state/logs/run-<timestamp>.jsonl)")
	verbose := flags.Bool("verbose", false, "also print full JSONL events to stderr")
	mode := flags.String("mode", appConfig.Mode, "capability mode: plan, workspace-write or full-access")
	checkpointDir := flags.String("checkpoint-dir", "", "directory for pre-write checkpoints")
	maxTokens := flags.Int("max-tokens", appConfig.MaxTokens, "maximum total tokens per run (0 = unlimited)")
	doomLoopLimit := flags.Int("doom-loop-limit", 3, "stop when the same tool call repeats this many times in a row")
	contextStrategy := flags.String("context-strategy", appConfig.ContextStrategy, "context strategy: bounded, none, sliding, handoff, compaction or decision-gated")
	contextBudget := flags.Int("context-budget", appConfig.ContextBudget, "estimated-token budget before compaction")
	sandboxBackend := flags.String("sandbox", appConfig.Sandbox, "command backend: host, docker or docker-wsl")
	sandboxImage := flags.String("sandbox-image", appConfig.SandboxImage, "docker image used by the docker sandbox backend")
	policyPath := flags.String("policy", "", "policy rules file (default <workspace>/.xcode/policy.json)")
	resumeFlag := flags.Bool("resume", false, "resume the last unfinished turn from the recovery log")
	contractFlag := flags.String("contract", "", "task contract JSON enabling the completion gate (goal, verify_commands, optional review)")
	typedShadow := flags.Bool("typed-shadow", false, "mount the typed shadow classifier (local or jev per .xcode/config) alongside decision-gated compaction; the jev backend makes real systemone calls only when enabled")
	nonInteractive := flags.Bool("non-interactive", false, "never prompt; deny commands requiring approval unless policy explicitly allows them")
	noDelegate := flags.Bool("no-delegate", false, "disable subagents for single-agent comparisons")
	maxModelCalls := flags.Int("max-model-calls", 0, "shared Generate-call limit, including subagents and summaries (0 = unlimited)")
	resultFile := flags.String("result-file", "", "write a JSON run result on completion or failure; not a correctness grade")
	promptFile := flags.String("prompt-file", "", "read the task from a UTF-8 file instead of positional arguments")
	providerRetries := flags.Int("provider-retries", 2, "additional provider HTTP attempts (0 disables retries)")
	thinking := flags.String("thinking", "enabled", "DeepSeek thinking mode: enabled or disabled")
	reasoningEffort := flags.String("reasoning-effort", "high", "DeepSeek reasoning effort: low, high or max")
	maxOutputTokens := flags.Int("max-output-tokens", 0, "DeepSeek per-request output limit (0 = 8192)")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	providerExplicit := false
	flags.Visit(func(f *flag.Flag) {
		if f.Name == "provider" {
			providerExplicit = true
		}
	})
	// 首次运行向导与 demo/Key 矛盾检测：仅在交互式终端且 provider 未配置时介入。
	if !isolated && !*nonInteractive && *resultFile == "" && !providerExplicit {
		chosen, changed, setupErr := runSetupWizard(appConfig)
		if setupErr != nil {
			return setupErr
		}
		if changed {
			*provider = chosen
			appConfig.Provider = chosen
			appConfig.ProviderSet = true
		}
	}
	// 位置参数为空时进入交互模式；有参数时为一次性命令。
	interactive := flags.NArg() == 0 && *promptFile == ""
	record := newRunRecord(runRecordConfig{
		Workspace: *workspace, Provider: *provider, Mode: *mode, Sandbox: *sandboxBackend,
		ContextStrategy: *contextStrategy, MaxSteps: *maxSteps, MaxTokens: *maxTokens,
		MaxModelCalls: *maxModelCalls, Timeout: timeout.String(), NoDelegate: *noDelegate,
		NonInteractive: *nonInteractive,
	})
	var budget *harness.ModelBudget
	defer func() {
		if budget != nil {
			record.Model = budget.Snapshot()
		}
		record.finish(runErr)
		if *resultFile != "" {
			runErr = errors.Join(runErr, writeRunRecord(*resultFile, record))
		}
	}()
	record.Config.ContextBudget, record.Config.SandboxImage = *contextBudget, *sandboxImage
	record.Config.Isolated = isolated
	record.Config.DoomLoopLimit, record.Config.ProviderRetries = *doomLoopLimit, *providerRetries
	if *resultFile != "" {
		if _, err := os.Lstat(*resultFile); err == nil {
			return fmt.Errorf("result-file already exists: %s", *resultFile)
		} else if !os.IsNotExist(err) {
			return err
		}
	}
	if *timeout <= 0 || *maxSteps <= 0 || *maxTokens < 0 || *maxModelCalls < 0 || *providerRetries < 0 {
		return fmt.Errorf("timeout and max-steps must be positive; token and model-call limits must not be negative")
	}
	if (*nonInteractive || *resultFile != "") && (interactive || *resumeFlag) {
		return fmt.Errorf("non-interactive/result-file runs require a task and do not support REPL or resume")
	}
	if *resultFile != "" && !*nonInteractive {
		return fmt.Errorf("result-file requires --non-interactive")
	}
	if *nonInteractive && configErr != nil {
		return configErr
	}
	if *typedShadow && *nonInteractive {
		return fmt.Errorf("typed-shadow is not yet accounted for by batch run budgets; disable it for non-interactive runs")
	}
	prompt := strings.Join(flags.Args(), " ")
	if *promptFile != "" {
		if flags.NArg() != 0 {
			return fmt.Errorf("prompt-file and positional task are mutually exclusive")
		}
		data, err := os.ReadFile(*promptFile)
		if err != nil {
			return fmt.Errorf("read prompt: %w", err)
		}
		prompt = strings.TrimSpace(string(data))
		if prompt == "" {
			return fmt.Errorf("prompt file is empty")
		}
	}
	record.PromptHash = hashRunBytes([]byte(prompt))
	version, err := executableHarnessVersion()
	if err != nil {
		return err
	}
	record.BinaryVersion = version
	if strings.EqualFold(*provider, "bailian") {
		bailianRun := bailianSettings(appConfig, *providerRetries)
		record.Config.Model = bailianRun.Model
		record.Config.Endpoint = safeEndpoint(bailianRun.BaseURL)
	}
	var deepseekConfig deepseek.Config
	if strings.EqualFold(*provider, "deepseek") {
		if *reasoningEffort != "low" && *reasoningEffort != "high" && *reasoningEffort != "max" {
			return fmt.Errorf("reasoning-effort must be low, high or max")
		}
		deepseekConfig = deepseekSettings(appConfig, *thinking, *reasoningEffort, *providerRetries, *maxOutputTokens)
		record.Config.Model, record.Config.Endpoint = deepseekConfig.Model, safeEndpoint(deepseekConfig.BaseURL)
		record.Config.Thinking, record.Config.ReasoningEffort = deepseekConfig.Thinking, deepseekConfig.ReasoningEffort
		record.Config.MaxOutputTokens = deepseekConfig.MaxOutputTokens
	}

	// Index 建立、工具执行和模型请求共享同一套 Ctrl+C 与超时信号。
	signalContext, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	ctx, cancel := context.WithTimeout(signalContext, *timeout)
	defer cancel()

	// 完整工具结果保存在本地 Artifact Store，模型消息只接收受限预览。
	artifacts, err := artifactstore.NewFileStore(*artifactDir)
	if err != nil {
		return err
	}
	// 三种只读工具共享 Workspace Index 和 ReadSet，保证路径与忽略规则一致。
	readOnlyTools, err := tools.NewReadOnlySet(ctx, *workspace, artifacts)
	if err != nil {
		return err
	}
	record.Config.Workspace = readOnlyTools.WorkspaceRoot()
	// 根据 --provider 选择模型；密钥与端点来自配置文件（环境变量兜底）。
	var selectedModel harness.Model
	if strings.EqualFold(*provider, "deepseek") {
		selectedModel, err = deepseek.New(deepseekConfig)
	} else {
		selectedModel, err = buildModelWithRetryLimit(appConfig, *provider, *providerRetries)
	}
	if err != nil {
		return err
	}
	// 完整事件按 JSONL 落盘供事后排查；终端默认只渲染每步的极简进度行。
	logPath := strings.TrimSpace(*eventLog)
	if logPath == "" {
		logPath = filepath.Join(".xcode-state", "logs", "run-"+time.Now().UTC().Format("20060102T150405.000000000Z")+".jsonl")
	}
	record.EventLog, _ = filepath.Abs(logPath)
	if err := os.MkdirAll(filepath.Dir(logPath), 0o700); err != nil {
		return err
	}
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := errors.Join(logFile.Sync(), logFile.Close()); closeErr != nil {
			if *nonInteractive {
				runErr = errors.Join(runErr, closeErr)
			} else {
				fmt.Fprintln(os.Stderr, "warning: close event log:", closeErr)
			}
		}
	}()
	fmt.Fprintf(os.Stderr, "[xcode] provider=%s event log: %s\n", *provider, logPath)

	// 日志写入属于诊断路径：记录首个错误并在结束时提示，不让它中断 Agent 主流程。
	var logWriteErr error
	var sinkMu sync.Mutex
	sink := harness.EventSinkFunc(func(event harness.Event) {
		sinkMu.Lock()
		defer sinkMu.Unlock()
		if event.Kind == harness.ApprovalResolved {
			record.Approvals++
		}
		data, encodeErr := json.Marshal(event)
		if encodeErr != nil {
			if logWriteErr == nil {
				logWriteErr = encodeErr
			}
			return
		}
		if _, writeErr := logFile.Write(append(data, '\n')); writeErr != nil && logWriteErr == nil {
			logWriteErr = writeErr
		}
		if *verbose {
			fmt.Fprintln(os.Stderr, string(data))
			return
		}
		writeCompactEvent(os.Stderr, event)
	})
	budget = harness.NewModelBudget(*maxModelCalls, *maxTokens, sink)
	summaryModel := budget.Wrap(selectedModel, "summary")
	selectedModel = budget.Wrap(selectedModel, "agent")

	// 创建 delegate 工具并注册到 Registry。
	// factory 使用延迟引用打破 Agent↔Executor 循环依赖。
	// 子智能体用独立 Executor（自有锁），共享策略/快照/Registry，避免 delegate 持锁导致死锁。
	var registryRef *harness.Registry
	var engineRef *policy.Engine
	var checkpointsRef *checkpoint.Store
	var approver executor.Approver
	if !*nonInteractive {
		approver = newTerminalApprover()
	}
	delegate := harness.NewDelegate(
		func(ctx context.Context, role, prompt string, maxSteps int, subSink harness.EventSink) (string, error) {
			subExecutor, err := executor.New(registryRef, engineRef, approver, checkpointsRef, subSink)
			if err != nil {
				return "", err
			}
			subAgent := harness.NewAgent(selectedModel, subExecutor, maxSteps, subSink)
			return subAgent.Run(ctx, prompt)
		},
		readOnlyTools.WorkspaceRoot(),
	)
	delegate.SetEventSink(sink)

	// 写入与执行工具一并注册；加入 delegate 后重建。
	baseTools := append(readOnlyTools.Tools(), readOnlyTools.WriteTools()...)
	allTools := baseTools
	if !*noDelegate {
		allTools = append(allTools, delegate)
	}
	registry, err := harness.NewRegistry(allTools...)
	if err != nil {
		return err
	}
	// 策略引擎、写前快照仓库与交互审批组成统一执行管线。
	engine, err := policy.NewEngine(policy.Mode(*mode))
	if err != nil {
		return err
	}
	// 加载可选的项目级规则文件；默认位于工作区 .xcode/policy.json。
	resolvedPolicyPath := *policyPath
	if resolvedPolicyPath == "" {
		resolvedPolicyPath = filepath.Join(readOnlyTools.WorkspaceRoot(), ".xcode", "policy.json")
	}
	ruleSet, rulesErr := policy.LoadRulesFile(resolvedPolicyPath)
	if err := rulesErr; err != nil {
		return err
	}
	engine.SetRules(ruleSet)
	if ruleSet != nil {
		data, _ := json.Marshal(ruleSet)
		record.Config.PolicyHash = hashRunBytes(data)
	}
	if *checkpointDir == "" {
		*checkpointDir = filepath.Join(readOnlyTools.WorkspaceRoot(), ".xcode-state", "checkpoints")
	}
	checkpoints, err := checkpoint.NewStore(*checkpointDir, readOnlyTools.WorkspaceRoot())
	if err != nil {
		return err
	}
	secure, err := executor.New(registry, engine, approver, checkpoints, sink)
	if err != nil {
		return err
	}
	registryRef = registry
	engineRef = engine
	checkpointsRef = checkpoints

	// ToolCall 状态 journal，与事件日志同目录层次，便于对账。
	journal, journalErr := executor.OpenJournal(filepath.Join(readOnlyTools.WorkspaceRoot(), ".xcode-state", "journal", "run-"+time.Now().UTC().Format("20060102T150405.000000000Z")+".jsonl"))
	if err := journalErr; err != nil {
		return err
	}
	defer func() { runErr = errors.Join(runErr, journal.Close()) }()
	secure.SetJournal(journal)

	// 只读工具的 ReadSet 使用共享纪元，压缩后同范围读取会重新注入全文。
	epoch := contextmgr.NewEpoch()
	readOnlyTools.SetEpochProvider(epoch.Current)
	// 选择命令执行后端；docker 需要守护进程可达，启动前探测并给出明确错误。
	// docker-wsl 经 wsl.exe 调用 WSL 内的 Docker CE，无需 Docker Desktop。
	backendChoice := strings.ToLower(strings.TrimSpace(*sandboxBackend))
	switch backendChoice {
	case "", "host":
	case "docker":
		if err := sandbox.ProbeDocker(ctx, false); err != nil {
			return err
		}
		readOnlyTools.SetSandbox(sandbox.DockerBackend{Image: *sandboxImage})
	case "docker-wsl":
		if err := sandbox.ProbeDocker(ctx, true); err != nil {
			return err
		}
		readOnlyTools.SetSandbox(sandbox.DockerBackend{Image: *sandboxImage, ViaWSL: true})
	default:
		if err := fmt.Errorf("unknown sandbox backend %q: choose host, docker or docker-wsl", *sandboxBackend); err != nil {
			return err
		}
	}
	manager, mgrErr := contextmgr.NewManager(contextmgr.Strategy(*contextStrategy), *contextBudget, epoch)
	if err := mgrErr; err != nil {
		return err
	}
	notes := memory.NewSessionStore(filepath.Join(readOnlyTools.WorkspaceRoot(), ".xcode-state", "memory", "session-notes.json"))
	manager.SetNoteRecorder(notes)
	// compaction 策略需要模型摘要器；demo 模型不具备总结能力，保持 nil 即回退占位符。
	if !strings.EqualFold(*provider, "demo") {
		manager.SetSummarizer(chatSummarizer(summaryModel))
	}

	// 类型化 shadow 分类器（local/jev）——与故障评测路径共用 mountTypedShadow，
	// 一次性命令与 REPL 的自动压缩都能跑；后端与模型配置来自 .xcode/config（解耦）。
	// 是否启用由 --typed-shadow 决定；jev 后端发起的真实调用由用户把关。
	if *typedShadow && strings.EqualFold(*contextStrategy, "decision-gated") {
		backend := appConfig.TypedClassifierBackend
		if strings.TrimSpace(backend) == "" {
			backend = "local"
		}
		if err := mountTypedShadow(manager, appConfig, backend, *provider, selectedModel, appConfig.TypedClassifierEndpoint, appConfig.TypedClassifierModel, false); err != nil {
			return err
		}
	}

	// 组装选定的 Model、统一执行入口、最大步数和事件输出端。
	agent := harness.NewAgent(selectedModel, secure, *maxSteps, sink)
	// 前导三段组装：项目规则（AGENTS.md）→ 技能清单 → L2 项目记忆。
	// 各段独立缺席不影响其他；任何一段读取失败都只跳过该段，不中断启动。
	var prelude strings.Builder
	if instructions, err := memory.LoadProjectInstructions(*workspace); err != nil {
		fmt.Fprintf(os.Stderr, "warning: read AGENTS.md failed: %v\n", err)
	} else if instructions != "" {
		prelude.WriteString("[project-instructions]\n" + instructions + "\n\n")
	}
	if skills, err := memory.LoadSkills(*workspace); err != nil {
		fmt.Fprintf(os.Stderr, "warning: load skills failed: %v\n", err)
	} else if listing := memory.RenderSkillsList(skills); listing != "" {
		prelude.WriteString("[skills]\n" + listing + "\n")
	}
	if projectMemory, memErr := memory.LoadProjectMemory(*workspace); memErr == nil && strings.TrimSpace(projectMemory) != "" {
		prelude.WriteString("[project-memory]\n" + projectMemory)
	}
	if trimmed := strings.TrimSpace(prelude.String()); trimmed != "" {
		agent.SetProjectMemory(trimmed)
		record.Config.PreludeHash = hashRunBytes([]byte(trimmed))
	}
	agent.SetContextManager(manager)

	// 轮次恢复日志在全部装配完成后挂载；一次性与交互模式共用同一份日志文件。
	turnLog, turnLogErr := harness.NewTurnLog(turnLogPath(readOnlyTools.WorkspaceRoot()))
	if err := turnLogErr; err != nil {
		return err
	}
	defer func() { runErr = errors.Join(runErr, turnLog.Close()) }()
	agent.SetTurnLog(turnLog)

	// 一次性恢复——从最后安全边界续跑被中断的轮次，不重新提交用户任务。
	if *resumeFlag {
		options := harness.RunOptions{MaxSteps: *maxSteps, MaxTotalTokens: *maxTokens, DoomLoopLimit: *doomLoopLimit}
		if err := runResume(ctx, agent, readOnlyTools.WorkspaceRoot(), checkpoints, strings.TrimSpace(prelude.String()), options); err != nil {
			return err
		}
		return nil
	}

	// 交互模式——无位置参数时进入 REPL 多轮对话。
	if interactive {
		projectMemo := strings.TrimSpace(prelude.String())
		session := harness.NewSession(selectedModel, secure, *maxSteps, sink, projectMemo)
		session.SetTurnLog(turnLog)
		session.SetContextManager(manager)
		// /model 热切换：重载配置、重建模型（含预算包装），并同步 delegate 子智能体
		// （闭包按引用捕获 selectedModel，重新赋值即全链路生效）与压缩摘要器。
		switchProvider := func(provider string) (string, error) {
			fresh, loadErr := config.Load(*workspace)
			if loadErr != nil {
				return "", loadErr
			}
			next, buildErr := buildModelWithRetryLimit(fresh, provider, *providerRetries)
			if buildErr != nil {
				return "", buildErr
			}
			selectedModel = budget.Wrap(next, "agent")
			session.SetModel(selectedModel)
			if !strings.EqualFold(provider, "demo") {
				manager.SetSummarizer(chatSummarizer(budget.Wrap(next, "summary")))
			}
			return displayModelName(fresh, provider), nil
		}
		// 尝试恢复上次会话。
		offerResume(session, readOnlyTools.WorkspaceRoot())
		// 提示未完成的轮次；交互内恢复留待后续，先用一次性 --resume 完成。
		if unfinished, since, turnErr := recovery.HasUnfinishedTurn(turnLogPath(readOnlyTools.WorkspaceRoot())); turnErr == nil && unfinished {
			fmt.Printf("[resume] 检测到未完成的轮次（%s 前中断）。退出后运行 xcode --resume 继续。\n",
				time.Since(since).Round(time.Second))
		}
		// 进入 REPL 循环。
		return runInteractive(ctx, session, replConfig{
			Provider:       *provider,
			Model:          displayModelName(appConfig, *provider),
			Mode:           *mode,
			Workspace:      readOnlyTools.WorkspaceRoot(),
			Timeout:        *timeout,
			Engine:         engine,
			SwitchProvider: switchProvider,
		})
	}

	// flag.Args 取得用户任务的全部单词，再用空格还原成一个 prompt。
	options := harness.RunOptions{MaxTotalTokens: *maxTokens, DoomLoopLimit: *doomLoopLimit}
	// 完成门禁——契约来自 --contract 文件；指纹函数让验证证据与工作区版本绑定，
	// 模型在验证后再次修改文件时，缓存证据自动失效，门禁会在新版本上重新取证。
	if strings.TrimSpace(*contractFlag) != "" {
		contract, contractErr := loadContract(*contractFlag)
		if err := contractErr; err != nil {
			return err
		}
		options.Contract = contract
		data, _ := json.Marshal(contract)
		record.Config.ContractHash = hashRunBytes(data)
		if *noDelegate && contract.Review {
			return fmt.Errorf("contract review requires delegate; remove --no-delegate or disable review")
		}
		options.WorkspaceFingerprint = wireWorkspaceFingerprint(readOnlyTools.WorkspaceRoot())
		fmt.Fprintf(os.Stderr, "[xcode] completion gate enabled: %d verify command(s), review=%t\n",
			len(contract.VerifyCommands), contract.Review)
	}
	result, agentErr := agent.RunWithOptions(ctx, prompt, options)
	record.Answer, record.StopReason, record.Steps = result.Answer, result.StopReason, result.Steps
	// 日志写入失败只提示，不掩盖也不替代 Agent 本身的运行结果。
	if logWriteErr != nil {
		fmt.Fprintln(os.Stderr, "warning: event log write failed:", logWriteErr)
	}
	if *nonInteractive {
		agentErr = errors.Join(agentErr, logWriteErr, journal.Err(), turnLog.Err())
	}
	if agentErr != nil {
		return agentErr
	}
	// 停止原因写 stderr 进度流，最终答案写 stdout，两条输出职责分离。
	fmt.Fprintf(os.Stderr, "[xcode] stop reason: %s\n", result.StopReason)
	fmt.Println(result.Answer)
	return nil
}

// writeCompactEvent 把一条运行事件渲染成单行极简进度；流式增量等高频事件不输出。
func writeCompactEvent(writer io.Writer, event harness.Event) {
	switch event.Kind {
	case harness.ToolStarted:
		fmt.Fprintf(writer, "[step %d] %s %s\n", event.Step, event.Tool, compactLine(event.Data, 120))
	case harness.ToolCompleted:
		if strings.HasPrefix(event.Data, "ERROR: ") {
			fmt.Fprintf(writer, "[step %d] %s error: %s\n", event.Step, event.Tool, compactLine(strings.TrimPrefix(event.Data, "ERROR: "), 160))
			return
		}
		if event.RepeatedRead {
			fmt.Fprintf(writer, "[step %d] %s ok %s repeated_read\n", event.Step, event.Tool, event.Resource)
			return
		}
		line := fmt.Sprintf("[step %d] %s ok %s %dB→%dB", event.Step, event.Tool, event.Resource, event.OriginalBytes, event.VisibleBytes)
		if event.Truncated {
			line += " truncated"
		}
		fmt.Fprintln(writer, line)
	case harness.PolicyDecided:
		fmt.Fprintf(writer, "[policy] %s %s\n", event.Tool, event.Data)
	case harness.ContextBuilt:
		fmt.Fprintf(writer, "[context] tokens=%d epoch=%d %s\n", event.ContextTokens, event.ContextEpoch, event.Data)
	case harness.ContextCompacted:
		fmt.Fprintf(writer, "[context] compacted (%s) epoch=%d\n", event.Data, event.ContextEpoch)
	case harness.ApprovalResolved:
		fmt.Fprintf(writer, "[approval] %s %s\n", event.Tool, event.Data)
	case harness.CheckpointCreated:
		fmt.Fprintf(writer, "[checkpoint] %s id=%s\n", event.Resource, event.CheckpointID)
	case harness.ModelCompleted:
		fmt.Fprintf(writer, "[step %d] model finish=%s tokens=%d\n", event.Step, event.FinishReason, event.TotalTokens)
	case harness.RunCompleted:
		fmt.Fprintf(writer, "[done] steps=%d\n", event.Step)
	case harness.RunFailed:
		fmt.Fprintf(writer, "[failed] step %d: %s\n", event.Step, compactLine(event.Error, 160))
	case harness.CompletionGateBlocked:
		fmt.Fprintf(writer, "[gate] blocked: %s\n", compactLine(event.Data, 160))
	case harness.CompletionGatePassed:
		fmt.Fprintf(writer, "[gate] passed fingerprint=%s\n", compactLine(event.Data, 16))
	}
}

// compactLine 把文本压成单行并限制长度，防止一条进度占满终端。
func compactLine(text string, maxRunes int) string {
	text = strings.Join(strings.Fields(text), " ")
	runes := []rune(text)
	if len(runes) <= maxRunes {
		return text
	}
	return string(runes[:maxRunes-1]) + "…"
}

// chatSummarizer 返回用指定模型压缩工作历史的摘要函数；启动装配与 /model 热切换共用。
func chatSummarizer(summarizer harness.Model) func(context.Context, string) (string, error) {
	return func(ctx context.Context, oldText string) (string, error) {
		response, err := summarizer.Generate(ctx, []harness.Message{
			{Role: "user", Content: "请把下面这段 Agent 工作历史总结成不超过 200 字的要点，保留目标、已改文件、验证结果、失败与下一步：\n" + oldText},
		}, nil, harness.ModelEventSinkFunc(func(harness.ModelEvent) {}))
		if err != nil {
			return "", err
		}
		return response.Content, nil
	}
}

// buildModel 根据 provider 名称创建模型实现，配置来自配置文件与环境变量取值链。
func buildModel(cfg config.Config, provider string) (harness.Model, error) {
	return buildModelWithRetryLimit(cfg, provider, 2)
}

func buildModelWithRetryLimit(cfg config.Config, provider string, retries int) (harness.Model, error) {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "demo":
		return model.Demo{}, nil
	case "bailian":
		return bailian.New(bailianSettings(cfg, retries))
	case "deepseek":
		return deepseek.New(deepseekSettings(cfg, "enabled", "high", retries, 8192))
	default:
		return nil, fmt.Errorf("unsupported provider %q: choose demo, bailian or deepseek", provider)
	}
}

// noticeLegacyDotEnv 检测项目根目录的旧版 .env 并提示一次性迁移；只读不注入。
func noticeLegacyDotEnv() {
	file, err := os.Open(".env")
	if err != nil {
		return
	}
	defer file.Close()
	legacyNames := map[string]bool{
		"DASHSCOPE_API_KEY": true, "DASHSCOPE_BASE_URL": true, "DASHSCOPE_MODEL": true,
		"DEEPSEEK_API_KEY": true, "DEEPSEEK_BASE_URL": true, "DEEPSEEK_MODEL": true,
		"TYPESAFE_API_KEY": true, "TYPESAFE_ENDPOINT": true, "TYPESAFE_MODEL": true,
	}
	var found []string
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		name, _, ok := strings.Cut(strings.TrimSpace(scanner.Text()), "=")
		if ok && legacyNames[strings.TrimSpace(name)] {
			found = append(found, strings.TrimSpace(name))
		}
	}
	if len(found) == 0 {
		return
	}
	globalPath, pathErr := config.GlobalPath()
	hint := "(无法定位全局配置路径)"
	if pathErr == nil {
		hint = globalPath
	}
	fmt.Fprintf(os.Stderr, "[notice] 检测到旧版 .env（%s 等）。该文件已不再生效：请把密钥迁移到\n"+
		"[notice] %s（如 bailian-api-key=sk-...），或改用环境变量。详见 README。\n",
		strings.Join(found, ", "), hint)
}

// check 统一处理 CLI 中不可恢复的错误，避免每次重复打印和退出逻辑。
func check(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		// 非零退出码便于 Shell、脚本和测试判断本次运行失败。
		os.Exit(1)
	}
}
