package policy

// 本文件实现项目级策略规则：定义、加载校验和请求匹配。规则只做判定，不执行任何动作。

import (
	"encoding/json" // 严格解码规则文件。
	"fmt"           // 构造校验与匹配错误信息。
	"os"            // 读取规则文件并识别不存在。
	"path/filepath" // 规范化路径前缀。
	"strings"       // 清理输入空白并按斜杠比较路径。
)

// maxRulesFileBytes 限制规则文件大小，防止误读大文件。
const maxRulesFileBytes = 64 << 10

// Rule 保存一条用户策略规则；匹配条件与判决由文件编写者显式声明。
type Rule struct {
	ID            string   `json:"id"`                       // 保存规则唯一标识；命中时进入判定链路输出。
	Tool          string   `json:"tool,omitempty"`           // 保存匹配的工具名；空字符串匹配任意工具。
	PathPrefix    string   `json:"path_prefix,omitempty"`    // 保存匹配的工作区相对路径前缀；对写入类请求生效。
	CommandPrefix []string `json:"command_prefix,omitempty"` // 保存匹配的命令前缀；逐元素与请求命令精确比较。
	Decision      Decision `json:"decision"`                 // 保存命中后的判决：allow、ask 或 deny。
	Reason        string   `json:"reason,omitempty"`         // 保存人工备注；进入判定链路输出便于审计。
}

// RuleSet 保存按文件顺序排列的规则集合。
type RuleSet struct {
	Rules []Rule `json:"rules"` // 保存全部规则；匹配时按切片顺序取首条命中。
}

// LoadRulesFile 读取并校验规则文件；文件不存在返回 nil 而不是错误（规则是可选配置）。
func LoadRulesFile(path string) (*RuleSet, error) {
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read policy rules: %w", err)
	}
	if len(raw) > maxRulesFileBytes {
		return nil, fmt.Errorf("policy rules exceed %d bytes", maxRulesFileBytes)
	}
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	var ruleSet RuleSet
	if err := decoder.Decode(&ruleSet); err != nil {
		return nil, fmt.Errorf("decode policy rules: %w", err)
	}
	seen := make(map[string]struct{}, len(ruleSet.Rules))
	for index := range ruleSet.Rules {
		rule := &ruleSet.Rules[index]
		if strings.TrimSpace(rule.ID) == "" {
			return nil, fmt.Errorf("policy rule %d requires id", index)
		}
		if _, exists := seen[rule.ID]; exists {
			return nil, fmt.Errorf("duplicate policy rule id %q", rule.ID)
		}
		seen[rule.ID] = struct{}{}
		switch rule.Decision {
		case Allow, Ask, Deny:
		default:
			return nil, fmt.Errorf("policy rule %q has invalid decision %q", rule.ID, rule.Decision)
		}
		hasMatcher := strings.TrimSpace(rule.Tool) != "" ||
			strings.TrimSpace(rule.PathPrefix) != "" ||
			len(rule.CommandPrefix) > 0
		if !hasMatcher {
			return nil, fmt.Errorf("policy rule %q requires tool, path_prefix or command_prefix", rule.ID)
		}
		for _, element := range rule.CommandPrefix {
			if strings.TrimSpace(element) == "" {
				return nil, fmt.Errorf("policy rule %q command_prefix contains empty element", rule.ID)
			}
		}
	}
	return &ruleSet, nil
}

// Match 按文件顺序返回首条命中请求的规则；没有命中返回 nil。
func (s *RuleSet) Match(request Request) *Rule {
	if s == nil {
		return nil
	}
	for index := range s.Rules {
		rule := &s.Rules[index]
		if !ruleMatches(rule, request) {
			continue
		}
		return rule
	}
	return nil
}

// ruleMatches 判断单条规则是否命中：工具、路径前缀和命令前缀全部条件同时满足才算命中。
func ruleMatches(rule *Rule, request Request) bool {
	if rule.Tool != "" && rule.Tool != request.Tool {
		return false
	}
	if len(rule.CommandPrefix) > 0 {
		if len(request.Command) < len(rule.CommandPrefix) {
			return false
		}
		for i, element := range rule.CommandPrefix {
			if request.Command[i] == element {
				continue
			}
			// 首元素为裸名（不含路径分隔符）时按程序基名匹配：
			// 规则写 "git" 可命中 exec 工具经 PATH 解析后的绝对路径；
			// 含分隔符的规则元素仍要求全路径精确匹配。
			if i == 0 && !strings.ContainsRune(element, '/') && !strings.ContainsRune(element, '\\') &&
				baseMatchesProgram(request.Command[0], element) {
				continue
			}
			return false
		}
	}
	if rule.PathPrefix != "" {
		prefix := normalizeRulePath(rule.PathPrefix)
		matched := false
		for _, candidate := range request.Paths {
			cleaned := normalizeRulePath(candidate)
			if cleaned == prefix || strings.HasPrefix(cleaned, prefix+"/") {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}
	return true
}

// baseMatchesProgram 判断请求首元素（可能是 PATH 解析后的绝对路径）的程序基名是否等于裸名规则元素；
// Windows 解析会补 .exe 后缀，比较时忽略该后缀且不区分大小写。
func baseMatchesProgram(resolved, name string) bool {
	base := filepath.Base(resolved)
	if strings.EqualFold(base, name) {
		return true
	}
	return strings.EqualFold(strings.TrimSuffix(base, ".exe"), name)
}

// normalizeRulePath 把路径统一成斜杠分隔的相对形式，使规则与请求可比较。
func normalizeRulePath(value string) string {
	return strings.Trim(filepath.ToSlash(filepath.Clean(strings.TrimSpace(value))), "/")
}
