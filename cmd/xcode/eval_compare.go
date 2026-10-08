package main

// 本文件实现 eval 子命令的三个动词：baseline（固化/列出）、compare（对比）、regress（失败转任务草稿）。

import (
	"bufio"         // 逐行读取 Trace。
	"encoding/json" // 读取 Trace 与打印任务草稿。
	"flag"          // 动词自己的参数解析。
	"fmt"           // 输出与错误。
	"os"            // 复制基线报告与读取 Trace。
	"path/filepath" // 组装 .eval 与基线路径。
	"strings"       // 清理标签字符串。

	"xcode/internal/eval"
)

// runEvalBaseline 实现 baseline 动词：save 固化命名基线，list 列出现有基线。
func runEvalBaseline(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: xcode eval baseline save <label> --run <dir> | list")
	}
	switch args[0] {
	case "list":
		entries, err := os.ReadDir(".eval/baselines")
		if err != nil || len(entries) == 0 {
			fmt.Println("no baselines recorded")
			return nil
		}
		for _, entry := range entries {
			fmt.Println(entry.Name())
		}
		return nil
	case "save":
		// 标签是位置参数且必须位于最前；Go flag 遇非旗标即停止解析，因此先手动取标签。
		rest := args[1:]
		if len(rest) == 0 || strings.HasPrefix(rest[0], "-") {
			return fmt.Errorf("usage: xcode eval baseline save <label> --run <dir>")
		}
		label := strings.TrimSpace(rest[0])
		flags := flag.NewFlagSet("xcode eval baseline save", flag.ContinueOnError)
		runDir := flags.String("run", "", "run directory containing report.json")
		evalRoot := flags.String("eval-root", ".eval", "eval output root")
		if err := flags.Parse(rest[1:]); err != nil {
			return err
		}
		if label == "" || *runDir == "" {
			return fmt.Errorf("baseline save requires a label and --run <dir>")
		}
		report, err := eval.LoadReport(filepath.Join(*runDir, "report.json"))
		if err != nil {
			return err
		}
		target := filepath.Join(*evalRoot, "baselines", label)
		if err := os.MkdirAll(target, 0o700); err != nil {
			return err
		}
		encoded, err := json.MarshalIndent(report, "", "  ")
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(target, "report.json"), append(encoded, '\n'), 0o600); err != nil {
			return err
		}
		fmt.Printf("baseline %q saved from run %s\n", label, report.RunID)
		return nil
	default:
		return fmt.Errorf("unknown baseline verb %q: choose save or list", args[0])
	}
}

// runEvalCompare 实现 compare 动词：对比基线与当前运行，回归时返回错误（非零退出码）。
func runEvalCompare(args []string) error {
	flags := flag.NewFlagSet("xcode eval compare", flag.ContinueOnError)
	baseline := flags.String("baseline", "", "baseline label or directory containing report.json")
	current := flags.String("current", "", "current run directory containing report.json")
	evalRoot := flags.String("eval-root", ".eval", "eval output root")
	if err := flags.Parse(args); err != nil {
		return err
	}
	basePath := resolveReportPath(*evalRoot, *baseline)
	currPath := resolveReportPath(*evalRoot, *current)
	if basePath == "" || currPath == "" {
		return fmt.Errorf("compare requires --baseline <label|dir> and --current <dir>")
	}
	baseReport, err := eval.LoadReport(basePath)
	if err != nil {
		return err
	}
	currReport, err := eval.LoadReport(currPath)
	if err != nil {
		return err
	}
	comparison := eval.CompareReports(baseReport, currReport)
	fmt.Print(eval.WriteComparisonMarkdown(comparison))
	if comparison.Regressed > 0 {
		return fmt.Errorf("comparison found %d regressed task(s)", comparison.Regressed)
	}
	return nil
}

// resolveReportPath 把 label 或目录解析成 report.json 路径。
func resolveReportPath(evalRoot, value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if filepath.Base(value) == "report.json" {
		return value
	}
	if _, err := os.Stat(filepath.Join(value, "report.json")); err == nil {
		return filepath.Join(value, "report.json")
	}
	return eval.BaselinePath(evalRoot, value)
}

// runEvalRegress 实现 regress 动词：从失败 Trace 提取任务草稿打印给人工修剪。
func runEvalRegress(args []string) error {
	flags := flag.NewFlagSet("xcode eval regress", flag.ContinueOnError)
	trace := flags.String("trace", "", "failed trial trace jsonl path")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *trace == "" {
		return fmt.Errorf("regress requires --trace <file>")
	}
	file, err := os.Open(*trace)
	if err != nil {
		return err
	}
	defer file.Close()

	taskID, prompt, failure := "", "", ""
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		var record struct {
			TaskID string `json:"task_id"`
			Event  struct {
				Kind  string `json:"kind"`
				Data  string `json:"data"`
				Error string `json:"error"`
			} `json:"event"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &record); err != nil {
			continue
		}
		if record.TaskID != "" && taskID == "" {
			taskID = record.TaskID
		}
		switch record.Event.Kind {
		case "run.started":
			prompt = record.Event.Data
		case "run.failed":
			failure = record.Event.Error
		case "tool.completed":
			// 评分失败（如 verify-failed）不产生 run.failed；工具层错误观察是兜底信号。
			if failure == "" && strings.HasPrefix(record.Event.Data, "ERROR: ") {
				failure = record.Event.Data
			}
		}
	}
	if taskID == "" {
		return fmt.Errorf("trace contains no task id")
	}
	if failure == "" {
		failure = "trial failed without run.failed event; inspect trace manually"
	}

	stub := map[string]any{
		"id":                        "regress-" + taskID,
		"prompt":                    prompt,
		"files":                     map[string]string{"TODO.md": "人工补充最小 fixture"},
		"verify_command":            nil,
		"expected_tool":             "",
		"expected_arguments":        nil,
		"expected_output_substring": "",
		"demo_script":               nil,
		"regression_note":           failure,
		"max_steps":                 8,
		"timeout_seconds":           60,
	}
	encoded, err := json.MarshalIndent(stub, "", "  ")
	if err != nil {
		return err
	}
	fmt.Println("# 回归任务草稿（人工修剪后加入 testdata/eval/tasks.json）：")
	fmt.Println(string(encoded))
	return nil
}
