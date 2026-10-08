package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"xcode/internal/eval"
)

// TestExecutableHarnessVersionIsStable 验证同一个可执行文件会得到稳定的代码身份。
func TestExecutableHarnessVersionIsStable(t *testing.T) {
	first, err := executableHarnessVersion()
	if err != nil {
		t.Fatalf("first executableHarnessVersion: %v", err)
	}
	second, err := executableHarnessVersion()
	if err != nil {
		t.Fatalf("second executableHarnessVersion: %v", err)
	}
	if first != second {
		t.Fatalf("harness versions differ: %q != %q", first, second)
	}
	if !strings.HasPrefix(first, "s3-eval-v0+sha256:") || len(strings.TrimPrefix(first, "s3-eval-v0+sha256:")) != 64 {
		t.Fatalf("harness version = %q", first)
	}
}

// TestRunEvalDemoProducesCompleteReport 验证 eval 子命令可以使用 Demo Provider 完成整套离线任务。
func TestRunEvalDemoProducesCompleteReport(t *testing.T) {
	catalogPath := writeEvalCatalog(t)
	outputDir := filepath.Join(t.TempDir(), "eval-output")
	err := runEval(context.Background(), []string{
		"--catalog", catalogPath,
		"--output", outputDir,
		"--provider", "demo",
		"--trials", "1",
	})
	if err != nil {
		t.Fatalf("runEval: %v", err)
	}

	reportData, err := os.ReadFile(filepath.Join(outputDir, "report.json"))
	if err != nil {
		t.Fatalf("read report.json: %v", err)
	}
	var report eval.Report
	if err := json.Unmarshal(reportData, &report); err != nil {
		t.Fatalf("decode report.json: %v", err)
	}
	if report.RunSummary.TotalTasks != 9 || report.RunSummary.TotalTrials != 9 || report.RunSummary.PassedTrials != 9 {
		t.Fatalf("run summary = %#v", report.RunSummary)
	}
	if !strings.HasPrefix(report.Config.HarnessVersion, "s3-eval-v0+sha256:") {
		t.Fatalf("harness version = %q", report.Config.HarnessVersion)
	}
	if report.Config.Provider != "demo" || report.Config.Model != "demo" || report.Config.ProviderEndpoint != "" {
		t.Fatalf("provider config = %#v", report.Config)
	}
	tracePaths, err := filepath.Glob(filepath.Join(outputDir, "traces", "*.jsonl"))
	if err != nil {
		t.Fatalf("glob traces: %v", err)
	}
	if len(tracePaths) != 9 {
		t.Fatalf("trace count = %d, want 9", len(tracePaths))
	}
	if _, err := os.Stat(filepath.Join(outputDir, "report.md")); err != nil {
		t.Fatalf("report.md: %v", err)
	}
}

// TestRunEvalRejectsPromptArgument 验证 eval 子命令拒绝 Catalog 之外的额外 Prompt。
func TestRunEvalRejectsPromptArgument(t *testing.T) {
	err := runEval(context.Background(), []string{"unexpected-prompt"})
	if err == nil || !strings.Contains(err.Error(), "does not accept a prompt") {
		t.Fatalf("runEval error = %v", err)
	}
}

// TestWriteEvalProgress 验证 Trial 进度会形成简洁单行文本，并区分开始、失败和中止。
func TestWriteEvalProgress(t *testing.T) {
	var output strings.Builder
	writeEvalProgress(&output, eval.ProgressEvent{
		Kind:     eval.ProgressTrialStarted,
		Position: 1,
		Total:    30,
		TaskID:   "read_root_readme",
		Trial:    1,
	})
	writeEvalProgress(&output, eval.ProgressEvent{
		Kind:     eval.ProgressTrialCompleted,
		Position: 1,
		Total:    30,
		TaskID:   "read_root_readme",
		Trial:    1,
		Result: eval.TrialResult{
			TaskID:  "read_root_readme",
			Trial:   1,
			Outcome: eval.TrialFailed,
			Error:   "tool result mismatch\nsee report",
			Metrics: eval.Metrics{WallTimeMillis: 1250, Steps: 2, ToolCalls: 1, TotalTokens: 420},
		},
	})
	writeEvalProgress(&output, eval.ProgressEvent{
		Kind:     eval.ProgressTrialAborted,
		Position: 3,
		Total:    30,
		TaskID:   "read_nested_go",
		Trial:    1,
		Error:    "create trace\npermission denied",
	})
	writeEvalProgress(&output, eval.ProgressEvent{
		Kind:     eval.ProgressTrialCompleted,
		Position: 2,
		Total:    30,
		TaskID:   "read_hidden_rule",
		Trial:    1,
		Result: eval.TrialResult{
			TaskID:  "read_hidden_rule",
			Trial:   1,
			Outcome: eval.TrialError,
			Error:   "context deadline exceeded",
			Metrics: eval.Metrics{WallTimeMillis: 30000, Steps: 1},
		},
	})

	text := output.String()
	for _, expected := range []string{
		"[01/30] START task=read_root_readme trial=1\n",
		"[01/30] FAIL task=read_root_readme trial=1 elapsed=1250ms steps=2 tools=1 tokens=420 error=\"tool result mismatch see report\"\n",
		"[02/30] ERROR task=read_hidden_rule trial=1 elapsed=30000ms steps=1 tools=0 tokens=0 error=\"context deadline exceeded\"\n",
		"[03/30] ABORT task=read_nested_go trial=1 error=\"create trace permission denied\"\n",
	} {
		if !strings.Contains(text, expected) {
			t.Fatalf("progress output missing %q:\n%s", expected, text)
		}
	}
}

func writeEvalCatalog(t *testing.T) string {
	t.Helper()

	type demoStep struct {
		Tool      string            `json:"tool"`
		Arguments map[string]string `json:"arguments"`
	}
	type task struct {
		ID                      string            `json:"id"`
		Prompt                  string            `json:"prompt"`
		Files                   map[string]string `json:"files"`
		ExpectedTool            string            `json:"expected_tool"`
		ExpectedArguments       map[string]string `json:"expected_arguments"`
		ExpectedOutputSubstring string            `json:"expected_output_substring"`
		DemoScript              []demoStep        `json:"demo_script"`
		VerifyCommand           []string          `json:"verify_command,omitempty"`
		MaxSteps                int               `json:"max_steps"`
		TimeoutSeconds          int               `json:"timeout_seconds"`
	}
	tasks := make([]task, 8)
	for index := range tasks {
		path := fmt.Sprintf("fixture-%d.txt", index)
		content := fmt.Sprintf("eval-fixture-%d", index)
		tasks[index] = task{
			ID:                      fmt.Sprintf("cli_task_%d", index),
			Prompt:                  "请调用 read_file 读取 " + path,
			Files:                   map[string]string{path: content},
			ExpectedTool:            "read_file",
			ExpectedArguments:       map[string]string{"path": path},
			ExpectedOutputSubstring: content,
			DemoScript:              []demoStep{{Tool: "read_file", Arguments: map[string]string{"path": path}}},
			MaxSteps:                4,
			TimeoutSeconds:          30,
		}
	}
	// 追加一个使用 verify_command 的写任务，端到端覆盖环境结果验证路径。
	goBinary := "go"
	if runtime.GOOS == "windows" {
		goBinary = "go.exe"
	}
	tasks = append(tasks, task{
		ID:             "cli_verify_task",
		Prompt:         "请读取 verify.txt 并确认内容",
		Files:          map[string]string{"verify.txt": "verify-fixture"},
		DemoScript:     []demoStep{{Tool: "read_file", Arguments: map[string]string{"path": "verify.txt"}}},
		VerifyCommand:  []string{filepath.Join(runtime.GOROOT(), "bin", goBinary), "version"},
		MaxSteps:       4,
		TimeoutSeconds: 60,
	})
	data, err := json.Marshal(struct {
		SchemaVersion int    `json:"schema_version"`
		Tasks         []task `json:"tasks"`
	}{SchemaVersion: 3, Tasks: tasks})
	if err != nil {
		t.Fatalf("encode catalog: %v", err)
	}
	path := filepath.Join(t.TempDir(), "tasks.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write catalog: %v", err)
	}
	return path
}
