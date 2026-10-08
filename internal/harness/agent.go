package harness

// 实现模型与工具间的最小控制循环

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// ErrMaxSteps：标识Agent达到最大迭代数仍未正常结束
// 包级变量，Err开头导出变量，调用方可以用errors.Is判断错误种类
var ErrMaxSteps = errors.New("maximum agent steps reached")

// Agent结构体：模型、工具注册表、运行上限和事件输出端
type Agent struct {
	model         Model      // 类型是接口
	runner        ToolRunner // 保存统一工具执行入口。
	maxSteps      int
	sink          EventSink      // 类型是接口
	ctxm          ContextManager // 保存可选的上下文投影管理器；nil 表示完整历史直接投给模型。
	projectMemory string         // 保存本次 Run 的 L2 项目记忆前导。
	turnLog       *TurnLog       // 可选的轮次恢复日志；nil 表示不落盘（Eval 与既有测试路径行为不变）。
}

// 创建Agent
func NewAgent(model Model, runner ToolRunner, maxSteps int, sink EventSink) *Agent {
	if maxSteps <= 0 {
		maxSteps = 8
	}

	// Null Object模式
	if sink == nil {
		sink = EventSinkFunc(func(Event) {})
	}

	return &Agent{model: model, runner: runner, maxSteps: maxSteps, sink: sink}
}

// ContextManager 把完整历史投影成预算内的模型可见消息；实现必须不修改传入切片。
type ContextManager interface {
	Project(ctx context.Context, messages []Message) Projection
}

// Projection 保存一次投影的结果；contextmgr.Manager 直接返回本类型，
// 因此接口与实现无需任何适配层（同形但不同包的类型不能互相赋值，必须共用此定义）。
type Projection struct {
	Messages  []Message // 保存投给模型的消息切片。
	Tokens    int       // 保存投影后的估算 token 数。
	Epoch     uint64    // 保存投影发生时的纪元。
	Compacted bool      // 表示是否发生了压缩。
	Kind      string    // 保存压缩种类。
}

// ShadowSummarizer 由可选的投影管理器实现，用于把 shadow 对照汇总带进压缩事件。
type ShadowSummarizer interface {
	ShadowSummary() string
}

// SetContextManager 为 Agent 装配上下文投影管理器；必须在 Run 之前调用。
func (a *Agent) SetContextManager(manager ContextManager) { a.ctxm = manager }

// SetProjectMemory 设置本次 Run 的 L2 项目记忆前导；等价于 RunOptions.ProjectMemory。
func (a *Agent) SetProjectMemory(text string) { a.projectMemory = text }

// SetTurnLog 为 Agent 挂载轮次恢复日志；必须在 Run 之前调用。
// 交互模式经 Session 转发挂载，一次性 CLI 直接挂载。
func (a *Agent) SetTurnLog(log *TurnLog) { a.turnLog = log }

// Run 按默认选项执行一次循环，只返回最终答案；需要停止原因等完整结果时使用 RunWithOptions。
func (a *Agent) Run(ctx context.Context, prompt string) (string, error) {
	result, err := a.RunWithOptions(ctx, prompt, RunOptions{})
	return result.Answer, err
}

// RunWithOptions 完成一次单轮对话：组装初始消息后委托 RunTurn。
// L2 项目记忆在此注入为前导消息；多轮场景由 Session 负责前导管理。
func (a *Agent) RunWithOptions(ctx context.Context, prompt string, options RunOptions) (RunResult, error) {
	messages := []Message{{Role: "user", Content: prompt}}
	prelude := options.ProjectMemory
	if prelude == "" {
		prelude = a.projectMemory
	}
	if prelude != "" {
		messages = append([]Message{{Role: "user", Content: "[project-memory]\n" + prelude}}, messages...)
	}
	return a.RunTurn(ctx, messages, options)
}

// RunTurn 执行一次对话轮次：接受完整消息历史（末尾已是本轮用户输入），
// 运行模型-工具循环直到模型给出纯文本或受控停止。
// RunResult.Messages 包含本轮追加的 assistant/tool 消息，供 Session 跨轮持久化。
// 前导注入（L2/L1）由调用方负责——Session 在调用前已更新位置 0 的前导消息。
func (a *Agent) RunTurn(ctx context.Context, messages []Message, options RunOptions) (RunResult, error) {
	maxSteps := options.steps(a.maxSteps)
	tokenBudget := options.MaxTotalTokens
	doom := newDoomLoopDetector(options.doomLimit())

	// 发出 RunStarted，Data 取最后一条 user 消息（本轮输入）。
	lastInput := ""
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == "user" {
			lastInput = messages[i].Content
			break
		}
	}
	a.emit(Event{Kind: RunStarted, Data: lastInput})
	var totalUsage Usage
	// 轮次开始边界——记录本轮用户输入，崩溃后恢复器从这段记录重建消息历史。
	a.turnLog.AppendStarted(lastInput, len(options.ResumePending) > 0)

	// 恢复预处理——对账后判定可安全重试的未完成调用先补执行，
	// 让重建的消息历史重新满足“每个 tool_call 都有对应 tool 消息”的模型协议要求。
	// 补执行与正常循环共用 runToolCall 路径，事件与恢复日志完全一致；step 0 表示恢复补执行。
	for _, call := range options.ResumePending {
		if a.runToolCall(ctx, 0, call, &messages, doom) {
			err := fmt.Errorf("%w: %s repeated %d times in a row", ErrDoomLoop, call.Name, doom.streak)
			a.fail(0, StopDoomLoop, err)
			return RunResult{StopReason: StopDoomLoop, Steps: 0, Usage: totalUsage, Messages: messages}, err
		}
	}

	// 每步最多调用一次模型；step 从 1 开始，便于事件展示。
	for step := 1; step <= maxSteps; step++ {
		if err := ctx.Err(); err != nil {
			a.fail(step, StopFailed, err)
			return RunResult{StopReason: StopFailed, Steps: step, Usage: totalUsage, Messages: messages}, err
		}
		if tokenBudget > 0 && totalUsage.TotalTokens >= tokenBudget {
			err := fmt.Errorf("%w: spent %d tokens", ErrBudget, totalUsage.TotalTokens)
			a.fail(step, StopBudget, err)
			return RunResult{StopReason: StopBudget, Steps: step - 1, Usage: totalUsage, Messages: messages}, err
		}

		// 先按预算生成模型可见投影；完整历史保持不变（不变量 1 与 2）。
		modelInput := messages
		if a.ctxm != nil {
			projection := a.ctxm.Project(ctx, messages)
			modelInput = projection.Messages
			a.emit(Event{
				Kind:          ContextBuilt,
				Step:          step,
				Data:          projection.Kind,
				ContextTokens: projection.Tokens,
				ContextEpoch:  projection.Epoch,
			})
			if projection.Compacted {
				compactedData := string(projection.Kind)
				if summarizer, canSummarize := a.ctxm.(ShadowSummarizer); canSummarize {
					compactedData = fmt.Sprintf("%s %s", projection.Kind, summarizer.ShadowSummary())
				}
				a.emit(Event{Kind: ContextCompacted, Step: step, Data: compactedData, ContextEpoch: projection.Epoch})
			}
		}
		modelSink := ModelEventSinkFunc(func(event ModelEvent) {
			a.emitModelEvent(step, event)
		})
		response, err := a.model.Generate(ctx, modelInput, a.runner.Specs(), modelSink)
		if err != nil {
			a.fail(step, StopFailed, err)
			return RunResult{StopReason: StopFailed, Steps: step, Usage: totalUsage, Messages: messages}, fmt.Errorf("generate at step %d: %w", step, err)
		}

		assistant := Message{
			Role:      "assistant",
			Content:   response.Content,
			ToolCalls: response.ToolCalls,
		}
		messages = append(messages, assistant)
		// 模型请求完成边界——assistant 消息（含全部工具调用）已确定，落盘供恢复重建。
		a.turnLog.AppendAssistant(step, assistant)
		totalUsage.InputTokens += response.Usage.InputTokens
		totalUsage.OutputTokens += response.Usage.OutputTokens
		totalUsage.TotalTokens += response.Usage.TotalTokens
		a.emit(Event{
			Kind:         ModelCompleted,
			Step:         step,
			Data:         response.Content,
			InputTokens:  response.Usage.InputTokens,
			OutputTokens: response.Usage.OutputTokens,
			TotalTokens:  response.Usage.TotalTokens,
			FinishReason: response.FinishReason,
		})

		if len(response.ToolCalls) == 0 {
			// 完成门禁——纯文本只是模型的完成声明；契约存在时先在当前工作区上重取环境证据，
			// 未满足则把确定性失败证据回注为 user 消息并继续循环（消耗剩余步数），不凭自述结束。
			// 门禁的验证/审查调用走统一执行管线，但不进入消息历史与恢复日志：恢复后门禁会重新取证。
			if options.Contract != nil {
				gate := a.runCompletionGate(ctx, step, options)
				if !gate.Passed {
					a.emit(Event{Kind: CompletionGateBlocked, Step: step, Data: strings.Join(gate.Reasons, "; ")})
					messages = append(messages, Message{Role: "user", Content: gate.UserMessage})
					continue
				}
				a.emit(Event{Kind: CompletionGatePassed, Step: step, Data: gate.Fingerprint})
			}
			// 轮次正常完成——写终止记录，恢复器据此认定没有未完成轮次。
			// 失败、受控停止与崩溃不写终止记录，这些轮次保持未终止，是 --resume 的恢复对象。
			a.turnLog.AppendCompleted(step, response.Content)
			a.emit(Event{Kind: RunCompleted, Step: step, Data: response.Content, StopReason: StopCompleted})
			return RunResult{
				Answer:     response.Content,
				StopReason: StopCompleted,
				Steps:      step,
				Usage:      totalUsage,
				Messages:   messages,
			}, nil
		}
		for _, call := range response.ToolCalls {
			// 单调用执行路径抽为 runToolCall，恢复预处理与正常循环共用同一实现。
			if a.runToolCall(ctx, step, call, &messages, doom) {
				err := fmt.Errorf("%w: %s repeated %d times in a row", ErrDoomLoop, call.Name, doom.streak)
				a.fail(step, StopDoomLoop, err)
				return RunResult{StopReason: StopDoomLoop, Steps: step, Usage: totalUsage, Messages: messages}, err
			}
		}
	}
	a.fail(maxSteps, StopMaxSteps, ErrMaxSteps)
	return RunResult{StopReason: StopMaxSteps, Steps: maxSteps - 1, Usage: totalUsage, Messages: messages}, ErrMaxSteps
}

// runToolCall 执行单个工具调用并追加 tool 消息；返回 true 表示 doom-loop 触发，
// 调用方应停止本次 Run。工具开始与完成边界同步写入轮次恢复日志：
// begin 记录使崩溃窗口可被 journal 对账，end 记录是“结果已落盘”的恢复游标。
func (a *Agent) runToolCall(ctx context.Context, step int, call ToolCall, messages *[]Message, doom *doomLoopDetector) bool {
	if doom.register(call) {
		return true
	}
	a.emit(Event{
		Kind:       ToolStarted,
		Step:       step,
		Tool:       call.Name,
		ToolCallID: call.ID,
		Data:       string(call.Arguments),
	})
	// 工具开始边界——此后副作用随时可能发生，journal 与恢复日志共同标记该窗口。
	a.turnLog.AppendToolBegin(step, call)
	result, toolErr := a.runner.Execute(ctx, call)
	output := result.Observation
	if toolErr != nil {
		output = "ERROR: " + toolErr.Error()
		result = ToolResult{Observation: output}
	}
	*messages = append(*messages, Message{
		Role:       "tool",
		Content:    output,
		ToolCallID: call.ID,
	})
	a.emit(Event{
		Kind:          ToolCompleted,
		Step:          step,
		Tool:          call.Name,
		ToolCallID:    call.ID,
		Data:          output,
		ArtifactID:    result.ArtifactID,
		ContentHash:   result.ContentHash,
		Resource:      result.Resource,
		OriginalBytes: result.OriginalBytes,
		VisibleBytes:  result.VisibleBytes,
		Truncated:     result.Truncated,
		RepeatedRead:  result.RepeatedRead,
	})
	// 工具完成边界——模型可见输出已确定，恢复器看到 end 记录即知该调用无需再处理。
	a.turnLog.AppendToolEnd(step, call, output)
	return false
}

// emitModelEvent 负责把 Provider 产生的流式事件转换成统一 Harness Event。
func (a *Agent) emitModelEvent(step int, modelEvent ModelEvent) {
	event := Event{
		Step:       step,
		Tool:       modelEvent.ToolName,
		ToolCallID: modelEvent.ToolCallID,
	}

	switch modelEvent.Kind {
	case ModelTextDelta:
		event.Kind = ModelTextDeltaEmitted
		event.Data = modelEvent.TextDelta
	case ModelToolCallDelta:
		// 使用指针保留有效的 index=0，同时让其他事件省略这个字段。
		index := modelEvent.ToolCallIndex
		event.ToolCallIndex = &index
		event.Kind = ModelToolCallDeltaEmitted
		event.Data = modelEvent.ArgumentsDelta
	case ModelUsage:
		event.Kind = ModelUsageEmitted
		event.InputTokens = modelEvent.Usage.InputTokens
		event.OutputTokens = modelEvent.Usage.OutputTokens
		event.TotalTokens = modelEvent.Usage.TotalTokens
	case ModelFinished:
		event.Kind = ModelFinishedEmitted
		event.FinishReason = modelEvent.FinishReason
	default:
		return
	}

	a.emit(event)
}

// emit
func (a *Agent) emit(event Event) {
	event.Time = time.Now().UTC()
	a.sink.Emit(event)
}

// fail 记录一次 Run 的异常或受控停止事件，并携带 Harness 层停止原因。
func (a *Agent) fail(step int, reason StopReason, err error) {
	a.emit(Event{Kind: RunFailed, Step: step, StopReason: reason, Error: err.Error()})
}
