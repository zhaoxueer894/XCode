// Package executor 实现统一工具执行管线：意图描述 → 策略决策 → 用户审批 → 写前快照 → 真正执行。
// 所有工具动作都必须经过这条管线，任何工具不得绕过它直接产生副作用。
package executor

import (
	"context"       // 在管线每个阶段响应取消。
	"encoding/json" // 把工具参数交给意图描述接口。
	"fmt"           // 构造策略拒绝与审批失败的说明。
	"strings"       // 拼接意图与命令的展示文字。
	"time"          // 为管线事件补齐 UTC 时间。

	"xcode/internal/checkpoint" // 写前快照的保存与恢复。
	"xcode/internal/harness"    // 复用 ToolCall、ToolResult、Event 协议。
	"xcode/internal/policy"     // 策略引擎与请求事实。
)

// Intent 保存一次工具调用在执行前可以确定的副作用事实。
type Intent struct {
	Arguments json.RawMessage // 保存标准化后的执行参数；为空时保留原参数。
	Workdir   string          // 保存命令执行目录，用于审批显示与会话授权。
	Mutating  bool            // 表示本次调用是否会修改工作区文件。
	Paths     []string        // 保存将被修改文件的工作区相对路径，用于写前快照。
	Command   []string        // 保存将要执行的完整命令参数数组；非命令类调用为空。
}

// IntentDescriber 由需要策略判断的工具实现；只有内置只读工具允许不实现该接口。
type IntentDescriber interface {
	DescribeIntent(raw json.RawMessage) (Intent, error)
}

// ApprovalScope 表示用户对一次 ask 请求给出的批准范围。
type ApprovalScope string

const (
	ApprovalDenied  ApprovalScope = "denied"  // 用户拒绝本次执行。
	ApprovalOnce    ApprovalScope = "once"    // 只批准当前这一次调用。
	ApprovalSession ApprovalScope = "session" // 批准本次会话内同类调用。
)

// Approver 负责把 ask 决策交给用户当场确认；nil 表示运行环境无法交互。
type Approver interface {
	Approve(ctx context.Context, request policy.Request) (ApprovalScope, error)
}

// Executor 组合 Registry、策略引擎、审批入口、快照仓库和事件输出端。
type Executor struct {
	lock        chan struct{}     // 容量 1 的信号量；select 获取使等待可被取消中断。
	registry    *harness.Registry // 保存真正查找并执行工具的注册表。
	engine      *policy.Engine    // 保存本次运行的策略引擎。
	approver    Approver          // 保存 ask 决策的用户确认入口。
	checkpoints *checkpoint.Store // 保存写前快照仓库。
	sink        harness.EventSink // 保存管线事件的输出端。
	journal     *Journal          // 保存可选的 ToolCall 状态日志；nil 表示不落盘（Eval 等场景）。
}

// New 创建统一执行入口；registry、engine 和 checkpoints 是必需依赖。
func New(
	registry *harness.Registry,
	engine *policy.Engine,
	approver Approver,
	checkpoints *checkpoint.Store,
	sink harness.EventSink,
) (*Executor, error) {
	if registry == nil || engine == nil || checkpoints == nil {
		return nil, fmt.Errorf("executor requires registry, policy engine and checkpoint store")
	}
	if sink == nil {
		sink = harness.EventSinkFunc(func(harness.Event) {})
	}
	return &Executor{lock: make(chan struct{}, 1), registry: registry, engine: engine, approver: approver, checkpoints: checkpoints, sink: sink}, nil
}

// SetJournal 挂载 ToolCall 状态日志；必须在第一次 Execute 之前调用。
func (x *Executor) SetJournal(journal *Journal) { x.journal = journal }

// record 记录一次状态迁移：写入 journal 并向 Trace 发出 toolcall.state 事件。
func (x *Executor) record(toolCallID, tool string, state CallState, summary string) {
	x.journal.Record(toolCallID, tool, state, summary)
	x.emit(harness.Event{
		Kind:       harness.ToolCallState,
		Tool:       tool,
		ToolCallID: toolCallID,
		Data:       string(state),
	})
}

// Specs 直接转发注册表的工具声明，使 Executor 可以作为 Agent 的 ToolRunner。
func (x *Executor) Specs() []harness.ToolSpec {
	return x.registry.Specs()
}

// Execute 按固定顺序运行管线；策略拒绝和用户拒绝都以错误返回，由 Agent 转成 Observation。
func (x *Executor) Execute(ctx context.Context, call harness.ToolCall) (harness.ToolResult, error) {
	select {
	case x.lock <- struct{}{}:
		defer func() { <-x.lock }()
	case <-ctx.Done():
		return harness.ToolResult{}, ctx.Err()
	}
	if err := ctx.Err(); err != nil {
		return harness.ToolResult{}, err
	}
	tool, ok := x.registry.Tool(call.Name)
	if !ok {
		return harness.ToolResult{}, fmt.Errorf("unknown tool %q", call.Name)
	}

	// 第一步：让工具在执行前声明自己的副作用；描述失败视为参数无效，直接终止。
	intent := Intent{}
	if describer, hasIntent := tool.(IntentDescriber); hasIntent {
		described, err := describer.DescribeIntent(call.Arguments)
		if err != nil {
			return harness.ToolResult{}, err
		}
		intent = described
		if len(intent.Arguments) > 0 {
			call.Arguments = intent.Arguments
		}
	} else {
		switch call.Name {
		case "read_file", "list_files", "search_text", "git_diff", "read_skill", "delegate":
		default:
			return harness.ToolResult{}, fmt.Errorf("tool %q has no declared execution intent", call.Name)
		}
	}
	x.emit(harness.Event{
		Kind:       harness.ToolPlanned,
		Tool:       call.Name,
		ToolCallID: call.ID,
		Data:       describeIntent(intent),
	})
	x.record(call.ID, call.Name, StatePlanned, describeIntent(intent))

	// 第二步：策略决策；决策结果无论放行与否都进入 Trace。
	request := policy.Request{Tool: call.Name, Mutating: intent.Mutating, Command: intent.Command, Workdir: intent.Workdir}
	decision := x.engine.Decide(request)
	x.emit(harness.Event{
		Kind:       harness.PolicyDecided,
		Tool:       call.Name,
		ToolCallID: call.ID,
		Data:       fmt.Sprintf("mode=%s decision=%s", x.engine.Mode(), decision),
	})
	switch decision {
	case policy.Deny:
		x.record(call.ID, call.Name, StateDenied, "policy denied")
		return harness.ToolResult{}, fmt.Errorf("policy denied %s in %s mode", call.Name, x.engine.Mode())
	case policy.Allow:
		// 非交互放行也记录 approved，使 journal 状态链保持统一。
		x.record(call.ID, call.Name, StateApproved, "allowed without interactive approval")
	case policy.Ask:
		// 第三步：把 ask 决策交给用户当场确认；无法交互的环境等同拒绝。
		if x.approver == nil {
			x.emit(harness.Event{Kind: harness.ApprovalResolved, Tool: call.Name, ToolCallID: call.ID, Data: string(ApprovalDenied)})
			x.record(call.ID, call.Name, StateDenied, "approval unavailable")
			return harness.ToolResult{}, fmt.Errorf("%s requires interactive approval but no approver is available", call.Name)
		}
		scope, err := x.approver.Approve(ctx, request)
		if err != nil {
			x.emit(harness.Event{Kind: harness.ApprovalResolved, Tool: call.Name, ToolCallID: call.ID, Data: string(ApprovalDenied)})
			x.record(call.ID, call.Name, StateDenied, "approval failed")
			return harness.ToolResult{}, fmt.Errorf("resolve approval for %s: %w", call.Name, err)
		}
		x.emit(harness.Event{Kind: harness.ApprovalResolved, Tool: call.Name, ToolCallID: call.ID, Data: string(scope)})
		switch scope {
		case ApprovalSession, ApprovalOnce:
			// 只有真实批准才记录 approved；用户拒绝是终态 denied，不得进入 unknown。
			x.record(call.ID, call.Name, StateApproved, string(scope))
			if scope == ApprovalSession {
				x.engine.GrantSession(policy.GrantKey(request))
			}
		default:
			x.record(call.ID, call.Name, StateDenied, "user denied")
			return harness.ToolResult{}, fmt.Errorf("user denied %s", call.Name)
		}
	}

	if err := ctx.Err(); err != nil {
		return harness.ToolResult{}, err
	}
	if len(intent.Command) > 0 {
		entry, err := x.checkpoints.CaptureWorkspace(ctx)
		if err != nil {
			return harness.ToolResult{}, fmt.Errorf("pre-command checkpoint: %w", err)
		}
		x.emit(harness.Event{Kind: harness.CheckpointCreated, Tool: call.Name, ToolCallID: call.ID, Resource: ".", CheckpointID: entry.ID})
	}
	// 第四步：为每个将被修改的文件建立写前快照；快照失败时禁止执行（核心不变量 5）。
	for _, relative := range intent.Paths {
		entry, err := x.checkpoints.Capture(ctx, relative)
		if err != nil {
			return harness.ToolResult{}, fmt.Errorf("pre-checkpoint %q: %w", relative, err)
		}
		x.emit(harness.Event{
			Kind:         harness.CheckpointCreated,
			Tool:         call.Name,
			ToolCallID:   call.ID,
			Resource:     entry.Path,
			CheckpointID: entry.ID,
			ContentHash:  entry.ContentHash,
		})
	}

	// 第五步：真正执行工具；执行前后记录状态，崩溃窗口的条目将停留为 started。
	x.record(call.ID, call.Name, StateStarted, "side effects imminent")
	result, err := x.registry.Execute(ctx, call)
	if err != nil {
		x.record(call.ID, call.Name, StateFailed, compactJournalSummary(err.Error()))
	} else {
		x.record(call.ID, call.Name, StateSucceeded, result.Resource)
	}
	return result, err
}

// describeIntent 把意图压成一行可读文字，进入 tool.planned 事件。
func describeIntent(intent Intent) string {
	parts := []string{fmt.Sprintf("mutating=%t", intent.Mutating)}
	if len(intent.Paths) > 0 {
		parts = append(parts, "paths="+strings.Join(intent.Paths, ","))
	}
	if len(intent.Command) > 0 {
		parts = append(parts, fmt.Sprintf("command=%q workdir=%q", intent.Command, intent.Workdir))
	}
	return strings.Join(parts, " ")
}

// emit 为管线事实补齐发生时间，保持普通 CLI 日志和 Eval Trace 的时间字段有效。
func (x *Executor) emit(event harness.Event) {
	event.Time = time.Now().UTC()
	x.sink.Emit(event)
}

// compactJournalSummary 把说明压成单行并限制 160 字符，避免 journal 行过长。
func compactJournalSummary(value string) string {
	single := strings.ReplaceAll(strings.ReplaceAll(value, "\r", " "), "\n", " ")
	runes := []rune(single)
	if len(runes) <= 160 {
		return single
	}
	return string(runes[:159]) + "…"
}
