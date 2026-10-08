package harness

// 本文件实现证据驱动完成门禁：把模型给出的纯文本当作"完成声明"而非事实，
// 在 RunTurn 接受它之前，用 TaskContract 声明的验证命令在当前工作区上重取环境证据，
// 并可选地用 reviewer 子智能体做 fresh-context 独立审查。任何一项不满足，
// 门禁把确定性失败证据作为 user 消息回注当前循环，在剩余预算内继续修复；
// 环境 Outcome 始终是最终事实源（核心不变量 9）。

import (
	"context"       // 门禁执行验证命令时响应取消。
	"encoding/json" // 构造 exec 与 delegate 的工具调用参数。
	"fmt"           // 构造判定说明与回注消息。
	"strconv"       // 解析 exec 输出中的退出码。
	"strings"       // 拼接验收条件、解析判定行。
	"time"          // 记录证据取证时间。
)

// VerifyCommand 保存契约要求的一条验证命令；门禁在模型声明完成时执行它。
type VerifyCommand struct {
	Name    string   `json:"name"`    // 保存命令的人类可读名称，例如 "go test"。
	Command []string `json:"command"` // 保存完整的参数数组；与 exec 工具的参数语义一致。
}

// Evidence 保存一次验证取证的确定性结果；证据与取证时的工作区指纹绑定。
type Evidence struct {
	Name        string    `json:"name"`                  // 保存对应的验证命令名称。
	Command     []string  `json:"command"`               // 保存实际执行的参数数组。
	ExitCode    int       `json:"exit_code"`             // 保存命令退出码；启动失败等无退出码情形为 -1。
	Passed      bool      `json:"passed"`                // 表示本次取证是否满足通过条件（退出码 0）。
	Fingerprint string    `json:"fingerprint,omitempty"` // 保存取证时的工作区指纹；证据只对该版本有效。
	Time        time.Time `json:"time"`                  // 保存取证时间。
	Detail      string    `json:"detail,omitempty"`      // 保存失败时的一行原因摘要。
}

// TaskContract 保存一次运行的完成契约：目标、验收条件、必需验证命令与运行期取得的证据。
// 由调用方（CLI --contract 文件或评测任务）提供；Evidence 与 reviewer 状态在运行期填充。
type TaskContract struct {
	Goal           string          `json:"goal"`                 // 保存任务目标，供 reviewer 审查使用。
	Acceptance     []string        `json:"acceptance,omitempty"` // 保存验收条件清单；reviewer 逐条核对。
	VerifyCommands []VerifyCommand `json:"verify_commands"`      // 保存门禁必须执行的验证命令；至少一条。
	Review         bool            `json:"review,omitempty"`     // 表示是否在证据通过后追加 fresh-context reviewer 审查。
	Evidence       []Evidence      `json:"evidence,omitempty"`   // 保存门禁已经取得的验证证据，按取证顺序追加。
	reviewerPassed string          `json:"-"`                    // 保存 reviewer 通过时的工作区指纹；空表示尚未通过。
}

// gateResult 保存一次门禁评估的结论；Passed 为 false 时 UserMessage 必须携带可回注的证据。
type gateResult struct {
	Passed      bool     // 表示门禁是否放行完成声明。
	Fingerprint string   // 保存本次评估使用的工作区指纹（可能为空）。
	Reasons     []string // 保存全部阻塞原因；进入 completion.gate.blocked 事件。
	UserMessage string   // 保存回注给模型的确定性失败证据文本。
}

// maxGateDetailBytes 限制单条失败证据进入回注消息的长度，防止一条输出占满上下文。
const maxGateDetailBytes = 800

// runCompletionGate 在模型给出纯文本后评估完成契约；全部检查通过才允许结束轮次。
// 验证命令与 reviewer 都经由 a.runner（统一执行管线）执行，因此同样经过策略、审批、
// 快照与 journal——门禁没有第二条绕过安全管线的执行路径。
func (a *Agent) runCompletionGate(ctx context.Context, step int, options RunOptions) gateResult {
	contract := options.Contract
	result := gateResult{}

	// 第一步：取当前工作区指纹；指纹函数缺失时证据不与版本绑定（每次都重新执行验证）。
	fingerprint := ""
	if options.WorkspaceFingerprint != nil {
		fp, err := options.WorkspaceFingerprint()
		if err != nil {
			result.Reasons = append(result.Reasons, "workspace fingerprint unavailable: "+err.Error())
			result.UserMessage = gateUserMessage(contract, result.Reasons)
			return result
		}
		fingerprint = fp
	}
	result.Fingerprint = fingerprint

	// 第二步：逐条验证命令。指纹未变且已有通过证据时直接复用；否则现在执行。
	for i, verify := range contract.VerifyCommands {
		if contract.hasFreshEvidence(verify, fingerprint) {
			continue
		}
		evidence := a.executeGateVerify(ctx, step, i, verify)
		evidence.Fingerprint = fingerprint
		contract.Evidence = append(contract.Evidence, evidence)
		if !evidence.Passed {
			result.Reasons = append(result.Reasons,
				fmt.Sprintf("verify command %q failed: %s", verify.Name, evidence.Detail))
		}
	}
	if len(result.Reasons) > 0 {
		result.UserMessage = gateUserMessage(contract, result.Reasons)
		return result
	}

	// 第三步：可选的 fresh-context reviewer 审查；reviewer 结论只在同一指纹下有效一次。
	if contract.Review {
		if !hasGateTool(a.runner, "delegate") {
			result.Reasons = append(result.Reasons, "review requested but the delegate tool is not available")
			result.UserMessage = gateUserMessage(contract, result.Reasons)
			return result
		}
		if contract.reviewerPassed != fingerprint {
			issues, err := a.executeGateReview(ctx, step, contract)
			if err != nil {
				result.Reasons = append(result.Reasons, "reviewer failed: "+err.Error())
				result.UserMessage = gateUserMessage(contract, result.Reasons)
				return result
			}
			if issues != "" {
				result.Reasons = append(result.Reasons, "reviewer found issues: "+issues)
				result.UserMessage = gateUserMessage(contract, result.Reasons)
				return result
			}
			contract.reviewerPassed = fingerprint
		}
	}

	result.Passed = true
	return result
}

// hasFreshEvidence 判断某条验证命令是否已有"同一命令 + 通过 + 同一工作区指纹"的证据。
// 指针不可用（fingerprint 为空）时永远返回 false，强制每次声明完成都重新取证。
func (c *TaskContract) hasFreshEvidence(verify VerifyCommand, fingerprint string) bool {
	if fingerprint == "" {
		return false
	}
	for _, evidence := range c.Evidence {
		if evidence.Passed && evidence.Fingerprint == fingerprint && sameCommand(evidence.Command, verify.Command) {
			return true
		}
	}
	return false
}

// sameCommand 比较两个参数数组是否完全一致。
func sameCommand(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// executeGateVerify 通过统一执行管线运行一条验证命令，并把结果折算成证据。
// exec 工具把非零退出码当作正常结果返回（不作为工具错误），因此这里解析退出码判定。
func (a *Agent) executeGateVerify(ctx context.Context, step, index int, verify VerifyCommand) Evidence {
	arguments, _ := json.Marshal(map[string]any{"command": verify.Command})
	call := ToolCall{ID: fmt.Sprintf("gate-verify-%d", index+1), Name: "exec", Arguments: arguments}
	a.emit(Event{
		Kind: ToolStarted, Step: step, Tool: "exec", ToolCallID: call.ID,
		Data: "[gate] " + strings.Join(verify.Command, " "),
	})
	toolResult, err := a.runner.Execute(ctx, call)
	output := toolResult.Observation
	if err != nil {
		output = err.Error()
	}
	a.emit(Event{
		Kind: ToolCompleted, Step: step, Tool: "exec", ToolCallID: call.ID,
		Data: compactGateText(output, 200),
	})

	evidence := Evidence{Name: verify.Name, Command: verify.Command, Time: time.Now().UTC(), ExitCode: -1}
	if err != nil {
		evidence.Detail = compactGateText(err.Error(), maxGateDetailBytes)
		return evidence
	}
	exitCode, ok := parseExecExitCode(output)
	if !ok {
		evidence.Detail = "could not parse exit code from exec output"
		return evidence
	}
	evidence.ExitCode = exitCode
	if exitCode != 0 {
		evidence.Detail = "exit_code=" + strconv.Itoa(exitCode) + "; output: " + compactGateText(output, maxGateDetailBytes)
		return evidence
	}
	evidence.Passed = true
	return evidence
}

// executeGateReview 以 reviewer 角色运行一次 fresh-context 独立审查。
// 返回 issues 文本（通过时为空）；reviewer 的判断只作为辅助，环境证据仍是最终事实源。
func (a *Agent) executeGateReview(ctx context.Context, step int, contract *TaskContract) (string, error) {
	var b strings.Builder
	b.WriteString("你是完成门禁的独立审查者，带全新上下文，没有看过开发过程。\n")
	b.WriteString("任务目标：" + contract.Goal + "\n")
	if len(contract.Acceptance) > 0 {
		b.WriteString("验收条件（逐条核对）：\n")
		for _, item := range contract.Acceptance {
			b.WriteString("- " + item + "\n")
		}
	}
	b.WriteString("只读审查当前工作区状态，不要修改任何文件。\n")
	b.WriteString("最后一行必须且只能是以下两种格式之一：\nGATE-VERDICT: PASS\nGATE-VERDICT: ISSUES: <具体问题>\n")

	arguments, _ := json.Marshal(map[string]any{"role": "reviewer", "prompt": b.String(), "max_steps": 8})
	call := ToolCall{ID: "gate-review", Name: "delegate", Arguments: arguments}
	a.emit(Event{
		Kind: ToolStarted, Step: step, Tool: "delegate", ToolCallID: call.ID,
		Data: "[gate] reviewer review",
	})
	toolResult, err := a.runner.Execute(ctx, call)
	output := toolResult.Observation
	if err != nil {
		a.emit(Event{Kind: ToolCompleted, Step: step, Tool: "delegate", ToolCallID: call.ID, Data: compactGateText(err.Error(), 200)})
		return "", err
	}
	a.emit(Event{Kind: ToolCompleted, Step: step, Tool: "delegate", ToolCallID: call.ID, Data: compactGateText(output, 200)})

	issues, found := parseGateVerdict(output)
	if !found {
		return "", fmt.Errorf("reviewer output has no GATE-VERDICT line")
	}
	return issues, nil
}

// parseGateVerdict 从 reviewer 输出中取最后一行 GATE-VERDICT 判定。
// found 表示输出携带判定行；issues 为空表示 PASS，非空为问题文本。
func parseGateVerdict(output string) (issues string, found bool) {
	verdict := ""
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "GATE-VERDICT:") {
			continue
		}
		verdict = strings.TrimSpace(strings.TrimPrefix(line, "GATE-VERDICT:"))
	}
	switch {
	case verdict == "":
		return "", false
	case strings.EqualFold(verdict, "PASS"):
		return "", true
	default:
		return verdict, true
	}
}

// parseExecExitCode 从 exec 工具的 Observation 中解析 exit_code 字段。
func parseExecExitCode(observation string) (int, bool) {
	index := strings.Index(observation, "exit_code=")
	if index < 0 {
		return 0, false
	}
	rest := observation[index+len("exit_code="):]
	end := 0
	for end < len(rest) && rest[end] >= '0' && rest[end] <= '9' {
		end++
	}
	if end == 0 {
		return 0, false
	}
	value, err := strconv.Atoi(rest[:end])
	if err != nil {
		return 0, false
	}
	return value, true
}

// hasGateTool 判断当前注册表中是否存在某个工具（用于可选依赖的探测）。
func hasGateTool(runner ToolRunner, name string) bool {
	for _, spec := range runner.Specs() {
		if spec.Name == name {
			return true
		}
	}
	return false
}

// gateUserMessage 把阻塞原因组装成回注给模型的确定性证据消息。
func gateUserMessage(contract *TaskContract, reasons []string) string {
	var b strings.Builder
	b.WriteString("[completion-gate] 完成声明被拒绝——环境证据未满足契约，请在剩余步数内继续修复后再次结束：\n")
	for _, reason := range reasons {
		b.WriteString("- " + reason + "\n")
	}
	b.WriteString("契约要求的验证命令：\n")
	for _, verify := range contract.VerifyCommands {
		b.WriteString("- " + verify.Name + ": " + strings.Join(verify.Command, " ") + "\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// compactGateText 把文本压成单行并限制长度，避免门禁证据占满事件或消息。
func compactGateText(value string, limit int) string {
	single := strings.Join(strings.Fields(value), " ")
	runes := []rune(single)
	if len(runes) <= limit {
		return single
	}
	return string(runes[:limit-1]) + "…"
}
