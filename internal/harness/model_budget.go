package harness

import (
	"context"
	"fmt"
	"strings"
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
	usageByPurpose      map[string]Usage // per-purpose accounting; stage budget caps read this
	sink                EventSink
}

type ModelBudgetSnapshot struct {
	Calls        int   `json:"calls"`
	MissingUsage int   `json:"missing_usage"`
	Usage        Usage `json:"usage"`
}

func NewModelBudget(maxCalls, maxTokens int, sink EventSink) *ModelBudget {
	return &ModelBudget{maxCalls: maxCalls, maxTokens: maxTokens, sink: sink, usageByPurpose: map[string]Usage{}}
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
	// Planner stage cap: diagnosis may spend at most 30% of the run token
	// budget. On reaching the cap the planner is treated as final (tools off,
	// forced to emit its report) instead of being killed — a killed planner
	// stays "incomplete" forever and deadlocks the developer order check.
	stageCapped := false
	if strings.HasPrefix(m.purpose, "sub:planner") && b.maxTokens > 0 {
		b.mu.Lock()
		stageCapped = b.usageByPurpose[m.purpose].TotalTokens*10 >= b.maxTokens*3
		b.mu.Unlock()
	}
	finalRequest, gate := false, 0
	if m.purpose != "summary" {
		notice, final, g := b.guidance(ctx, call, spent, missing)
		if stageCapped {
			final = true
			notice += "\nPLANNER STAGE BUDGET EXHAUSTED: the diagnosis stage has used its 30% share of the run budget. Tools are disabled. Write the structured diagnosis report now: files, root cause, reproduction command, functions to change."
		}
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
	staged := b.usageByPurpose[m.purpose]
	staged.InputTokens += usage.InputTokens
	staged.OutputTokens += usage.OutputTokens
	staged.TotalTokens += usage.TotalTokens
	b.usageByPurpose[m.purpose] = staged
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

// Exploration gate thresholds: with no observed production edit, falling
// below the remaining-budget share removes exploration tools step by step.
// Calibrated from observed failure traces: runs that burned the whole budget
// without editing ignored text nudges for 15+ rounds; hard narrowing works.
const (
	explorationNoSearchBelow = 0.5 // 剩余不足一半：禁 search_text / list_files
	explorationNoExecBelow   = 0.3 // 剩余不足三成：再禁 exec，只留读/改/diff
)

// restrictExplorationTools filters the advertised tool set by gate level;
// it returns a new slice. read_file/apply_patch/git_diff stay at every level:
// the gate forces switching to implementation, it never blocks implementing.
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

// pruneWithheldToolCalls drops calls outside the advertised set (all of them
// when final). Pruning happens before the response enters the transcript, so
// the assistant message never references dropped calls and stays consistent.
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

// guidance builds the per-request runtime notice and reports whether this
// is the last admitted request (final) plus the exploration gate level
// (0 none, 1 no-search, 2 no-search-no-exec). Both re-evaluate per request;
// the gate lifts as soon as a production edit is observed.
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
	// The gate needs a progress snapshot with zero production edits; direct
	// model calls without a snapshot and unlimited interactive budgets are unaffected.
	progress, hasProgress := ctx.Value(runtimeProgressKey{}).(*runtimeProgress)
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
