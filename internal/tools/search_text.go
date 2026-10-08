package tools

// 本文件优先使用 rg 搜索文本，并在 rg 不可用时使用纯 Go 回退。

import (
	"bufio"         // 逐行读取 rg JSON 和普通文本文件。
	"bytes"         // 收集 rg 的简短错误输出。
	"context"       // 取消搜索进程和纯 Go 文件遍历。
	"encoding/json" // 解析模型参数和 rg --json 消息。
	"errors"        // 判断 rg 无匹配的退出码。
	"fmt"           // 构造命中行和搜索错误。
	"os"            // 纯 Go 回退时打开索引文件。
	"os/exec"       // 使用参数数组启动 rg，不经过 Shell。
	"path/filepath" // 生成索引文件的绝对路径。
	"strings"       // 执行纯文本包含判断和输出拼接。
	"unicode/utf8"  // 跳过明显的非 UTF-8 文本。

	"xcode/internal/harness" // 返回 search_text 工具声明和结果。
)

const (
	defaultSearchResults = 50  // 默认最多返回 50 条命中。
	maxSearchResults     = 200 // 单次搜索最多返回 200 条命中。
	maxSearchLineRunes   = 500 // 每条命中最多展示 500 个字符。
)

// SearchText 在未忽略的工作区文本文件中查找普通文字。
type SearchText struct {
	runtime *readOnlyRuntime // 保存共享 Workspace、Index 和结果控制器。
}

// searchHit 保存一条搜索命中的路径、行号、列号和行正文。
type searchHit struct {
	Path   string // 保存相对工作区根目录的文件路径。
	Line   int    // 保存从 1 开始的行号。
	Column int    // 保存从 1 开始的字节列号。
	Text   string // 保存压缩为单行并限制长度的命中正文。
}

// Spec 返回搜索文字、路径、大小写和结果数量参数。
func (t *SearchText) Spec() harness.ToolSpec {
	return harness.ToolSpec{
		Name:        "search_text",
		Description: "Search literal text in indexed workspace files and return path, line, column, and preview",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"query":          map[string]any{"type": "string"},
				"path":           map[string]any{"type": "string"},
				"case_sensitive": map[string]any{"type": "boolean"},
				"max_results":    map[string]any{"type": "integer", "minimum": 1, "maximum": maxSearchResults},
			},
			"required":             []string{"query"},
			"additionalProperties": false,
		},
	}
}

// Execute 搜索普通文字，把完整命中列表保存为 Artifact，并返回受限预览。
func (t *SearchText) Execute(ctx context.Context, raw json.RawMessage) (harness.ToolResult, error) {
	if err := ctx.Err(); err != nil {
		return harness.ToolResult{}, err
	}
	if t.runtime.index == nil {
		return harness.ToolResult{}, fmt.Errorf("workspace index unavailable after a change; start a new run")
	}
	var args struct {
		Query         string `json:"query"`          // 保存需要按原义查找的文字，不作为正则表达式。
		Path          string `json:"path"`           // 保存搜索起点；空值表示工作区根目录。
		CaseSensitive bool   `json:"case_sensitive"` // 表示是否区分大小写。
		MaxResults    int    `json:"max_results"`    // 保存最多返回的命中数量。
	}
	if err := decodeArguments(raw, &args); err != nil {
		return harness.ToolResult{}, err
	}
	if args.Query == "" {
		return harness.ToolResult{}, fmt.Errorf("query is required")
	}
	if strings.TrimSpace(args.Path) == "" {
		args.Path = "."
	}
	if args.MaxResults == 0 {
		args.MaxResults = defaultSearchResults
	}
	if args.MaxResults < 1 || args.MaxResults > maxSearchResults {
		return harness.ToolResult{}, fmt.Errorf("max_results must be between 1 and %d", maxSearchResults)
	}
	_, base, info, err := t.runtime.workspace.ResolveExisting(args.Path)
	if err != nil {
		return harness.ToolResult{}, err
	}
	if t.runtime.workspace.IsDiscoveryIgnored(base, info.IsDir()) {
		return harness.ToolResult{}, fmt.Errorf("path %q is outside the default discovery view", args.Path)
	}

	hits, limited, engine, err := t.search(ctx, args.Query, base, args.CaseSensitive, args.MaxResults)
	if err != nil {
		return harness.ToolResult{}, err
	}
	lines := []string{fmt.Sprintf("engine=%s matches=%d limited=%t", engine, len(hits), limited)}
	for _, hit := range hits {
		lines = append(lines, fmt.Sprintf("%s:%d:%d: %s", hit.Path, hit.Line, hit.Column, hit.Text))
	}
	if len(hits) == 0 {
		lines = append(lines, "No matches found.")
	}
	content := []byte(strings.Join(lines, "\n"))
	resource := fmt.Sprintf("search_text:%s:%s", base, args.Query)
	return t.runtime.results.build(ctx, resource, "", "", content)
}

// search 选择 rg 或纯 Go 搜索；rg 启动失败时自动使用纯 Go 回退。
func (t *SearchText) search(ctx context.Context, query, base string, caseSensitive bool, limit int) ([]searchHit, bool, string, error) {
	if _, err := exec.LookPath("rg"); err == nil {
		hits, limited, err := t.searchWithRG(ctx, query, base, caseSensitive, limit)
		if err == nil {
			return hits, limited, "rg", nil
		}
		if ctx.Err() != nil {
			return nil, false, "rg", ctx.Err()
		}
	}
	hits, limited, err := t.searchWithGo(ctx, query, base, caseSensitive, limit)
	return hits, limited, "go", err
}

// rgMessage 保存 `rg --json` 中当前实现需要读取的字段。
type rgMessage struct {
	Type string `json:"type"` // 保存消息类型；只有 match 表示搜索命中。
	Data struct {
		Path struct {
			Text string `json:"text"` // 保存命中文件相对路径。
		} `json:"path"`
		Lines struct {
			Text string `json:"text"` // 保存命中所在的完整行。
		} `json:"lines"`
		LineNumber int `json:"line_number"` // 保存从 1 开始的行号。
		Submatches []struct {
			Start int `json:"start"` // 保存从 0 开始的字节列位置。
		} `json:"submatches"`
	} `json:"data"`
}

// searchWithRG 使用固定字符串模式读取 rg 的结构化命中结果。
func (t *SearchText) searchWithRG(ctx context.Context, query, base string, caseSensitive bool, limit int) ([]searchHit, bool, error) {
	searchContext, cancel := context.WithCancel(ctx)
	defer cancel()
	arguments := []string{
		"--json",
		"--hidden",
		"--no-messages",
		"--fixed-strings",
		"--glob", "!.git/**",
		"--glob", "!.eval/**",
		"--glob", "!.xcode-state/**",
	}
	if !caseSensitive {
		arguments = append(arguments, "--ignore-case")
	}
	arguments = append(arguments, "--", query, filepath.FromSlash(base))
	command := exec.CommandContext(searchContext, "rg", arguments...)
	command.Dir = t.runtime.workspace.Root()
	stdout, err := command.StdoutPipe()
	if err != nil {
		return nil, false, err
	}
	var stderr bytes.Buffer
	command.Stderr = &stderr
	if err := command.Start(); err != nil {
		return nil, false, err
	}

	hits := make([]searchHit, 0, limit)
	limited := false
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 64<<10), 1<<20)
	for scanner.Scan() {
		var message rgMessage
		if err := json.Unmarshal(scanner.Bytes(), &message); err != nil || message.Type != "match" {
			continue
		}
		path := filepath.ToSlash(filepath.Clean(filepath.FromSlash(message.Data.Path.Text)))
		if _, allowed := t.runtime.index.File(path); !allowed {
			continue
		}
		column := 1
		if len(message.Data.Submatches) > 0 {
			column = message.Data.Submatches[0].Start + 1
		}
		hits = append(hits, searchHit{
			Path:   path,
			Line:   message.Data.LineNumber,
			Column: column,
			Text:   compactSearchLine(message.Data.Lines.Text),
		})
		if len(hits) == limit {
			limited = true
			cancel()
			break
		}
	}
	scanErr := scanner.Err()
	waitErr := command.Wait()
	if scanErr != nil && ctx.Err() == nil {
		return nil, false, scanErr
	}
	if limited {
		return hits, true, nil
	}
	if waitErr != nil {
		var exitError *exec.ExitError
		if errors.As(waitErr, &exitError) && exitError.ExitCode() == 1 {
			return hits, false, nil
		}
		return nil, false, fmt.Errorf("rg search failed: %s", strings.TrimSpace(stderr.String()))
	}
	return hits, false, nil
}

// searchWithGo 逐个读取工作区索引中允许的文件，并执行普通字符串查找。
func (t *SearchText) searchWithGo(ctx context.Context, query, base string, caseSensitive bool, limit int) ([]searchHit, bool, error) {
	hits := make([]searchHit, 0, limit)
	needle := query
	if !caseSensitive {
		needle = strings.ToLower(query)
	}
	for _, record := range t.runtime.index.Files() {
		if err := ctx.Err(); err != nil {
			return nil, false, err
		}
		if !pathWithinBase(record.Path, base) {
			continue
		}
		file, err := os.Open(filepath.Join(t.runtime.workspace.Root(), filepath.FromSlash(record.Path)))
		if err != nil {
			return nil, false, fmt.Errorf("open %q for search: %w", record.Path, err)
		}
		scanner := bufio.NewScanner(file)
		scanner.Buffer(make([]byte, 64<<10), 1<<20)
		lineNumber := 0
		for scanner.Scan() {
			lineNumber++
			line := scanner.Text()
			if !utf8.ValidString(line) {
				continue
			}
			haystack := line
			if !caseSensitive {
				haystack = strings.ToLower(line)
			}
			column := strings.Index(haystack, needle)
			if column < 0 {
				continue
			}
			hits = append(hits, searchHit{
				Path:   record.Path,
				Line:   lineNumber,
				Column: column + 1,
				Text:   compactSearchLine(line),
			})
			if len(hits) == limit {
				_ = file.Close()
				return hits, true, nil
			}
		}
		scanErr := scanner.Err()
		closeErr := file.Close()
		if scanErr != nil {
			return nil, false, fmt.Errorf("search %q: %w", record.Path, scanErr)
		}
		if closeErr != nil {
			return nil, false, fmt.Errorf("close %q after search: %w", record.Path, closeErr)
		}
	}
	return hits, false, nil
}

// pathWithinBase 判断索引文件是否位于指定搜索文件或目录中。
func pathWithinBase(filePath, base string) bool {
	return base == "." || filePath == base || strings.HasPrefix(filePath, strings.TrimSuffix(base, "/")+"/")
}

// compactSearchLine 把一条命中压成单行，并限制为 500 个字符。
func compactSearchLine(line string) string {
	line = strings.TrimSpace(strings.ReplaceAll(line, "\r", ""))
	runes := []rune(line)
	if len(runes) <= maxSearchLineRunes {
		return line
	}
	return string(runes[:maxSearchLineRunes-1]) + "…"
}
