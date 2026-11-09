package harness

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// ModelBudget belongs to a whole run, including delegates and summaries. The
// token ceiling is checked before starting another request: a running request
// may exceed it. MaxCalls counts Generate calls, not provider transport retries.
type ModelBudget struct {
	mu                  sync.Mutex
	maxCalls, maxTokens int
	calls, missingUsage int
	usage               Usage
	sink                EventSink
}

type ModelBudgetSnapshot struct {
	Calls        int   `json:"calls"`
	MissingUsage int   `json:"missing_usage"`
	Usage        Usage `json:"usage"`
}

func NewModelBudget(maxCalls, maxTokens int, sink EventSink) *ModelBudget {
	return &ModelBudget{maxCalls: maxCalls, maxTokens: maxTokens, sink: sink}
}

func (b *ModelBudget) Snapshot() ModelBudgetSnapshot {
	b.mu.Lock()
	defer b.mu.Unlock()
	return ModelBudgetSnapshot{Calls: b.calls, MissingUsage: b.missingUsage, Usage: b.usage}
}

// Wrap can wrap different provider clients while sharing one run budget.
func (b *ModelBudget) Wrap(model Model, purpose string) Model {
	return &budgetModel{model: model, budget: b, purpose: purpose}
}

type budgetModel struct {
	model   Model
	budget  *ModelBudget
	purpose string
}

func (m *budgetModel) Generate(ctx context.Context, messages []Message, tools []ToolSpec, sink ModelEventSink) (ModelResponse, error) {
	if err := ctx.Err(); err != nil {
		return ModelResponse{}, err
	}
	b := m.budget
	b.mu.Lock()
	if (b.maxCalls > 0 && b.calls >= b.maxCalls) || (b.maxTokens > 0 && b.usage.TotalTokens >= b.maxTokens) {
		b.mu.Unlock()
		return ModelResponse{}, fmt.Errorf("%w: shared model request/token limit reached", ErrBudget)
	}
	b.calls++
	call := b.calls
	spent, missing := b.usage.TotalTokens, b.missingUsage
	b.mu.Unlock()
	id := fmt.Sprintf("model-%06d", call)
	finalRequest, gate := false, 0
	if m.purpose != "summary" {
		notice, final, g := b.guidance(ctx, call, spent, missing)
		finalRequest, gate = final, g
		input := make([]Message, 0, len(messages)+1)
		input = append(input, Message{Role: "system", Content: notice})
		messages = append(input, messages...)
		switch {
		case finalRequest:
			// 最后一个允许的请求：此时发起的工具调用，其结果永远等不到下一次
			// 模型请求来消费。禁用工具，让这一请求直接产出最终回答。
			tools = nil
		case gate > 0:
			// 探索门禁：预算过半仍无生产修改时逐级收窄工具，把"再搜一轮"
			// 从选项里物理删除。观察到生产修改后立即解除（含 exec，验证需要）。
			tools = restrictExplorationTools(tools, gate)
		}
		b.emit(Event{Kind: EventKind("budget.status"), ToolCallID: id, Data: notice})
	}
	b.emit(Event{Kind: ModelRequestStarted, ToolCallID: id, Data: m.purpose})
	var usage Usage
	known := false
	response, err := m.model.Generate(ctx, messages, tools, ModelEventSinkFunc(func(event ModelEvent) {
		if event.Kind == ModelUsage {
			usage, known = event.Usage, true
		}
		if sink != nil {
			sink.EmitModelEvent(event)
		}
	}))
	if !known && (response.Usage != (Usage{})) {
		usage, known = response.Usage, true
	}
	// 声明侧收窄不够：Provider（尤其带思考续传的）可能"记得"未声明的工具并
	// 继续调用。在响应进入 Agent 循环前剪掉未声明工具的调用，否则门禁与最后
	// 请求的禁用在执行侧被绕过。finalRequest 时 advertised 为 nil，全部剪除。
	if err == nil && (finalRequest || gate > 0) {
		response.ToolCalls = pruneWithheldToolCalls(response.ToolCalls, tools)
	}
	if usage.TotalTokens == 0 {
		usage.TotalTokens = usage.InputTokens + usage.OutputTokens
	}
	b.mu.Lock()
	b.usage.InputTokens += usage.InputTokens
	b.usage.OutputTokens += usage.OutputTokens
	b.usage.TotalTokens += usage.TotalTokens
	if !known {
		b.missingUsage++
	}
	b.mu.Unlock()
	event := Event{Kind: ModelRequestCompleted, ToolCallID: id, Data: m.purpose,
		InputTokens: usage.InputTokens, OutputTokens: usage.OutputTokens, TotalTokens: usage.TotalTokens}
	if err != nil {
		event.Error = err.Error()
	}
	b.emit(event)
	return response, err
}

// 探索门禁阈值：剩余预算份额低于该值且该智能体尚无 apply_patch 生产修改时，
// 按等级移除探索类工具。数值来自 2026-11-09 未见题轮的轨迹证据：4/5 失败题
// 零生产修改烧满预算，文本催促被无视 15+ 轮；硬性收窄与最后请求禁工具同理。
const (
	explorationNoSearchBelow = 0.5 // 剩余不足一半：禁 search_text / list_files
	explorationNoExecBelow   = 0.3 // 剩余不足三成：再禁 exec，只留读/改/diff
)

// restrictExplorationTools 按门禁等级过滤工具声明；返回新切片，不改调用方数据。
// read_file / apply_patch / git_diff 任何等级都保留：门禁的目的是强制转入实现，
// 不是禁止实现。delegate 模式下各智能体按自身进度快照受同一规则约束。
func restrictExplorationTools(specs []ToolSpec, gate int) []ToolSpec {
	blocked := map[string]bool{"search_text": true, "list_files": true}
	if gate >= 2 {
		blocked["exec"] = true
	}
	kept := make([]ToolSpec, 0, len(specs))
	for _, spec := range specs {
		if !blocked[spec.Name] {
			kept = append(kept, spec)
		}
	}
	return kept
}

// pruneWithheldToolCalls 移除不在当次声明列表里的工具调用（final 时全部移除）。
// 剪除发生在响应进入消息历史之前，助手消息不会包含被剪调用，协议保持一致。
func pruneWithheldToolCalls(calls []ToolCall, advertised []ToolSpec) []ToolCall {
	if len(calls) == 0 {
		return calls
	}
	allowed := make(map[string]bool, len(advertised))
	for _, spec := range advertised {
		allowed[spec.Name] = true
	}
	kept := make([]ToolCall, 0, len(calls))
	for _, call := range calls {
		if allowed[call.Name] {
			kept = append(kept, call)
		}
	}
	return kept
}

// guidance 生成每次模型请求前的运行时提示，并返回该请求是否为预算内最后一个允许
// 的请求（final），以及探索门禁等级（gate：0 无、1 禁搜索、2 禁搜索+执行）。
// final 时调用方禁用全部工具；gate>0 时按级收窄。两者依据的都是请求时刻的快照，
// 一旦观察到生产修改，门禁在下一请求立即解除。
func (b *ModelBudget) guidance(ctx context.Context, call, spent, missing int) (string, bool, int) {
	calls, tokens := "unlimited", "unlimited"
	fraction := 1.0
	final := false
	if b.maxCalls > 0 {
		left := max(0, b.maxCalls-call+1)
		calls = fmt.Sprintf("%d/%d including this request", left, b.maxCalls)
		fraction = min(fraction, float64(left)/float64(b.maxCalls))
		if left <= 1 {
			final = true
		}
	}
	if b.maxTokens > 0 {
		left := max(0, b.maxTokens-spent)
		tokens = fmt.Sprintf("%d/%d cumulative input+output tokens", left, b.maxTokens)
		fraction = min(fraction, float64(left)/float64(b.maxTokens))
		// 剩余不足约 5% 时按最后一请求处理：再排工具调用的期望收益已低于
		// 产出干净最终回答的价值。
		if left*20 <= b.maxTokens {
			final = true
		}
	}
	clock := "not set"
	if deadline, ok := ctx.Deadline(); ok {
		clock = fmt.Sprintf("%.0f seconds", max(0, time.Until(deadline).Seconds()))
	}
	// 探索门禁只在能读到该智能体的进度快照且尚无生产修改时触发；
	// 直接调用模型（无快照）不受影响，交互式无限预算也不受影响。
	progress, hasProgress := ctx.Value(runtimeProgressKey{}).(runtimeProgress)
	gate := 0
	if hasProgress && progress.ProductionEdits == 0 {
		switch {
		case fraction <= explorationNoExecBelow:
			gate = 2
		case fraction <= explorationNoSearchBelow:
			gate = 1
		}
	}
	phase := "Locate the relevant code with narrow searches and reproduce the issue once. Form a concrete hypothesis, then implement the smallest justified production change."
	switch {
	case final:
		phase = "FINAL REQUEST: no further model request is allowed after this one, so tools are disabled — a tool call now could never be observed. Write the final answer directly: what changed, how it was verified with actual command results, and what remains unverified."
	case gate >= 2:
		phase = "EXPLORATION CLOSED: over 70% of the budget is spent with no observed production edit. Search, listing and command execution are disabled for this request; they return as soon as an edit is observed. Read one file you already identified, form the fix, and apply it with apply_patch now. An imperfect but justified change beats another round of reading."
	case gate == 1:
		phase = "EXPLORATION BUDGET OVER: over half the budget is spent with no observed production edit. Search and listing tools are disabled for this request. Read the files you already identified and implement the smallest justified change with apply_patch now."
	case fraction <= 0.2:
		phase = "The budget is nearly exhausted. Validate the current change with focused tests, inspect the diff, and finish with accurate results and remaining limitations."
	case fraction <= 0.5:
		phase = "At least half the budget is used. For implementation tasks, prioritize a justified minimal production fix and its focused test over further broad investigation."
	}
	progressText := ""
	if hasProgress {
		progressText = fmt.Sprintf("\nObserved via apply_patch: %d production edits, %d test-only edits; %d tool calls and %d successful command(s) since the last observed production edit. Shell edits are not counted; inspect the diff if needed.", progress.ProductionEdits, progress.TestEdits, progress.CallsSinceEdit, progress.ExecOKAfterEdit)
		switch {
		case progress.ProductionEdits > 0 && progress.ExecOKAfterEdit > 0:
			progressText += " A production change is in place and at least one command run after it exited successfully. If those runs verified the requested behavior, finish now with an accurate summary of the change and its verification; do not expand scope, refactor, or keep investigating."
		case progress.ProductionEdits > 0 && progress.CallsSinceEdit >= 6:
			progressText += " A production change exists but no successful command has been observed after it. Run one focused verification of the changed behavior, or make the necessary follow-up edit, then finish; stop broad exploration."
		case progress.ProductionEdits == 0 && progress.CallsSinceEdit >= 8:
			progressText += " If the user requested an implementation, exploration has not yet produced an observed production fix. Avoid another broad search. Use the current hypothesis to make the smallest justified production change, or run one focused reproduction to resolve the specific remaining uncertainty. Adding a test alone does not fix the implementation."
		}
	}
	return fmt.Sprintf(`[runtime budget; replaces the previous notice]
Remaining model requests: %s. Remaining tokens: %s. Deadline remaining: %s. Missing usage reports: %d.
Follow the user's requested scope. For coding tasks: %s%s
Use the prepared local toolchain and cached dependencies. Avoid downloading comparison versions or repeatedly repairing unrelated environment problems. After repeated setup errors, record the blocker and use another local way to verify the hypothesis. Keep reads narrow; retrieve older evidence by artifact_id when needed. Never claim a test passed without its actual result.`, calls, tokens, clock, missing, phase, progressText), final, gate
}

func (b *ModelBudget) emit(event Event) {
	if b.sink != nil {
		event.Time = time.Now().UTC()
		b.sink.Emit(event)
	}
}
