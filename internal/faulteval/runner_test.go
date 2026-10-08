package faulteval

// 故障评测端到端测试：用真实执行栈在离线脚本上验证三策略对照、
// 重复副作用统计、门禁拦截计数与最小 Trace Replay。

import (
	"context"       // 阶段运行。
	"encoding/json" // 任务目录编码。
	"os"            // 写临时任务目录文件。
	"path/filepath" // 临时目录路径。
	"strings"       // 断言恢复摘要。
	"testing"       // Go 标准测试框架。

	"xcode/internal/artifactstore" // 工件存储。
	"xcode/internal/checkpoint"    // 快照仓库。
	"xcode/internal/contextmgr"    // 投影管理器。
	"xcode/internal/executor"      // 统一执行管线与 journal。
	"xcode/internal/harness"       // Agent、恢复日志与门禁契约。
	"xcode/internal/model"         // 脚本化 Demo。
	"xcode/internal/policy"        // workspace-write 档位。
	"xcode/internal/tools"         // 工具集。
	"xcode/internal/workspace"     // 门禁指纹。
)

// fixtureTask 构造"读文件 → 补丁 → exec go test"的标准故障任务。
func fixtureTask(faultAfter int, gate bool) FaultTask {
	return FaultTask{
		ID:     "test-fault-fix",
		Prompt: "修复 calc.go 里 Add 函数的 bug 并用 go test 验证",
		Files: map[string]string{
			"go.mod":       "module buggyapp\n\ngo 1.26\n",
			"calc.go":      "package main\n\nimport \"fmt\"\n\nfunc Add(a int, b int) int {\n\treturn a - b\n}\n\nfunc main() {\n\tfmt.Println(Add(2, 3))\n}\n",
			"calc_test.go": "package main\n\nimport \"testing\"\n\nfunc TestAdd(t *testing.T) {\n\tif Add(2, 3) != 5 {\n\t\tt.Fatalf(\"Add(2,3) = %d, want 5\", Add(2, 3))\n\t}\n}\n",
		},
		MaxSteps:        8,
		TimeoutSeconds:  90,
		VerifyCommand:   []string{"go", "test", "./..."},
		FaultAfterTools: faultAfter,
		DemoScript: []FaultStep{
			{Tool: "read_file", Arguments: []byte(`{"path":"calc.go"}`)},
			{Tool: "apply_patch", Arguments: []byte(`{"path":"calc.go","operation":"update","old_string":"return a - b","new_string":"return a + b"}`)},
			{Tool: "exec", Arguments: []byte(`{"command":["go","test","./..."]}`)},
		},
		Gate: gate,
	}
}

// fixtureCatalog 用任务列表构造目录并填好测试所需的内部哈希字段。
func fixtureCatalog(t *testing.T, tasks ...FaultTask) FaultCatalog {
	t.Helper()
	catalog := FaultCatalog{SchemaVersion: faultCatalogSchema, Tasks: tasks}
	catalog.loadedHash = "test-catalog"
	resolved, err := hashFaultCatalog(catalog)
	if err != nil {
		t.Fatal(err)
	}
	catalog.resolvedHash = resolved
	return catalog
}

// fixtureConfig 构造与目录哈希匹配的运行配置。
func fixtureConfig(catalog FaultCatalog, budget int) FaultConfig {
	return FaultConfig{
		HarnessVersion: "test",
		Provider:       "demo",
		Model:          "demo",
		Trials:         1,
		ContextBudget:  budget,
		CatalogHash:    catalog.loadedHash,
	}
}

// testStackBuilder 返回测试用栈装配回调：真实执行栈，按策略挂恢复日志与门禁。
func testStackBuilder(t *testing.T, artifacts *artifactstore.FileStore, budget int) StackFunc {
	t.Helper()
	return func(ctx context.Context, ws string, task FaultTask, sink harness.EventSink,
		strategy Strategy, skipSteps int) (*Stack, error) {
		contextStrategy := contextmgr.StrategyCompaction
		if strategy == StrategyDecisionGated {
			contextStrategy = contextmgr.StrategyDecisionGated
		}
		epoch := contextmgr.NewEpoch()
		readOnlyTools, err := tools.NewReadOnlySetWithEpoch(ctx, ws, artifacts, epoch.Current)
		if err != nil {
			return nil, err
		}
		manager, err := contextmgr.NewManager(contextStrategy, budget, epoch)
		if err != nil {
			return nil, err
		}
		registry, err := harness.NewRegistry(append(readOnlyTools.Tools(), readOnlyTools.WriteTools()...)...)
		if err != nil {
			return nil, err
		}
		engine, err := policy.NewEngine(policy.ModeWorkspaceWrite)
		if err != nil {
			return nil, err
		}
		checkpoints, err := checkpoint.NewStore(filepath.Join(ws, ".xcode-state", "checkpoints"), ws)
		if err != nil {
			return nil, err
		}
		secure, err := executor.New(registry, engine, nil, checkpoints, sink)
		if err != nil {
			return nil, err
		}
		journalDir := filepath.Join(ws, ".xcode-state", "journal")
		journal, err := executor.OpenJournal(filepath.Join(journalDir, "run-phase.jsonl"))
		if err != nil {
			return nil, err
		}
		secure.SetJournal(journal)
		demo := model.NewScriptedDemo(testScriptSteps(task.DemoScript))
		demo.Advance(skipSteps)
		agent := harness.NewAgent(demo, secure, task.MaxSteps, sink)
		agent.SetContextManager(manager)

		stack := &Stack{
			Agent:       agent,
			Store:       checkpoints,
			TurnLogPath: filepath.Join(ws, ".xcode-state", "recovery", "turn-latest.jsonl"),
			JournalDir:  journalDir,
		}
		if strategy != StrategyReference {
			turnLog, logErr := harness.NewTurnLog(stack.TurnLogPath)
			if logErr != nil {
				return nil, logErr
			}
			agent.SetTurnLog(turnLog)
		}
		if task.Gate && len(task.VerifyCommand) > 0 {
			stack.Contract = &harness.TaskContract{
				Goal:           task.Prompt,
				VerifyCommands: []harness.VerifyCommand{{Name: "verify", Command: task.VerifyCommand}},
			}
			stack.Fingerprint = func() (string, error) { return workspace.Fingerprint(ws) }
		}
		return stack, nil
	}
}

// testScriptSteps 转换脚本步骤为脚本化模型输入。
func testScriptSteps(steps []FaultStep) []model.ScriptStep {
	converted := make([]model.ScriptStep, 0, len(steps))
	for _, step := range steps {
		converted = append(converted, model.ScriptStep{Tool: step.Tool, Arguments: string(step.Arguments)})
	}
	return converted
}

// runOnce 用单任务目录跑一次三策略评测，返回报告。
func runOnce(t *testing.T, catalog FaultCatalog, budget int) FaultReport {
	t.Helper()
	outputDir := t.TempDir()
	artifacts, err := artifactstore.NewFileStore(filepath.Join(outputDir, "artifacts"))
	if err != nil {
		t.Fatal(err)
	}
	runner, err := NewStackRunner(outputDir, "test-run", fixtureConfig(catalog, budget), testStackBuilder(t, artifacts, budget))
	if err != nil {
		t.Fatal(err)
	}
	report, err := runner.Run(context.Background(), catalog)
	if err != nil {
		t.Fatal(err)
	}
	return report
}

// findResult 按策略定位结果。
func findResult(t *testing.T, report FaultReport, strategy Strategy) TrialResult {
	t.Helper()
	for _, result := range report.Results {
		if result.Strategy == strategy {
			return result
		}
	}
	t.Fatalf("no result for strategy %s", strategy)
	return TrialResult{}
}

// TestFaultStrategiesEndToEnd 验证三策略对照：durable 无重复副作用，reference 重跑产生重复。
func TestFaultStrategiesEndToEnd(t *testing.T) {
	report := runOnce(t, fixtureCatalog(t, fixtureTask(2, false)), 1500)

	durable := findResult(t, report, StrategyDurable)
	if durable.Outcome != "passed" {
		t.Fatalf("durable outcome = %s (%s), want passed", durable.Outcome, durable.Error)
	}
	if !durable.Metrics.Recovered {
		t.Fatal("durable must report recovered")
	}
	if durable.Metrics.DuplicateSideEffects != 0 {
		t.Fatalf("durable must not duplicate side effects, got %d", durable.Metrics.DuplicateSideEffects)
	}
	if !strings.Contains(durable.RecoveryNote, "pending=0") {
		t.Fatalf("recovery note = %q", durable.RecoveryNote)
	}

	reference := findResult(t, report, StrategyReference)
	if reference.Outcome != "passed" {
		t.Fatalf("reference outcome = %s (%s), want passed", reference.Outcome, reference.Error)
	}
	if !reference.Metrics.RestartedFromScratch {
		t.Fatal("reference must report restart")
	}
	if reference.Metrics.DuplicateSideEffects != 1 {
		t.Fatalf("reference replayed patch should count 1 duplicate, got %d", reference.Metrics.DuplicateSideEffects)
	}

	gated := findResult(t, report, StrategyDecisionGated)
	if gated.Outcome != "passed" || !gated.Metrics.Recovered {
		t.Fatalf("decision-gated outcome = %s, want passed+recovered", gated.Outcome)
	}
	// 三策略的汇总必须齐备。
	if len(report.Strategies) != 3 {
		t.Fatalf("strategy summaries = %d, want 3", len(report.Strategies))
	}
}

// TestFaultGateBlocksCounted 验证门禁拦截被计数并写入提前完成率指标。
func TestFaultGateBlocksCounted(t *testing.T) {
	gateTask := fixtureTask(0, true)
	gateTask.MaxSteps = 4
	report := runOnce(t, fixtureCatalog(t, gateTask), 1500)

	result := findResult(t, report, StrategyDurable)
	if result.Metrics.GateBlocks == 0 {
		t.Fatal("gate task must record at least one gate block")
	}
	if result.Outcome != "failed" {
		t.Fatalf("broken-test task must fail verification, got %s", result.Outcome)
	}
	summary := report.Strategies[0]
	if summary.PrematureRate <= 0 {
		t.Fatal("premature rate must be positive when gate blocked")
	}
}

// TestFaultReplayRerunsSameTaskAndStrategy 验证最小 Trace Replay：同任务同策略的全新重跑。
func TestFaultReplayRerunsSameTaskAndStrategy(t *testing.T) {
	catalog := fixtureCatalog(t, fixtureTask(2, false))
	outputDir := t.TempDir()
	artifacts, err := artifactstore.NewFileStore(filepath.Join(outputDir, "artifacts"))
	if err != nil {
		t.Fatal(err)
	}
	runner, err := NewStackRunner(outputDir, "test-run", fixtureConfig(catalog, 1500), testStackBuilder(t, artifacts, 1500))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runner.Run(context.Background(), catalog); err != nil {
		t.Fatal(err)
	}

	replayOut := t.TempDir()
	result, err := runner.Replay(context.Background(), catalog, ReplayRequest{
		TracePath:   filepath.Join(outputDir, "traces", "test-fault-fix-durable-trial-01.jsonl"),
		CatalogPath: "in-memory",
		OutputDir:   replayOut,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.SourceTaskID != "test-fault-fix" || result.TaskID != "test-fault-fix" {
		t.Fatalf("replay task mismatch: %s -> %s", result.SourceTaskID, result.TaskID)
	}
	if result.Strategy != StrategyDurable {
		t.Fatalf("replay strategy = %s, want durable", result.Strategy)
	}
	if result.Result.Outcome != "passed" || !result.Result.Metrics.Recovered {
		t.Fatalf("replay outcome = %s, want passed+recovered", result.Result.Outcome)
	}
	entries, err := os.ReadDir(filepath.Join(replayOut, "traces"))
	if err != nil || len(entries) != 1 {
		t.Fatalf("replay must produce one fresh trace, found %d (%v)", len(entries), err)
	}
}

// TestLoadFaultCatalogValidation 覆盖目录加载校验。
func TestLoadFaultCatalogValidation(t *testing.T) {
	validTask := fixtureTask(1, false)
	dir := t.TempDir()

	writeCatalog := func(name string, tasks []FaultTask) string {
		t.Helper()
		catalog := FaultCatalog{SchemaVersion: faultCatalogSchema, Tasks: tasks}
		encoded, err := json.Marshal(catalog)
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, encoded, 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}

	sixTasks := make([]FaultTask, 6)
	for i := range sixTasks {
		task := validTask
		task.ID = validTask.ID + "-" + string(rune('a'+i))
		sixTasks[i] = task
	}
	if _, _, err := LoadFaultCatalog(writeCatalog("valid.json", sixTasks)); err != nil {
		t.Fatalf("valid catalog rejected: %v", err)
	}
	if _, _, err := LoadFaultCatalog(writeCatalog("too-few.json", sixTasks[:5])); err == nil {
		t.Fatal("catalog with 5 tasks must be rejected")
	}
	badFault := validTask
	badFault.FaultAfterTools = 99
	if _, _, err := LoadFaultCatalog(writeCatalog("bad-fault.json", append(sixTasks[:5], badFault))); err == nil {
		t.Fatal("fault_after_tools beyond script length must be rejected")
	}
	if _, _, err := LoadFaultCatalog(filepath.Join(dir, "absent.json")); err == nil {
		t.Fatal("missing catalog must be rejected")
	}
}
