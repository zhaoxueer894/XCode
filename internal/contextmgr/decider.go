package contextmgr

// 本文件实现决策门控可逆压缩：把完整历史拆成"证据单元"，由决策器对每个
// 单元给出 KEEP / TRUNCATE / DROP 三值判定，再按判定组装模型可见投影。
// 固定保留项：当前目标（首条消息）、未解决错误、验证证据（exit_code 的工具结果）、
// 最近窗口内的全部单元。原文永远留在调用方的 Durable Transcript 中——压缩只改变
// 投影（核心不变量 1 与 2），因此任何决策都可通过重新投影"回退"。

import (
	"context" // shadow 决策器调用模型时需要取消信号。
	"strings" // 识别错误前缀、退出码标记与只读工具结果。

	"xcode/internal/harness" // 复用 Message 协议类型。
)

// Decision 表示一个证据单元在投影中的去留判定。
type Decision string

const (
	DecisionKeep     Decision = "keep"     // 单元完整保留在投影中。
	DecisionTruncate Decision = "truncate" // 单元保留结构但内容压缩为摘要。
	DecisionDrop     Decision = "drop"     // 单元整体移出投影（可经 artifact_id 回取）。
)

// Unit 是一个证据单元：构成"assistant 工具调用 + 其全部 tool 回应"的完整证据对，
// 或一条独立消息（目标、纯文本等）。单元是压缩的最小粒度——工具调用与结果永不拆散。
type Unit struct {
	Messages []harness.Message // 保存本单元包含的有序消息；assistant 的 ToolCalls 与其 tool 回应同属一个单元。
}

// UnitDecision 保存一个单元的判定与一行理由；进入决策日志供追溯。
type UnitDecision struct {
	Index    int      // 保存单元在本次拆分中的序号，从 0 开始。
	Decision Decision // 保存判定结果。
	Reason   string   // 保存得出判定的一行理由，例如 "recent window" 或 "read-only externalized"。
}

// ShadowRecord 保存 shadow 决策器与实际生效决策的对照；shadow 失败时记录回退。
type ShadowRecord struct {
	Index    int      // 保存单元序号。
	Applied  Decision // 保存实际生效（规则决策器给出）的判定。
	Shadow   Decision // 保存 shadow 决策器给出的判定；失败时为空。
	Agree    bool     // 表示两者是否一致。
	Fallback bool     // 表示 shadow 是否因失败或低置信度回退到规则判定。
	Note     string   // 保存失败原因或补充说明。
}

// CompactionDecider 对一组证据单元逐个给出判定；返回切片长度必须等于 units 长度。
type CompactionDecider interface {
	Decide(units []Unit) []UnitDecision
}

// RuleDecider 是确定性规则决策器：固定保留项之外，只读工具对移出、旧散文截断。
// 它是 decision-gated 策略的默认决策器，也是 shadow 决策器的回退基准。
type RuleDecider struct{}

// Decide 按固定规则给出全部单元的判定。
func (RuleDecider) Decide(units []Unit) []UnitDecision {
	total := 0
	for _, unit := range units {
		total += len(unit.Messages)
	}
	recentStart := total - keepRecentMessages // 最近窗口的起始消息下标（可能为负）。

	decisions := make([]UnitDecision, 0, len(units))
	cursor := 0 // 当前单元首条消息的全局下标。
	for index, unit := range units {
		end := cursor + len(unit.Messages)
		decision, reason := decideUnit(unit, cursor, end, recentStart, index)
		decisions = append(decisions, UnitDecision{Index: index, Decision: decision, Reason: reason})
		cursor = end
	}
	return decisions
}

// decideUnit 对单个单元应用规则；cursor/end 是它在整体消息序列中的位置。
func decideUnit(unit Unit, cursor, end, recentStart, index int) (Decision, string) {
	switch {
	case index == 0:
		return DecisionKeep, "current goal"
	case end > recentStart:
		return DecisionKeep, "recent window"
	case hasUnresolvedError(unit):
		return DecisionKeep, "unresolved error"
	case hasVerificationEvidence(unit):
		return DecisionKeep, "verification evidence"
	case isReadOnlyPair(unit):
		return DecisionDrop, "read-only result externalized in artifacts"
	default:
		return DecisionTruncate, "old prose truncated to headline"
	}
}

// hasUnresolvedError 判断单元内是否有以 ERROR: 开头的工具结果（未解决错误固定保留）。
func hasUnresolvedError(unit Unit) bool {
	for _, message := range unit.Messages {
		if message.Role == "tool" && strings.HasPrefix(message.Content, "ERROR:") {
			return true
		}
	}
	return false
}

// hasVerificationEvidence 判断单元内是否携带退出码标记的命令结果（验证证据固定保留）。
func hasVerificationEvidence(unit Unit) bool {
	for _, message := range unit.Messages {
		if message.Role == "tool" && strings.Contains(message.Content, "exit_code=") {
			return true
		}
	}
	return false
}

// isReadOnlyPair 判断单元是否是"assistant 只读工具调用 + 其结果"的证据对；
// 只读结果的内容都已进入 Artifact Store，可随时按 artifact_id 回取。
func isReadOnlyPair(unit Unit) bool {
	if len(unit.Messages) < 2 || unit.Messages[0].Role != "assistant" || len(unit.Messages[0].ToolCalls) == 0 {
		return false
	}
	for _, call := range unit.Messages[0].ToolCalls {
		switch call.Name {
		case "read_file", "list_files", "search_text", "read_skill", "git_diff":
		default:
			return false
		}
	}
	return true
}

// ClassificationFunc 是 shadow 决策器依赖的类型化分类调用：输入证据单元，输出逐单元判定。
// 由调用方用具备结构化输出能力的模型实现；任何失败都应通过 error 返回，由 Manager 回退。
type ClassificationFunc func(ctx context.Context, units []Unit) ([]UnitDecision, error)

// TypedModelDecider 是实验性的类型化决策器（Jev 模式的自研实现，未接入 TypeSafe Jev 产品）：
// 把压缩判定外包给一个快速分类模型。真接入 TypeSafe Jev 需另行立项（OpenRouter 端点 + Key + 外发授权）。
// 它只作为 shadow 决策器使用——判定被记录并与规则决策对照，但绝不直接生效；
// 分类失败或置信度不足时回退确定性规则。
type TypedModelDecider struct {
	classify    ClassificationFunc // 保存实际的类型化分类调用。
	minConfide  float64            // 保存接受的最低置信度；低于该值按回退处理。
	LastAttempt string             // 保存最近一次 shadow 尝试的结果说明，供诊断。
}

// NewTypedModelDecider 创建 shadow 决策器；minConfidence 小于等于 0 时使用默认 0.6。
func NewTypedModelDecider(classify ClassificationFunc, minConfidence float64) *TypedModelDecider {
	if minConfidence <= 0 {
		minConfidence = 0.6
	}
	return &TypedModelDecider{classify: classify, minConfide: minConfidence}
}

// Decide 调用分类模型并校验输出；单元数不符、出现低置信度或任何错误都整体回退。
func (j *TypedModelDecider) Decide(units []Unit) []UnitDecision {
	if j == nil || j.classify == nil {
		j.LastAttempt = "no classifier wired; fallback"
		return RuleDecider{}.Decide(units)
	}
	decisions, err := j.classify(context.Background(), units)
	if err != nil {
		j.LastAttempt = "classify failed: " + err.Error()
		return RuleDecider{}.Decide(units)
	}
	if len(decisions) != len(units) {
		j.LastAttempt = "classifier returned wrong unit count; fallback"
		return RuleDecider{}.Decide(units)
	}
	for _, decision := range decisions {
		if decision.Reason == "low_confidence" {
			j.LastAttempt = "classifier confidence below threshold; fallback"
			return RuleDecider{}.Decide(units)
		}
	}
	j.LastAttempt = "classified ok"
	return decisions
}
