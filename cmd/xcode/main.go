package main

// 提供用户可以直接运行的命令行程序

import (
	"bufio"         // 按行读取项目根目录中的 .env 配置。
	"context"       // 控制整次 Agent 运行的取消与超时。
	"encoding/json" // 把结构化 Event 编码成一行 JSON。
	"errors"        // 判断 .env 不存在这一种可忽略情况。
	"flag"          // 使用标准库解析 --workspace 和 --max-steps 参数。
	"fmt"           // 输出最终答案和错误信息。
	"io"            // 让极简进度渲染函数可以写入任意输出目标。
	"os"            // 访问命令行标准输出、标准错误和退出码。
	"os/signal"     // 把 Ctrl+C 转换为 Context 取消信号。
	"path/filepath" // 生成事件日志的默认路径并创建父目录。
	"runtime"       // 区分 Windows ACL 与 Unix 权限位的检查方式。
	"strings"       // 把多个位置参数重新拼成完整用户提示。
	"time"          // 表示整次运行的超时时间。

	"xcode/internal/artifactstore" // 把普通 CLI 的完整工具结果保存到本地状态目录。
	"xcode/internal/checkpoint"    // 写前快照的保存与恢复。
	"xcode/internal/config"        // 配置文件加载与合并。
	"xcode/internal/contextmgr"    // 模型可见投影与压缩策略。
	"xcode/internal/executor"      // 统一工具执行管线。
	"xcode/internal/harness"       // 组装 Registry、Agent 和 EventSink。
	"xcode/internal/memory"        // L1 会话笔记与 L2 项目记忆。
	"xcode/internal/model"         // 使用不需要 API Key 的 Demo Model。
	"xcode/internal/model/bailian" // 提供阿里云百炼的流式模型适配器。
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
func main() {
	// 每次启动都从当前项目根目录读取 .env；文件不存在时仍允许使用离线 Demo 模式。
	check(loadDotEnv(".env"))
	// eval 使用独立 FlagSet
	if len(os.Args) > 1 && os.Args[1] == "eval" {
		evalContext, stop := signal.NotifyContext(context.Background(), os.Interrupt)
		defer stop()
		check(runEval(evalContext, os.Args[2:]))
		return
	}

	// 长任务故障评测子命令（三策略对照 + replay）。
	if len(os.Args) > 1 && os.Args[1] == "eval-fault" {
		faultContext, stop := signal.NotifyContext(context.Background(), os.Interrupt)
		defer stop()
		check(runFaultEval(faultContext, os.Args[2:]))
		return
	}

	// 查看与恢复写前快照的独立子命令。
	if len(os.Args) > 1 && os.Args[1] == "checkpoints" {
		check(runCheckpoints(os.Args[2:]))
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "restore" {
		check(runRestore(os.Args[2:]))
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "policy" {
		check(runPolicyCheck(os.Args[2:]))
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "journal" {
		check(runJournal(os.Args[2:]))
		return
	}

	// 预扫描 --workspace 以定位配置文件，配置值作为 flag 默认值。
	presetWorkspace := "."
	for i, arg := range os.Args {
		if arg == "--workspace" && i+1 < len(os.Args) {
			presetWorkspace = os.Args[i+1]
		}
		if strings.HasPrefix(arg, "--workspace=") {
			presetWorkspace = strings.TrimPrefix(arg, "--workspace=")
		}
	}
	appConfig, configErr := config.Load(presetWorkspace)
	if configErr != nil {
		fmt.Fprintf(os.Stderr, "warning: load config: %v\n", configErr)
	}

	// 定义可选命令行参数；返回的是指针，调用 flag.Parse 后再解引用读取值。
	// 默认值来自配置文件（flag 显式传入时覆盖配置）。
	workspace := flag.String("workspace", presetWorkspace, "workspace root")
	maxSteps := flag.Int("max-steps", appConfig.MaxSteps, "maximum model iterations")
	provider := flag.String("provider", appConfig.Provider, "model provider: demo or bailian")
	timeout := flag.Duration("timeout", appConfig.Timeout, "maximum duration of one run")
	artifactDir := flag.String("artifact-dir", ".xcode-state/artifacts", "directory for full tool result artifacts")
	eventLog := flag.String("event-log", "", "JSONL event log path (default .xcode-state/logs/run-<timestamp>.jsonl)")
	verbose := flag.Bool("verbose", false, "also print full JSONL events to stderr")
	mode := flag.String("mode", appConfig.Mode, "capability mode: plan, workspace-write or full-access")
	checkpointDir := flag.String("checkpoint-dir", "", "directory for pre-write checkpoints")
	maxTokens := flag.Int("max-tokens", appConfig.MaxTokens, "maximum total tokens per run (0 = unlimited)")
	doomLoopLimit := flag.Int("doom-loop-limit", 3, "stop when the same tool call repeats this many times in a row")
	contextStrategy := flag.String("context-strategy", appConfig.ContextStrategy, "context strategy: none, sliding, handoff or compaction")
	contextBudget := flag.Int("context-budget", appConfig.ContextBudget, "estimated-token budget before compaction")
	sandboxBackend := flag.String("sandbox", appConfig.Sandbox, "command backend: host, docker or docker-wsl")
	sandboxImage := flag.String("sandbox-image", "alpine:3.20", "docker image used by the docker sandbox backend")
	policyPath := flag.String("policy", "", "policy rules file (default <workspace>/.xcode/policy.json)")
	resumeFlag := flag.Bool("resume", false, "resume the last unfinished turn from the recovery log")
	contractFlag := flag.String("contract", "", "task contract JSON enabling the completion gate (goal, verify_commands, optional review)")
	typedShadow := flag.Bool("typed-shadow", false, "mount the typed shadow classifier (local or jev per .xcode/config) alongside decision-gated compaction; the jev backend makes real systemone calls only when enabled")
	flag.Parse()
	// 位置参数为空时进入交互模式；有参数时为一次性命令。
	interactive := flag.NArg() == 0

	// Index 建立、工具执行和模型请求共享同一套 Ctrl+C 与超时信号。
	signalContext, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	ctx, cancel := context.WithTimeout(signalContext, *timeout)
	defer cancel()

	// 完整工具结果保存在本地 Artifact Store，模型消息只接收受限预览。
	artifacts, err := artifactstore.NewFileStore(*artifactDir)
	check(err)
	// 三种只读工具共享 Workspace Index 和 ReadSet，保证路径与忽略规则一致。
	readOnlyTools, err := tools.NewReadOnlySet(ctx, *workspace, artifacts)
	check(err)

	// 根据 --provider 选择模型；百炼密钥只从当前进程环境变量读取。
	selectedModel, err := buildModel(*provider)
	check(err)

	// 完整事件按 JSONL 落盘供事后排查；终端默认只渲染每步的极简进度行。
	logPath := strings.TrimSpace(*eventLog)
	if logPath == "" {
		logPath = filepath.Join(".xcode-state", "logs", "run-"+time.Now().UTC().Format("20060102T150405.000000000Z")+".jsonl")
	}
	check(os.MkdirAll(filepath.Dir(logPath), 0o700))
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	check(err)
	defer logFile.Close()
	fmt.Fprintf(os.Stderr, "[xcode] provider=%s event log: %s\n", *provider, logPath)

	// 日志写入属于诊断路径：记录首个错误并在结束时提示，不让它中断 Agent 主流程。
	var logWriteErr error
	sink := harness.EventSinkFunc(func(event harness.Event) {
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

	// 创建 delegate 工具并注册到 Registry。
	// factory 使用延迟引用打破 Agent↔Executor 循环依赖。
	// 子智能体用独立 Executor（自有锁），共享策略/快照/Registry，避免 delegate 持锁导致死锁。
	var registryRef *harness.Registry
	var engineRef *policy.Engine
	var checkpointsRef *checkpoint.Store
	delegate := harness.NewDelegate(
		func(ctx context.Context, role, prompt string, maxSteps int, subSink harness.EventSink) (string, error) {
			subExecutor, err := executor.New(registryRef, engineRef, nil, checkpointsRef, subSink)
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
	allTools := append(baseTools, delegate)
	registry, err := harness.NewRegistry(allTools...)
	check(err)

	// 策略引擎、写前快照仓库与交互审批组成统一执行管线。
	engine, err := policy.NewEngine(policy.Mode(*mode))
	check(err)
	// 加载可选的项目级规则文件；默认位于工作区 .xcode/policy.json。
	resolvedPolicyPath := *policyPath
	if resolvedPolicyPath == "" {
		resolvedPolicyPath = filepath.Join(readOnlyTools.WorkspaceRoot(), ".xcode", "policy.json")
	}
	ruleSet, rulesErr := policy.LoadRulesFile(resolvedPolicyPath)
	check(rulesErr)
	engine.SetRules(ruleSet)
	if *checkpointDir == "" {
		*checkpointDir = filepath.Join(readOnlyTools.WorkspaceRoot(), ".xcode-state", "checkpoints")
	}
	checkpoints, err := checkpoint.NewStore(*checkpointDir, readOnlyTools.WorkspaceRoot())
	check(err)
	secure, err := executor.New(registry, engine, newTerminalApprover(), checkpoints, sink)
	check(err)
	registryRef = registry
	engineRef = engine
	checkpointsRef = checkpoints

	// ToolCall 状态 journal，与事件日志同目录层次，便于对账。
	journal, journalErr := executor.OpenJournal(filepath.Join(readOnlyTools.WorkspaceRoot(), ".xcode-state", "journal", "run-"+time.Now().UTC().Format("20060102T150405.000000000Z")+".jsonl"))
	check(journalErr)
	defer journal.Close()
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
		check(sandbox.ProbeDocker(ctx, false))
		readOnlyTools.SetSandbox(sandbox.DockerBackend{Image: *sandboxImage})
	case "docker-wsl":
		check(sandbox.ProbeDocker(ctx, true))
		readOnlyTools.SetSandbox(sandbox.DockerBackend{Image: *sandboxImage, ViaWSL: true})
	default:
		check(fmt.Errorf("unknown sandbox backend %q: choose host, docker or docker-wsl", *sandboxBackend))
	}
	manager, mgrErr := contextmgr.NewManager(contextmgr.Strategy(*contextStrategy), *contextBudget, epoch)
	check(mgrErr)
	notes := memory.NewSessionStore(filepath.Join(readOnlyTools.WorkspaceRoot(), ".xcode-state", "memory", "session-notes.json"))
	manager.SetNoteRecorder(notes)
	// compaction 策略需要模型摘要器；demo 模型不具备总结能力，保持 nil 即回退占位符。
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

	// 类型化 shadow 分类器（local/jev）——与故障评测路径共用 mountTypedShadow，
	// 一次性命令与 REPL 的自动压缩都能跑；后端与模型配置来自 .xcode/config（解耦）。
	// 是否启用由 --typed-shadow 决定；jev 后端发起的真实调用由用户把关。
	if *typedShadow && strings.EqualFold(*contextStrategy, "decision-gated") {
		backend := appConfig.TypedClassifierBackend
		if strings.TrimSpace(backend) == "" {
			backend = "local"
		}
		check(mountTypedShadow(manager, backend, *provider, selectedModel, appConfig.TypedClassifierEndpoint, appConfig.TypedClassifierModel, appConfig.TypedClassifierKeyEnvVar, false))
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
	}
	agent.SetContextManager(manager)

	// 轮次恢复日志在全部装配完成后挂载；一次性与交互模式共用同一份日志文件。
	turnLog, turnLogErr := harness.NewTurnLog(turnLogPath(readOnlyTools.WorkspaceRoot()))
	check(turnLogErr)
	defer turnLog.Close()
	agent.SetTurnLog(turnLog)

	// 一次性恢复——从最后安全边界续跑被中断的轮次，不重新提交用户任务。
	if *resumeFlag {
		options := harness.RunOptions{MaxSteps: *maxSteps, MaxTotalTokens: *maxTokens, DoomLoopLimit: *doomLoopLimit}
		check(runResume(ctx, agent, readOnlyTools.WorkspaceRoot(), checkpoints, strings.TrimSpace(prelude.String()), options))
		return
	}

	// 交互模式——无位置参数时进入 REPL 多轮对话。
	if interactive {
		projectMemo := strings.TrimSpace(prelude.String())
		session := harness.NewSession(selectedModel, secure, *maxSteps, sink, projectMemo)
		session.SetTurnLog(turnLog)
		session.SetContextManager(manager)
		// 尝试恢复上次会话。
		offerResume(session, readOnlyTools.WorkspaceRoot())
		// 提示未完成的轮次；交互内恢复留待后续，先用一次性 --resume 完成。
		if unfinished, since, turnErr := recovery.HasUnfinishedTurn(turnLogPath(readOnlyTools.WorkspaceRoot())); turnErr == nil && unfinished {
			fmt.Printf("[resume] 检测到未完成的轮次（%s 前中断）。退出后运行 xcode --resume 继续。\n",
				time.Since(since).Round(time.Second))
		}
		// 进入 REPL 循环。
		check(runInteractive(ctx, session, replConfig{
			Provider:  *provider,
			Model:     envOrDefault("DASHSCOPE_MODEL", "demo"),
			Mode:      *mode,
			Workspace: readOnlyTools.WorkspaceRoot(),
			Timeout:   *timeout,
			Engine:    engine,
		}))
		return
	}

	// flag.Args 取得用户任务的全部单词，再用空格还原成一个 prompt。
	options := harness.RunOptions{MaxTotalTokens: *maxTokens, DoomLoopLimit: *doomLoopLimit}
	// 完成门禁——契约来自 --contract 文件；指纹函数让验证证据与工作区版本绑定，
	// 模型在验证后再次修改文件时，缓存证据自动失效，门禁会在新版本上重新取证。
	if strings.TrimSpace(*contractFlag) != "" {
		contract, contractErr := loadContract(*contractFlag)
		check(contractErr)
		options.Contract = contract
		options.WorkspaceFingerprint = wireWorkspaceFingerprint(readOnlyTools.WorkspaceRoot())
		fmt.Fprintf(os.Stderr, "[xcode] completion gate enabled: %d verify command(s), review=%t\n",
			len(contract.VerifyCommands), contract.Review)
	}
	result, runErr := agent.RunWithOptions(ctx, strings.Join(flag.Args(), " "), options)
	// 日志写入失败只提示，不掩盖也不替代 Agent 本身的运行结果。
	if logWriteErr != nil {
		fmt.Fprintln(os.Stderr, "warning: event log write failed:", logWriteErr)
	}
	check(runErr)
	// 停止原因写 stderr 进度流，最终答案写 stdout，两条输出职责分离。
	fmt.Fprintf(os.Stderr, "[xcode] stop reason: %s\n", result.StopReason)
	fmt.Println(result.Answer)
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

// buildModel 根据 provider 名称创建模型实现，并校验百炼所需的运行配置。
func buildModel(provider string) (harness.Model, error) {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "demo":
		return model.Demo{}, nil
	case "bailian":
		return bailian.New(bailian.Config{
			APIKey:  strings.TrimSpace(os.Getenv("DASHSCOPE_API_KEY")),
			BaseURL: envOrDefault("DASHSCOPE_BASE_URL", defaultBailianBaseURL),
			Model:   envOrDefault("DASHSCOPE_MODEL", defaultBailianModel),
		})
	default:
		return nil, fmt.Errorf("unsupported provider %q: choose demo or bailian", provider)
	}
}

// loadDotEnv 把受支持的 .env 配置加载到当前进程，不修改全局 Shell 或 Git 配置。
func loadDotEnv(path string) error {
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("open %s: %w", path, err)
	}
	defer file.Close()

	// 密钥文件不能允许同组用户或其他用户读取；0600 是推荐权限。
	info, err := file.Stat()
	if err != nil {
		return fmt.Errorf("inspect %s: %w", path, err)
	}
	// Windows 的 FileMode.Perm 不表示真实 ACL，不能用 Unix 位判断密钥文件是否安全。
	// Windows ACL 的精确校验需要平台安全描述符支持，留待后续实现。
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("%s permissions are too open: run chmod 600 %s", path, path)
	}

	// 白名单避免把项目文件中的任意名称都注入进进程环境。
	allowed := map[string]struct{}{
		"DASHSCOPE_API_KEY":  {},
		"DASHSCOPE_BASE_URL": {},
		"DASHSCOPE_MODEL":    {},
		// TypeSafe Jev 真接入的密钥与端点配置。
		"TYPESAFE_API_KEY":  {},
		"TYPESAFE_ENDPOINT": {},
		"TYPESAFE_MODEL":    {},
		// 类型化分类器的独立 Key（配置文件 typed-classifier-api-key-env 指向它）。
		"XCODE_CLASSIFIER_API_KEY": {},
	}

	scanner := bufio.NewScanner(file)
	for lineNumber := 1; scanner.Scan(); lineNumber++ {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		name, value, ok := strings.Cut(line, "=")
		name = strings.TrimSpace(name)
		if !ok || name == "" {
			return fmt.Errorf("%s:%d: expected NAME=value", path, lineNumber)
		}
		if _, ok := allowed[name]; !ok {
			continue
		}

		// 只去除包裹整个值的一对引号，不改动值中间的字符。
		value = strings.TrimSpace(value)
		if len(value) >= 2 && ((value[0] == '\'' && value[len(value)-1] == '\'') || (value[0] == '"' && value[len(value)-1] == '"')) {
			value = value[1 : len(value)-1]
		}
		if value == "" {
			return fmt.Errorf("%s:%d: %s must not be empty", path, lineNumber, name)
		}
		if err := os.Setenv(name, value); err != nil {
			return fmt.Errorf("set %s from %s: %w", name, path, err)
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}
	return nil
}

// envOrDefault 返回非空环境变量；变量缺失时使用安全默认值。
func envOrDefault(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}

// check 统一处理 CLI 中不可恢复的错误，避免每次重复打印和退出逻辑。
func check(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		// 非零退出码便于 Shell、脚本和测试判断本次运行失败。
		os.Exit(1)
	}
}
