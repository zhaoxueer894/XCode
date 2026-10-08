// Package policy 在工具执行前给出 allow、ask 或 deny 决策；它只做判断，不承担隔离。
package policy

import (
	"encoding/json" // 使授权键无参数拼接歧义。
	"fmt"           // 构造非法 Mode 的错误信息。
	"strings"       // 规范化 Mode 输入并拼接授权键。
	"sync"          // 保护会话级授权记录的并发读写。
)

// Mode 表示一次运行允许的最大能力档位。
type Mode string

const (
	ModePlan           Mode = "plan"            // 只读规划：拒绝一切修改与命令执行。
	ModeWorkspaceWrite Mode = "workspace-write" // 默认档位：允许工作区内写入，命令执行需要审批。
	ModeFullAccess     Mode = "full-access"     // 完全放行：命令执行不再询问，仍受工具自身边界约束。
)

// Decision 表示策略引擎对一次工具请求的结论。
type Decision string

const (
	Allow Decision = "allow" // 直接执行。
	Ask   Decision = "ask"   // 需要用户当场批准后才能执行。
	Deny  Decision = "deny"  // 拒绝执行，并把原因作为 Observation 交还模型。
)

// Request 保存策略判断所需的最小事实：工具名、是否修改工作区、以及要执行的完整命令。
type Request struct {
	Workdir  string   // 保存命令的真实工作目录；授权不会扩展到其他目录。
	Tool     string   // 保存发起请求的工具名称，例如 apply_patch。
	Mutating bool     // 表示该调用是否会修改工作区文件。
	Command  []string // 保存 exec 类调用的完整参数数组；非命令类调用为空。
	Paths    []string // 保存将被修改文件的工作区相对路径；供路径前缀规则匹配。
}

// Engine 保存当前运行的 Mode 和用户在本次会话内给出的持久授权。
type Engine struct {
	mode   Mode                // 保存本次运行选定的能力档位。
	rules  *RuleSet            // 保存可选的用户规则；nil 表示没有规则文件。
	mu     sync.Mutex          // 保护 grants 的并发读写。
	grants map[string]struct{} // 保存会话级授权键；命中后同类请求不再询问。
}

// NewEngine 校验 Mode 取值并创建策略引擎；空值使用默认的 workspace-write。
func NewEngine(mode Mode) (*Engine, error) {
	normalized := Mode(strings.ToLower(strings.TrimSpace(string(mode))))
	if normalized == "" {
		normalized = ModeWorkspaceWrite
	}
	switch normalized {
	case ModePlan, ModeWorkspaceWrite, ModeFullAccess:
		return &Engine{mode: normalized, grants: make(map[string]struct{})}, nil
	default:
		return nil, fmt.Errorf("unsupported mode %q: choose plan, workspace-write or full-access", mode)
	}
}

// Mode 返回引擎当前使用的能力档位。
func (e *Engine) Mode() Mode {
	return e.mode
}

// SetRules 挂载用户规则集合；nil 表示不使用规则文件。
func (e *Engine) SetRules(ruleSet *RuleSet) { e.rules = ruleSet }

// TrustCommand 添加一条会话级 allow 规则，使命令前缀匹配的 exec 免审批。
func (e *Engine) TrustCommand(prefix []string) {
	if len(prefix) == 0 {
		return
	}
	rule := Rule{
		ID:            "trusted-" + strings.Join(prefix, "-"),
		Tool:          "exec",
		CommandPrefix: prefix,
		Decision:      Allow,
		Reason:        "trusted via /trust command",
	}
	if e.rules == nil {
		e.rules = &RuleSet{}
	}
	e.rules.Rules = append(e.rules.Rules, rule)
}

// MatchRule 返回首条命中请求的用户规则；没有命中或没有规则时第二个返回值为 false。
func (e *Engine) MatchRule(request Request) (*Rule, bool) {
	rule := e.rules.Match(request)
	return rule, rule != nil
}

// Decide 按"内置硬安全 > 用户规则 > 档位默认"的优先级返回决策。
// plan 档的拒绝先于一切规则生效；用户规则可收紧也可放宽，但打不穿底线。
func (e *Engine) Decide(request Request) Decision {
	// 第一段：内置硬安全。规划模式下任何修改和命令执行都被拒绝，用户规则不可逾越。
	if e.mode == ModePlan {
		if request.Mutating || len(request.Command) > 0 {
			return Deny
		}
		return Allow
	}
	// 第二段：用户规则。文件顺序首条命中生效。
	if rule := e.rules.Match(request); rule != nil {
		return rule.Decision
	}
	// 第三段：档位默认，即 原有行为。
	if e.mode == ModeFullAccess {
		return Allow
	}
	if len(request.Command) > 0 {
		// 命令执行的副作用无法预先枚举，因此默认需要用户当场批准。
		if e.SessionGranted(GrantKey(request)) {
			return Allow
		}
		return Ask
	}
	// 工作区内写入正是本模式授予的能力；路径边界由工具与 Workspace 保证。
	return Allow
}

// GrantSession 记录一条会话级授权；相同授权键的后续请求直接放行。
func (e *Engine) GrantSession(key string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.grants[key] = struct{}{}
}

// SessionGranted 返回授权键是否已经获得会话级批准。
func (e *Engine) SessionGranted(key string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	_, granted := e.grants[key]
	return granted
}

// GrantKey 将工具、完整参数和工作目录一起编码；批准 go version 不会放行 go run。
func GrantKey(request Request) string {
	encoded, _ := json.Marshal(request)
	return string(encoded)
}
