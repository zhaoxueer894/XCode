package harness_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"xcode/internal/harness"
)

func TestSharedBudgetCountsWorkersAndSummariesOnce(t *testing.T) {
	var events []harness.Event
	budget := harness.NewModelBudget(2, 0, harness.EventSinkFunc(func(e harness.Event) { events = append(events, e) }))
	model := modelFunc(func(_ context.Context, _ []harness.Message, _ []harness.ToolSpec, sink harness.ModelEventSink) (harness.ModelResponse, error) {
		usage := harness.Usage{InputTokens: 5, OutputTokens: 2, TotalTokens: 7}
		sink.EmitModelEvent(harness.ModelEvent{Kind: harness.ModelUsage, Usage: usage})
		return harness.ModelResponse{Usage: usage}, nil
	})
	for _, purpose := range []string{"worker", "summary"} {
		if _, err := budget.Wrap(model, purpose).Generate(context.Background(), nil, nil, nil); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := budget.Wrap(model, "parent").Generate(context.Background(), nil, nil, nil); !errors.Is(err, harness.ErrBudget) {
		t.Fatalf("budget error = %v", err)
	}
	got := budget.Snapshot()
	if got.Calls != 2 || got.Usage.TotalTokens != 14 || got.MissingUsage != 0 {
		t.Fatalf("snapshot = %+v", got)
	}
	var requests []harness.Event
	for _, event := range events {
		if event.Kind == harness.ModelRequestStarted || event.Kind == harness.ModelRequestCompleted {
			requests = append(requests, event)
		}
	}
	if len(requests) != 4 || requests[3].Data != "summary" {
		t.Fatalf("events = %+v", events)
	}
}

func TestBudgetGuidanceDistinguishesExplorationTestsAndProductionEdits(t *testing.T) {
	registry, err := harness.NewRegistry(staticTool{name: "read_file", output: "evidence"}, staticTool{name: "apply_patch", output: "updated"})
	if err != nil {
		t.Fatal(err)
	}
	step := 0
	model := modelFunc(func(_ context.Context, messages []harness.Message, _ []harness.ToolSpec, _ harness.ModelEventSink) (harness.ModelResponse, error) {
		step++
		notice := messages[0].Content
		if step == 9 && !strings.Contains(notice, "8 tool calls") {
			t.Fatalf("no exploration notice: %s", notice)
		}
		if step == 10 && (!strings.Contains(notice, "0 production edits, 1 test-only edits") || !strings.Contains(notice, "Adding a test alone")) {
			t.Fatalf("test was counted as a fix: %s", notice)
		}
		if step == 11 {
			if !strings.Contains(notice, "1 production edits, 1 test-only edits; 0 tool calls") {
				t.Fatalf("progress did not reset: %s", notice)
			}
			return harness.ModelResponse{Content: "done", Usage: harness.Usage{TotalTokens: 10}}, nil
		}
		args, _ := json.Marshal(map[string]string{"path": fmt.Sprintf("source%d.go", step)})
		call := harness.ToolCall{ID: fmt.Sprintf("progress-%d", step), Name: "read_file", Arguments: args}
		if step == 9 {
			call.Name = "apply_patch"
			call.Arguments = json.RawMessage(`{"path":"tests/test_case.py","operation":"create"}`)
		}
		if step == 10 {
			call.Name = "apply_patch"
			call.Arguments = json.RawMessage(`{"path":"source.go","operation":"update"}`)
		}
		return harness.ModelResponse{ToolCalls: []harness.ToolCall{call}, Usage: harness.Usage{TotalTokens: 10}}, nil
	})
	budget := harness.NewModelBudget(20, 2000, nil)
	agent := harness.NewAgent(budget.Wrap(model, "agent"), registry, 12, nil)
	if _, err := agent.Run(context.Background(), "implement the requested fix"); err != nil {
		t.Fatal(err)
	}
}

func TestBudgetNoticeUsesSharedUsageWithoutGrowingHistory(t *testing.T) {
	budget := harness.NewModelBudget(4, 200, nil)
	history := []harness.Message{{Role: "user", Content: "fix the task"}}
	var seen [][]harness.Message
	model := modelFunc(func(_ context.Context, messages []harness.Message, _ []harness.ToolSpec, _ harness.ModelEventSink) (harness.ModelResponse, error) {
		seen = append(seen, messages)
		return harness.ModelResponse{Usage: harness.Usage{TotalTokens: 60}}, nil
	})
	agent, summary := budget.Wrap(model, "agent"), budget.Wrap(model, "summary")
	_, _ = agent.Generate(context.Background(), history, nil, nil)
	_, _ = summary.Generate(context.Background(), history, nil, nil)
	_, _ = agent.Generate(context.Background(), history, nil, nil)
	if len(history) != 1 || history[0].Role != "user" || len(seen[0]) != 2 || seen[0][0].Role != "system" || len(seen[1]) != 1 || len(seen[2]) != 2 {
		t.Fatalf("notice polluted transcript/summary: %#v", seen)
	}
	if !strings.Contains(seen[0][0].Content, "200/200") || !strings.Contains(seen[2][0].Content, "80/200") || !strings.Contains(seen[2][0].Content, "2/4") || !strings.Contains(seen[2][0].Content, "half the budget") {
		t.Fatalf("stale or misleading notice: %s", seen[2][0].Content)
	}
}

func TestSharedTokenBudgetPreservesUsageOnProviderFailure(t *testing.T) {
	budget := harness.NewModelBudget(0, 6, nil)
	model := budget.Wrap(modelFunc(func(_ context.Context, _ []harness.Message, _ []harness.ToolSpec, sink harness.ModelEventSink) (harness.ModelResponse, error) {
		sink.EmitModelEvent(harness.ModelEvent{Kind: harness.ModelUsage, Usage: harness.Usage{InputTokens: 7}})
		return harness.ModelResponse{}, errors.New("stream interrupted")
	}), "agent")
	if _, err := model.Generate(context.Background(), nil, nil, nil); err == nil {
		t.Fatal("provider failure lost")
	}
	if _, err := model.Generate(context.Background(), nil, nil, nil); !errors.Is(err, harness.ErrBudget) {
		t.Fatalf("budget error = %v", err)
	}
	if got := budget.Snapshot(); got.Calls != 1 || got.Usage.TotalTokens != 7 || got.MissingUsage != 0 {
		t.Fatalf("snapshot = %+v", got)
	}
}

func TestSharedBudgetFlagsMissingUsage(t *testing.T) {
	budget := harness.NewModelBudget(0, 0, nil)
	model := budget.Wrap(modelFunc(func(context.Context, []harness.Message, []harness.ToolSpec, harness.ModelEventSink) (harness.ModelResponse, error) {
		return harness.ModelResponse{Content: "done"}, nil
	}), "agent")
	_, _ = model.Generate(context.Background(), nil, nil, nil)
	if budget.Snapshot().MissingUsage != 1 {
		t.Fatal("unknown usage was treated as known zero")
	}
}
