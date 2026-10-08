package main

// 类型化分类器延迟基准：同一组真实形状的证据单元，
// 对 Jev（systemone）、聊天 LLM（qwen）、规则决策器三种分类器测单次判定耗时。
// 门控：XCODE_CLASSIFIER_BENCH=1 才运行——会发起真实 API 调用（Jev 与百炼各数次），
// 常规测试默认跳过、零成本。规则决策器为纯内存计算，用大批量迭代折算单次耗时。

import (
	"context"       // 分类调用取消信号。
	"encoding/json" // 构造证据单元参数。
	"os"            // 读取门控开关与 .env 密钥。
	"strconv"       // 延迟数字格式化。
	"strings"       // .env 解析。
	"testing"       // Go 标准测试框架。
	"time"          // 延迟测量。

	"xcode/internal/contextmgr"    // RuleDecider 与决策类型。
	"xcode/internal/harness"       // 消息与模型协议。
	"xcode/internal/model/bailian" // qwen 聊天分类器。
	"xcode/internal/model/jev"     // Jev systemone 客户端。
)

// benchUnits 构造一组真实形状的证据单元（对应一次典型压缩：目标 + 多个只读对 + 错误 + 验证 + 散文）。
func benchUnits() []contextmgr.Unit {
	pair := func(id, tool, args, output string) []harness.Message {
		return []harness.Message{
			{Role: "assistant", ToolCalls: []harness.ToolCall{{ID: id, Name: tool, Arguments: json.RawMessage(args)}}},
			{Role: "tool", Content: output, ToolCallID: id},
		}
	}
	units := []contextmgr.Unit{{Messages: []harness.Message{{Role: "user", Content: "修复 calc.go 里 Add 函数的 bug 并用 go test 验证"}}}}
	for i := 0; i < 5; i++ {
		units = append(units, contextmgr.Unit{Messages: pair(
			string(rune('a'+i)), "read_file", `{"path":"calc.go"}`,
			"[resource=calc.go artifact_id=sha256:abc"+strconv.Itoa(i)+"] path=calc.go lines=1-11\npackage main ...")})
	}
	units = append(units, contextmgr.Unit{Messages: pair(
		"err1", "apply_patch", `{"path":"calc.go"}`, "ERROR: old_string not found in \"calc.go\"")})
	units = append(units, contextmgr.Unit{Messages: pair(
		"ver1", "exec", `{"command":["go","test","./..."]}`,
		"command=go test exit_code=0 duration_ms=1200 ok 0B→0B")})
	units = append(units, contextmgr.Unit{
		Messages: []harness.Message{{Role: "assistant", Content: "我分析了代码，Add 函数的实现有问题，需要把减号换成加号，然后运行测试确认。"}}})
	return units
}

// loadBenchEnv 把项目根 .env 的 KEY=VALUE 注入进程环境（测试进程不会经过 main 的 loadDotEnv）。
func loadBenchEnv(t *testing.T) {
	t.Helper()
	// go test 的 CWD 是包目录（cmd/xcode），.env 在仓库根：逐级向上查找。
	raw, err := os.ReadFile("../../.env")
	if err != nil {
		t.Skip("no .env file; skipping classifier latency benchmark")
	}
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if name, value, ok := strings.Cut(line, "="); ok {
			name = strings.TrimSpace(name)
			value = strings.Trim(strings.TrimSpace(value), "\"'")
			if os.Getenv(name) == "" {
				os.Setenv(name, value)
			}
		}
	}
}

// sampleClassifier 对同一组单元重复 samples 次分类，返回每次调用的耗时（毫秒）。
func sampleClassifier(t *testing.T, name string, samples int, classify func() error) []float64 {
	t.Helper()
	var latencies []float64
	for i := 0; i < samples; i++ {
		started := time.Now()
		if err := classify(); err != nil {
			t.Fatalf("%s classify failed on sample %d: %v", name, i+1, err)
		}
		latencies = append(latencies, float64(time.Since(started).Microseconds())/1000.0)
	}
	return latencies
}

// summarize 输出均值/最小/最大延迟。
func summarize(t *testing.T, name string, latencies []float64, decisionsPerCall int) {
	total := 0.0
	min, max := latencies[0], latencies[0]
	for _, v := range latencies {
		total += v
		if v < min {
			min = v
		}
		if v > max {
			max = v
		}
	}
	mean := total / float64(len(latencies))
	perDecision := mean / float64(decisionsPerCall)
	t.Logf("%-28s calls=%d mean=%.1fms min=%.1fms max=%.1fms | 每次判定约 %.3fms（%d 单元/次）",
		name, len(latencies), mean, min, max, perDecision, decisionsPerCall)
}

// TestClassifierLatencyBenchmark 三分类器延迟对照；XCODE_CLASSIFIER_BENCH=1 才运行。
func TestClassifierLatencyBenchmark(t *testing.T) {
	if os.Getenv("XCODE_CLASSIFIER_BENCH") != "1" {
		t.Skip("set XCODE_CLASSIFIER_BENCH=1 to run the real-call latency benchmark (makes real API calls)")
	}
	loadBenchEnv(t)
	units := benchUnits()
	const samples = 5

	// 1) 规则决策器：纯内存，测 10000 次取平均（微秒级）。
	started := time.Now()
	for i := 0; i < 10000; i++ {
		contextmgr.RuleDecider{}.Decide(units)
	}
	ruleMeanMs := float64(time.Since(started).Microseconds()) / 1000.0 / 10000.0
	t.Logf("%-28s calls=10000 mean=%.6fms（纯内存，无网络）", "RuleDecider (deterministic)", ruleMeanMs)

	// 2) Jev systemone：一次批量调用判定全部单元。
	apiKey := os.Getenv("TYPESAFE_API_KEY")
	if apiKey == "" {
		t.Fatal("XCODE_CLASSIFIER_BENCH=1 requires TYPESAFE_API_KEY for the jev backend")
	}
	jevClient, err := jev.New(apiKey, os.Getenv("TYPESAFE_ENDPOINT"), os.Getenv("TYPESAFE_MODEL"))
	if err != nil {
		t.Fatal(err)
	}
	jevLatencies := sampleClassifier(t, "Jev (systemone)", samples, func() error {
		state := make(map[string]string, len(units))
		questions := make(map[string]jev.Question, len(units))
		for index, unit := range units {
			var body strings.Builder
			for _, message := range unit.Messages {
				body.WriteString("(" + message.Role + ") " + message.Content + "\n")
			}
			id := "unit-" + strconv.Itoa(index)
			state[id] = body.String()
			questions[id] = jev.Question{
				Type:         jev.QuestionChoice,
				Instructions: "Context compaction decision for this evidence unit.",
				Criteria: map[string]string{
					"keep":     "goal, unresolved error, verification evidence, recent",
					"truncate": "old prose or write-tool pair",
					"drop":     "read-only result externalized in artifacts",
				},
			}
		}
		_, err := jevClient.SystemOne(context.Background(), state, questions)
		return err
	})
	summarize(t, "Jev (systemone, batched)", jevLatencies, len(units))

	// 3) qwen 聊天分类器：Jev 诞生前的常用方案，一次 chat 调用判定全部单元。
	classifierModel, err := bailian.New(bailian.Config{
		APIKey:  os.Getenv("DASHSCOPE_API_KEY"),
		BaseURL: os.Getenv("DASHSCOPE_BASE_URL"),
		Model:   "qwen-plus",
	})
	if err != nil {
		t.Fatal(err)
	}
	classify := buildTypedClassify(classifierModel)
	qwenLatencies := sampleClassifier(t, "qwen chat (pre-Jev baseline)", samples, func() error {
		_, err := classify(context.Background(), units)
		return err
	})
	summarize(t, "qwen chat (pre-Jev baseline)", qwenLatencies, len(units))
}
