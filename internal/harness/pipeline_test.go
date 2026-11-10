package harness_test

// Pipeline regression tests: stage order enforcement, gate precondition,
// advertised-tool filtering per orchestrate mode, planner stage cap.

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"xcode/internal/harness"
)

// scriptedSubagentFactory: every sub-agent is driven by a scripted model
// returning one fixed reply per role.
func scriptedSubagentFactory(responses map[string]string) harness.SubagentFactory {
	subRegistry, err := harness.NewRegistry(staticTool{name: "read_file", output: "content"})
	if err != nil {
		panic(err)
	}
	return func(ctx context.Context, role, prompt string, maxSteps int, sink harness.EventSink) (string, error) {
		model := modelFunc(func(_ context.Context, _ []harness.Message, _ []harness.ToolSpec, _ harness.ModelEventSink) (harness.ModelResponse, error) {
			return harness.ModelResponse{Content: responses[role], Usage: harness.Usage{TotalTokens: 5}}, nil
		})
		return harness.NewAgent(model, subRegistry, maxSteps, sink).Run(ctx, prompt)
	}
}

func TestPipelineOrderEnforcesPlannerFirst(t *testing.T) {
	observations := []string{}
	var registry *harness.Registry
	delegate := harness.NewDelegate(scriptedSubagentFactory(map[string]string{
		"planner":   "diagnosis report",
		"developer": "implemented",
	}), t.TempDir())
	delegate.EnablePipeline()
	var err error
	registry, err = harness.NewRegistry(delegate)
	if err != nil {
		t.Fatal(err)
	}
	step := 0
	model := modelFunc(func(_ context.Context, _ []harness.Message, _ []harness.ToolSpec, _ harness.ModelEventSink) (harness.ModelResponse, error) {
		step++
		switch step {
		case 1: // Jumping ahead: developer before planner must be rejected.
			call := harness.ToolCall{ID: "early-dev", Name: "delegate", Arguments: json.RawMessage(`{"role":"developer","prompt":"fix it"}`)}
			return harness.ModelResponse{ToolCalls: []harness.ToolCall{call}, Usage: harness.Usage{TotalTokens: 5}}, nil
		case 2:
			call := harness.ToolCall{ID: "plan", Name: "delegate", Arguments: json.RawMessage(`{"role":"planner","prompt":"diagnose"}`)}
			return harness.ModelResponse{ToolCalls: []harness.ToolCall{call}, Usage: harness.Usage{TotalTokens: 5}}, nil
		case 3:
			call := harness.ToolCall{ID: "dev", Name: "delegate", Arguments: json.RawMessage(`{"role":"developer","prompt":"implement per report"}`)}
			return harness.ModelResponse{ToolCalls: []harness.ToolCall{call}, Usage: harness.Usage{TotalTokens: 5}}, nil
		}
		// Step 4 just returns; the assertion ran on the captured observations.
		return harness.ModelResponse{Content: "done", Usage: harness.Usage{TotalTokens: 5}}, nil
	})
	// Capture tool observations to assert the order error.
	capturing := harness.NewAgent(&observationCapturer{inner: model, sink: &observations}, registry, 8, nil)
	if _, err := capturing.Run(context.Background(), "fix"); err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(observations, "\n")
	if !strings.Contains(joined, "pipeline order: delegate role=planner first") {
		t.Fatalf("missing order enforcement error: %s", joined)
	}
	if missing := delegate.MissingStages(); len(missing) != 0 {
		t.Fatalf("stages should be complete after planner+developer ran: %v", missing)
	}
}

// observationCapturer records every tool observation, then forwards.
type observationCapturer struct {
	inner harness.Model
	sink  *[]string
}

func (c *observationCapturer) Generate(ctx context.Context, messages []harness.Message, tools []harness.ToolSpec, sink harness.ModelEventSink) (harness.ModelResponse, error) {
	for _, message := range messages {
		if message.Role == "tool" {
			*c.sink = append(*c.sink, message.Content)
		}
	}
	return c.inner.Generate(ctx, messages, tools, sink)
}

func TestPipelineGatePreconditionBlocksEarlyCompletion(t *testing.T) {
	delegate := harness.NewDelegate(scriptedSubagentFactory(map[string]string{
		"planner":   "diagnosis report",
		"developer": "implemented and tests pass",
		"reviewer":  "GATE-VERDICT: PASS",
	}), t.TempDir())
	delegate.EnablePipeline()
	registry, err := harness.NewRegistry(delegate)
	if err != nil {
		t.Fatal(err)
	}
	step := 0
	var blocked []string
	model := modelFunc(func(_ context.Context, messages []harness.Message, _ []harness.ToolSpec, _ harness.ModelEventSink) (harness.ModelResponse, error) {
		step++
		for _, message := range messages {
			if strings.Contains(message.Content, "planner stage has not completed") {
				blocked = append(blocked, message.Content)
			}
		}
		switch step {
		case 1: // Claiming completion before any stage ran must be blocked.
			return harness.ModelResponse{Content: "done", Usage: harness.Usage{TotalTokens: 5}}, nil
		case 2:
			call := harness.ToolCall{ID: "plan", Name: "delegate", Arguments: json.RawMessage(`{"role":"planner","prompt":"diagnose"}`)}
			return harness.ModelResponse{ToolCalls: []harness.ToolCall{call}, Usage: harness.Usage{TotalTokens: 5}}, nil
		case 3:
			call := harness.ToolCall{ID: "dev", Name: "delegate", Arguments: json.RawMessage(`{"role":"developer","prompt":"implement"}`)}
			return harness.ModelResponse{ToolCalls: []harness.ToolCall{call}, Usage: harness.Usage{TotalTokens: 5}}, nil
		}
		return harness.ModelResponse{Content: "done", Usage: harness.Usage{TotalTokens: 5}}, nil
	})
	agent := harness.NewAgent(model, registry, 8, nil)
	result, err := agent.RunWithOptions(context.Background(), "fix", harness.RunOptions{
		Contract:             &harness.TaskContract{Goal: "fix", Review: true},
		PipelinePrecondition: delegate.MissingStages,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.StopReason != harness.StopCompleted {
		t.Fatalf("stop reason = %s, want completed", result.StopReason)
	}
	if len(blocked) == 0 {
		t.Fatal("early completion was not blocked by the pipeline precondition")
	}
}

func TestOrchestrateFiltersAdvertisedTools(t *testing.T) {
	delegate := harness.NewDelegate(scriptedSubagentFactory(map[string]string{"reviewer": "ok"}), t.TempDir())
	registry, err := harness.NewRegistry(
		staticTool{name: "read_file", output: "content"},
		staticTool{name: "apply_patch", output: "updated"},
		staticTool{name: "git_diff", output: "diff"},
		delegate,
	)
	if err != nil {
		t.Fatal(err)
	}
	for _, testCase := range []struct {
		orchestrate string
		want        []string
	}{
		{"", []string{"apply_patch", "delegate", "git_diff", "read_file"}},
		{"single", []string{"apply_patch", "git_diff", "read_file"}},
		{"single-review", []string{"apply_patch", "git_diff", "read_file"}},
		{"pipeline", []string{"delegate", "git_diff"}},
	} {
		var advertised []string
		model := modelFunc(func(_ context.Context, _ []harness.Message, tools []harness.ToolSpec, _ harness.ModelEventSink) (harness.ModelResponse, error) {
			for _, spec := range tools {
				advertised = append(advertised, spec.Name)
			}
			return harness.ModelResponse{Content: "done", Usage: harness.Usage{TotalTokens: 5}}, nil
		})
		agent := harness.NewAgent(model, registry, 4, nil)
		if _, err := agent.RunWithOptions(context.Background(), "fix", harness.RunOptions{Orchestrate: testCase.orchestrate}); err != nil {
			t.Fatal(err)
		}
		if strings.Join(advertised, ",") != strings.Join(testCase.want, ",") {
			t.Fatalf("orchestrate=%q advertised %v, want %v", testCase.orchestrate, advertised, testCase.want)
		}
	}
}

func TestPlannerStageCapForcesFinalAnswer(t *testing.T) {
	budget := harness.NewModelBudget(10, 100, nil)
	call := 0
	var notices []string
	var toolCounts []int
	model := modelFunc(func(_ context.Context, messages []harness.Message, tools []harness.ToolSpec, _ harness.ModelEventSink) (harness.ModelResponse, error) {
		call++
		notices = append(notices, messages[0].Content)
		toolCounts = append(toolCounts, len(tools))
		if call >= 3 {
			return harness.ModelResponse{Content: "structured diagnosis report", Usage: harness.Usage{TotalTokens: 40}}, nil
		}
		return harness.ModelResponse{Content: "thinking", Usage: harness.Usage{TotalTokens: 40}}, nil
	})
	planner := budget.Wrap(model, "sub:planner")
	ctx := context.Background()
	_, _ = planner.Generate(ctx, nil, []harness.ToolSpec{{Name: "read_file"}}, nil) // spends 40, over the 30% cap
	_, _ = planner.Generate(ctx, nil, []harness.ToolSpec{{Name: "read_file"}}, nil)
	if len(toolCounts) != 2 || toolCounts[1] != 0 {
		t.Fatalf("second request must have tools disabled after the stage cap, got %v", toolCounts)
	}
	if !strings.Contains(notices[1], "PLANNER STAGE BUDGET EXHAUSTED") {
		t.Fatalf("missing stage-cap directive: %s", notices[1])
	}
	_ = fmt.Sprint()
}
