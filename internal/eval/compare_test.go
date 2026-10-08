package eval

// 本文件验证新机制：标准差计算、失败聚类、对比判定与报告加载往返。

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// TestStdDevValues 验证标准差的口径：单样本为 0、两样本为半差、恒定序列为 0。
func TestStdDevValues(t *testing.T) {
	// 通过汇总函数间接覆盖：构造三个同任务 Trial，检查 Task 级标准差。
	results := []TrialResult{
		{TaskID: "t", Trial: 1, Outcome: TrialPassed, Metrics: Metrics{Steps: 2, ToolCalls: 1, TotalTokens: 100}},
		{TaskID: "t", Trial: 2, Outcome: TrialPassed, Metrics: Metrics{Steps: 4, ToolCalls: 2, TotalTokens: 200}},
		{TaskID: "t", Trial: 3, Outcome: TrialPassed, Metrics: Metrics{Steps: 6, ToolCalls: 3, TotalTokens: 300}},
	}
	summaries := summarizeTasks(results)
	if len(summaries) != 1 {
		t.Fatalf("summaries = %d, want 1", len(summaries))
	}
	summary := summaries[0]
	if summary.AverageSteps != 4 || summary.StdDevSteps < 1.6329 || summary.StdDevSteps > 1.6330 {
		t.Fatalf("steps avg=%v stddev=%v, want 4 / 1.6329", summary.AverageSteps, summary.StdDevSteps)
	}
	if summary.StdDevTokens < 81.6496 || summary.StdDevTokens > 81.6498 {
		t.Fatalf("token stddev = %v, want 81.6497", summary.StdDevTokens)
	}
	// 恒定序列标准差为 0。
	constant := []TrialResult{
		{TaskID: "c", Trial: 1, Outcome: TrialPassed, Metrics: Metrics{Steps: 3}},
		{TaskID: "c", Trial: 2, Outcome: TrialPassed, Metrics: Metrics{Steps: 3}},
	}
	constantSummary := summarizeTasks(constant)[0]
	if constantSummary.StdDevSteps != 0 {
		t.Fatalf("constant stddev = %v, want 0", constantSummary.StdDevSteps)
	}
}

// TestClusterFailuresGroupsBySignature 验证聚类按签名归组并按数量降序。
func TestClusterFailuresGroupsBySignature(t *testing.T) {
	results := []TrialResult{
		{TaskID: "a", Trial: 1, Outcome: TrialFailed, Error: "verify command failed: exit status 1\nFAIL TestX"},
		{TaskID: "b", Trial: 1, Outcome: TrialFailed, Error: "verify command failed: exit status 1"},
		{TaskID: "c", Trial: 1, Outcome: TrialFailed, Error: "policy denied exec in plan mode"},
		{TaskID: "d", Trial: 1, Outcome: TrialError, Error: "connection refused"},
		{TaskID: "e", Trial: 1, Outcome: TrialPassed},
	}
	clusters := ClusterFailures(results)
	if len(clusters) != 3 {
		t.Fatalf("clusters = %#v", clusters)
	}
	if clusters[0].Name != "verify-failed" || clusters[0].Count != 2 {
		t.Fatalf("top cluster = %#v", clusters[0])
	}
	if len(clusters[0].TrialRefs) != 2 || clusters[0].TrialRefs[0] != "a#1" {
		t.Fatalf("trial refs = %#v", clusters[0].TrialRefs)
	}
}

// TestCompareReportsVerdicts 验证五种判定的核心路径。
func TestCompareReportsVerdicts(t *testing.T) {
	base := Report{
		RunID: "base",
		TaskSummaries: []TaskSummary{
			{TaskID: "same", EvaluatedTrials: 3, SuccessRate: 1, AverageSteps: 4, TotalTokens: 1000, AverageContextTokens: 6000},
			{TaskID: "worse", EvaluatedTrials: 3, SuccessRate: 1, AverageSteps: 4, TotalTokens: 1000},
			{TaskID: "gone", EvaluatedTrials: 3, SuccessRate: 1},
		},
	}
	current := Report{
		RunID: "curr",
		TaskSummaries: []TaskSummary{
			{TaskID: "same", EvaluatedTrials: 3, SuccessRate: 1, AverageSteps: 4, TotalTokens: 1000, AverageContextTokens: 6000},
			{TaskID: "worse", EvaluatedTrials: 3, SuccessRate: 0.5, AverageSteps: 4, TotalTokens: 1000},
			{TaskID: "fresh", EvaluatedTrials: 3, SuccessRate: 1},
		},
	}
	comparison := CompareReports(base, current)
	verdicts := make(map[string]Verdict)
	for _, task := range comparison.Tasks {
		verdicts[task.TaskID] = task.Verdict
	}
	if verdicts["same"] != VerdictUnchanged {
		t.Fatalf("same = %v", verdicts["same"])
	}
	if verdicts["worse"] != VerdictRegressed {
		t.Fatalf("worse = %v", verdicts["worse"])
	}
	if verdicts["fresh"] != VerdictNew {
		t.Fatalf("fresh = %v", verdicts["fresh"])
	}
	if verdicts["gone"] != VerdictMissing {
		t.Fatalf("gone = %v", verdicts["gone"])
	}
	if comparison.Regressed != 2 || comparison.Improved != 0 {
		t.Fatalf("regressed=%d improved=%d", comparison.Regressed, comparison.Improved)
	}

	// 上下文峰值下降超阈值判 improved。
	ctxBase := Report{TaskSummaries: []TaskSummary{{TaskID: "x", EvaluatedTrials: 1, SuccessRate: 1, AverageContextTokens: 6338}}}
	ctxCurr := Report{TaskSummaries: []TaskSummary{{TaskID: "x", EvaluatedTrials: 1, SuccessRate: 1, AverageContextTokens: 5256}}}
	ctxComparison := CompareReports(ctxBase, ctxCurr)
	if ctxComparison.Tasks[0].Verdict != VerdictImproved || ctxComparison.Improved != 1 {
		t.Fatalf("ctx comparison = %#v", ctxComparison.Tasks[0])
	}
}

// TestLoadReportRoundTrip 验证报告写盘后可原样读回。
func TestLoadReportRoundTrip(t *testing.T) {
	report := Report{SchemaVersion: 6, RunID: "round-trip", Label: "s9"}
	path := filepath.Join(t.TempDir(), "report.json")
	encoded, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadReport(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.RunID != "round-trip" || loaded.Label != "s9" || loaded.SchemaVersion != 6 {
		t.Fatalf("loaded = %#v", loaded)
	}
}
