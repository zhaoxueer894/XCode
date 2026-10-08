package harness

// 本文件实现多轮对话的 Session：管理消息历史（L0）、结构化工作笔记（L1）
// 和项目记忆（L2），协调三层记忆在轮间的注入与更新。
//
// 三层记忆架构：
//   L0 消息历史 —— Durable Transcript，每轮追加，由 ContextManager 管理投影
//   L1 工作笔记 —— 结构化 JSON，每轮结束后从工具事件确定性提取
//   L2 项目记忆 —— .xcode/memory.md，会话开始时加载，全程只读
//
// L1+L2 合并为前导消息（位置 0），ContextManager 压缩时始终保留首条消息，
// 因此工作笔记不受压缩影响。

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// WorkingNote 保存 L1 短期记忆：当前会话的结构化状态摘要。
// 每轮结束后从工具事件确定性提取（不依赖模型），始终紧凑（< 200 token）。
type WorkingNote struct {
	Objective     string   `json:"objective"`      // 保存最新用户输入（当前任务目标）。
	Plan          []string `json:"plan"`           // 保存步骤清单（可带 ✅/⏳ 标记）。
	FilesRead     []string `json:"files_read"`     // 保存本会话读取过的文件（去重）。
	FilesModified []string `json:"files_modified"` // 保存本会话修改过的文件（去重）。
	TestCommand   string   `json:"test_command"`   // 保存最近使用的测试命令。
	TestStatus    string   `json:"test_status"`    // 保存最近测试结果摘要。
	Errors        []string `json:"errors"`         // 保存本会话遇到的错误（最多 5 条）。
	NextSteps     []string `json:"next_steps"`     // 保存建议的下一步操作。
}

// Session 管理多轮对话的完整状态：消息历史、工作笔记和项目记忆。
type Session struct {
	agent       *Agent      // 保存对话使用的 Agent 实例。
	messages    []Message   // L0：完整消息历史（Durable Transcript，不变量 1）。
	workingNote WorkingNote // L1：结构化工作笔记，每轮确定性更新。
	projectMemo string      // L2：项目记忆正文，会话开始时加载。
	options     RunOptions  // 每轮的运行控制选项。
	turn        int         // 当前轮次编号（从 1 开始）。
	totalUsage  Usage       // 跨轮累计 Token 用量。
	turnEvents  []Event     // 当前轮的事件（用于 L1 确定性提取）。
	extSink     EventSink   // 外部事件接收端（journal/终端），可为 nil。
	startedAt   time.Time   // 会话开始时间。
}

// NewSession 创建多轮对话会话；model/runner/maxSteps 用于构造内部 Agent。
// projectMemo 是 L2 项目记忆正文（可为空）；extSink 接收所有事件用于落盘或展示。
func NewSession(model Model, runner ToolRunner, maxSteps int, extSink EventSink, projectMemo string) *Session {
	s := &Session{
		projectMemo: projectMemo,
		options:     RunOptions{},
		extSink:     extSink,
		startedAt:   time.Now().UTC(),
	}
	// 包装 sink：收集本轮事件用于 L1 提取，同时转发给外部接收端。
	wrappedSink := EventSinkFunc(func(e Event) {
		s.turnEvents = append(s.turnEvents, e)
		if extSink != nil {
			extSink.Emit(e)
		}
	})
	s.agent = NewAgent(model, runner, maxSteps, wrappedSink)
	return s
}

// Turn 处理一次用户输入：追加消息 → 更新前导 → 执行轮次 → 确定性更新 L1。
func (s *Session) Turn(ctx context.Context, input string) (RunResult, error) {
	s.turn++
	s.turnEvents = nil // 重置本轮事件收集

	// 更新 L1 目标（用户最新输入就是当前目标）。
	s.workingNote.Objective = input

	// 更新或创建前导消息（位置 0 = L2 + L1 合并）。
	s.updatePrelude()

	// 追加用户消息到 L0。
	s.messages = append(s.messages, Message{Role: "user", Content: input})

	// 执行本轮循环（Agent.RunTurn 内部由 ContextManager 管理投影）。
	result, err := s.agent.RunTurn(ctx, s.messages, s.options)
	if err != nil {
		// 即使失败也保存消息（可能包含部分工具结果）。
		s.messages = result.Messages
		s.updateWorkingNotes()
		s.accumulateUsage(result.Usage)
		return result, err
	}

	// 持久化 L0（本轮追加的 assistant/tool 消息已在 result.Messages 中）。
	s.messages = result.Messages

	// 确定性更新 L1 工作笔记（从工具事件提取，不用模型）。
	s.updateWorkingNotes()

	// 累计跨轮用量。
	s.accumulateUsage(result.Usage)

	return result, nil
}

// TurnCount 返回当前轮次编号。
func (s *Session) TurnCount() int { return s.turn }

// TotalUsage 返回跨轮累计 Token 用量。
func (s *Session) TotalUsage() Usage { return s.totalUsage }

// WorkingNote 返回 L1 工作笔记的当前快照。
func (s *Session) WorkingNote() WorkingNote { return s.workingNote }

// SetTurnLog 给内部 Agent 挂载轮次恢复日志；交互模式在启动时调用一次。
func (s *Session) SetTurnLog(log *TurnLog) { s.agent.SetTurnLog(log) }

// SetContextManager 给内部 Agent 装配投影管理器； REPL 路径在启动时调用，
// 使交互模式的自动压缩（含决策门控与 shadow 分类）与一次性命令行为一致。
func (s *Session) SetContextManager(manager ContextManager) { s.agent.SetContextManager(manager) }

// Clear 清空会话历史和 L1 笔记，保留 L2 项目记忆。
func (s *Session) Clear() {
	s.messages = nil
	s.workingNote = WorkingNote{}
	s.turn = 0
	s.totalUsage = Usage{}
	s.turnEvents = nil
}

// updatePrelude 更新消息列表位置 0 的前导（L2 项目记忆 + L1 工作笔记）。
// ContextManager 的 sliding 策略始终保留首条消息，因此前导不受压缩影响。
func (s *Session) updatePrelude() {
	var b strings.Builder
	if s.projectMemo != "" {
		b.WriteString("[project-memory]\n" + s.projectMemo + "\n\n")
	}
	if note := s.renderWorkingNote(); note != "" {
		b.WriteString("[session-notes]\n" + note)
	}
	prelude := strings.TrimSpace(b.String())
	if prelude == "" {
		return // 没有任何前导内容
	}
	// 检查位置 0 是否已是前导消息（以 [project-memory] 或 [session-notes] 开头）。
	if len(s.messages) > 0 && s.messages[0].Role == "user" &&
		(strings.HasPrefix(s.messages[0].Content, "[project-memory]") ||
			strings.HasPrefix(s.messages[0].Content, "[session-notes]")) {
		s.messages[0].Content = prelude // 原地更新
	} else {
		// 插入前导到位置 0。
		s.messages = append([]Message{{Role: "user", Content: prelude}}, s.messages...)
	}
}

// renderWorkingNote 把 L1 笔记渲染为紧凑文本（< 200 token）。
func (s *Session) renderWorkingNote() string {
	n := s.workingNote
	if n.Objective == "" && len(n.FilesModified) == 0 && len(n.FilesRead) == 0 {
		return "" // 空笔记不注入
	}
	var b strings.Builder
	b.WriteString("行为提示: apply_patch 成功后不需要重新读取文件验证；信任工具结果，直接继续下一步。\n")
	if n.Objective != "" {
		fmt.Fprintf(&b, "目标: %s\n", truncateForNote(n.Objective, 80))
	}
	if len(n.FilesModified) > 0 {
		fmt.Fprintf(&b, "已修改: %s\n", strings.Join(n.FilesModified, ", "))
	}
	if len(n.FilesRead) > 0 {
		fmt.Fprintf(&b, "已读取: %s\n", strings.Join(firstN(n.FilesRead, 5), ", "))
	}
	if n.TestStatus != "" {
		fmt.Fprintf(&b, "测试: %s (%s)\n", n.TestStatus, n.TestCommand)
	}
	if len(n.Errors) > 0 {
		fmt.Fprintf(&b, "错误: %s\n", strings.Join(firstN(n.Errors, 3), "; "))
	}
	if len(n.NextSteps) > 0 {
		fmt.Fprintf(&b, "下一步: %s\n", strings.Join(firstN(n.NextSteps, 3), "; "))
	}
	return b.String()
}

// updateWorkingNotes 从本轮工具事件中确定性提取信息更新 L1 笔记。
// 不依赖模型——同样的输入永远产生同样的输出。
func (s *Session) updateWorkingNotes() {
	for _, e := range s.turnEvents {
		switch e.Kind {
		case ToolCompleted:
			switch e.Tool {
			case "apply_patch":
				if path := extractFilePath(e.Resource); path != "" {
					s.workingNote.FilesModified = addToSet(s.workingNote.FilesModified, path)
				}
			case "read_file":
				if path := extractFilePath(e.Resource); path != "" {
					s.workingNote.FilesRead = addToSet(s.workingNote.FilesRead, path)
				}
			case "exec":
				cmd := extractExecCommand(e.Resource)
				if isTestCommand(cmd) {
					s.workingNote.TestCommand = cmd
					s.workingNote.TestStatus = extractTestOutcome(e.Data)
				}
			}
		case RunFailed:
			if e.Error != "" {
				s.workingNote.Errors = append(s.workingNote.Errors, truncateForNote(e.Error, 80))
				if len(s.workingNote.Errors) > 5 {
					s.workingNote.Errors = s.workingNote.Errors[1:] // 保留最近 5 条
				}
			}
		}
	}
}

// accumulateUsage 累计跨轮 Token 用量。
func (s *Session) accumulateUsage(u Usage) {
	s.totalUsage.InputTokens += u.InputTokens
	s.totalUsage.OutputTokens += u.OutputTokens
	s.totalUsage.TotalTokens += u.TotalTokens
}

// Compact 把旧对话历史替换为一条紧凑摘要，保留前导（L1+L2）和工作笔记。
// 用于 REPL 的 /compact 命令，在长会话中手动回收上下文空间。
func (s *Session) Compact() {
	if len(s.messages) <= 2 {
		return
	}
	prelude := s.messages[0]
	summary := fmt.Sprintf("[compacted] 之前的对话（%d 轮，%d 条消息）已压缩。当前状态见工作笔记。", s.turn, len(s.messages))
	s.messages = []Message{prelude, {Role: "user", Content: summary}}
	s.updatePrelude()
}

// ======== 崩溃恢复 ========

// SessionState 保存会话的持久化快照，用于崩溃后恢复。
type SessionState struct {
	Messages    []Message   `json:"messages"`     // L0 完整消息历史。
	WorkingNote WorkingNote `json:"working_note"` // L1 结构化工作笔记。
	TurnCount   int         `json:"turn_count"`   // 已完成轮次数。
	TotalUsage  Usage       `json:"total_usage"`  // 跨轮累计用量。
	StartedAt   time.Time   `json:"started_at"`   // 会话开始时间。
	LastActive  time.Time   `json:"last_active"`  // 最后活跃时间。
}

// Save 把当前会话状态持久化到磁盘。
func (s *Session) Save(path string) error {
	state := SessionState{
		Messages:    s.messages,
		WorkingNote: s.workingNote,
		TurnCount:   s.turn,
		TotalUsage:  s.totalUsage,
		StartedAt:   s.startedAt,
		LastActive:  time.Now().UTC(),
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("encode session state: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create session directory: %w", err)
	}
	return os.WriteFile(path, data, 0o600)
}

// RestoreFrom 从持久化快照恢复会话状态。
func (s *Session) RestoreFrom(state *SessionState) {
	s.messages = state.Messages
	s.workingNote = state.WorkingNote
	s.turn = state.TurnCount
	s.totalUsage = state.TotalUsage
	s.startedAt = state.StartedAt
	// 更新前导以反映恢复的 L1 笔记。
	s.updatePrelude()
}

// LoadSessionState 从磁盘读取会话快照；文件不存在返回 nil。
func LoadSessionState(path string) (*SessionState, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read session state: %w", err)
	}
	var state SessionState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, fmt.Errorf("decode session state: %w", err)
	}
	return &state, nil
}

// ======== 辅助函数 ========

// extractFilePath 从资源名中提取文件路径。
// 输入形如 "apply_patch:calc.go" 或 "read_file:notes.md#L1-L50"。
func extractFilePath(resource string) string {
	if resource == "" {
		return ""
	}
	// 去掉工具前缀（"apply_patch:" 或 "read_file:" 等）。
	if idx := strings.Index(resource, ":"); idx >= 0 {
		resource = resource[idx+1:]
	}
	// 去掉行范围后缀（"#L1-L50"）。
	if idx := strings.Index(resource, "#"); idx >= 0 {
		resource = resource[:idx]
	}
	return strings.TrimSpace(resource)
}

// extractExecCommand 从 exec 资源名中提取命令文本。
// 输入形如 "exec:go test ./..." 或 "exec:C:\git.exe status"。
func extractExecCommand(resource string) string {
	if resource == "" {
		return ""
	}
	if idx := strings.Index(resource, ":"); idx >= 0 {
		return strings.TrimSpace(resource[idx+1:])
	}
	return resource
}

// isTestCommand 判断命令是否是测试命令。
func isTestCommand(cmd string) bool {
	lower := strings.ToLower(cmd)
	return strings.Contains(lower, "go test") ||
		strings.Contains(lower, "pytest") ||
		strings.Contains(lower, "jest") ||
		strings.Contains(lower, "npm test") ||
		strings.Contains(lower, "make test")
}

// extractTestOutcome 从工具输出中提取测试结果摘要。
func extractTestOutcome(data string) string {
	if data == "" {
		return "unknown"
	}
	lower := strings.ToLower(data)
	if strings.Contains(lower, "pass") && !strings.Contains(lower, "fail") {
		return "PASS"
	}
	if strings.Contains(lower, "fail") {
		return "FAIL"
	}
	if strings.Contains(lower, "error") {
		return "ERROR"
	}
	return "done"
}

// addToSet 向去重列表添加元素（保持顺序）。
func addToSet(list []string, item string) []string {
	for _, existing := range list {
		if existing == item {
			return list
		}
	}
	return append(list, item)
}

// firstN 返回列表前 n 个元素。
func firstN(list []string, n int) []string {
	if len(list) <= n {
		return list
	}
	return list[:n]
}

// truncateForNote 截断文本到指定字符数。
func truncateForNote(s string, limit int) string {
	runes := []rune(s)
	if len(runes) <= limit {
		return s
	}
	return string(runes[:limit]) + "…"
}
