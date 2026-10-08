package harness

// 本文件实现运行控制：停止原因、Token 预算、doom-loop 检测与运行结果。

import (
	"encoding/json" // 规范化工具参数，使字段顺序不同的相同参数得到同一指纹。
	"errors"        // 定义预算与 doom-loop 的哨兵错误。
)

// StopReason 表示一次 Agent Run 结束时 Harness 层的原因。
// 它与模型层的 FinishReason 不同：FinishReason 描述单次模型调用为何停止，
// StopReason 描述整次 Run 为何结束，供事件、报告和终端统一消费。
type StopReason string

const (
	StopCompleted StopReason = "completed" // 模型给出最终答案，Run 正常结束。
	StopMaxSteps  StopReason = "max_steps" // 达到最大模型迭代数仍未结束。
	StopBudget    StopReason = "budget"    // 累计 Token 用量达到预算上限。
	StopDoomLoop  StopReason = "doom_loop" // 连续重复相同工具调用达到上限。
	StopFailed    StopReason = "failed"    // 模型错误、超时或取消等异常结束。
)

// ErrBudget 表示 Run 因 Token 预算耗尽而受控停止；调用方用 errors.Is 识别。
var ErrBudget = errors.New("agent token budget exhausted")

// ErrDoomLoop 表示 Run 因连续重复相同工具调用而受控停止；调用方用 errors.Is 识别。
var ErrDoomLoop = errors.New("agent repeated the same tool call too many times")

// defaultDoomLoopLimit 保存 doom-loop 的默认触发次数；连续相同达到该值即停止。
const defaultDoomLoopLimit = 3

// RunOptions 保存调用方对一次 Run 的控制参数；零值表示全部使用默认行为。
type RunOptions struct {
	MaxSteps             int                    // 保存单次 Run 允许的最大模型迭代数；小于等于 0 时沿用 Agent 构造时的步数。
	MaxTotalTokens       int                    // 保存单次 Run 允许消耗的累计 Token 上限；小于等于 0 表示不限制。
	DoomLoopLimit        int                    // 保存连续相同工具调用被判为 doom-loop 的次数；小于等于 0 使用默认 3。
	ProjectMemory        string                 // 保存启动前注入的 L2 项目记忆正文；空字符串表示没有项目记忆。
	ResumePending        []ToolCall             // 保存对账后判定可安全重试的未完成调用；仅恢复路径使用，普通 Run 保持 nil。
	Contract             *TaskContract          // 完成门禁契约；nil 表示本运行不启用门禁，行为与原实现一致。
	WorkspaceFingerprint func() (string, error) // 返回当前工作区内容指纹；nil 时验证证据不与工作区版本绑定。
}

// steps 返回本次 Run 实际使用的最大步数；选项未设置时回退到 Agent 构造值。
func (o RunOptions) steps(fallback int) int {
	if o.MaxSteps > 0 {
		return o.MaxSteps
	}
	return fallback
}

// doomLimit 返回 doom-loop 的触发次数；选项未设置时使用默认值。
func (o RunOptions) doomLimit() int {
	if o.DoomLoopLimit > 0 {
		return o.DoomLoopLimit
	}
	return defaultDoomLoopLimit
}

// RunResult 保存一次 Agent Run 的结构化结果，供 CLI 和调用方直接使用。
type RunResult struct {
	Answer     string     // 保存模型给出的最终答案文本；异常结束时为空字符串。
	StopReason StopReason // 保存本次 Run 结束的 Harness 层原因，取值为 StopReason 常量。
	Steps      int        // 保存本次 Run 实际完成的模型迭代数，从 1 开始计数。
	Usage      Usage      // 保存本次 Run 全部模型调用的累计 Token 用量。
	Messages   []Message  // 保存本轮结束后完整消息历史，供 Session 跨轮持久化。
}

// doomLoopDetector 检测连续完全相同的工具调用；它只比较相邻调用，不记忆全部历史。
type doomLoopDetector struct {
	limit   int    // 保存触发停止所需的连续相同次数。
	lastKey string // 保存上一次工具调用的指纹；指纹由工具名和规范化参数拼接。
	streak  int    // 保存当前连续相同调用次数；出现任何不同调用即归零。
}

// newDoomLoopDetector 创建一个 doom-loop 检测器；limit 小于等于 1 时按 1 处理。
func newDoomLoopDetector(limit int) *doomLoopDetector {
	if limit < 1 {
		limit = 1
	}
	return &doomLoopDetector{limit: limit}
}

// register 记录一次即将执行的工具调用，并在连续相同次数达到上限时返回 true。
// 返回 true 表示本次调用不应再执行，Run 应当以 doom_loop 停止。
func (d *doomLoopDetector) register(call ToolCall) bool {
	key := call.Name + "\x00" + normalizeCallArguments(call.Arguments)
	if key == d.lastKey {
		d.streak++
	} else {
		d.lastKey = key
		d.streak = 1
	}
	return d.streak >= d.limit
}

// normalizeCallArguments 把 JSON 参数转成稳定文本，使字段顺序或空格不同的相同参数共用指纹。
func normalizeCallArguments(raw json.RawMessage) string {
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return string(raw)
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return string(raw)
	}
	return string(encoded)
}
