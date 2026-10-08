package workspace

// 本文件解析工作区根目录 .gitignore 的常用规则，不承担路径安全检查。

import (
	"bufio"   // 按行读取 .gitignore。
	"fmt"     // 为规则文件读取错误补充路径信息。
	"os"      // 打开工作区根目录的 .gitignore。
	"path"    // 匹配单个路径片段中的 *、? 和字符范围。
	"strings" // 清理规则并统一使用斜杠路径。
)

// IgnoreMatcher 保存按声明顺序排列的忽略规则；后面的匹配规则覆盖前面的规则。
type IgnoreMatcher struct {
	rules        []ignoreRule // 保存从 .gitignore 解析出的有效规则。
	hasNegations bool         // 表示是否存在 ! 规则；遍历目录时据此决定能否提前跳过。
}

// ignoreRule 保存一条已经拆解的 .gitignore 规则。
type ignoreRule struct {
	pattern       string // 保存移除 !、开头 / 和结尾 / 后的斜杠路径模式。
	negated       bool   // true 表示这条规则重新包含此前被忽略的路径。
	directoryOnly bool   // true 表示规则只匹配目录及目录中的内容。
	anchored      bool   // true 表示规则从工作区根目录开始匹配。
	hasSlash      bool   // true 表示模式包含目录层级。
}

// LoadIgnoreMatcher 读取根目录 .gitignore；文件不存在时返回空规则集合。
func LoadIgnoreMatcher(root string) (*IgnoreMatcher, error) {
	matcher := &IgnoreMatcher{}
	file, err := os.Open(root + string(os.PathSeparator) + ".gitignore")
	if os.IsNotExist(err) {
		return matcher, nil
	}
	if err != nil {
		return nil, fmt.Errorf("open workspace .gitignore: %w", err)
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, `\#`) || strings.HasPrefix(line, `\!`) {
			line = line[1:]
		}
		rule := ignoreRule{}
		if strings.HasPrefix(line, "!") {
			rule.negated = true
			matcher.hasNegations = true
			line = strings.TrimPrefix(line, "!")
		}
		rule.anchored = strings.HasPrefix(line, "/")
		line = strings.TrimPrefix(line, "/")
		rule.directoryOnly = strings.HasSuffix(line, "/")
		line = strings.TrimSuffix(line, "/")
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		rule.pattern = line
		rule.hasSlash = strings.Contains(line, "/")
		matcher.rules = append(matcher.rules, rule)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read workspace .gitignore: %w", err)
	}
	return matcher, nil
}

// Ignore 依次应用全部规则，返回相对路径最终是否应被隐藏。
func (m *IgnoreMatcher) Ignore(relative string, isDirectory bool) bool {
	relative = strings.Trim(strings.ReplaceAll(relative, `\`, "/"), "/")
	if relative == "" || relative == "." {
		return false
	}
	ignored := false
	for _, rule := range m.rules {
		if rule.matches(relative, isDirectory) {
			ignored = !rule.negated
		}
	}
	return ignored
}

// CanSkipIgnoredDirectories 表示遍历器是否可以直接跳过被忽略目录。
func (m *IgnoreMatcher) CanSkipIgnoredDirectories() bool {
	return !m.hasNegations
}

// matches 判断一条规则是否命中当前文件或目录。
func (r ignoreRule) matches(relative string, isDirectory bool) bool {
	segments := strings.Split(relative, "/")
	if r.directoryOnly {
		lastDirectory := len(segments)
		if !isDirectory {
			lastDirectory--
		}
		for end := 1; end <= lastDirectory; end++ {
			if r.matchesPath(segments[:end]) {
				return true
			}
		}
		return false
	}
	return r.matchesPath(segments)
}

// matchesPath 根据规则是否锚定、是否包含斜杠选择匹配方式。
func (r ignoreRule) matchesPath(segments []string) bool {
	patternSegments := strings.Split(r.pattern, "/")
	if r.anchored || r.hasSlash {
		return matchSegments(patternSegments, segments)
	}
	for _, segment := range segments {
		matched, err := path.Match(r.pattern, segment)
		if err == nil && matched {
			return true
		}
	}
	return false
}

// matchSegments 使用递归处理 **，使它可以跨越任意数量的目录层级。
func matchSegments(patterns, values []string) bool {
	if len(patterns) == 0 {
		return len(values) == 0
	}
	if patterns[0] == "**" {
		if matchSegments(patterns[1:], values) {
			return true
		}
		return len(values) > 0 && matchSegments(patterns, values[1:])
	}
	if len(values) == 0 {
		return false
	}
	matched, err := path.Match(patterns[0], values[0])
	return err == nil && matched && matchSegments(patterns[1:], values[1:])
}
