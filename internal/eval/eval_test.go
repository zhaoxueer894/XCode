package eval

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"xcode/internal/eventstore"
	"xcode/internal/harness"
)

// TestLoadCatalogAppliesDefaultsAndReturnsIdentity 验证有效 Catalog 会得到默认预算和稳定身份。
func TestLoadCatalogAppliesDefaultsAndReturnsIdentity(t *testing.T) {
	tasks := validTasks()
	tasks[0].MaxSteps = 0
	tasks[0].TimeoutSeconds = 0
	path := writeCatalog(t, tasks)

	catalog, catalogHash, err := LoadCatalog(path)
	if err != nil {
		t.Fatalf("LoadCatalog: %v", err)
	}
	if len(catalog.Tasks) != 8 {
		t.Fatalf("task count = %d, want 8", len(catalog.Tasks))
	}
	if catalog.Tasks[0].MaxSteps != 4 || catalog.Tasks[0].TimeoutSeconds != 30 {
		t.Fatalf("defaults = max_steps %d, timeout %d", catalog.Tasks[0].MaxSteps, catalog.Tasks[0].TimeoutSeconds)
	}
	if len(catalogHash) != 64 || catalog.loadedHash != catalogHash || catalog.resolvedHash == "" {
		t.Fatalf("catalog identity = raw %q, loaded %q, resolved %q", catalogHash, catalog.loadedHash, catalog.resolvedHash)
	}
}

// TestLoadCatalogRejectsInvalidTasks 验证 Catalog 会拒绝不安全或不可复现的任务定义。
func TestLoadCatalogRejectsInvalidTasks(t *testing.T) {
	testCases := []struct {
		name      string
		mutate    func([]Task) []Task
		wantError string
	}{
		{
			name:      "too few tasks",
			mutate:    func(tasks []Task) []Task { return tasks[:7] },
			wantError: "8 to 16 tasks",
		},
		{
			name: "duplicate task id",
			mutate: func(tasks []Task) []Task {
				tasks[1].ID = tasks[0].ID
				return tasks
			},
			wantError: "duplicate task id",
		},
		{
			name: "invalid task id",
			mutate: func(tasks []Task) []Task {
				tasks[0].ID = "bad/id"
				return tasks
			},
			wantError: "invalid id",
		},
		{
			name: "missing expected tool",
			mutate: func(tasks []Task) []Task {
				tasks[0].ExpectedTool = ""
				return tasks
			},
			wantError: "expected_tool",
		},
		{
			name: "expected arguments is not object",
			mutate: func(tasks []Task) []Task {
				tasks[0].ExpectedArguments = json.RawMessage(`[]`)
				return tasks
			},
			wantError: "JSON object",
		},
		{
			name: "fixture escapes workspace",
			mutate: func(tasks []Task) []Task {
				tasks[0].Files = map[string]string{"../outside.txt": "secret"}
				return tasks
			},
			wantError: "escapes workspace",
		},
		{
			name: "normalized duplicate fixture",
			mutate: func(tasks []Task) []Task {
				tasks[0].Files = map[string]string{"answer.txt": "first", "nested/../answer.txt": "second"}
				return tasks
			},
			wantError: "resolve to the same file",
		},
		{
			name: "case folded duplicate fixture",
			mutate: func(tasks []Task) []Task {
				tasks[0].Files = map[string]string{"README.md": "first", "readme.md": "second"}
				return tasks
			},
			wantError: "resolve to the same file",
		},
		{
			name: "file directory conflict",
			mutate: func(tasks []Task) []Task {
				tasks[0].Files = map[string]string{"internal": "file", "internal/main.go": "package main"}
				return tasks
			},
			wantError: "conflict as file and directory",
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			path := writeCatalog(t, testCase.mutate(validTasks()))
			_, _, err := LoadCatalog(path)
			if err == nil || !strings.Contains(err.Error(), testCase.wantError) {
				t.Fatalf("LoadCatalog error = %v, want substring %q", err, testCase.wantError)
			}
		})
	}
}

// TestNewRunnerValidatesConfig 验证 Runner 在运行前拒绝缺少评测身份的配置。
func TestNewRunnerValidatesConfig(t *testing.T) {
	execute := func(context.Context, string, Task, harness.EventSink) (string, error) { return "", nil }
	base := validConfig("catalog-hash")

	testCases := []struct {
		name      string
		config    Config
		wantError string
	}{
		{name: "zero trials", config: func() Config { value := base; value.Trials = 0; return value }(), wantError: "trials"},
		{name: "missing harness version", config: func() Config { value := base; value.HarnessVersion = ""; return value }(), wantError: "harness version"},
		{name: "missing provider", config: func() Config { value := base; value.Provider = ""; return value }(), wantError: "provider"},
		{name: "missing model", config: func() Config { value := base; value.Model = ""; return value }(), wantError: "model"},
		{name: "missing catalog hash", config: func() Config { value := base; value.CatalogHash = ""; return value }(), wantError: "catalog hash"},
		{name: "bailian missing endpoint", config: func() Config { value := base; value.Provider = "bailian"; return value }(), wantError: "provider endpoint"},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			_, err := NewRunner(t.TempDir(), "run", testCase.config, execute)
			if err == nil || !strings.Contains(err.Error(), testCase.wantError) {
				t.Fatalf("NewRunner error = %v, want substring %q", err, testCase.wantError)
			}
		})
	}
}

// TestRunnerRejectsCatalogIdentityMismatch 验证报告不能把另一份 Catalog 标记成当前配置的任务集。
func TestRunnerRejectsCatalogIdentityMismatch(t *testing.T) {
	catalog, catalogHash, err := LoadCatalog(writeCatalog(t, validTasks()))
	if err != nil {
		t.Fatalf("LoadCatalog: %v", err)
	}
	execute := func(context.Context, string, Task, harness.EventSink) (string, error) { return "", nil }

	t.Run("raw hash mismatch", func(t *testing.T) {
		config := validConfig("another-catalog-hash")
		runner, err := NewRunner(t.TempDir(), "run", config, execute)
		if err != nil {
			t.Fatalf("NewRunner: %v", err)
		}
		_, err = runner.Run(context.Background(), catalog)
		if err == nil || !strings.Contains(err.Error(), "does not match") {
			t.Fatalf("Run error = %v, want catalog mismatch", err)
		}
	})

	t.Run("catalog changed after load", func(t *testing.T) {
		changed := catalog
		changed.Tasks = append([]Task(nil), catalog.Tasks...)
		changed.Tasks[0].Prompt = "changed prompt"
		runner, err := NewRunner(t.TempDir(), "run", validConfig(catalogHash), execute)
		if err != nil {
			t.Fatalf("NewRunner: %v", err)
		}
		_, err = runner.Run(context.Background(), changed)
		if err == nil || !strings.Contains(err.Error(), "changed after loading") {
			t.Fatalf("Run error = %v, want changed catalog", err)
		}
	})
}

// TestRunnerProducesIsolatedTracesAndReports 验证完整 Eval 会隔离 Trial、记录 Trace 并生成两级汇总。
func TestRunnerProducesIsolatedTracesAndReports(t *testing.T) {
	catalog, catalogHash, err := LoadCatalog(writeCatalog(t, validTasks()))
	if err != nil {
		t.Fatalf("LoadCatalog: %v", err)
	}
	outputDir := filepath.Join(t.TempDir(), "run-output")
	var workspaces []string
	execute := func(_ context.Context, workspace string, task Task, sink harness.EventSink) (string, error) {
		workspaces = append(workspaces, workspace)
		var path string
		for name := range task.Files {
			path = name
			break
		}
		content, err := os.ReadFile(filepath.Join(workspace, filepath.FromSlash(path)))
		if err != nil {
			return "", err
		}
		now := time.Now().UTC()
		arguments, _ := json.Marshal(map[string]string{"path": path})
		sink.Emit(harness.Event{Time: now, Kind: harness.RunStarted})
		sink.Emit(harness.Event{Time: now.Add(time.Millisecond), Kind: harness.ToolStarted, Step: 1, Tool: "read_file", ToolCallID: "call-1", Data: string(arguments)})
		sink.Emit(harness.Event{Time: now.Add(2 * time.Millisecond), Kind: harness.ToolCompleted, Step: 1, Tool: "read_file", ToolCallID: "call-1", Data: string(content)})
		sink.Emit(harness.Event{Time: now.Add(3 * time.Millisecond), Kind: harness.ModelUsageEmitted, Step: 2, InputTokens: 3, OutputTokens: 2, TotalTokens: 5})
		sink.Emit(harness.Event{Time: now.Add(4 * time.Millisecond), Kind: harness.RunCompleted, Step: 2, Data: string(content)})
		return string(content), nil
	}
	config := validConfig(catalogHash)
	config.Trials = 2
	runner, err := NewRunner(outputDir, "run-1", config, execute)
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}
	var progressEvents []ProgressEvent
	runner.SetProgressSink(func(event ProgressEvent) {
		progressEvents = append(progressEvents, event)
	})
	report, err := runner.Run(context.Background(), catalog)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if len(report.Results) != 16 || report.RunSummary.TotalTasks != 8 || report.RunSummary.TotalTrials != 16 || report.RunSummary.PassedTrials != 16 {
		t.Fatalf("report counts = results %d, summary %#v", len(report.Results), report.RunSummary)
	}
	if report.RunSummary.TotalInputTokens != 48 || report.RunSummary.TotalOutputTokens != 32 || report.RunSummary.TotalTokens != 80 {
		t.Fatalf("token totals = %#v", report.RunSummary)
	}
	if report.RunSummary.AverageSteps != 2 || report.RunSummary.AverageToolCalls != 1 || report.RunSummary.AverageRepeatedReads != 0 {
		t.Fatalf("average metrics = %#v", report.RunSummary)
	}
	if len(progressEvents) != 32 {
		t.Fatalf("progress event count = %d, want 32", len(progressEvents))
	}
	for index, result := range report.Results {
		started := progressEvents[index*2]
		completed := progressEvents[index*2+1]
		position := index + 1
		if started.Kind != ProgressTrialStarted || started.Position != position || started.Total != 16 || started.TaskID != result.TaskID || started.Trial != result.Trial {
			t.Fatalf("started progress %d = %#v", position, started)
		}
		if completed.Kind != ProgressTrialCompleted || completed.Position != position || completed.Total != 16 || completed.Result.TaskID != result.TaskID || completed.Result.Trial != result.Trial || completed.Result.Outcome != TrialPassed {
			t.Fatalf("completed progress %d = %#v", position, completed)
		}
	}
	for _, summary := range report.TaskSummaries {
		if summary.TotalTrials != 2 || summary.EvaluatedTrials != 2 || summary.PassedTrials != 2 || summary.FailedTrials != 0 || summary.ErroredTrials != 0 || summary.SuccessRate != 1 {
			t.Fatalf("task summary = %#v", summary)
		}
	}
	for _, workspace := range workspaces {
		if _, err := os.Stat(workspace); !os.IsNotExist(err) {
			t.Fatalf("workspace %q still exists or stat failed: %v", workspace, err)
		}
	}

	firstTrace := filepath.Join(outputDir, filepath.FromSlash(report.Results[0].TracePath))
	traceData, err := os.ReadFile(firstTrace)
	if err != nil {
		t.Fatalf("read trace: %v", err)
	}
	traceLines := strings.Split(strings.TrimSpace(string(traceData)), "\n")
	if len(traceLines) != 5 {
		t.Fatalf("trace line count = %d, want 5", len(traceLines))
	}
	for index, line := range traceLines {
		var record eventstore.TraceRecord
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatalf("decode trace line %d: %v", index+1, err)
		}
		if record.Sequence != uint64(index+1) || record.ConfigHash != report.ConfigHash || record.RunID != report.RunID {
			t.Fatalf("trace record %d = %#v", index+1, record)
		}
	}

	if err := WriteReport(outputDir, report); err != nil {
		t.Fatalf("WriteReport: %v", err)
	}
	jsonData, err := os.ReadFile(filepath.Join(outputDir, "report.json"))
	if err != nil {
		t.Fatalf("read report.json: %v", err)
	}
	var decoded Report
	if err := json.Unmarshal(jsonData, &decoded); err != nil {
		t.Fatalf("decode report.json: %v", err)
	}
	if decoded.RunID != report.RunID || decoded.RunSummary.PassedTrials != 16 {
		t.Fatalf("decoded report = %#v", decoded)
	}
	markdown, err := os.ReadFile(filepath.Join(outputDir, "report.md"))
	if err != nil {
		t.Fatalf("read report.md: %v", err)
	}
	if !strings.Contains(string(markdown), "Passed / failed / run errors: `16 / 0 / 0`") || !strings.Contains(string(markdown), "## Task summaries") {
		t.Fatalf("report.md missing summary:\n%s", markdown)
	}
}

// TestRunnerReportsAbortedTrialProgress 验证 Trace 无法创建时终端可以显示当前 Trial 已中止。
func TestRunnerReportsAbortedTrialProgress(t *testing.T) {
	catalog, catalogHash, err := LoadCatalog(writeCatalog(t, validTasks()))
	if err != nil {
		t.Fatalf("LoadCatalog: %v", err)
	}
	outputDir := t.TempDir()
	traceDir := filepath.Join(outputDir, "traces")
	if err := os.MkdirAll(traceDir, 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(filepath.Join(traceDir, "task_0-trial-01.jsonl"), []byte("existing"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	execute := func(context.Context, string, Task, harness.EventSink) (string, error) {
		return "", nil
	}
	runner, err := NewRunner(outputDir, "run", validConfig(catalogHash), execute)
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}
	var progressEvents []ProgressEvent
	runner.SetProgressSink(func(event ProgressEvent) {
		progressEvents = append(progressEvents, event)
	})

	_, err = runner.Run(context.Background(), catalog)
	if err == nil || !strings.Contains(err.Error(), "create trace") {
		t.Fatalf("Run error = %v, want create trace failure", err)
	}
	if len(progressEvents) != 2 || progressEvents[0].Kind != ProgressTrialStarted || progressEvents[1].Kind != ProgressTrialAborted {
		t.Fatalf("progress events = %#v", progressEvents)
	}
	if progressEvents[1].TaskID != "task_0" || progressEvents[1].Position != 1 || progressEvents[1].Total != 8 || !strings.Contains(progressEvents[1].Error, "create trace") {
		t.Fatalf("aborted progress = %#v", progressEvents[1])
	}
}

// TestGradeTaskOutcomeUsesToolTrace 验证 Task 根据工具调用和工具结果判定，不依赖模型最终答案的原样文字。
func TestGradeTaskOutcomeUsesToolTrace(t *testing.T) {
	task := Task{
		ExpectedTool:            "read_file",
		ExpectedArguments:       json.RawMessage(`{"path":"README.md"}`),
		ExpectedOutputSubstring: "# Alpha Fixture",
	}
	matchingEvents := []harness.Event{
		{Kind: harness.ToolStarted, Tool: "read_file", ToolCallID: "call-1", Data: `{ "path": "./README.md" }`},
		{Kind: harness.ToolCompleted, Tool: "read_file", ToolCallID: "call-1", Data: "# Alpha Fixture\n"},
	}
	if err := gradeTaskOutcome(task, matchingEvents); err != nil {
		t.Fatalf("gradeTaskOutcome matching trace: %v", err)
	}

	testCases := []struct {
		name      string
		events    []harness.Event
		wantError string
	}{
		{
			name:      "tool not called",
			events:    nil,
			wantError: "was not called",
		},
		{
			name: "wrong arguments",
			events: []harness.Event{
				{Kind: harness.ToolStarted, Tool: "read_file", ToolCallID: "call-1", Data: `{"path":"OTHER.md"}`},
			},
			wantError: "arguments different",
		},
		{
			name: "wrong tool output",
			events: []harness.Event{
				{Kind: harness.ToolStarted, Tool: "read_file", ToolCallID: "call-1", Data: `{"path":"README.md"}`},
				{Kind: harness.ToolCompleted, Tool: "read_file", ToolCallID: "call-1", Data: "different content"},
			},
			wantError: "result does not contain",
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			err := gradeTaskOutcome(task, testCase.events)
			if err == nil || !strings.Contains(err.Error(), testCase.wantError) {
				t.Fatalf("gradeTaskOutcome error = %v, want substring %q", err, testCase.wantError)
			}
		})
	}
}

// TestRunnerSeparatesTaskFailuresFromRunErrors 验证评分失败和 Provider 运行错误使用不同统计字段。
func TestRunnerSeparatesTaskFailuresFromRunErrors(t *testing.T) {
	catalog, catalogHash, err := LoadCatalog(writeCatalog(t, validTasks()))
	if err != nil {
		t.Fatalf("LoadCatalog: %v", err)
	}
	execute := func(_ context.Context, _ string, task Task, sink harness.EventSink) (string, error) {
		if task.ID == "task_2" {
			return "", context.DeadlineExceeded
		}
		if task.ID == "task_3" {
			return "", harness.ErrMaxSteps
		}
		sink.Emit(harness.Event{
			Kind:       harness.ToolStarted,
			Step:       1,
			Tool:       task.ExpectedTool,
			ToolCallID: "call-1",
			Data:       string(task.ExpectedArguments),
		})
		output := task.ExpectedOutputSubstring
		if task.ID == "task_1" {
			output = "unexpected tool output"
		}
		sink.Emit(harness.Event{
			Kind:       harness.ToolCompleted,
			Step:       1,
			Tool:       task.ExpectedTool,
			ToolCallID: "call-1",
			Data:       output,
		})
		return "模型可以自由改写最终答案", nil
	}
	runner, err := NewRunner(t.TempDir(), "run", validConfig(catalogHash), execute)
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}
	report, err := runner.Run(context.Background(), catalog)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if report.Results[0].Outcome != TrialPassed || report.Results[1].Outcome != TrialFailed || report.Results[2].Outcome != TrialError || report.Results[3].Outcome != TrialFailed {
		t.Fatalf("trial outcomes = %q, %q, %q, %q", report.Results[0].Outcome, report.Results[1].Outcome, report.Results[2].Outcome, report.Results[3].Outcome)
	}
	if report.RunSummary.TotalTrials != 8 || report.RunSummary.EvaluatedTrials != 7 || report.RunSummary.PassedTrials != 5 || report.RunSummary.FailedTrials != 2 || report.RunSummary.ErroredTrials != 1 {
		t.Fatalf("run summary = %#v", report.RunSummary)
	}
	if report.TaskSummaries[1].SuccessRate != 0 || report.TaskSummaries[2].EvaluatedTrials != 0 || report.TaskSummaries[2].ErroredTrials != 1 || report.TaskSummaries[3].FailedTrials != 1 {
		t.Fatalf("task summaries = %#v", report.TaskSummaries)
	}
}

// TestCollectMetricsUsesStructuredToolResults 验证指标直接读取 tool.completed 的结构化字段。
func TestCollectMetricsUsesStructuredToolResults(t *testing.T) {
	events := []harness.Event{
		{Kind: harness.ToolStarted, Step: 1, Tool: "read_file", Data: `{"path":"README.md"}`},
		{
			Kind: harness.ToolCompleted, Step: 1, Tool: "read_file",
			ArtifactID: "sha256:aaaa", OriginalBytes: 10_000, VisibleBytes: 8_192,
		},
		{Kind: harness.ToolStarted, Step: 2, Tool: "read_file", Data: `{ "path": "./README.md" }`},
		{
			Kind: harness.ToolCompleted, Step: 2, Tool: "read_file",
			ArtifactID: "sha256:aaaa", OriginalBytes: 10_000, VisibleBytes: 0, RepeatedRead: true,
		},
		{Kind: harness.ToolStarted, Step: 3, Tool: "search_text", Data: `{"query":"XCode"}`},
		{
			Kind: harness.ToolCompleted, Step: 3, Tool: "search_text",
			ArtifactID: "sha256:bbbb", OriginalBytes: 500, VisibleBytes: 500,
		},
		{Kind: harness.ModelUsageEmitted, Step: 3, InputTokens: 7, OutputTokens: 5, TotalTokens: 12},
	}
	metrics := collectMetrics(events)
	if metrics.Steps != 3 || metrics.ToolCalls != 3 || metrics.RepeatedReads != 1 {
		t.Fatalf("tool metrics = %#v", metrics)
	}
	if metrics.Artifacts != 3 || metrics.ArtifactBytes != 20_500 || metrics.VisibleBytes != 8_692 {
		t.Fatalf("artifact metrics = %#v", metrics)
	}
	// 虚拟化字节 = 每次结果中未进入上下文的部分之和：(10000-8192) + (10000-0) + 0。
	if metrics.VirtualizedBytes != 11_808 {
		t.Fatalf("virtualized bytes = %d, want 11808", metrics.VirtualizedBytes)
	}
	if metrics.InputTokens != 7 || metrics.OutputTokens != 5 || metrics.TotalTokens != 12 {
		t.Fatalf("token metrics = %#v", metrics)
	}
}

func validTasks() []Task {
	tasks := make([]Task, 8)
	for index := range tasks {
		path := fmt.Sprintf("fixtures/file-%d.txt", index)
		content := fmt.Sprintf("fixture-content-%d", index)
		tasks[index] = Task{
			ID:                      fmt.Sprintf("task_%d", index),
			Prompt:                  "请读取 " + path,
			Files:                   map[string]string{path: content},
			ExpectedTool:            "read_file",
			ExpectedArguments:       json.RawMessage(fmt.Sprintf(`{"path":%q}`, path)),
			ExpectedOutputSubstring: content,
			DemoScript:              []DemoStep{{Tool: "read_file", Arguments: json.RawMessage(fmt.Sprintf(`{"path":%q}`, path))}},
			MaxSteps:                4,
			TimeoutSeconds:          30,
		}
	}
	return tasks
}

func validConfig(catalogHash string) Config {
	return Config{
		HarnessVersion: "test-harness",
		Provider:       "demo",
		Model:          "demo",
		Trials:         1,
		CatalogHash:    catalogHash,
	}
}

func writeCatalog(t *testing.T, tasks []Task) string {
	t.Helper()

	data, err := json.Marshal(Catalog{SchemaVersion: catalogSchemaVersion, Tasks: tasks})
	if err != nil {
		t.Fatalf("encode catalog: %v", err)
	}
	path := filepath.Join(t.TempDir(), "tasks.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write catalog: %v", err)
	}
	return path
}

// TestLoadCatalogRejectsSchema3InvalidTasks 验证 Schema 3 新增字段的校验规则。
func TestLoadCatalogRejectsSchema3InvalidTasks(t *testing.T) {
	testCases := []struct {
		name      string
		mutate    func([]Task) []Task
		wantError string
	}{
		{
			name: "missing demo_script",
			mutate: func(tasks []Task) []Task {
				tasks[0].DemoScript = nil
				return tasks
			},
			wantError: "demo_script",
		},
		{
			name: "verify command without program",
			mutate: func(tasks []Task) []Task {
				tasks[0].VerifyCommand = []string{"   "}
				return tasks
			},
			wantError: "program name",
		},
		{
			name: "neither expected fields nor verify command",
			mutate: func(tasks []Task) []Task {
				tasks[0].ExpectedTool = ""
				tasks[0].ExpectedArguments = nil
				tasks[0].ExpectedOutputSubstring = ""
				return tasks
			},
			wantError: "either expected_tool fields or verify_command",
		},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			_, _, err := LoadCatalog(writeCatalog(t, testCase.mutate(validTasks())))
			if err == nil || !strings.Contains(err.Error(), testCase.wantError) {
				t.Fatalf("LoadCatalog error = %v, want substring %q", err, testCase.wantError)
			}
		})
	}
}

// TestCollectStopReason 验证从事件序列提取 Harness 层停止原因的规则。
func TestCollectStopReason(t *testing.T) {
	testCases := []struct {
		name   string
		events []harness.Event
		want   string
	}{
		{
			name:   "completed carries reason",
			events: []harness.Event{{Kind: harness.RunCompleted, StopReason: harness.StopCompleted}},
			want:   "completed",
		},
		{
			name:   "controlled failure carries reason",
			events: []harness.Event{{Kind: harness.RunFailed, StopReason: harness.StopDoomLoop}},
			want:   "doom_loop",
		},
		{
			name:   "failure without reason defaults to failed",
			events: []harness.Event{{Kind: harness.RunFailed}},
			want:   "failed",
		},
		{
			name: "skips non run events and uses the last run event",
			events: []harness.Event{
				{Kind: harness.RunCompleted, StopReason: harness.StopCompleted},
				{Kind: harness.ToolStarted},
				{Kind: harness.RunFailed, StopReason: harness.StopBudget},
			},
			want: "budget",
		},
		{
			name:   "no run events",
			events: nil,
			want:   "",
		},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := collectStopReason(testCase.events); got != testCase.want {
				t.Fatalf("collectStopReason = %q, want %q", got, testCase.want)
			}
		})
	}
}

// verifyTestGoBinary 返回当前工具链的 go 可执行文件绝对路径，避免依赖 PATH。
func verifyTestGoBinary(t *testing.T) string {
	t.Helper()
	name := "go"
	if runtime.GOOS == "windows" {
		name = "go.exe"
	}
	return filepath.Join(runtime.GOROOT(), "bin", name)
}

// TestVerifyWorkspaceOutcomeUsesExitCode 验证环境结果判定完全取决于验证命令的退出码。
func TestVerifyWorkspaceOutcomeUsesExitCode(t *testing.T) {
	workspace := t.TempDir()
	goBinary := verifyTestGoBinary(t)

	passing := Task{VerifyCommand: []string{goBinary, "version"}, TimeoutSeconds: 60}
	if err := verifyWorkspaceOutcome(context.Background(), workspace, passing); err != nil {
		t.Fatalf("verifyWorkspaceOutcome passing: %v", err)
	}

	failing := Task{VerifyCommand: []string{goBinary, "version", "--definitely-bogus"}, TimeoutSeconds: 60}
	err := verifyWorkspaceOutcome(context.Background(), workspace, failing)
	if err == nil || !strings.Contains(err.Error(), "verify command failed") {
		t.Fatalf("verifyWorkspaceOutcome failing error = %v, want verify command failed", err)
	}
}
