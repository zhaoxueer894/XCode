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
	if m.purpose != "summary" {
		notice := b.guidance(ctx, call, spent, missing)
		input := make([]Message, 0, len(messages)+1)
		input = append(input, Message{Role: "system", Content: notice})
		messages = append(input, messages...)
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

func (b *ModelBudget) guidance(ctx context.Context, call, spent, missing int) string {
	calls, tokens := "unlimited", "unlimited"
	fraction := 1.0
	if b.maxCalls > 0 {
		left := max(0, b.maxCalls-call+1)
		calls = fmt.Sprintf("%d/%d including this request", left, b.maxCalls)
		fraction = min(fraction, float64(left)/float64(b.maxCalls))
	}
	if b.maxTokens > 0 {
		left := max(0, b.maxTokens-spent)
		tokens = fmt.Sprintf("%d/%d cumulative input+output tokens", left, b.maxTokens)
		fraction = min(fraction, float64(left)/float64(b.maxTokens))
	}
	clock := "not set"
	if deadline, ok := ctx.Deadline(); ok {
		clock = fmt.Sprintf("%.0f seconds", max(0, time.Until(deadline).Seconds()))
	}
	phase := "Locate the relevant code with narrow searches and reproduce the issue once. Form a concrete hypothesis, then implement the smallest justified production change."
	if fraction <= 0.5 {
		phase = "At least half the budget is used. For implementation tasks, prioritize a justified minimal production fix and its focused test over further broad investigation."
	}
	if fraction <= 0.2 {
		phase = "The budget is nearly exhausted. Validate the current change with focused tests, inspect the diff, and finish with accurate results and remaining limitations."
	}
	progressText := ""
	if progress, ok := ctx.Value(runtimeProgressKey{}).(runtimeProgress); ok {
		progressText = fmt.Sprintf("\nObserved via apply_patch: %d production edits, %d test-only edits; %d tool calls since the last observed production edit. Shell edits are not counted; inspect the diff if needed.", progress.ProductionEdits, progress.TestEdits, progress.CallsSinceEdit)
		if progress.ProductionEdits == 0 && progress.CallsSinceEdit >= 8 {
			progressText += " If the user requested an implementation, exploration has not yet produced an observed production fix. Avoid another broad search. Use the current hypothesis to make the smallest justified production change, or run one focused reproduction to resolve the specific remaining uncertainty. Adding a test alone does not fix the implementation."
		}
	}
	return fmt.Sprintf(`[runtime budget; replaces the previous notice]
Remaining model requests: %s. Remaining tokens: %s. Deadline remaining: %s. Missing usage reports: %d.
Follow the user's requested scope. For coding tasks: %s%s
Use the prepared local toolchain and cached dependencies. Avoid downloading comparison versions or repeatedly repairing unrelated environment problems. After repeated setup errors, record the blocker and use another local way to verify the hypothesis. Keep reads narrow; retrieve older evidence by artifact_id when needed. Never claim a test passed without its actual result.`, calls, tokens, clock, missing, phase, progressText)
}

func (b *ModelBudget) emit(event Event) {
	if b.sink != nil {
		event.Time = time.Now().UTC()
		b.sink.Emit(event)
	}
}
