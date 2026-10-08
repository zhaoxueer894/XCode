package jev_test

// 真实连通性冒烟：验证端点可达、鉴权有效与报文形状假设成立。
// 默认跳过——只有显式设置 XCODE_JEV_SMOKE=1 才运行，保证常规测试零配额消耗。
// 代价：单次调用，state 只含两行文本（无代码内容），成本接近于零。

import (
	"context" // 冒烟调用的取消信号。
	"os"      // 读取跳过开关与 Key。
	"testing" // Go 标准测试框架。

	"xcode/internal/model/jev" // 被测客户端。
)

func TestJevConnectivitySmoke(t *testing.T) {
	if os.Getenv("XCODE_JEV_SMOKE") != "1" {
		t.Skip("set XCODE_JEV_SMOKE=1 to run the one-shot connectivity smoke against the real endpoint")
	}
	apiKey := os.Getenv("TYPESAFE_API_KEY")
	if apiKey == "" {
		t.Fatal("XCODE_JEV_SMOKE=1 requires TYPESAFE_API_KEY")
	}
	client, err := jev.New(apiKey, os.Getenv("TYPESAFE_ENDPOINT"), os.Getenv("TYPESAFE_MODEL"))
	if err != nil {
		t.Fatal(err)
	}
	answers, err := client.SystemOne(context.Background(),
		map[string]string{
			"unit-0": "(user) 修复 calc.go 里 Add 函数的 bug",
			"unit-1": "(assistant) read_file calc.go",
		},
		map[string]jev.Question{
			"unit-0": {Type: jev.QuestionChoice, Instructions: "上下文压缩判定：该证据单元应如何处理？",
				Criteria: map[string]string{"keep": "当前目标或未解决错误", "truncate": "旧散文", "drop": "只读结果"}},
			"unit-1": {Type: jev.QuestionChoice, Instructions: "上下文压缩判定：该证据单元应如何处理？",
				Criteria: map[string]string{"keep": "最近窗口或验证证据", "drop": "只读结果已入 Artifact"}},
		})
	if err != nil {
		t.Fatalf("connectivity smoke failed: %v", err)
	}
	for id, answer := range answers {
		t.Logf("answer %s: choice=%s confidence=%.2f model=%s", id, answer.Choice, answer.Confidence, answer.Model)
	}
	if len(answers) != 2 {
		t.Fatalf("expected 2 answers, got %d", len(answers))
	}
}
