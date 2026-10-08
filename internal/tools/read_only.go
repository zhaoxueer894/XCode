package tools

// 本文件只组装只读工具的共享依赖，并把完整结果转换成受限 Observation。

import (
	"bytes"         // 让 JSON Decoder 从工具参数字节读取。
	"context"       // 把取消信号传给 Index 和 Artifact Store。
	"encoding/json" // 严格解码模型提供的工具参数。
	"fmt"           // 构造参数和依赖错误。
	"io"            // 检查一份 JSON 参数之后是否还有额外值。
	"unicode/utf8"  // 确保 UTF-8 预览不会截断在一个字符中间。

	"xcode/internal/artifactstore" // 保存模型上下文之外的完整工具结果。
	"xcode/internal/harness"       // 返回结构化 ToolResult 和 Tool 列表。
	"xcode/internal/sandbox"       // 可选的受限命令执行后端。
	"xcode/internal/workspace"     // 共享路径边界、Index 和 ReadSet。
)

const defaultPreviewBytes = 8 << 10 // 模型每次最多看到 8 KiB 原始工具结果正文。

// ReadOnlySet 保存同一次 Agent Run 共享的三个只读工具和工作区状态。
type ReadOnlySet struct {
	runtime *readOnlyRuntime // 保存工具共享的工作区、索引和结果控制器。
}

// readOnlyRuntime 保存只读工具执行一次 Run 时共同使用的内部依赖。
type readOnlyRuntime struct {
	workspace *workspace.Workspace // 保存统一的真实根目录、边界和忽略规则。
	index     *workspace.Index     // 保存本次 Run 开始时建立的工作区索引。
	results   *resultController    // 保存 Artifact、Preview 和 ReadSet 处理器。
	sandbox   sandbox.Backend      // 保存可选的命令执行后端；nil 表示沿用 host 原路径。
}

// resultController 把完整结果保存为 Artifact，并生成模型可见预览。
type resultController struct {
	store        artifactstore.Store // 保存完整工具结果。
	reads        *workspace.ReadSet  // 识别当前 Epoch 中的重复文件范围。
	epoch        func() uint64       // 返回当前 Context Epoch；压缩发生时由共享计数器推进。
	previewBytes int                 // 保存单次允许进入上下文的最大正文字节数。
}

// NewReadOnlySet 建立一次 Run 共享的 Index、ReadSet 和三种只读工具；纪元固定为 1（无压缩语义）。
func NewReadOnlySet(ctx context.Context, root string, store artifactstore.Store) (*ReadOnlySet, error) {
	return NewReadOnlySetWithEpoch(ctx, root, store, nil)
}

// NewReadOnlySetWithEpoch 建立使用外部纪元计数器的只读工具集；
// epochProvider 返回当前 Context Epoch，传入 nil 时固定为 1（无压缩语义）。
func NewReadOnlySetWithEpoch(ctx context.Context, root string, store artifactstore.Store, epochProvider func() uint64) (*ReadOnlySet, error) {
	if store == nil {
		return nil, fmt.Errorf("artifact store is required")
	}
	if epochProvider == nil {
		epochProvider = func() uint64 { return 1 }
	}
	view, err := workspace.Open(root)
	if err != nil {
		return nil, err
	}
	index, err := workspace.BuildIndex(ctx, view)
	if err != nil {
		return nil, err
	}
	runtime := &readOnlyRuntime{
		workspace: view,
		index:     index,
		results: &resultController{
			store:        store,
			reads:        workspace.NewReadSet(),
			epoch:        epochProvider,
			previewBytes: defaultPreviewBytes,
		},
	}
	return &ReadOnlySet{runtime: runtime}, nil
}

// SetEpochProvider 替换结果控制器的纪元来源；必须在工具注册后、运行前调用一次。
func (s *ReadOnlySet) SetEpochProvider(provider func() uint64) {
	s.runtime.results.epoch = provider
}

// SetSandbox 注入命令执行后端；nil 表示继续使用宿主执行路径。必须在运行前调用。
func (s *ReadOnlySet) SetSandbox(backend sandbox.Backend) {
	s.runtime.sandbox = backend
}

// Tools 返回应注册到同一个 Registry 的四个只读工具。
func (s *ReadOnlySet) Tools() []harness.Tool {
	return []harness.Tool{
		&ReadFile{runtime: s.runtime},
		&ListFiles{runtime: s.runtime},
		&SearchText{runtime: s.runtime},
		&ReadSkill{runtime: s.runtime},
	}
}

// WriteTools 返回写入与执行工具；它们与只读工具共享同一套 Workspace 与结果控制器，
// 必须通过统一 Executor 注册使用，不得绕过策略、审批和写前快照。
func (s *ReadOnlySet) WriteTools() []harness.Tool {
	return []harness.Tool{
		&ApplyPatch{runtime: s.runtime},
		&Exec{runtime: s.runtime},
		&GitDiff{runtime: s.runtime},
	}
}

// WorkspaceRoot 返回共享工作区的真实根路径，供 Checkpoint Store 绑定同一边界。
func (s *ReadOnlySet) WorkspaceRoot() string {
	return s.runtime.workspace.Root()
}

// invalidate 在文件被写工具修改后刷新索引记录并清除对应的读取记录。
func (r *readOnlyRuntime) invalidate(ctx context.Context, relative string) error {
	if relative == ".gitignore" {
		return r.refreshAfterCommand(ctx)
	}
	r.results.reads.InvalidatePath(relative)
	if r.index != nil {
		return r.index.Refresh(ctx, r.workspace, relative)
	}
	return nil
}

// NewReadFile 保留已公开的构造入口，并为单独使用的 read_file 创建内存 Artifact Store。
func NewReadFile(root string) (*ReadFile, error) {
	view, err := workspace.Open(root)
	if err != nil {
		return nil, err
	}
	runtime := &readOnlyRuntime{
		workspace: view,
		results: &resultController{
			store:        artifactstore.NewMemoryStore(),
			reads:        workspace.NewReadSet(),
			epoch:        func() uint64 { return 1 },
			previewBytes: defaultPreviewBytes,
		},
	}
	return &ReadFile{runtime: runtime}, nil
}

// build 保存完整结果、查询 ReadSet，并创建结构化 ToolResult。
func (c *resultController) build(
	ctx context.Context,
	resource string,
	readKey string,
	sourceHash string,
	content []byte,
) (harness.ToolResult, error) {
	artifact, err := c.store.Put(ctx, content)
	if err != nil {
		return harness.ToolResult{}, err
	}
	if sourceHash == "" {
		sourceHash = artifact.ContentHash
	}
	repeated := false
	if readKey != "" {
		repeated = c.reads.Observe(workspace.ReadRecord{
			Resource:     readKey,
			ContentHash:  sourceHash,
			ArtifactID:   artifact.ID,
			VisibleEpoch: c.epoch(),
		})
	}
	if repeated {
		observation := fmt.Sprintf(
			"[resource=%s artifact_id=%s content_hash=%s repeated_read=true]\nContent is already visible in the current context epoch.",
			resource,
			artifact.ID,
			sourceHash,
		)
		return harness.ToolResult{
			Observation:   observation,
			ArtifactID:    artifact.ID,
			ContentHash:   sourceHash,
			Resource:      resource,
			OriginalBytes: len(content),
			VisibleBytes:  0,
			Truncated:     false,
			RepeatedRead:  true,
		}, nil
	}

	preview := utf8Preview(content, c.previewBytes)
	truncated := len(preview) < len(content)
	header := fmt.Sprintf(
		"[resource=%s artifact_id=%s content_hash=%s original_bytes=%d visible_bytes=%d truncated=%t]\n",
		resource,
		artifact.ID,
		sourceHash,
		len(content),
		len(preview),
		truncated,
	)
	observation := header + string(preview)
	if truncated {
		observation += "\n[preview truncated; use a narrower path, search, or line range]"
	}
	return harness.ToolResult{
		Observation:   observation,
		ArtifactID:    artifact.ID,
		ContentHash:   sourceHash,
		Resource:      resource,
		OriginalBytes: len(content),
		VisibleBytes:  len(preview),
		Truncated:     truncated,
	}, nil
}

// decodeArguments 严格解码一个 JSON 对象，拒绝未知字段和结尾附加值。
func decodeArguments(raw json.RawMessage, destination any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return fmt.Errorf("decode arguments: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return fmt.Errorf("decode arguments: trailing JSON values")
	}
	return nil
}

// utf8Preview 截取指定字节数，并退回到最后一个完整 UTF-8 字符边界。
func utf8Preview(content []byte, limit int) []byte {
	if len(content) <= limit {
		return append([]byte(nil), content...)
	}
	preview := append([]byte(nil), content[:limit]...)
	for len(preview) > 0 && !utf8.Valid(preview) {
		preview = preview[:len(preview)-1]
	}
	return preview
}

// refreshAfterCommand 重新加载 ignore、索引和读取记录；失败时工具视图不再返回旧索引。
func (r *readOnlyRuntime) refreshAfterCommand(ctx context.Context) error {
	r.results.reads = workspace.NewReadSet()
	view, err := workspace.Open(r.workspace.Root())
	if err != nil {
		r.index = nil
		return err
	}
	r.workspace = view
	index, err := workspace.BuildIndex(ctx, view)
	r.index = index
	return err
}
