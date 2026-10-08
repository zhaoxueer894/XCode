package harness

// 公共类型：定义Model、Tool、Message、ToolCall和Event
// 即Harnees的核心协议和运行时类型
import (
	"context"       // 模型和工具调用之间传递取消、超时等信号
	"encoding/json" // 表达模型生成的原始json工具参数
	"time"
)

type Message struct {
	Role       string     `json:"role"`                   // 消息角色：tool、assistant、user
	Content    string     `json:"content,omitempty"`      // 消息正文
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`   // 本次要调用的tools
	ToolCallID string     `json:"tool_call_id,omitempty"` // tool消息对应的工具调用ID
}

// 模型希望harness执行的工具调用，以结构体定义
type ToolCall struct {
	ID        string          `json:"id"`        // 调用标识
	Name      string          `json:"name"`      // 工具名，如read_file
	Arguments json.RawMessage `json:"arguments"` // 原始json参数
}

// 单个工具说明：名称、用途、参数
type ToolSpec struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"input_schema"`
}

// ToolResult 保存工具的模型可见内容，以及完整结果在 Artifact Store 中的身份和大小。
type ToolResult struct {
	Observation   string // 保存真正追加进模型消息历史的受限预览或重复读取说明。
	ArtifactID    string // 保存完整工具结果的内容寻址编号。
	ContentHash   string // 保存源文件或完整结果的 SHA-256，用于识别内容变化。
	Resource      string // 保存结果对应的规范资源，例如 internal/harness/types.go#L1-L80。
	OriginalBytes int    // 保存完整工具结果的字节数。
	VisibleBytes  int    // 保存完整结果中实际进入模型上下文的字节数，不包含元数据标题。
	Truncated     bool   // 表示完整结果是否因为预览上限而只向模型展示一部分。
	RepeatedRead  bool   // 表示同一文件范围和内容仍然存在于当前 Context Epoch。
}

// Usage 保存一次模型请求的 Token 用量。
type Usage struct {
	InputTokens  int `json:"input_tokens,omitempty"`  // 输入消息与工具声明使用的 Token。
	OutputTokens int `json:"output_tokens,omitempty"` // 模型生成内容使用的 Token。
	TotalTokens  int `json:"total_tokens,omitempty"`  // 输入与输出 Token 总和。
}

// ModelEventKind 表示 Provider 无关的流式模型事件类型。
type ModelEventKind string

const (
	ModelTextDelta     ModelEventKind = "text.delta"      // 模型新增了一段文本。
	ModelToolCallDelta ModelEventKind = "tool_call.delta" // 模型新增了一段工具调用信息。
	ModelUsage         ModelEventKind = "usage"           // Provider 返回 Token 用量。
	ModelFinished      ModelEventKind = "finished"        // Provider 返回停止原因。
)

// ModelEvent 表示模型生成过程中的一条归一化流式事件。
type ModelEvent struct {
	Kind           ModelEventKind // 当前事件的类型。
	TextDelta      string         // 本次新增的文本片段。
	ToolCallIndex  int            // 工具调用在本次响应中的稳定位置。
	ToolCallID     string         // 工具调用 ID。
	ToolName       string         // 工具名称。
	ArgumentsDelta string         // 本次新增的 JSON 参数字符串片段。
	Usage          Usage          // usage 事件携带的 Token 统计。
	FinishReason   string         // finished 事件携带的停止原因。
}

// ModelEventSink 接收模型生成过程中的流式事件。
type ModelEventSink interface {
	EmitModelEvent(ModelEvent)
}

// ModelEventSinkFunc 让普通函数可以直接充当 ModelEventSink。
type ModelEventSinkFunc func(ModelEvent)

// EmitModelEvent 负责把一条流式模型事件交给被包装的函数处理。
func (f ModelEventSinkFunc) EmitModelEvent(event ModelEvent) {
	f(event)
}

// 一次模型调用的归一化结果：可以返回Content ｜ ToolCalls调用
type ModelResponse struct {
	Content      string     // 文本
	ToolCalls    []ToolCall // 工具调用列表
	Usage        Usage      // 本次请求最终 Token 用量。
	FinishReason string     // Provider 返回的停止原因。
}

// Model接口： 隔离不同模型供应商协议差异，实现Generate方法
// 模型接收当前消息历史、可用工具说明，返回ModelResponse
type Model interface {
	Generate(context.Context, []Message, []ToolSpec, ModelEventSink) (ModelResponse, error)
}

// Tool接口:实现 方法1.获取工具Spec 2.解析参数并执行Tool
// 首字母大写，其他包可以用
// 起执行结果区分完整 Artifact 与模型可见 Observation。
type Tool interface {
	Spec() ToolSpec
	Execute(context.Context, json.RawMessage) (ToolResult, error)
}

// ToolRunner 是 Agent 使用的统一工具执行入口；Registry 和安全 Executor 都实现它。
type ToolRunner interface {
	Specs() []ToolSpec
	Execute(context.Context, ToolCall) (ToolResult, error)
}

// 结构化事件类型（自定义字符串类型）
type EventKind string

// 常量枚举
// 常量名称 常量类型 常量值
const (
	RunStarted     EventKind = "run.started"     // 一次Agent Run已经开始
	ModelCompleted EventKind = "model.completed" // 一次模型调用已完成
	ToolStarted    EventKind = "tool.started"    // 一次工具调用已开始
	ToolCompleted  EventKind = "tool.completed"  // 一次工具调用已完成
	RunCompleted   EventKind = "run.completed"   // Agent Run正常结束
	RunFailed      EventKind = "run.failed"      // Agent Run因错误结束
)

// 执行管线事件：意图、策略、审批与写前快照。
const (
	ToolPlanned       EventKind = "tool.planned"       // 工具已声明本次调用的副作用意图。
	PolicyDecided     EventKind = "policy.decided"     // 策略引擎给出 allow/ask/deny 决策。
	ApprovalResolved  EventKind = "approval.resolved"  // 用户对 ask 决策给出批准或拒绝。
	CheckpointCreated EventKind = "checkpoint.created" // 修改前的文件快照已建立。
)

// 上下文管理事件：投影构建与压缩发生。
const (
	ContextBuilt     EventKind = "context.built"     // 一次模型请求前已生成预算内投影。
	ContextCompacted EventKind = "context.compacted" // 本次投影发生了压缩并推进了 Epoch。
)

// 工具调用状态事件：journal 迁移同步进入 Trace。
const (
	ToolCallState EventKind = "toolcall.state" // 一次工具调用的状态迁移；data 保存状态名。
)

// 完成门禁事件：门禁拒绝或放行模型的完成声明。
const (
	CompletionGateBlocked EventKind = "completion.gate.blocked" // 门禁拒绝完成声明；data 保存阻塞原因。
	CompletionGatePassed  EventKind = "completion.gate.passed"  // 门禁放行；data 保存当时的工作区指纹。
)

// 多智能体生命周期事件。
const (
	AgentSpawned   EventKind = "agent.spawned"
	AgentCompleted EventKind = "agent.completed"
)

// 流式模型运行事件。
const (
	ModelTextDeltaEmitted     EventKind = "model.text.delta"      // 模型新增文本片段。
	ModelToolCallDeltaEmitted EventKind = "model.tool_call.delta" // 模型新增工具调用片段。
	ModelUsageEmitted         EventKind = "model.usage"           // 模型返回 Token 用量。
	ModelFinishedEmitted      EventKind = "model.finished"        // 模型返回停止原因。
)

// 结构化运行记录
type Event struct {
	Time          time.Time  `json:"time"`
	Kind          EventKind  `json:"kind"`
	Step          int        `json:"step,omitempty"`            // 当前模型迭代步数
	Tool          string     `json:"tool,omitempty"`            // 相关工具名称
	ToolCallID    string     `json:"tool_call_id,omitempty"`    // 相关工具调用 ID。
	ToolCallIndex *int       `json:"tool_call_index,omitempty"` // 工具增量的 index；指针使有效的 0 不被省略。
	Data          string     `json:"data,omitempty"`            // 事件携带的文本数据
	ArtifactID    string     `json:"artifact_id,omitempty"`     // 保存完整工具结果的 Artifact ID。
	ContentHash   string     `json:"content_hash,omitempty"`    // 保存源文件或完整结果的 SHA-256。
	Resource      string     `json:"resource,omitempty"`        // 保存工具结果对应的规范资源名称。
	CheckpointID  string     `json:"checkpoint_id,omitempty"`   // 保存写前快照的编号，恢复时使用。
	OriginalBytes int        `json:"original_bytes,omitempty"`  // 保存完整工具结果的字节数。
	VisibleBytes  int        `json:"visible_bytes,omitempty"`   // 保存原始结果中进入模型上下文的字节数。
	Truncated     bool       `json:"truncated,omitempty"`       // 表示模型收到的结果是否为受限预览。
	RepeatedRead  bool       `json:"repeated_read,omitempty"`   // 表示同一内容范围是否仍在当前 Context Epoch 可见。
	InputTokens   int        `json:"input_tokens,omitempty"`    // 输入 Token 用量。
	OutputTokens  int        `json:"output_tokens,omitempty"`   // 输出 Token 用量。
	TotalTokens   int        `json:"total_tokens,omitempty"`    // 总 Token 用量。
	FinishReason  string     `json:"finish_reason,omitempty"`   // 模型停止原因。
	StopReason    StopReason `json:"stop_reason,omitempty"`     // Harness 层的 Run 结束原因。
	ContextTokens int        `json:"context_tokens,omitempty"`  // 保存投影后的估算 token 数（context.built）。
	ContextEpoch  uint64     `json:"context_epoch,omitempty"`   // 保存投影发生时的 Context Epoch。
	Error         string     `json:"error,omitempty"`           // 失败原因
	AgentID       string     `json:"agent_id,omitempty"`        // 子智能体标识；空表示编排者自身。
}

// Agent事件Sink接口
type EventSink interface {
	Emit(Event)
}

// 适配器：让普通函数也能作为EventSink使用
type EventSinkFunc func(Event)

// Emit 调用函数本身，从而让EventSinkFunc满足EventSink接口
func (f EventSinkFunc) Emit(event Event) {
	f(event)
}
