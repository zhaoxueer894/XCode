package harness

// 本文件实现 delegate 工具：编排者生成子智能体执行任务并等待其完成。
// 子智能体与父共享全部基础设施（模型、工具、策略、沙箱、快照、Artifact），
// 仅上下文（消息历史）独立——这是 Orchestrator-Worker 的核心设计。

import (
	"context"       // 传递取消与超时。
	"encoding/json" // 接收模型提供的 JSON 参数。
	"fmt"           // 构造参数错误与事件数据。
	"os"            // 读取角色技能文件。
	"path/filepath" // 组装技能路径。
	"strings"       // 清理参数空白。
	"sync"          // 保护子智能体编号自增。
	"time"          // 事件时间戳。
)

// SubagentFactory 创建并运行一个子智能体；由接线方注入以打破 Agent↔Executor 循环依赖。
type SubagentFactory func(ctx context.Context, role, prompt string, maxSteps int, sink EventSink) (string, error)

// Delegate 是编排者可调用的子智能体生成工具。
type Delegate struct {
	factory       SubagentFactory // 创建子智能体的工厂函数。
	workspaceRoot string          // 工作区根路径，用于加载角色技能。
	eventSink     EventSink       // 父的事件接收端，由接线方注入。
	mu            sync.Mutex      // 保护子智能体编号自增。
	nextID        int             // 下一个子智能体编号。
}

// NewDelegate 创建 delegate 工具；factory 由接线方提供（通常是 NewAgent + Run 的闭包）。
func NewDelegate(factory SubagentFactory, workspaceRoot string) *Delegate {
	return &Delegate{factory: factory, workspaceRoot: workspaceRoot}
}

// SetEventSink 设置父的事件接收端；必须在 Run 之前调用。
func (d *Delegate) SetEventSink(sink EventSink) {
	d.eventSink = sink
}

// Spec 返回 delegate 的名称、用途和参数约束。
func (d *Delegate) Spec() ToolSpec {
	return ToolSpec{
		Name:        "delegate",
		Description: "Spawn a subagent with fresh context. Returns result text. Use max_steps (default 12, max 16) to allow enough iterations for multi-file tasks. Roles: planner/developer/reviewer or custom from .xcode/skills/",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"role":      map[string]any{"type": "string", "description": "skill name defining the subagent's behavior (e.g. planner, developer, reviewer)"},
				"prompt":    map[string]any{"type": "string", "description": "the task for the subagent"},
				"max_steps": map[string]any{"type": "integer", "minimum": 1, "maximum": 16, "description": "maximum model iterations for the subagent"},
			},
			"required":             []string{"prompt"},
			"additionalProperties": false,
		},
	}
}

// delegateArgs 保存 delegate 的参数。
type delegateArgs struct {
	Role     string `json:"role"`      // 保存角色名；空表示无角色前导。
	Prompt   string `json:"prompt"`    // 保存子智能体的任务描述。
	MaxSteps int    `json:"max_steps"` // 保存子智能体最大迭代数；0 使用默认 8。
}

// Execute 加载角色、生成子智能体并运行，返回其结果摘要。
func (d *Delegate) Execute(ctx context.Context, raw json.RawMessage) (ToolResult, error) {
	if err := ctx.Err(); err != nil {
		return ToolResult{}, err
	}
	var args delegateArgs
	if err := json.Unmarshal(raw, &args); err != nil {
		return ToolResult{}, fmt.Errorf("decode arguments: %w", err)
	}
	if strings.TrimSpace(args.Prompt) == "" {
		return ToolResult{}, fmt.Errorf("prompt is required")
	}
	if args.MaxSteps == 0 {
		args.MaxSteps = 12
	}
	if args.MaxSteps < 1 || args.MaxSteps > 16 {
		return ToolResult{}, fmt.Errorf("max_steps must be between 1 and 16")
	}

	// 加载角色技能正文作为前导指令（可选）。
	roleInstructions := ""
	if strings.TrimSpace(args.Role) != "" {
		body, err := d.loadRole(args.Role)
		if err != nil {
			return ToolResult{}, err
		}
		roleInstructions = body
	}

	// 生成子智能体编号。
	d.mu.Lock()
	d.nextID++
	agentID := fmt.Sprintf("sub-%d", d.nextID)
	d.mu.Unlock()

	// 组装子智能体的完整提示：角色指令 + 任务描述。
	fullPrompt := args.Prompt
	if roleInstructions != "" {
		fullPrompt = "[role: " + args.Role + "]\n" + roleInstructions + "\n\n[task]\n" + args.Prompt
	}

	// 发出生成事件。
	d.emitSpawn(agentID, args.Role, args.Prompt)

	// 创建子事件桥接 sink 并运行子智能体。
	subSink := &SubagentSink{AgentID: agentID, Parent: d.eventSink}
	if d.factory == nil {
		return ToolResult{}, fmt.Errorf("delegate factory is not wired")
	}
	result, err := d.factory(ctx, args.Role, fullPrompt, args.MaxSteps, subSink)

	// 发出完成事件。
	if err != nil {
		d.emitComplete(agentID, args.Role, "failed: "+err.Error())
		return ToolResult{}, fmt.Errorf("subagent %s: %w", agentID, err)
	}
	d.emitComplete(agentID, args.Role, truncateRunes(result, 200))

	observation := fmt.Sprintf("[subagent %s (%s) completed]\n%s", agentID, args.Role, result)
	return ToolResult{
		Observation:   observation,
		Resource:      "delegate:" + agentID,
		OriginalBytes: len(result),
		VisibleBytes:  len(result),
	}, nil
}

// loadRole 从 .xcode/skills/ 加载角色技能正文；三个默认角色有内置兜底。
func (d *Delegate) loadRole(role string) (string, error) {
	path := filepath.Join(d.workspaceRoot, ".xcode", "skills", role+".md")
	data, err := os.ReadFile(path)
	if err != nil {
		if builtin, ok := builtinRoles[role]; ok {
			return builtin, nil // 技能文件缺失时使用内置兜底。
		}
		return "", fmt.Errorf("role %q not found in .xcode/skills/ and no builtin fallback", role)
	}
	return trimSkillMeta(string(data)), nil
}

// builtinRoles 保存三个默认角色的兜底指令；技能文件缺失时使用。
var builtinRoles = map[string]string{
	"planner":   "你是规划专家。分解任务、列出步骤与风险，不修改任何文件。",
	"developer": "你是实现专家。按任务最小化修改代码，运行测试验证。",
	"reviewer":  "你是独立审查者。只看代码本身，不看开发过程。输出 pass 或 issues-found。",
}

// SubagentSink 把子智能体的全部事件桥接到父的 sink，附 agent_id 标签。
type SubagentSink struct {
	AgentID string    // 子智能体标识，附加到每条事件。
	Parent  EventSink // 父的事件接收端；nil 时丢弃。
}

// Emit 把事件附上 agent_id 后转发给父 sink。
func (s *SubagentSink) Emit(event Event) {
	if s.Parent == nil {
		return
	}
	event.AgentID = s.AgentID
	s.Parent.Emit(event)
}

// emitSpawn 发出 agent.spawned 事件。
func (d *Delegate) emitSpawn(agentID, role, prompt string) {
	if d.eventSink == nil {
		return
	}
	d.eventSink.Emit(Event{
		Time:    time.Now().UTC(),
		Kind:    AgentSpawned,
		AgentID: agentID,
		Tool:    "delegate",
		Data:    fmt.Sprintf("role=%s prompt=%.100s", role, prompt),
	})
}

// emitComplete 发出 agent.completed 事件。
func (d *Delegate) emitComplete(agentID, role, summary string) {
	if d.eventSink == nil {
		return
	}
	d.eventSink.Emit(Event{
		Time:    time.Now().UTC(),
		Kind:    AgentCompleted,
		AgentID: agentID,
		Tool:    "delegate",
		Data:    summary,
	})
}

// truncateRunes 截取文本前 limit 个字符。
func truncateRunes(value string, limit int) string {
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit]) + "…"
}

// trimSkillMeta 去掉技能文件开头的 name:/description: 元信息行和分隔符。
func trimSkillMeta(content string) string {
	lines := strings.Split(content, "\n")
	for i, line := range lines {
		t := strings.TrimSpace(line)
		if t == "---" || t == "" {
			return strings.TrimSpace(strings.Join(lines[i+1:], "\n"))
		}
		if !strings.HasPrefix(t, "name:") && !strings.HasPrefix(t, "description:") {
			return strings.TrimSpace(content)
		}
	}
	return strings.TrimSpace(content)
}
