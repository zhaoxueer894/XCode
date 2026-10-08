package workspace

// 本文件为一次 Agent Run 建立稳定排序的最小工作区索引。

import (
	"context"       // 在遍历和哈希文件时响应取消。
	"crypto/sha256" // 计算每个文件的完整内容哈希。
	"encoding/hex"  // 把文件哈希转换成十六进制字符串。
	"fmt"           // 构造索引上限错误和语言摘要文字。
	"go/ast"        // 读取 Go 语法树中的声明。
	"go/parser"     // 解析 Go 文件的符号和 import。
	"go/token"      // 为 Go 解析器提供文件位置集合。
	"io"            // 分块读取文件并计算哈希。
	"io/fs"         // 遍历工作区目录树。
	"os"            // 打开文件并读取 Go 源码。
	"path/filepath" // 生成相对路径并识别扩展名。
	"sort"          // 保证文件、符号和依赖顺序稳定。
	"strconv"       // 去除 Go import 字符串两侧引号。
	"strings"       // 识别扩展名和方法接收者。
	"sync"          // 保护运行期索引刷新。
)

const (
	maxIndexFiles   = 20_000  // 单次索引最多记录 2 万个文件，防止误扫巨大目录。
	maxGoParseBytes = 2 << 20 // 只对不超过 2 MiB 的 Go 文件提取语法摘要。
)

// FileRecord 保存工作区索引中一个文件的身份与代码摘要。
type FileRecord struct {
	Path         string   // 保存相对工作区根目录的斜杠路径。
	SizeBytes    int64    // 保存文件完整字节数。
	Language     string   // 保存根据扩展名与文本检查识别出的语言名称。
	ContentHash  string   // 保存文件完整内容的 SHA-256 十六进制值。
	Symbols      []string // 保存 Go 文件中的类型、函数、方法、变量和常量名称。
	Dependencies []string // 保存 Go 文件 import 的包路径。
}

// Index 保存稳定排序的文件记录和按路径查找记录的映射。
type Index struct {
	mu     sync.RWMutex          // 保护文件记录与路径映射。
	files  []FileRecord          // 保存按 Path 排序的全部未忽略文件。
	byPath map[string]FileRecord // 保存规范相对路径到文件记录的映射。
}

// BuildIndex 遍历工作区，应用忽略规则并生成最小代码索引。
func BuildIndex(ctx context.Context, workspace *Workspace) (*Index, error) {
	records := make([]FileRecord, 0)
	err := filepath.WalkDir(workspace.Root(), func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if path == workspace.Root() {
			return nil
		}
		relative, err := filepath.Rel(workspace.Root(), path)
		if err != nil {
			return err
		}
		relative = filepath.ToSlash(relative)

		// 符号链接不进入索引；read_file 对显式链接仍会解析并执行边界检查。
		if entry.Type()&os.ModeSymlink != 0 {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if workspace.IsDiscoveryIgnored(relative, entry.IsDir()) {
			if entry.IsDir() && workspace.CanSkipIgnoredDirectories() {
				return filepath.SkipDir
			}
			if !entry.IsDir() {
				return nil
			}
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		if len(records) >= maxIndexFiles {
			return fmt.Errorf("workspace index exceeds %d files", maxIndexFiles)
		}

		hash, err := hashFile(ctx, path)
		if err != nil {
			return fmt.Errorf("hash %q: %w", relative, err)
		}
		record := FileRecord{
			Path:        relative,
			SizeBytes:   info.Size(),
			Language:    detectLanguage(path),
			ContentHash: hash,
		}
		if record.Language == "Go" && info.Size() <= maxGoParseBytes {
			record.Symbols, record.Dependencies = describeGoFile(path)
		}
		records = append(records, record)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("build workspace index: %w", err)
	}
	sort.Slice(records, func(left, right int) bool {
		return records[left].Path < records[right].Path
	})
	byPath := make(map[string]FileRecord, len(records))
	for _, record := range records {
		byPath[record.Path] = record
	}
	return &Index{files: records, byPath: byPath}, nil
}

// Files 返回索引文件记录的独立副本，调用方无法修改 Index 内部切片。
func (i *Index) Files() []FileRecord {
	i.mu.RLock()
	defer i.mu.RUnlock()
	return append([]FileRecord(nil), i.files...)
}

// File 根据规范相对路径返回文件记录。
func (i *Index) File(relative string) (FileRecord, bool) {
	i.mu.RLock()
	defer i.mu.RUnlock()
	record, exists := i.byPath[filepath.ToSlash(filepath.Clean(filepath.FromSlash(relative)))]
	return record, exists
}

// Refresh 在文件被写工具修改后更新对应索引记录；文件删除或被默认过滤时移除记录。
func (i *Index) Refresh(ctx context.Context, workspace *Workspace, relative string) error {
	relative = filepath.ToSlash(filepath.Clean(filepath.FromSlash(relative)))
	i.mu.Lock()
	defer i.mu.Unlock()
	// 先移除旧记录；后续按文件的当前状态决定是否重新加入。
	if _, exists := i.byPath[relative]; exists {
		delete(i.byPath, relative)
		for index, record := range i.files {
			if record.Path == relative {
				i.files = append(i.files[:index], i.files[index+1:]...)
				break
			}
		}
	}
	target := filepath.Join(workspace.Root(), filepath.FromSlash(relative))
	info, err := os.Lstat(target)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || workspace.IsDiscoveryIgnored(relative, false) {
		return nil
	}
	hash, err := hashFile(ctx, target)
	if err != nil {
		return fmt.Errorf("hash %q: %w", relative, err)
	}
	record := FileRecord{
		Path:        relative,
		SizeBytes:   info.Size(),
		Language:    detectLanguage(target),
		ContentHash: hash,
	}
	if record.Language == "Go" && info.Size() <= maxGoParseBytes {
		record.Symbols, record.Dependencies = describeGoFile(target)
	}
	i.byPath[relative] = record
	i.files = append(i.files, record)
	sort.Slice(i.files, func(left, right int) bool {
		return i.files[left].Path < i.files[right].Path
	})
	return nil
}

// LanguageSummary 返回类似 Go=12, Markdown=3 的稳定语言统计文字。
func (i *Index) LanguageSummary() string {
	i.mu.RLock()
	defer i.mu.RUnlock()
	counts := make(map[string]int)
	for _, record := range i.files {
		counts[record.Language]++
	}
	names := make([]string, 0, len(counts))
	for name := range counts {
		names = append(names, name)
	}
	sort.Strings(names)
	parts := make([]string, 0, len(names))
	for _, name := range names {
		parts = append(parts, fmt.Sprintf("%s=%d", name, counts[name]))
	}
	return strings.Join(parts, ", ")
}

// hashFile 分块计算完整文件 SHA-256，并在每次读取前检查 Context。
func hashFile(ctx context.Context, path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hasher := sha256.New()
	buffer := make([]byte, 32<<10)
	for {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		count, readErr := file.Read(buffer)
		if count > 0 {
			if _, err := hasher.Write(buffer[:count]); err != nil {
				return "", err
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return "", readErr
		}
	}
	return hex.EncodeToString(hasher.Sum(nil)), nil
}

// detectLanguage 根据扩展名返回适合工作区索引展示的语言名称。
func detectLanguage(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".go":
		return "Go"
	case ".md", ".markdown":
		return "Markdown"
	case ".json":
		return "JSON"
	case ".yaml", ".yml":
		return "YAML"
	case ".toml":
		return "TOML"
	case ".js", ".mjs", ".cjs":
		return "JavaScript"
	case ".ts", ".tsx":
		return "TypeScript"
	case ".py":
		return "Python"
	case ".sh", ".zsh", ".bash":
		return "Shell"
	case ".html", ".htm":
		return "HTML"
	case ".css":
		return "CSS"
	case ".txt", "":
		return "Text"
	default:
		return "Other"
	}
}

// describeGoFile 使用 Go 语法树提取顶层符号、方法和 import；解析失败时返回空摘要。
func describeGoFile(path string) ([]string, []string) {
	file, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.SkipObjectResolution)
	if err != nil {
		return nil, nil
	}
	symbols := make([]string, 0)
	for _, declaration := range file.Decls {
		switch value := declaration.(type) {
		case *ast.FuncDecl:
			name := value.Name.Name
			if value.Recv != nil && len(value.Recv.List) > 0 {
				name = receiverName(value.Recv.List[0].Type) + "." + name
			}
			symbols = append(symbols, name)
		case *ast.GenDecl:
			for _, specification := range value.Specs {
				switch item := specification.(type) {
				case *ast.TypeSpec:
					symbols = append(symbols, item.Name.Name)
				case *ast.ValueSpec:
					for _, name := range item.Names {
						symbols = append(symbols, name.Name)
					}
				}
			}
		}
	}
	dependencies := make([]string, 0, len(file.Imports))
	for _, imported := range file.Imports {
		value, err := strconv.Unquote(imported.Path.Value)
		if err == nil {
			dependencies = append(dependencies, value)
		}
	}
	sort.Strings(symbols)
	sort.Strings(dependencies)
	return symbols, dependencies
}

// receiverName 把 Go 方法接收者类型转换成简短名称。
func receiverName(expression ast.Expr) string {
	switch value := expression.(type) {
	case *ast.Ident:
		return value.Name
	case *ast.StarExpr:
		return receiverName(value.X)
	case *ast.IndexExpr:
		return receiverName(value.X)
	case *ast.IndexListExpr:
		return receiverName(value.X)
	default:
		return "receiver"
	}
}
