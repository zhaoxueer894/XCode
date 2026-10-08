package main

// 分歧案例提取：同一组证据单元，规则 / qwen 聊天 / Jev 三个分类器逐单元判定，
// 输出全部对照行并标记分歧——供"31% 分歧谁对"的逐案分析使用。
// 门控：XCODE_CLASSIFIER_BENCH=1 才运行（真实 API 调用各一次，成本约零）。

import (
	"context" // 分类调用。
	"os"      // 门控开关与 Key。
	"strings" // 内容截断。
	"testing" // Go 标准测试框架。

	"xcode/internal/contextmgr"    // RuleDecider 与决策类型。
	"xcode/internal/model/bailian" // qwen 聊天分类器。
	"xcode/internal/model/jev"     // Jev systemone 客户端。
)

// TestClassifierDisagreementSamples 逐单元对照三类分类器的判定并标记分歧。
func TestClassifierDisagreementSamples(t *testing.T) {
	if os.Getenv("XCODE_CLASSIFIER_BENCH") != "1" {
		t.Skip("set XCODE_CLASSIFIER_BENCH=1 to run the per-unit disagreement extraction (real API calls)")
	}
	loadBenchEnv(t)
	units := benchUnits()

	ruleDecisions := contextmgr.RuleDecider{}.Decide(units)

	jevClient, err := jev.New(os.Getenv("TYPESAFE_API_KEY"), os.Getenv("TYPESAFE_ENDPOINT"), os.Getenv("TYPESAFE_MODEL"))
	if err != nil {
		t.Fatal(err)
	}
	jevDecisions, err := buildJevSystemOneClassify(jevClient)(context.Background(), units)
	if err != nil {
		t.Fatalf("jev classify: %v", err)
	}
	qwenModel, err := bailian.New(bailian.Config{
		APIKey:  os.Getenv("DASHSCOPE_API_KEY"),
		BaseURL: os.Getenv("DASHSCOPE_BASE_URL"),
		Model:   "qwen-plus",
	})
	if err != nil {
		t.Fatal(err)
	}
	qwenDecisions, err := buildTypedClassify(qwenModel)(context.Background(), units)
	if err != nil {
		t.Fatalf("qwen classify: %v", err)
	}

	t.Logf("%-3s %-52s %-9s %-9s %-9s", "unit", "内容(截断)", "规则", "qwen", "jev")
	for index, unit := range units {
		head := unitContentHead(unit)
		rule := decisionAt(ruleDecisions, index)
		qwen := decisionAt(qwenDecisions, index)
		jevDecision := decisionAt(jevDecisions, index)
		marker := ""
		if qwen != rule || jevDecision != rule {
			marker = "  <-- 分歧"
		}
		t.Logf("%-3d %-52s %-9s %-9s %-9s%s", index, head, rule, qwen, jevDecision, marker)
	}
}

// decisionAt 取指定单元的判定；越界返回问号（分类器输出不齐时可见）。
func decisionAt(decisions []contextmgr.UnitDecision, index int) string {
	for _, decision := range decisions {
		if decision.Index == index {
			return string(decision.Decision)
		}
	}
	return "?"
}

// unitContentHead 拼接单元内容并截断为单行摘要。
func unitContentHead(unit contextmgr.Unit) string {
	var builder strings.Builder
	for _, message := range unit.Messages {
		builder.WriteString(message.Role + ":")
		builder.WriteString(message.Content)
		builder.WriteString(" | ")
	}
	text := builder.String()
	runes := []rune(text)
	if len(runes) > 50 {
		return string(runes[:49]) + "…"
	}
	return strings.TrimSuffix(text, " | ")
}
