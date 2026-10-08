package contextmgr

// 本包实现模型可见投影的管理：token 估算、Context Epoch 与三种压缩策略。
// 完整会话历史由 Agent 持有且永不被修改；本包只读取它并返回新的投影切片。

import (
	"context" // 摘要器调用需要取消信号。
	"fmt"     // 构造占位符、交接摘要与错误。
	"strings" // 从工具结果头部提取资源名。
	"sync/atomic"

	"xcode/internal/harness"
)

// Strategy 表示模型可见投影采用的管理策略。
type Strategy string

const (
	StrategyNone       Strategy = "none"       // 不压缩，完整历史直接投影。
	StrategySliding    Strategy = "sliding"    // 超预算时把较旧消息替换为占位说明。
	StrategyHandoff    Strategy = "handoff"    // 超预算时重置历史，只保留结构化交接。
	StrategyCompaction Strategy = "compaction" // 滑动裁剪之后，再请模型把被裁剪区域总结成摘要。

	// StrategyDecisionGated 按证据单元执行 KEEP/TRUNCATE/DROP 的决策门控压缩。
	// 与滑动窗口的区别：固定保留目标、未解决错误与验证证据，只读工具结果整体移出。
	StrategyDecisionGated Strategy = "decision-gated"
)

const (
	defaultBudgetTokens   = 12000 // 未指定预算时使用的默认估算 token 上限。
	keepRecentMessages    = 6     // 压缩时始终保留在上下文里的最近消息数。
	maxHandoffResources   = 20    // 交接摘要最多列出的文件资源数。
	maxHandoffPromptRunes = 800   // 交接摘要中原始任务说明的最大字符数。
)

// placeholderTag 是被裁剪消息的占位符前缀，评测与排错都靠它识别被裁剪区域。
const placeholderTag = "[context-pruned]"

// EstimateTokens 用确定性启发式估算一段文本的 token 数量。
// ASCII 字符平均约 4 个折算 1 token；非 ASCII 字符按 1 字符 1 token 估算。
// 该估算只用于压缩决策与指标统计，不声称等于 Provider 的真实计费数。
func EstimateTokens(text string) int {
	ascii := 0
	other := 0
	for _, r := range text {
		if r < 128 {
			ascii++
		} else {
			other++
		}
	}
	return (ascii+3)/4 + other
}

// EstimateMessagesTokens 估算整段消息历史的 token 总量，包含工具调用参数。
func EstimateMessagesTokens(messages []harness.Message) int {
	total := 0
	for _, message := range messages {
		total += EstimateTokens(message.Content) + 4
		for _, call := range message.ToolCalls {
			total += EstimateTokens(string(call.Arguments))
		}
	}
	return total
}

// Epoch 保存当前 Context Epoch 的原子计数器；压缩发生时递增。
// ReadSet 用它判定"同范围同哈希"是否仍然等于"无需重新注入"。
type Epoch struct {
	value atomic.Uint64 // 保存当前纪元值；初始为 1。
}

// NewEpoch 创建从 1 开始的纪元计数器。
func NewEpoch() *Epoch {
	epoch := &Epoch{}
	epoch.value.Store(1)
	return epoch
}

// Current 返回当前纪元值。
func (e *Epoch) Current() uint64 { return e.value.Load() }

// Advance 递增并返回新纪元值；每次真实压缩恰好调用一次。
func (e *Epoch) Advance() uint64 { return e.value.Add(1) }

// Summarizer 把一段被裁剪的旧历史总结成摘要文本；由具备总结能力的模型实现。
// 返回错误时调用方回退为确定性占位符，不让摘要失败中断运行。
type Summarizer func(ctx context.Context, oldText string) (string, error)

// NoteRecorder 接收 handoff 产生的会话笔记；由 L1 会话记忆实现。
type NoteRecorder interface {
	RecordHandoff(content string) error
}

// Manager 依据策略与预算把完整历史投影成模型可见消息。
// 投影结果直接使用 harness.Projection；类型统一定义在 harness 包，
// 这样 *Manager 天然满足 harness.ContextManager 接口，不需要适配层。
type Manager struct {
	strategy      Strategy          // 保存本管理器使用的压缩策略。
	budget        int               // 保存触发压缩的估算 token 上限。
	epoch         *Epoch            // 保存共享纪元计数器。
	summarizer    Summarizer        // 保存可选的模型摘要器；compaction 策略使用。
	notes         NoteRecorder      // 保存可选的会话笔记入口；handoff 策略使用。
	decider       CompactionDecider // 保存 decision-gated 策略的生效决策器；nil 使用 RuleDecider。
	shadow        CompactionDecider // 保存可选的 shadow 决策器；判定被记录但不生效（Jev 模式的自研实现；TypeSafe Jev 产品未接入）。
	lastDecisions []UnitDecision    // 保存最近一次门控压缩的生效判定，供测试与追溯。
	lastShadow    []ShadowRecord    // 保存最近一次 shadow 对照记录；未启用 shadow 时为空。
}

// NewManager 校验参数并创建投影管理器；策略为空视为 none，预算小于等于 0 使用默认值。
func NewManager(strategy Strategy, budget int, epoch *Epoch) (*Manager, error) {
	normalized := Strategy(strings.ToLower(strings.TrimSpace(string(strategy))))
	switch normalized {
	case "", StrategyNone:
		normalized = StrategyNone
	case StrategySliding, StrategyHandoff, StrategyCompaction, StrategyDecisionGated:
	default:
		return nil, fmt.Errorf("unknown context strategy %q", strategy)
	}
	if budget <= 0 {
		budget = defaultBudgetTokens
	}
	if epoch == nil {
		epoch = NewEpoch()
	}
	return &Manager{strategy: normalized, budget: budget, epoch: epoch}, nil
}

// SetSummarizer 设置模型摘要器；nil 表示回退为占位符。
func (m *Manager) SetSummarizer(summarizer Summarizer) { m.summarizer = summarizer }

// SetNoteRecorder 设置会话笔记入口；nil 表示 handoff 只投影不落盘。
func (m *Manager) SetNoteRecorder(recorder NoteRecorder) { m.notes = recorder }

// Strategy 返回管理器当前策略。
func (m *Manager) Strategy() Strategy { return m.strategy }

// SetCompactionDecider 设置 decision-gated 策略的生效决策器；nil 使用确定性 RuleDecider。
func (m *Manager) SetCompactionDecider(decider CompactionDecider) { m.decider = decider }

// SetShadowDecider 设置 shadow 决策器（实验通道）：其判定被记录并与生效判定对照，但绝不生效。
func (m *Manager) SetShadowDecider(decider CompactionDecider) { m.shadow = decider }

// LastDecisions 返回最近一次门控压缩的生效判定；用于测试与决策追溯。
func (m *Manager) LastDecisions() []UnitDecision { return m.lastDecisions }

// LastShadow 返回最近一次 shadow 对照记录；未启用 shadow 时为空。
func (m *Manager) LastShadow() []ShadowRecord { return m.lastShadow }

// ShadowSummary 返回最近一次 shadow 对照的一行汇总，满足 harness.ShadowSummarizer 接口；
// Agent 在 context.compacted 事件中携带它，使 shadow 对照可从 Trace 重算。
func (m *Manager) ShadowSummary() string {
	if len(m.lastShadow) == 0 {
		return "shadow=none"
	}
	agree, disagree, fallback := 0, 0, 0
	for _, record := range m.lastShadow {
		switch {
		case record.Fallback:
			fallback++
		case record.Agree:
			agree++
		default:
			disagree++
		}
	}
	return fmt.Sprintf("shadow=%d agree=%d disagree=%d fallback=%d", len(m.lastShadow), agree, disagree, fallback)
}

// Project 按策略把完整历史投影成模型可见消息。
// 返回的切片要么是调用方切片的原样引用（未压缩），要么是全新构造的切片（压缩）；
// 两种情况下调用方传入的切片内容都不会被修改。
func (m *Manager) Project(ctx context.Context, messages []harness.Message) harness.Projection {
	tokens := EstimateMessagesTokens(messages)
	base := harness.Projection{Messages: messages, Tokens: tokens, Epoch: m.epoch.Current()}
	// 历史还很短时无论策略都不压缩，保证小任务零干扰。
	if m.strategy == StrategyNone || tokens <= m.budget || len(messages) <= keepRecentMessages+1 {
		return base
	}
	switch m.strategy {
	case StrategySliding:
		return m.slide(ctx, messages, tokens, false)
	case StrategyCompaction:
		return m.slide(ctx, messages, tokens, true)
	case StrategyHandoff:
		return m.handoff(messages, tokens)
	case StrategyDecisionGated:
		return m.decisionGate(ctx, messages, tokens)
	}
	return base
}

// slide 执行滑动裁剪：保留首条任务消息与最近 keepRecentMessages 条，
// 中间区域替换为占位符（compaction 时替换为模型摘要），并推进纪元。
func (m *Manager) slide(ctx context.Context, messages []harness.Message, tokens int, summarize bool) harness.Projection {
	dropped := messages[1 : len(messages)-keepRecentMessages]
	droppedTokens := tokens - EstimateMessagesTokens(messages[len(messages)-keepRecentMessages:]) - EstimateTokens(messages[0].Content)

	replacement := harness.Message{Role: "user"}
	if summarize && m.summarizer != nil {
		if summary, err := m.summarizer(ctx, renderMessages(dropped)); err == nil && strings.TrimSpace(summary) != "" {
			replacement.Content = fmt.Sprintf(
				"[compacted-summary] 更早的 %d 条消息（约 %d token）已被模型总结如下：\n%s",
				len(dropped), droppedTokens, strings.TrimSpace(summary))
		}
	}
	if replacement.Content == "" {
		replacement.Content = fmt.Sprintf(
			"%s 更早的 %d 条消息（约 %d token）已移出上下文；完整历史仍在可持久化记录中，可用 read_file 的 artifact_id 参数回取。",
			placeholderTag, len(dropped), droppedTokens)
	}

	projected := make([]harness.Message, 0, keepRecentMessages+2)
	projected = append(projected, messages[0], replacement)
	projected = append(projected, messages[len(messages)-keepRecentMessages:]...)
	return harness.Projection{
		Messages:  projected,
		Tokens:    EstimateMessagesTokens(projected),
		Epoch:     m.epoch.Advance(),
		Compacted: true,
		Kind:      strategyKind(summarize),
	}
}

// handoff 执行上下文重置：只保留原始任务加结构化交接摘要，并推进纪元。
func (m *Manager) handoff(messages []harness.Message, tokens int) harness.Projection {
	note := buildHandoffNote(messages)
	projected := []harness.Message{{Role: "user", Content: note}}
	if m.notes != nil {
		// 落盘失败不影响本次运行：L1 笔记是证据存档，不是主流程依赖。
		_ = m.notes.RecordHandoff(note)
	}
	return harness.Projection{
		Messages:  projected,
		Tokens:    EstimateMessagesTokens(projected),
		Epoch:     m.epoch.Advance(),
		Compacted: true,
		Kind:      "handoff",
	}
}

// buildHandoffNote 生成结构化交接摘要：原始任务、工具调用统计、触碰过的资源与最后的错误。
func buildHandoffNote(messages []harness.Message) string {
	var builder strings.Builder
	prompt := []rune(messages[0].Content)
	if len(prompt) > maxHandoffPromptRunes {
		prompt = prompt[:maxHandoffPromptRunes]
	}
	fmt.Fprintf(&builder, "%s\n[handoff] 以下为本任务的上下文交接摘要：\n", string(prompt))

	toolCalls := 0
	errors := 0
	entries := make([]string, 0, 8)
	seen := make(map[string]struct{})
	lastToolTail := ""
	for _, message := range messages {
		if message.Role != "tool" {
			continue
		}
		toolCalls++
		if strings.HasPrefix(message.Content, "ERROR:") {
			errors++
		}
		if resource := extractResource(message.Content); resource != "" {
			if _, exists := seen[resource]; !exists && len(entries) < maxHandoffResources {
				seen[resource] = struct{}{}
				if artifactID := extractTagged(message.Content, "artifact_id"); artifactID != "" {
					entries = append(entries, fmt.Sprintf("%s (artifact=%s)", resource, artifactID))
				} else {
					entries = append(entries, resource)
				}
			}
		}
		lastToolTail = tailRunes(message.Content, 200)
	}
	fmt.Fprintf(&builder, "工具调用 %d 次，其中错误 %d 次。\n", toolCalls, errors)
	if len(entries) > 0 {
		fmt.Fprintf(&builder, "已完成的读取（不要重复执行；如需内容请改用 read_file 的 artifact_id 参数按行回取）：\n")
		for _, entry := range entries {
			fmt.Fprintf(&builder, "- %s\n", entry)
		}
	}
	if lastToolTail != "" {
		fmt.Fprintf(&builder, "最后一个工具结果结尾：\n%s\n", lastToolTail)
	}
	fmt.Fprintf(&builder, "请基于以上交接信息继续原始任务：已完成的步骤不要重做；信息不足时最多回取少量范围，然后直接给出最终答案。\n")
	return builder.String()
}

// extractResource 从工具结果头部的 [resource=...] 标记中提取资源名。
func extractResource(content string) string {
	start := strings.Index(content, "[resource=")
	if start < 0 {
		return ""
	}
	rest := content[start+len("[resource="):]
	end := strings.IndexAny(rest, " ]")
	if end < 0 {
		return ""
	}
	return rest[:end]
}

// extractTagged 从工具结果头部的 key=value 标记中提取指定键的值，例如 artifact_id。
func extractTagged(content, key string) string {
	start := strings.Index(content, key+"=")
	if start < 0 {
		return ""
	}
	rest := content[start+len(key)+1:]
	end := strings.IndexAny(rest, " ]\n")
	if end < 0 {
		return ""
	}
	return rest[:end]
}

// renderMessages 把消息历史渲染成供摘要器阅读的纯文本。
func renderMessages(messages []harness.Message) string {
	var builder strings.Builder
	for _, message := range messages {
		fmt.Fprintf(&builder, "[%s] %s\n", message.Role, message.Content)
	}
	return builder.String()
}

// tailRunes 截取文本末尾最多 limit 个字符。
func tailRunes(value string, limit int) string {
	runes := []rune(value)
	if len(runes) <= limit {
		return strings.TrimSpace(value)
	}
	return strings.TrimSpace(string(runes[len(runes)-limit:]))
}

// strategyKind 返回压缩事件里记录的种类名。
func strategyKind(summarize bool) string {
	if summarize {
		return "compaction"
	}
	return "sliding"
}

// splitUnits 把消息历史拆成证据单元：goal（首条）单独成单元，
// assistant 的每次工具调用与其全部 tool 回应构成完整证据对，其余消息各成单元。
func splitUnits(messages []harness.Message) []Unit {
	units := make([]Unit, 0, len(messages))
	if len(messages) == 0 {
		return units
	}
	// 目标（首条消息）单独成单元 0；规则决策器与投影组装都依赖这个下标约定。
	units = append(units, Unit{Messages: []harness.Message{messages[0]}})
	index := 1
	for index < len(messages) {
		message := messages[index]
		if message.Role == "assistant" && len(message.ToolCalls) > 0 {
			unit := Unit{Messages: []harness.Message{message}}
			index++
			// 吸收本 assistant 全部工具调用的回应；回应按 tool_call_id 配对，顺序无关。
			pending := make(map[string]struct{}, len(message.ToolCalls))
			for _, call := range message.ToolCalls {
				pending[call.ID] = struct{}{}
			}
			for index < len(messages) && len(pending) > 0 {
				if messages[index].Role == "tool" {
					if _, ok := pending[messages[index].ToolCallID]; ok {
						delete(pending, messages[index].ToolCallID)
						unit.Messages = append(unit.Messages, messages[index])
						index++
						continue
					}
				}
				break
			}
			units = append(units, unit)
			continue
		}
		units = append(units, Unit{Messages: []harness.Message{message}})
		index++
	}
	return units
}

// decisionGate 执行决策门控压缩：拆分证据单元 → 决策器判定 →（可选）shadow 对照 →
// 按判定组装投影。KEEP 单元原样保留；TRUNCATE 单元保留结构但内容压成一行摘要；
// DROP 单元整体移出，并用一条汇总说明告诉模型可经 artifact_id 回取。
func (m *Manager) decisionGate(ctx context.Context, messages []harness.Message, tokens int) harness.Projection {
	units := splitUnits(messages)
	decider := m.decider
	if decider == nil {
		decider = RuleDecider{}
	}
	decisions := decider.Decide(units)
	m.lastDecisions = decisions

	// shadow 通道：判定只记录不生效；失败已由 TypedModelDecider 内部回退为规则判定。
	m.lastShadow = nil
	if m.shadow != nil {
		shadowDecisions := m.shadow.Decide(units)
		records := make([]ShadowRecord, 0, len(decisions))
		for i, applied := range decisions {
			record := ShadowRecord{Index: applied.Index, Applied: applied.Decision}
			if i < len(shadowDecisions) {
				record.Shadow = shadowDecisions[i].Decision
				record.Agree = record.Shadow == record.Applied
			} else {
				record.Fallback = true
				record.Note = "shadow returned fewer decisions"
			}
			records = append(records, record)
		}
		m.lastShadow = records
	}

	// 按判定组装投影；单元 0 就是目标，统一走判定分支。
	projected := make([]harness.Message, 0, len(messages))
	dropped, truncated, kept := 0, 0, 0
	for i, decision := range decisions {
		// 固定保留不变量：目标单元（0）永远 KEEP——拍板分类器建议丢弃目标时强制覆盖，
		// 否则投影可能为空，模型失去任务目标（实测曾导致切片越界 panic）。
		if decision.Index == 0 && decision.Decision != DecisionKeep {
			decision.Reason = "goal (classifier suggested " + string(decision.Decision) + ", overridden)"
			decision.Decision = DecisionKeep
			decisions[i] = decision
		}
		switch decision.Decision {
		case DecisionKeep:
			projected = append(projected, units[i].Messages...)
			kept++
		case DecisionTruncate:
			truncated++
			for _, message := range units[i].Messages {
				projected = append(projected, harness.Message{
					Role: message.Role, Content: truncateUnitContent(message.Content),
					ToolCalls: message.ToolCalls, ToolCallID: message.ToolCallID,
				})
			}
		default:
			dropped++
		}
	}
	if len(projected) == 0 { // 兜底：理论上目标强制保留后不会发生。
		projected = append(projected, messages[0])
	}
	if dropped > 0 || truncated > 0 {
		summary := harness.Message{Role: "user", Content: fmt.Sprintf(
			"[decision-gated] %s 按证据单元压缩完成：保留 %d、截断 %d、移出 %d 个单元；被移出的只读结果可经 read_file 的 artifact_id 参数按行回取。",
			placeholderTag, kept, truncated, dropped)}
		// 插入到 goal 之后，保持"目标优先"的顺序。
		projected = append(projected[:1], append([]harness.Message{summary}, projected[1:]...)...)
	}
	return harness.Projection{
		Messages:  projected,
		Tokens:    EstimateMessagesTokens(projected),
		Epoch:     m.epoch.Advance(),
		Compacted: true,
		Kind:      string(StrategyDecisionGated),
	}
}

// truncateUnitContent 把单元内单条消息的内容压缩为不超过 160 字符的一行摘要，
// 保留角色与配对结构，使"每个 tool_call 都有对应 tool 消息"的协议继续成立。
func truncateUnitContent(content string) string {
	trimmed := strings.Join(strings.Fields(content), " ")
	runes := []rune(trimmed)
	if len(runes) <= 160 {
		return trimmed
	}
	return string(runes[:159]) + "…"
}
