// Package bailian 实现阿里云百炼 OpenAI-compatible Chat Completions Provider。
package bailian

// chatRequest 表示发送给百炼 /chat/completions 的请求体。
type chatRequest struct {
	Model         string        `json:"model"`           // 百炼模型 ID，例如 qwen-plus。
	Messages      []chatMessage `json:"messages"`        // 当前完整消息历史。
	Tools         []chatTool    `json:"tools,omitempty"` // 可用本地工具声明。
	Stream        bool          `json:"stream"`          // true 表示使用 SSE 流式输出。
	StreamOptions streamOptions `json:"stream_options"`  // 控制流式响应附加信息。
}

// streamOptions 控制百炼流式响应是否在最后返回 Token 用量。
type streamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

// chatMessage 表示百炼协议中的一条消息。
type chatMessage struct {
	Role       string         `json:"role"`                   // user、assistant 或 tool。
	Content    string         `json:"content,omitempty"`      // 文本正文。
	ToolCalls  []chatToolCall `json:"tool_calls,omitempty"`   // assistant 发起的工具调用。
	ToolCallID string         `json:"tool_call_id,omitempty"` // tool 结果关联的调用 ID。
}

// chatTool 表示请求中的 function 工具声明。
type chatTool struct {
	Type     string           `json:"type"`     // Function Calling 使用 function。
	Function chatToolFunction `json:"function"` // 工具名称、说明和参数 Schema。
}

// chatToolFunction 描述一个可由模型请求的本地函数。
type chatToolFunction struct {
	Name        string         `json:"name"`        // 工具唯一名称。
	Description string         `json:"description"` // 工具用途。
	Parameters  map[string]any `json:"parameters"`  // 工具参数 JSON Schema。
}

// chatToolCall 表示请求消息中已经完整的工具调用。
type chatToolCall struct {
	ID       string               `json:"id"`       // 原始调用 ID。
	Type     string               `json:"type"`     // 固定为 function。
	Function chatToolCallFunction `json:"function"` // 完整名称和参数字符串。
}

// chatToolCallFunction 保存完整工具名称与 JSON 参数字符串。
type chatToolCallFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// chatChunk 表示 SSE data 行中的一条 JSON 流式响应对象。
type chatChunk struct {
	Choices []chatChoice `json:"choices"`         // 普通内容；usage-only chunk 可为空。
	Usage   *chatUsage   `json:"usage,omitempty"` // 最终 Token 用量。
}

// chatChoice 保存一个候选结果的增量和停止原因。
type chatChoice struct {
	Index        int       `json:"index"`         // 候选序号；只使用一个候选。
	Delta        chatDelta `json:"delta"`         // 本次新增的文本或工具调用信息。
	FinishReason string    `json:"finish_reason"` // null 会解码为空字符串。
}

// chatDelta 表示一次流式响应中新增加的内容。
type chatDelta struct {
	Content   string              `json:"content"`    // 本次新增文本。
	ToolCalls []chatToolCallDelta `json:"tool_calls"` // 本次新增工具调用片段。
}

// chatToolCallDelta 使用 Index 关联同一工具调用的多个片段。
type chatToolCallDelta struct {
	Index    int                       `json:"index"`    // 工具调用在响应中的稳定位置。
	ID       string                    `json:"id"`       // 通常只在首个片段出现。
	Type     string                    `json:"type"`     // 通常为 function。
	Function chatToolCallFunctionDelta `json:"function"` // 名称和参数增量。
}

// chatToolCallFunctionDelta 保存工具名称与参数增量。
type chatToolCallFunctionDelta struct {
	Name      string `json:"name"`      // 工具名称，通常只在首个片段出现。
	Arguments string `json:"arguments"` // 按到达顺序追加的参数片段。
}

// chatUsage 表示百炼协议中的 Token 统计字段。
type chatUsage struct {
	PromptTokens     int `json:"prompt_tokens"`     // 输入 Token。
	CompletionTokens int `json:"completion_tokens"` // 输出 Token。
	TotalTokens      int `json:"total_tokens"`      // 总 Token。
}

// apiErrorEnvelope 兼容 OpenAI-compatible 嵌套错误和百炼顶层错误格式。
type apiErrorEnvelope struct {
	Error     *apiErrorBody `json:"error,omitempty"` // OpenAI-compatible 错误详情。
	Code      any           `json:"code,omitempty"`  // 百炼顶层错误码。
	Message   string        `json:"message,omitempty"`
	RequestID string        `json:"request_id,omitempty"`
}

// apiErrorBody 只保存允许进入安全诊断信息的错误字段。
type apiErrorBody struct {
	Message string `json:"message"`
	Type    string `json:"type"`
	Code    any    `json:"code"`
}
