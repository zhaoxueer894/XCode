// Package recovery 实现 崩溃恢复对账：从轮次恢复日志、ToolCall journal
// 与写前快照 manifest 三类证据重建未完成轮次，并按副作用语义判定每个未完成调用
// 可以"安全重试"、"确认已生效"、"补终态说明"还是"必须人工确认"。
// 对账原则是保守的：无法证明安全的调用一律进入人工清单，绝不自动重放非幂等副作用
// （核心不变量 3 与 4）。
package recovery

import (
	"encoding/json" // 解析 tool.begin 记录中的原始调用参数。
	"fmt"           // 构造判定说明与错误。
	"strings"       // 拼接确定性说明文本。
	"time"          // 返回未完成轮次的开始时间。

	"xcode/internal/checkpoint" // 读取写前快照元数据与基线内容。
	"xcode/internal/executor"   // 读取 ToolCall journal 的状态机事实。
	"xcode/internal/harness"    // 复用 Message、ToolCall 与 TurnRecord 协议类型。
)

// Verdict 表示一个未完成工具调用的对账结论。
type Verdict string

const (
	VerdictRetry    Verdict = "retry"    // 未开始执行、被拒绝或只读调用：恢复时安全重试。
	VerdictApplied  Verdict = "applied"  // 副作用已确认生效：不重放，补一条确定性说明。
	VerdictConfirm  Verdict = "confirm"  // 非幂等且状态不明：禁止自动重放，等待人工确认。
	VerdictConflict Verdict = "conflict" // 实际文件与两种预期都不符：放弃自动恢复，转人工处理。
	VerdictLost     Verdict = "lost"     // journal 已有终态但输出丢失：不重放，按终态补确定性说明。
)

// confirmPlaceholder 是人工确认调用在重建消息中的占位正文；
// 用户给出结论后由 ResolveConfirm 替换为确定性说明。
const confirmPlaceholder = "[recovery-pending]"

// CallVerdict 保存一个未完成调用的对账结论和面向用户的一行解释。
type CallVerdict struct {
	Call    harness.ToolCall // 保存未完成的工具调用原文（ID、名称与参数）。
	Verdict Verdict          // 保存对账结论。
	Reason  string           // 保存得出该结论的证据说明，进入恢复报告。
	Output  string           // Verdict 为 applied/lost 时保存补进消息历史的确定性说明。
}

// Plan 保存一次恢复分析的完整结果，供 CLI 决策后继续执行轮次。
type Plan struct {
	RunID     string             // 保存产生未完成轮次的进程运行编号。
	TurnID    string             // 保存未完成轮次的编号。
	Resumed   bool               // 表示该轮次本身是否已经是一次恢复续跑。
	UserInput string             // 保存本轮用户输入；恢复不重新提交任务，只续跑该轮。
	Messages  []harness.Message  // 保存重建的轮内消息（含确定性补消息；confirm 项先占位）。
	Pending   []harness.ToolCall // 保存判定为可安全重试的调用，由 Agent 在恢复预处理中补执行。
	Verdicts  []CallVerdict      // 保存全部未完成调用的对账结论，按发生顺序排列。
	Completed bool               // 表示段内已有终止记录，即没有未完成轮次。
}

// Input 汇总恢复分析需要的全部证据源。
type Input struct {
	Records     []harness.TurnRecord    // 保存轮次恢复日志的全部记录。
	Journal     []executor.JournalEntry // 保存 ToolCall journal 的全部条目（可跨多次运行）。
	Checkpoints []checkpoint.Entry      // 保存快照 manifest 的全部条目（按创建顺序）。
	Store       *checkpoint.Store       // 保存快照仓库；读取基线内容重构补丁结果时使用。
	Workspace   string                  // 保存工作区根路径；用于读取文件当前实际状态。
}

// Analyze 重建最后一个未终止轮次段，并对其中全部未完成调用给出对账结论。
// 没有任何未完成轮次时返回 Completed 为 true 的空 Plan。
func Analyze(in Input) (*Plan, error) {
	// 恢复对象永远是"最后一个 turn.started 之后"的段；更早的段已被消费或终止。
	last := -1
	for i, record := range in.Records {
		if record.Kind == harness.TurnRecordStarted {
			last = i
		}
	}
	if last < 0 {
		return &Plan{Completed: true}, nil
	}
	start := in.Records[last]
	plan := &Plan{RunID: start.RunID, TurnID: start.TurnID, Resumed: start.Resumed}
	if start.Message != nil {
		plan.UserInput = start.Message.Content
	}
	plan.Messages = append(plan.Messages, *start.Message)

	// 按顺序重放段内记录：消息直接进历史，未配对的 tool.begin 进入对账。
	// 同时记录已回应与已开始的调用，用于识别"assistant 已落盘但工具还没开始"的窗口。
	open := map[string]harness.TurnRecord{}
	responded := map[string]bool{}
	begun := map[string]bool{}
	for _, record := range in.Records[last+1:] {
		switch record.Kind {
		case harness.TurnRecordCompleted:
			plan.Completed = true
			return plan, nil
		case harness.TurnRecordAssistant:
			if record.Message != nil {
				plan.Messages = append(plan.Messages, *record.Message)
			}
		case harness.TurnRecordToolEnd:
			if record.Message != nil {
				plan.Messages = append(plan.Messages, *record.Message)
			}
			responded[record.ToolCallID] = true
			delete(open, record.ToolCallID)
		case harness.TurnRecordToolBegin:
			open[record.ToolCallID] = record
			begun[record.ToolCallID] = true
		}
	}

	// 对全部未完成调用按段内发生顺序对账（map 遍历无序，所以按记录顺序重扫一遍）。
	ordered := make([]harness.TurnRecord, 0, len(open))
	for _, record := range in.Records[last+1:] {
		if record.Kind == harness.TurnRecordToolBegin {
			if _, pending := open[record.ToolCallID]; pending {
				ordered = append(ordered, record)
				delete(open, record.ToolCallID)
			}
		}
	}

	for _, record := range ordered {
		call := harness.ToolCall{ID: record.ToolCallID, Name: record.Tool, Arguments: record.Arguments}
		verdict := reconcileCall(in, call)
		plan.Verdicts = append(plan.Verdicts, verdict)
		switch verdict.Verdict {
		case VerdictRetry:
			plan.Pending = append(plan.Pending, call)
		case VerdictConfirm, VerdictConflict:
			// 占位消息保证"每个 tool_call 都有对应 tool 消息"的模型协议先成立；
			// confirm 由用户结论填充，conflict 出现时整个恢复会被 CLI 拒绝。
			plan.Messages = append(plan.Messages, harness.Message{Role: "tool", Content: confirmPlaceholder, ToolCallID: call.ID})
		default:
			// applied / lost：副作用已有结论，补入确定性说明，绝不重放。
			plan.Messages = append(plan.Messages, harness.Message{Role: "tool", Content: verdict.Output, ToolCallID: call.ID})
		}
	}

	// 第四个崩溃窗口：assistant（含工具调用）已落盘，但进程在对应 tool.begin 之前崩溃。
	// 这些调用从未进入执行管线，journal 不可能有 started 记录，重试绝对安全；
	// 若不补进 Pending，重建的历史会以"带工具调用的 assistant"结尾且没有回应，违反模型协议。
	for _, message := range plan.Messages {
		if message.Role != "assistant" {
			continue
		}
		for _, call := range message.ToolCalls {
			if responded[call.ID] || begun[call.ID] {
				continue
			}
			plan.Verdicts = append(plan.Verdicts, CallVerdict{
				Call: call, Verdict: VerdictRetry, Reason: "never started (crash before tool.begin)",
			})
			plan.Pending = append(plan.Pending, call)
		}
	}
	return plan, nil
}

// ResolveConfirm 把人工确认结论填进占位消息；output 是面向模型的确定性说明文本。
// 本项目不提供"重放"选项：确认清单里的调用只能跳过，或者放弃整个恢复。
func (p *Plan) ResolveConfirm(toolCallID, output string) {
	for i, message := range p.Messages {
		if message.ToolCallID == toolCallID && message.Content == confirmPlaceholder {
			p.Messages[i].Content = output
			return
		}
	}
}

// reconcileCall 结合 journal 状态与副作用语义判定单个未完成调用。
func reconcileCall(in Input, call harness.ToolCall) CallVerdict {
	state, known := lastJournalState(in.Journal, call.ID)
	switch {
	case !known || state == executor.StatePlanned || state == executor.StateApproved:
		// journal 没有 started 记录说明副作用尚未开始：无论什么工具，重试都安全。
		return CallVerdict{Call: call, Verdict: VerdictRetry, Reason: "not started (journal: " + journalStateName(state, known) + ")"}
	case state == executor.StateStarted:
		return reconcileStarted(in, call)
	default:
		// 终态（succeeded/failed/denied）已确定但输出丢失：按终态补确定性说明。
		return lostVerdict(call, state)
	}
}

// reconcileStarted 处理 journal 停留在 started 的调用：副作用可能已发生。
func reconcileStarted(in Input, call harness.ToolCall) CallVerdict {
	switch call.Name {
	case "read_file", "list_files", "search_text", "git_diff", "read_skill":
		return CallVerdict{Call: call, Verdict: VerdictRetry, Reason: "interrupted, but read-only tools have no side effects"}
	case "apply_patch":
		verdict, reason, err := reconcilePatch(in, call)
		if err != nil {
			return CallVerdict{Call: call, Verdict: VerdictConflict, Reason: err.Error()}
		}
		if verdict == VerdictApplied {
			return CallVerdict{Call: call, Verdict: VerdictApplied,
				Reason: reason, Output: appliedPatchNote(call)}
		}
		return CallVerdict{Call: call, Verdict: verdict, Reason: reason}
	default:
		// exec、delegate 等非幂等工具状态不明：禁止自动重放，进入人工确认清单。
		return CallVerdict{Call: call, Verdict: VerdictConfirm,
			Reason: "non-idempotent tool with unknown side effects; never auto-replayed"}
	}
}

// lostVerdict 为 journal 已有终态的调用生成确定性补消息。
func lostVerdict(call harness.ToolCall, state executor.CallState) CallVerdict {
	switch state {
	case executor.StateSucceeded:
		return CallVerdict{Call: call, Verdict: VerdictLost,
			Reason: "journal reports succeeded but output was lost in the crash",
			Output: "[recovery] journal reports this " + call.Name + " call succeeded before the crash, but its output was lost; not replayed. Inspect the workspace if needed."}
	case executor.StateDenied:
		return CallVerdict{Call: call, Verdict: VerdictLost,
			Reason: "journal reports the call was denied; no side effects occurred",
			Output: "ERROR: [recovery] journal reports this " + call.Name + " call was denied before the crash; not retried."}
	default:
		return CallVerdict{Call: call, Verdict: VerdictLost,
			Reason: "journal reports failed; treating the error as final",
			Output: "ERROR: [recovery] journal reports this " + call.Name + " call failed before the crash; not replayed."}
	}
}

// appliedPatchNote 生成 patch 已生效时的确定性说明文本。
func appliedPatchNote(call harness.ToolCall) string {
	var args struct {
		Path string `json:"path"`
	}
	_ = json.Unmarshal(call.Arguments, &args)
	return fmt.Sprintf("[recovery] apply_patch on %s already took effect before the crash (content hash match); not replayed.", args.Path)
}

// ExecCommandText 从工具调用参数中提取命令文本，供确认清单展示；无法解析时返回占位说明。
func ExecCommandText(arguments json.RawMessage) string {
	var args struct {
		Command []string `json:"command"`
	}
	if err := json.Unmarshal(arguments, &args); err != nil || len(args.Command) == 0 {
		return "(unreadable arguments)"
	}
	return strings.Join(args.Command, " ")
}

// lastJournalState 返回该调用在 journal 中的最后状态；没有任何记录时 known 为 false。
func lastJournalState(entries []executor.JournalEntry, toolCallID string) (executor.CallState, bool) {
	var last executor.JournalEntry
	found := false
	for _, entry := range entries {
		if entry.ToolCallID == toolCallID {
			last = entry
			found = true
		}
	}
	if !found {
		return "", false
	}
	return last.State, true
}

// journalStateName 把 journal 状态转成可读文本；无记录时显示 none。
func journalStateName(state executor.CallState, known bool) string {
	if !known {
		return "none"
	}
	return string(state)
}

// HasUnfinishedTurn 快速判断恢复日志里是否存在未终止的轮次段，并返回该段的开始时间。
// 交互模式在启动时用它提示用户改用 --resume 续跑。
func HasUnfinishedTurn(path string) (bool, time.Time, error) {
	records, err := harness.ReadTurnLog(path)
	if err != nil {
		return false, time.Time{}, err
	}
	last := -1
	for i, record := range records {
		if record.Kind == harness.TurnRecordStarted {
			last = i
		}
	}
	if last < 0 {
		return false, time.Time{}, nil
	}
	for _, record := range records[last+1:] {
		if record.Kind == harness.TurnRecordCompleted {
			return false, time.Time{}, nil
		}
	}
	return true, records[last].Time, nil
}
