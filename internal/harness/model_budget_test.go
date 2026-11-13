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

func TestStagedBudgetReservesRequestsAndClosesPlannerOnlyOnce(t *testing.T) {
	ctx := context.Background()
	budget := harness.NewStagedModelBudget(20, 20000, true, nil)
	var toolsPerCall []int
	model := modelFunc(func(_ context.Context, _ []harness.Message, tools []harness.ToolSpec, _ harness.ModelEventSink) (harness.ModelResponse, error) {
		toolsPerCall = append(toolsPerCall, len(tools))
		reply := harness.ModelResponse{Content: "done", Usage: harness.Usage{TotalTokens: 1000}}
		switch len(toolsPerCall) {
		case 1, 2:
			reply.ToolCalls = []harness.ToolCall{{ID: fmt.Sprint(len(toolsPerCall)), Name: "read_file",
				Arguments: json.RawMessage(fmt.Sprintf(`{"path":"file%d.go"}`, len(toolsPerCall)))}}
		case 3:
			reply.Content = ""
			reply.FinishReason = "tool_calls"
			reply.ToolCalls = []harness.ToolCall{{ID: "withheld", Name: "read_file"}}
		}
		return reply, nil
	})
	tools := []harness.ToolSpec{{Name: "read_file"}}
	reader := &gateStubTool{name: "read_file", observations: []string{"evidence"}}
	registry, _ := harness.NewRegistry(reader)
	worker := harness.NewAgent(budget.Wrap(model, "sub:planner"), registry, 5, nil)
	_, err := worker.RunWithOptions(ctx, "plan", harness.RunOptions{WorkerHandoff: func(_ context.Context, text string) error {
		if text == "done" {
			return nil
		}
		return fmt.Errorf("missing plan")
	}})
	if err != nil || reader.Calls() != 2 {
		t.Fatalf("planner correction did not preserve earlier tools: %v, calls=%d", err, reader.Calls())
	}
	if _, err := budget.Wrap(model, "sub:planner").Generate(ctx, nil, tools, nil); !errors.Is(err, harness.ErrBudget) {
		t.Fatalf("repeat delegation got %v, want ErrBudget", err)
	}
	if _, err := budget.Wrap(model, "sub:reviewer").Generate(ctx, nil, tools, nil); err != nil {
		t.Fatalf("planner exhausted reviewer allowance: %v", err)
	}
	if fmt.Sprint(toolsPerCall) != "[1 1 0 0 1]" {
		t.Fatalf("advertised tools per call = %v", toolsPerCall)
	}
}

func TestStagedPlannerLeavesCorrectionAfterLargeToolResults(t *testing.T) {
	costs := []int{2307, 3121, 4581, 7933, 8691, 7934, 8003}
	reader := &gateStubTool{name: "read_file", observations: []string{"evidence"}}
	registry, _ := harness.NewRegistry(reader)
	calls := 0
	model := modelFunc(func(_ context.Context, _ []harness.Message, tools []harness.ToolSpec, _ harness.ModelEventSink) (harness.ModelResponse, error) {
		calls++
		response := harness.ModelResponse{Usage: harness.Usage{TotalTokens: costs[calls-1]}}
		if calls <= 5 {
			response.ToolCalls = []harness.ToolCall{{ID: fmt.Sprint(calls), Name: "read_file",
				Arguments: json.RawMessage(fmt.Sprintf(`{"path":"file%d.go"}`, calls))}}
		} else if len(tools) != 0 {
			t.Fatal("planner should close early enough to fund its correction")
		} else if calls == 7 {
			response.Content = "Inspect the comparator and fix unknown operands."
		}
		return response, nil
	})
	budget := harness.NewStagedModelBudget(100, 200000, true, nil)
	result, err := harness.NewAgent(budget.Wrap(model, "sub:planner"), registry, 8, nil).
		RunWithOptions(context.Background(), "diagnose", harness.RunOptions{
			WorkerHandoff: func(_ context.Context, text string) error {
				if text == "" {
					return fmt.Errorf("empty handoff")
				}
				return nil
			},
		})
	if err != nil || result.StopReason != harness.StopCompleted || calls != 7 || reader.Calls() != 5 {
		t.Fatalf("result=%+v, calls=%d, executed tools=%d, err=%v", result, calls, reader.Calls(), err)
	}
}

func TestFunctionalRepairReserveCannotBeSpentByFirstSubmission(t *testing.T) {
	for _, test := range []struct {
		mode     string
		purposes []string
	}{
		{"single", []string{"agent"}},
		{"single-review", []string{"agent", "sub:reviewer"}},
		{"pipeline", []string{"agent", "sub:planner", "sub:developer", "sub:reviewer"}},
	} {
		t.Run(test.mode, func(t *testing.T) {
			b := harness.NewFunctionalModelBudget(100, 200000, test.mode, nil)
			model := modelFunc(func(context.Context, []harness.Message, []harness.ToolSpec, harness.ModelEventSink) (harness.ModelResponse, error) {
				return harness.ModelResponse{Content: "done", Usage: harness.Usage{TotalTokens: 5000}}, nil
			})
			for _, purpose := range test.purposes {
				for {
					_, err := b.Wrap(model, purpose).Generate(t.Context(), nil, nil, nil)
					if errors.Is(err, harness.ErrBudget) {
						break
					}
					if err != nil {
						t.Fatal(err)
					}
				}
			}
			if spent := b.Snapshot().Usage.TotalTokens; spent > 150000 {
				t.Fatalf("initial stages spent %d tokens, leaving less than the repair reserve", spent)
			}
			if _, err := b.Wrap(model, "repair").Generate(t.Context(), nil, nil, nil); err != nil {
				t.Fatalf("first submission consumed the repair allocation: %v", err)
			}
		})
	}
}

func TestFunctionalRepairReserveKeepsTenModelRequests(t *testing.T) {
	budget := harness.NewFunctionalModelBudget(100, 200000, "single", nil)
	model := modelFunc(func(context.Context, []harness.Message, []harness.ToolSpec, harness.ModelEventSink) (harness.ModelResponse, error) {
		return harness.ModelResponse{Content: "done", Usage: harness.Usage{TotalTokens: 100}}, nil
	})
	initial := budget.Wrap(model, "agent")
	repair := budget.Wrap(model, "repair")
	for i := 0; i < 90; i++ {
		if _, err := initial.Generate(t.Context(), nil, nil, nil); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := initial.Generate(t.Context(), nil, nil, nil); !errors.Is(err, harness.ErrBudget) {
		t.Fatalf("initial stage entered the repair reserve: %v", err)
	}
	for i := 0; i < 10; i++ {
		if _, err := repair.Generate(t.Context(), nil, nil, nil); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := repair.Generate(t.Context(), nil, nil, nil); !errors.Is(err, harness.ErrBudget) {
		t.Fatalf("repair exceeded its request allocation: %v", err)
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

func TestFinalBudgetRequestDisablesToolsByCallLimit(t *testing.T) {
	budget := harness.NewModelBudget(2, 0, nil)
	specs := []harness.ToolSpec{{Name: "read_file"}}
	var toolCounts []int
	var notices []string
	model := budget.Wrap(modelFunc(func(_ context.Context, messages []harness.Message, tools []harness.ToolSpec, _ harness.ModelEventSink) (harness.ModelResponse, error) {
		toolCounts = append(toolCounts, len(tools))
		notices = append(notices, messages[0].Content)
		return harness.ModelResponse{Usage: harness.Usage{TotalTokens: 1}}, nil
	}), "agent")
	for range 2 {
		if _, err := model.Generate(context.Background(), nil, specs, nil); err != nil {
			t.Fatal(err)
		}
	}
	if toolCounts[0] != 1 || toolCounts[1] != 0 {
		t.Fatalf("tools on final request should be disabled: %v", toolCounts)
	}
	if !strings.Contains(notices[1], "FINAL REQUEST") {
		t.Fatalf("missing final-request directive: %s", notices[1])
	}
	if strings.Contains(notices[0], "FINAL REQUEST") {
		t.Fatalf("final directive fired too early: %s", notices[0])
	}
}

func TestFinalBudgetRequestDisablesToolsByTokenShare(t *testing.T) {
	// Each request spends 96 tokens; 4 remain before request two, under the
	// 5% share, so it is treated as the final request.
	budget := harness.NewModelBudget(0, 100, nil)
	var toolCounts []int
	model := budget.Wrap(modelFunc(func(_ context.Context, _ []harness.Message, tools []harness.ToolSpec, _ harness.ModelEventSink) (harness.ModelResponse, error) {
		toolCounts = append(toolCounts, len(tools))
		return harness.ModelResponse{Usage: harness.Usage{TotalTokens: 96}}, nil
	}), "agent")
	for range 2 {
		if _, err := model.Generate(context.Background(), nil, []harness.ToolSpec{{Name: "read_file"}}, nil); err != nil {
			t.Fatal(err)
		}
	}
	if toolCounts[0] != 1 || toolCounts[1] != 0 {
		t.Fatalf("tools on final request should be disabled: %v", toolCounts)
	}
}

func TestBudgetGuidanceUrgesFinishAfterVerifiedProductionEdit(t *testing.T) {
	registry, err := harness.NewRegistry(staticTool{name: "apply_patch", output: "updated"}, staticTool{name: "exec", output: "command=ok exit_code=0"})
	if err != nil {
		t.Fatal(err)
	}
	step := 0
	model := modelFunc(func(_ context.Context, messages []harness.Message, _ []harness.ToolSpec, _ harness.ModelEventSink) (harness.ModelResponse, error) {
		step++
		notice := messages[0].Content
		if step == 3 {
			if !strings.Contains(notice, "1 production edits, 0 test-only edits; 1 tool calls and 1 successful command(s)") {
				t.Fatalf("successful command not tracked: %s", notice)
			}
			if !strings.Contains(notice, "finish now") {
				t.Fatalf("no finish guidance: %s", notice)
			}
		}
		if step == 4 {
			// A new production edit must reset the success counter: old green
			// runs never saw the newest change.
			if !strings.Contains(notice, "0 tool calls and 0 successful command(s)") {
				t.Fatalf("successful-command counter not reset: %s", notice)
			}
			return harness.ModelResponse{Content: "done", Usage: harness.Usage{TotalTokens: 10}}, nil
		}
		var call harness.ToolCall
		if step == 1 || step == 3 {
			call = harness.ToolCall{ID: fmt.Sprintf("edit-%d", step), Name: "apply_patch", Arguments: json.RawMessage(`{"path":"source.go","operation":"update"}`)}
		} else {
			call = harness.ToolCall{ID: fmt.Sprintf("verify-%d", step), Name: "exec", Arguments: json.RawMessage(`{"command":["go","test"]}`)}
		}
		return harness.ModelResponse{ToolCalls: []harness.ToolCall{call}, Usage: harness.Usage{TotalTokens: 10}}, nil
	})
	budget := harness.NewModelBudget(20, 2000, nil)
	agent := harness.NewAgent(budget.Wrap(model, "agent"), registry, 12, nil)
	if _, err := agent.Run(context.Background(), "implement the requested fix"); err != nil {
		t.Fatal(err)
	}
}

// failingExecTool simulates a non-zero exit; the structured exit code is
// the source of truth, not the observation text.
type failingExecTool struct{}

func (failingExecTool) Spec() harness.ToolSpec { return harness.ToolSpec{Name: "exec"} }

func (failingExecTool) Execute(context.Context, json.RawMessage) (harness.ToolResult, error) {
	return harness.ToolResult{Observation: "command=x exit_code=1", ExitCode: 1}, nil
}

func TestBudgetGuidanceFlagsUnverifiedProductionEdit(t *testing.T) {
	registry, err := harness.NewRegistry(failingExecTool{}, staticTool{name: "apply_patch", output: "updated"})
	if err != nil {
		t.Fatal(err)
	}
	step := 0
	model := modelFunc(func(_ context.Context, messages []harness.Message, _ []harness.ToolSpec, _ harness.ModelEventSink) (harness.ModelResponse, error) {
		step++
		notice := messages[0].Content
		if step == 8 {
			if !strings.Contains(notice, "no successful command has been observed after it") {
				t.Fatalf("failing commands counted as verification: %s", notice)
			}
			if strings.Contains(notice, "finish now") {
				t.Fatalf("unverified change urged to finish: %s", notice)
			}
			return harness.ModelResponse{Content: "done", Usage: harness.Usage{TotalTokens: 10}}, nil
		}
		if step == 1 {
			call := harness.ToolCall{ID: "edit-1", Name: "apply_patch", Arguments: json.RawMessage(`{"path":"source.go","operation":"update"}`)}
			return harness.ModelResponse{ToolCalls: []harness.ToolCall{call}, Usage: harness.Usage{TotalTokens: 10}}, nil
		}
		// Vary arguments to avoid the adjacent-duplicate doom-loop guard.
		call := harness.ToolCall{ID: fmt.Sprintf("fail-%d", step), Name: "exec", Arguments: json.RawMessage(fmt.Sprintf(`{"command":["go","test","-run","Case%d"]}`, step))}
		return harness.ModelResponse{ToolCalls: []harness.ToolCall{call}, Usage: harness.Usage{TotalTokens: 10}}, nil
	})
	budget := harness.NewModelBudget(20, 2000, nil)
	agent := harness.NewAgent(budget.Wrap(model, "agent"), registry, 12, nil)
	if _, err := agent.Run(context.Background(), "implement the requested fix"); err != nil {
		t.Fatal(err)
	}
}

func TestExplorationGateNarrowsToolsUntilFirstProductionEdit(t *testing.T) {
	registry, err := harness.NewRegistry(
		staticTool{name: "read_file", output: "content"},
		staticTool{name: "search_text", output: "hits"},
		staticTool{name: "list_files", output: "names"},
		staticTool{name: "exec", output: "command=ok exit_code=0"},
		staticTool{name: "apply_patch", output: "updated"},
	)
	if err != nil {
		t.Fatal(err)
	}
	has := func(tools []harness.ToolSpec, name string) bool {
		for _, spec := range tools {
			if spec.Name == name {
				return true
			}
		}
		return false
	}
	step := 0
	model := modelFunc(func(_ context.Context, messages []harness.Message, tools []harness.ToolSpec, _ harness.ModelEventSink) (harness.ModelResponse, error) {
		step++
		notice := messages[0].Content
		switch step {
		case 5:
			if len(tools) != 5 {
				t.Fatalf("early request should advertise all tools, got %d", len(tools))
			}
		case 7:
			if has(tools, "search_text") || has(tools, "list_files") {
				t.Fatalf("search tools should be gated at half budget: %v", tools)
			}
			if !has(tools, "exec") || !has(tools, "apply_patch") || !has(tools, "read_file") {
				t.Fatalf("level-1 gate must keep read/edit/exec: %v", tools)
			}
			if !strings.Contains(notice, "EXPLORATION BUDGET OVER") {
				t.Fatalf("missing gate directive: %s", notice)
			}
		case 10:
			if has(tools, "exec") || has(tools, "search_text") {
				t.Fatalf("exec should be gated at 70%% spent: %v", tools)
			}
			if !has(tools, "apply_patch") || !has(tools, "read_file") {
				t.Fatalf("level-2 gate must keep read/edit: %v", tools)
			}
			if !strings.Contains(notice, "EXPLORATION CLOSED") {
				t.Fatalf("missing closed directive: %s", notice)
			}
		case 11:
			// A production edit happened at step 10: the gate must lift at once
			// and restore exec for verification.
			if len(tools) != 5 {
				t.Fatalf("gate must lift after a production edit, got %d tools", len(tools))
			}
			if strings.Contains(notice, "EXPLORATION") {
				t.Fatalf("gate directive should be gone: %s", notice)
			}
		case 12:
			if len(tools) != 0 {
				t.Fatalf("final request must have no tools, got %d", len(tools))
			}
			return harness.ModelResponse{Content: "done", Usage: harness.Usage{TotalTokens: 100}}, nil
		}
		if step == 10 {
			call := harness.ToolCall{ID: "gate-edit", Name: "apply_patch", Arguments: json.RawMessage(`{"path":"source.go","operation":"update"}`)}
			return harness.ModelResponse{ToolCalls: []harness.ToolCall{call}, Usage: harness.Usage{TotalTokens: 100}}, nil
		}
		call := harness.ToolCall{ID: fmt.Sprintf("gate-read-%d", step), Name: "read_file", Arguments: json.RawMessage(fmt.Sprintf(`{"path":"file%d.go"}`, step))}
		return harness.ModelResponse{ToolCalls: []harness.ToolCall{call}, Usage: harness.Usage{TotalTokens: 100}}, nil
	})
	budget := harness.NewModelBudget(12, 1300, nil)
	agent := harness.NewAgent(budget.Wrap(model, "agent"), registry, 16, nil)
	if _, err := agent.Run(context.Background(), "implement the requested fix"); err != nil {
		t.Fatal(err)
	}
}

func TestGatePrunesWithheldToolCallsFromResponse(t *testing.T) {
	registry, err := harness.NewRegistry(
		staticTool{name: "read_file", output: "content"},
		staticTool{name: "search_text", output: "hits"},
		staticTool{name: "apply_patch", output: "updated"},
	)
	if err != nil {
		t.Fatal(err)
	}
	var executed []string
	events := harness.EventSinkFunc(func(e harness.Event) {
		if e.Kind == harness.ToolStarted {
			executed = append(executed, e.Tool)
		}
	})
	// Request 7 enters gate level 1 (6/12 remaining): search_text is not
	// advertised, but the model emits one from memory — it must be pruned.
	step := 0
	model := modelFunc(func(_ context.Context, _ []harness.Message, tools []harness.ToolSpec, _ harness.ModelEventSink) (harness.ModelResponse, error) {
		step++
		if step >= 8 {
			return harness.ModelResponse{Content: "done", Usage: harness.Usage{TotalTokens: 100}}, nil
		}
		calls := []harness.ToolCall{{
			ID: fmt.Sprintf("leak-%d", step), Name: "read_file",
			Arguments: json.RawMessage(fmt.Sprintf(`{"path":"f%d.go"}`, step)),
		}}
		if step == 7 {
			calls = append(calls, harness.ToolCall{
				ID: "leak-search", Name: "search_text",
				Arguments: json.RawMessage(`{"pattern":"x"}`),
			})
		}
		return harness.ModelResponse{ToolCalls: calls, Usage: harness.Usage{TotalTokens: 100}}, nil
	})
	budget := harness.NewModelBudget(12, 1300, events)
	agent := harness.NewAgent(budget.Wrap(model, "agent"), registry, 12, events)
	if _, err := agent.Run(context.Background(), "implement the requested fix"); err != nil {
		t.Fatal(err)
	}
	for _, name := range executed {
		if name == "search_text" {
			t.Fatalf("withheld tool leaked into execution: %v", executed)
		}
	}
	if len(executed) != 7 {
		t.Fatalf("expected 7 read_file executions, got %v", executed)
	}
}

// TestSubagentSharesRunProgressLedger: after the parent edits production
// code, a late-spawned sub-agent sees the edit in its very first request and
// is not treated as zero-edit by the exploration gate.
func TestSubagentSharesRunProgressLedger(t *testing.T) {
	budget := harness.NewModelBudget(20, 2000, nil)
	var subNotice string
	var registry *harness.Registry
	delegate := harness.NewDelegate(func(ctx context.Context, role, prompt string, maxSteps int, sink harness.EventSink) (string, error) {
		subModel := modelFunc(func(_ context.Context, messages []harness.Message, _ []harness.ToolSpec, _ harness.ModelEventSink) (harness.ModelResponse, error) {
			subNotice = messages[0].Content
			return harness.ModelResponse{Content: "sub done", Usage: harness.Usage{TotalTokens: 10}}, nil
		})
		sub := harness.NewAgent(budget.Wrap(subModel, "sub:"+role), registry, maxSteps, sink)
		return sub.Run(ctx, prompt)
	}, t.TempDir())
	var err error
	registry, err = harness.NewRegistry(staticTool{name: "apply_patch", output: "updated"}, delegate)
	if err != nil {
		t.Fatal(err)
	}
	step := 0
	parent := modelFunc(func(_ context.Context, _ []harness.Message, _ []harness.ToolSpec, _ harness.ModelEventSink) (harness.ModelResponse, error) {
		step++
		switch step {
		case 1:
			call := harness.ToolCall{ID: "edit", Name: "apply_patch", Arguments: json.RawMessage(`{"path":"source.go","operation":"update"}`)}
			return harness.ModelResponse{ToolCalls: []harness.ToolCall{call}, Usage: harness.Usage{TotalTokens: 10}}, nil
		case 2:
			call := harness.ToolCall{ID: "spawn", Name: "delegate", Arguments: json.RawMessage(`{"prompt":"continue the work"}`)}
			return harness.ModelResponse{ToolCalls: []harness.ToolCall{call}, Usage: harness.Usage{TotalTokens: 10}}, nil
		}
		return harness.ModelResponse{Content: "done", Usage: harness.Usage{TotalTokens: 10}}, nil
	})
	ctx := harness.WithRunProgress(context.Background())
	agent := harness.NewAgent(budget.Wrap(parent, "agent"), registry, 12, nil)
	if _, err := agent.Run(ctx, "implement the requested fix"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(subNotice, "1 production edits") {
		t.Fatalf("subagent did not see the shared ledger: %s", subNotice)
	}
}
