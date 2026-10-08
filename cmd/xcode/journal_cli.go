package main

// 本文件实现 xcode journal 子命令：查看状态迁移并标出崩溃窗口中的 unknown 调用。

import (
	"flag"          // 子命令参数解析。
	"fmt"           // 输出条目与 unknown 汇总。
	"path/filepath" // 定位 journal 目录。

	"xcode/internal/executor"
)

// runJournal 实现 journal 子命令：列出全部运行的迁移记录，最后汇总 unknown。
func runJournal(args []string) error {
	flags := flag.NewFlagSet("xcode journal", flag.ContinueOnError)
	workspaceFlag := flags.String("workspace", ".", "workspace root")
	if err := flags.Parse(args); err != nil {
		return err
	}
	directory := filepath.Join(*workspaceFlag, ".xcode-state", "journal")
	files, err := executor.ListJournalFiles(directory)
	if err != nil {
		return err
	}
	if len(files) == 0 {
		fmt.Println("no journal recorded")
		return nil
	}
	var all []executor.JournalEntry
	for _, path := range files {
		entries, err := executor.ReadJournalFile(path)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			fmt.Printf("%s  %s  %s  %s  %s\n",
				filepath.Base(path), entry.Time.Format("15:04:05"), entry.Tool, entry.ToolCallID, entry.State)
		}
		all = append(all, entries...)
	}
	unknown := executor.UnknownCalls(all)
	if len(unknown) == 0 {
		fmt.Println("unknown calls: none")
		return nil
	}
	fmt.Println("unknown calls (crash window; never auto-replay):")
	for _, entry := range unknown {
		fmt.Printf("  %s  %s  %s  (%s)\n", entry.Time.Format("15:04:05"), entry.Tool, entry.ToolCallID, entry.Summary)
	}
	return nil
}
