package executor

// 本文件实现 ToolCall 状态 journal：append-only JSONL 落盘与 unknown 判定。
// 崩溃时停留在非终态的条目由读取方标记 unknown，绝不自动重放（核心不变量 3 与 4）。

import (
	"encoding/json" // 序列化每条状态记录。
	"fmt"           // 构造读写错误。
	"os"            // 创建与读取 journal 文件。
	"path/filepath" // 按目录列出运行文件。
	"sort"          // 保持运行文件按名称排序。
	"strings"       // 拼接与清理。
	"sync"          // 保护并发追加。
	"time"          // 记录迁移时间。
)

// CallState 表示一次工具调用在管线中的状态。
type CallState string

const (
	StatePlanned   CallState = "planned"   // 意图已声明，等待策略判定。
	StateApproved  CallState = "approved"  // 策略放行（含规则 allow 或审批通过）。
	StateDenied    CallState = "denied"    // 策略或用户拒绝；终态。
	StateStarted   CallState = "started"   // 副作用即将发生。
	StateSucceeded CallState = "succeeded" // 工具正常返回；终态。
	StateFailed    CallState = "failed"    // 工具返回错误；终态。
	StateUnknown   CallState = "unknown"   // 读取方判定：崩溃窗口中的非终态条目。
)

// TerminalStates 保存全部终态；停在终态之外的条目被判定为 unknown。
var TerminalStates = map[CallState]struct{}{StateDenied: {}, StateSucceeded: {}, StateFailed: {}}

// JournalEntry 保存一条状态迁移记录。
type JournalEntry struct {
	Time       time.Time `json:"time"`         // 保存迁移发生的 UTC 时间。
	ToolCallID string    `json:"tool_call_id"` // 保存对应的工具调用 ID。
	Tool       string    `json:"tool"`         // 保存工具名称。
	State      CallState `json:"state"`        // 保存迁移到的状态。
	Summary    string    `json:"summary"`      // 保存一行可读说明，例如命中的规则或错误摘要。
}

// Journal 把状态迁移追加写入单个 JSONL 文件。
type Journal struct {
	mu       sync.Mutex // 保护文件写入。
	file     *os.File   // 保存以追加方式打开的 journal 文件。
	firstErr error      // 保存首个写入错误；诊断用途，不中断主流程。
}

// OpenJournal 以追加方式打开（或创建）journal 文件；目录自动创建，权限 0600。
func OpenJournal(path string) (*Journal, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create journal directory: %w", err)
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open journal: %w", err)
	}
	return &Journal{file: file}, nil
}

// Record 追加一条状态迁移；写入失败记录首个错误并安静返回（诊断不中断主流程）。
func (j *Journal) Record(toolCallID, tool string, state CallState, summary string) {
	if j == nil {
		return
	}
	entry := JournalEntry{Time: time.Now().UTC(), ToolCallID: toolCallID, Tool: tool, State: state, Summary: summary}
	encoded, err := json.Marshal(entry)
	if err != nil {
		j.rememberError(err)
		return
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	if _, err := j.file.Write(append(encoded, '\n')); err != nil {
		j.rememberError(err)
	}
}

// rememberError 只保存首个写入错误供 Err 返回。
func (j *Journal) rememberError(err error) {
	if j.firstErr == nil {
		j.firstErr = err
	}
}

// Err 返回首个写入错误；全部成功时为 nil。
func (j *Journal) Err() error {
	if j == nil {
		return nil
	}
	return j.firstErr
}

// Close 关闭 journal 文件。
func (j *Journal) Close() error {
	if j == nil || j.file == nil {
		return nil
	}
	return j.file.Close()
}

// ReadJournalFile 读取单个 journal 文件的全部条目。
func ReadJournalFile(path string) ([]JournalEntry, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read journal: %w", err)
	}
	var entries []JournalEntry
	for _, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var entry JournalEntry
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			return nil, fmt.Errorf("decode journal %s: %w", path, err)
		}
		entries = append(entries, entry)
	}
	return entries, nil
}

// ListJournalFiles 返回目录下全部 journal 文件路径，按文件名升序（即时间序）。
func ListJournalFiles(directory string) ([]string, error) {
	matches, err := filepath.Glob(filepath.Join(directory, "run-*.jsonl"))
	if err != nil {
		return nil, err
	}
	sort.Strings(matches)
	return matches, nil
}

// UnknownCalls 汇总每个工具调用的最后状态，返回仍处于非终态的调用（补 unknown 标记）。
func UnknownCalls(entries []JournalEntry) []JournalEntry {
	last := make(map[string]JournalEntry)
	for _, entry := range entries {
		last[entry.ToolCallID] = entry
	}
	var unknown []JournalEntry
	for _, entry := range last {
		if _, terminal := TerminalStates[entry.State]; !terminal {
			unknown = append(unknown, JournalEntry{
				Time: entry.Time, ToolCallID: entry.ToolCallID, Tool: entry.Tool,
				State: StateUnknown, Summary: "last recorded state: " + string(entry.State),
			})
		}
	}
	sort.Slice(unknown, func(i, j int) bool { return unknown[i].Time.Before(unknown[j].Time) })
	return unknown
}
