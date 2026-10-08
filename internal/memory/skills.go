package memory

// 本文件实现 Skills 技能目录：扫描 .xcode/skills/ 下的 Markdown 技能文件，
// 解析 name/description 元信息，并提供按名称读取正文与渲染清单。

import (
	"errors"        // 识别文件不存在。
	"fmt"           // 构造解析与读取错误。
	"os"            // 遍历与读取技能目录。
	"path/filepath" // 拼接技能目录与文件路径。
	"sort"          // 技能按名称稳定排序。
	"strings"       // 解析 frontmatter 行与清理文本。
)

// maxSkillMetaRunes 保存 name/description 单项的最大字符数。
const maxSkillMetaRunes = 200

// Skill 保存一个技能的元信息与文件位置。
type Skill struct {
	Name        string // 保存技能名称；与文件名去扩展名一致。
	Description string // 保存一行描述；用于前导清单展示。
	Path        string // 保存技能文件的绝对路径。
}

// SkillsDir 返回技能目录的约定路径。
func SkillsDir(root string) string {
	return filepath.Join(root, ".xcode", "skills")
}

// LoadSkills 扫描技能目录；目录不存在返回空切片而不是错误（技能是可选配置）。
// 每个文件解析出 name 与 description；name 与文件名不一致时返回错误。
func LoadSkills(root string) ([]Skill, error) {
	directory := SkillsDir(root)
	entries, err := os.ReadDir(directory)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read skills directory: %w", err)
	}
	skills := make([]Skill, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".md" {
			continue
		}
		base := strings.TrimSuffix(entry.Name(), ".md")
		path := filepath.Join(directory, entry.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read skill %s: %w", entry.Name(), err)
		}
		name, description := parseSkillMeta(string(data))
		if name == "" {
			name = base
		}
		if name != base {
			return nil, fmt.Errorf("skill %s declares name %q; name must match the file name", entry.Name(), name)
		}
		skills = append(skills, Skill{Name: name, Description: description, Path: path})
	}
	sort.Slice(skills, func(i, j int) bool { return skills[i].Name < skills[j].Name })
	return skills, nil
}

// parseSkillMeta 从技能正文开头解析 name: 与 description: 两个键值行。
func parseSkillMeta(content string) (name, description string) {
	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "---" {
			break
		}
		if value, found := cutMetaField(trimmed, "name:"); found {
			name = clampRunes(value, maxSkillMetaRunes)
		}
		if value, found := cutMetaField(trimmed, "description:"); found {
			description = clampRunes(value, maxSkillMetaRunes)
		}
	}
	return name, description
}

// cutMetaField 判断一行是否是 "键: 值" 形式并返回去除空白的值。
func cutMetaField(line, key string) (string, bool) {
	if !strings.HasPrefix(line, key) {
		return "", false
	}
	value := strings.TrimSpace(strings.TrimPrefix(line, key))
	return value, value != ""
}

// clampRunes 把文本限制在最多 limit 个字符。
func clampRunes(value string, limit int) string {
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit])
}

// RenderSkillsList 把技能清单渲染成前导中的一段文本；空清单返回空字符串。
func RenderSkillsList(skills []Skill) string {
	if len(skills) == 0 {
		return ""
	}
	var builder strings.Builder
	for _, skill := range skills {
		line := "- " + skill.Name
		if skill.Description != "" {
			line += ": " + skill.Description
		}
		builder.WriteString(line)
		builder.WriteString("\n")
	}
	return builder.String()
}

// ReadSkillBody 按名称读取技能正文（去掉元信息头，从空行或 --- 之后开始）。
func ReadSkillBody(root, name string) (string, error) {
	if strings.TrimSpace(name) == "" {
		return "", fmt.Errorf("skill name is required")
	}
	skills, err := LoadSkills(root)
	if err != nil {
		return "", err
	}
	for _, skill := range skills {
		if skill.Name != name {
			continue
		}
		data, err := os.ReadFile(skill.Path)
		if err != nil {
			return "", fmt.Errorf("read skill %s: %w", name, err)
		}
		return trimSkillMeta(string(data)), nil
	}
	return "", fmt.Errorf("unknown skill %q", name)
}

// trimSkillMeta 去掉技能开头的元信息行；正文从首个空行或 --- 之后开始。
func trimSkillMeta(content string) string {
	lines := strings.Split(content, "\n")
	for index, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "---" {
			return strings.TrimSpace(strings.Join(lines[index+1:], "\n"))
		}
		if trimmed == "" {
			return strings.TrimSpace(strings.Join(lines[index+1:], "\n"))
		}
		if !strings.HasPrefix(trimmed, "name:") && !strings.HasPrefix(trimmed, "description:") {
			// 未声明任何元信息时整个文件就是正文。
			return strings.TrimSpace(content)
		}
	}
	return strings.TrimSpace(content)
}

// maxInstructionsBytes 保存 AGENTS.md 的注入上限；超出截断并标注。
const maxInstructionsBytes = 16 << 10

// LoadProjectInstructions 读取工作区根的 AGENTS.md；不存在返回空字符串。
// 内容超过上限时截断到上限并在末尾标注，读取失败原样返回错误。
func LoadProjectInstructions(root string) (string, error) {
	data, err := os.ReadFile(filepath.Join(root, "AGENTS.md"))
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("read project instructions: %w", err)
	}
	content := string(data)
	if len(content) > maxInstructionsBytes {
		content = content[:maxInstructionsBytes] + "\n[truncated at 16 KiB]\n"
	}
	return strings.TrimSpace(content), nil
}
