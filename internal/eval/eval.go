// 运行固定task，验证结果，并生成eval报告
package eval

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex" // hash转十六进制字符串
	"encoding/json"
	"errors"
	"fmt"
	"html/template" // 渲染单文件 HTML 报告。
	"io"
	"math" // 计算标准差。
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"xcode/internal/eventstore"
	"xcode/internal/harness"
)

const (
	catalogSchemaVersion = 3
	reportSchemaVersion  = 6       // Schema 6 增加方差、失败簇与运行标签
	maxCatalogBytes      = 2 << 20 // catalog最大2MB，避免误读大文件
	maxCatalogTasks      = 16      // 允许 15 个任务：常规 + 压力 + 能力扩展
)

// Catalog 保存一组固定任务；SchemaVersion 用于以后显式升级格式
type Catalog struct {
	SchemaVersion int    `json:"schema_version"` // 保存 tasks.json 使用的字段格式版本号（当前为 1）。
	Tasks         []Task `json:"tasks"`          // 保存本次评测包含的全部 Task。
	loadedHash    string
	resolvedHash  string
}

// 单个task定义
type Task struct {
	ID                      string                `json:"id"`                         // Task唯一标识
	Prompt                  string                `json:"prompt"`                     // 此Task交给Agent的用户输入内容
	Files                   map[string]string     `json:"files"`                      // Task 的初始工作区文件；键是相对路径，值是文件内容
	ExpectedTool            string                `json:"expected_tool"`              // 保存本 Task 期望 Agent 调用的工具名称
	ExpectedArguments       json.RawMessage       `json:"expected_arguments"`         // 保存期望工具调用使用的完整 JSON 参数
	ExpectedOutputSubstring string                `json:"expected_output_substring"`  // 保存期望工具运行结果中必须出现的文字
	MaxSteps                int                   `json:"max_steps"`                  // 保存 Agent 处理当前 Task 时允许请求模型的最大轮数
	TimeoutSeconds          int                   `json:"timeout_seconds"`            // 保存当前 Trial 允许运行的最长秒数
	VerifyCommand           []string              `json:"verify_command,omitempty"`   // 保存环境结果验证命令的完整参数数组；Trial 结束后在隔离工作区执行，退出码 0 判定通过
	DemoScript              []DemoStep            `json:"demo_script,omitempty"`      // 保存离线 Demo Provider 逐步重放的工具调用脚本；真实 Provider 忽略
	DelegateScripts         map[string][]DemoStep `json:"delegate_scripts,omitempty"` // 保存各角色子智能体的离线脚本；键为角色名。
}

// DemoStep 保存任务目录中声明的一个脚本化 Demo 工具调用。
type DemoStep struct {
	Tool      string          `json:"tool"`                // 保存本步骤要调用的工具名称。
	Arguments json.RawMessage `json:"arguments,omitempty"` // 保存本步骤使用的完整 JSON 参数对象。
}

// TrialOutcome 表示一个 Trial 最终属于通过、任务失败还是运行错误。
type TrialOutcome string

const (
	TrialPassed TrialOutcome = "passed" // 表示 Agent 完成了 Task 声明的工具调用和结果要求
	TrialFailed TrialOutcome = "failed" // 表示运行正常结束，但工具调用或工具结果没有满足 Task 要求
	TrialError  TrialOutcome = "error"  // 表示 Provider、网络、超时、Trace 写入等运行环节发生错误
)

// Config: 本次Eval的相关参数配置
type Config struct {
	SchemaVersion       int    `json:"schema_version"`  // Config 使用的字段格式版本号
	HarnessVersion      string `json:"harness_version"` // 本次评测使用的 Harness 实现版本名称
	Provider            string `json:"provider"`        // 本次评测使用的模型服务提供方名称
	Model               string `json:"model"`           // 本次评测使用的具体模型名称
	ProviderEndpoint    string `json:"provider_endpoint,omitempty"`
	Trials              int    `json:"trials"`                          // 每个 Task 需要实际运行的次数
	ContextStrategy     string `json:"context_strategy,omitempty"`      // 保存本轮 Eval 使用的上下文管理策略；空值表示 none。
	ContextBudgetTokens int    `json:"context_budget_tokens,omitempty"` // 保存触发压缩的估算 token 预算；0 表示使用默认值。
	Label               string `json:"label,omitempty"`                 // 保存运行标签；进入 Config Hash 参与身份。
	CatalogHash         string `json:"catalog_hash"`                    // tasks.json 全部内容计算出的 SHA-256
}

// Metrics: 根据一个 Trial 的 Harness Event 计算出的指标
type Metrics struct {
	Steps            int   `json:"steps"`              // 本 Trial 运行到的最大 Step 编号
	ToolCalls        int   `json:"tool_calls"`         // 本 Trial 中 tool.started 事件的总数
	RepeatedReads    int   `json:"repeated_reads"`     // 同一工具使用相同参数再次调用的总次数
	Artifacts        int   `json:"artifacts"`          // 保存本 Trial 产生的带完整 Artifact 的工具结果数量。
	ArtifactBytes    int64 `json:"artifact_bytes"`     // 保存本 Trial 全部完整工具结果的原始字节总数。
	VisibleBytes     int64 `json:"visible_bytes"`      // 保存原始工具结果中实际进入模型上下文的字节总数。
	VirtualizedBytes int64 `json:"virtualized_bytes"`  // 保存 ArtifactBytes 减去 VisibleBytes 后未进入上下文的字节总数。
	InputTokens      int   `json:"input_tokens"`       // 本 Trial 所有 model.usage 输入 Token 的总和
	OutputTokens     int   `json:"output_tokens"`      // 本 Trial 所有 model.usage 输出 Token 的总和
	TotalTokens      int   `json:"total_tokens"`       // 本 Trial 所有 model.usage 总 Token 的总和
	WallTimeMillis   int64 `json:"wall_time_millis"`   // 本 Trial 从开始执行到结束的耗时，单位为毫秒
	MaxContextTokens int   `json:"max_context_tokens"` // 保存本 Trial 投影后估算 token 的峰值。
	Compactions      int   `json:"compactions"`        // 保存本 Trial 发生压缩（含摘要）的次数。
	Handoffs         int   `json:"handoffs"`           // 保存本 Trial 发生交接重置的次数。
}

// TrialResult: Task的单次运行结果和Trace位置
type TrialResult struct {
	TaskID     string       `json:"task_id"`               // Task编号
	Trial      int          `json:"trial"`                 // Trial编号
	Outcome    TrialOutcome `json:"outcome"`               // 保存本次 Trial 的最终分类：passed、failed 或 error
	Answer     string       `json:"answer"`                // Agent 为本次 Trial 返回的最终答案
	Error      string       `json:"error"`                 // 保存任务失败原因或运行错误；通过时保存空字符串
	TracePath  string       `json:"trace_path"`            // 本次 Trial 的 JSONL 文件相对于当前 Run 输出目录的路径
	StopReason string       `json:"stop_reason,omitempty"` // 保存本次 Trial 的 Harness 层结束原因，例如 completed 或 doom_loop
	Metrics    Metrics      `json:"metrics"`               // 本次 Trial 的 Step、工具调用、重复读取、Token 和耗时指标
}

// TaskSummary 保存同一个 Task 的全部 Trial 结果汇总。
type TaskSummary struct {
	TaskID                string  `json:"task_id"`                  // 保存当前汇总对应的 Task 编号。
	TotalTrials           int     `json:"total_trials"`             // 保存当前 Task 实际运行的 Trial 总数。
	EvaluatedTrials       int     `json:"evaluated_trials"`         // 保存完成任务判定的 Trial 数量，等于通过数与任务失败数之和。
	PassedTrials          int     `json:"passed_trials"`            // 保存当前 Task 验收成功的 Trial 数量。
	FailedTrials          int     `json:"failed_trials"`            // 保存运行完成但没有满足 Task 要求的 Trial 数量。
	ErroredTrials         int     `json:"errored_trials"`           // 保存因 Provider、网络、超时或记录错误而无法完成任务判定的 Trial 数量。
	SuccessRate           float64 `json:"success_rate"`             // 保存当前 Task 的成功率，计算方式为通过数除以完成判定的 Trial 数量。
	AverageSteps          float64 `json:"average_steps"`            // 保存当前 Task 每个 Trial 平均使用的 Step 数量。
	AverageToolCalls      float64 `json:"average_tool_calls"`       // 保存当前 Task 每个 Trial 平均发起的工具调用数量。
	AverageRepeatedReads  float64 `json:"average_repeated_reads"`   // 保存当前 Task 每个 Trial 平均发生的重复读取数量。
	TotalArtifacts        int     `json:"total_artifacts"`          // 保存当前 Task 全部 Trial 产生的 Artifact 数量。
	TotalArtifactBytes    int64   `json:"total_artifact_bytes"`     // 保存当前 Task 全部 Trial 的完整工具结果字节总数。
	TotalVisibleBytes     int64   `json:"total_visible_bytes"`      // 保存当前 Task 全部 Trial 进入模型上下文的原始结果字节总数。
	TotalVirtualizedBytes int64   `json:"total_virtualized_bytes"`  // 保存当前 Task 全部 Trial 未进入上下文的结果字节总数。
	TotalInputTokens      int     `json:"total_input_tokens"`       // 保存当前 Task 全部 Trial 使用的输入 Token 总数。
	TotalOutputTokens     int     `json:"total_output_tokens"`      // 保存当前 Task 全部 Trial 使用的输出 Token 总数。
	TotalTokens           int     `json:"total_tokens"`             // 保存当前 Task 全部 Trial 使用的输入与输出 Token 总数。
	AverageWallTimeMillis float64 `json:"average_wall_time_millis"` // 保存当前 Task 每个 Trial 的平均运行时间，单位为毫秒。
	StdDevSteps           float64 `json:"stddev_steps"`             // 保存该 Task 各 Trial Step 的标准差。
	StdDevToolCalls       float64 `json:"stddev_tool_calls"`        // 保存该 Task 各 Trial 工具调用数的标准差。
	StdDevTokens          float64 `json:"stddev_tokens"`            // 保存该 Task 各 Trial Token 的标准差。
	StdDevWallTimeMillis  float64 `json:"stddev_wall_time_millis"`  // 保存该 Task 各 Trial 耗时的标准差（毫秒）。
	StdDevContextTokens   float64 `json:"stddev_context_tokens"`    // 保存该 Task 各 Trial 上下文峰值的标准差。
	AverageContextTokens  float64 `json:"average_context_tokens"`   // 保存当前 Task 每个 Trial 的投影 token 峰值平均。
	TotalCompactions      int     `json:"total_compactions"`        // 保存当前 Task 全部 Trial 的压缩总次数。
	TotalHandoffs         int     `json:"total_handoffs"`           // 保存当前 Task 全部 Trial 的交接总次数。
}

// RunSummary 保存本次 Eval Run 中全部 Task 的结果汇总。
type RunSummary struct {
	TotalTasks             int     `json:"total_tasks"`               // 保存本次 Eval Run 实际参与汇总的 Task 总数。
	EvaluatedTasks         int     `json:"evaluated_tasks"`           // 保存至少有一个 Trial 完成任务判定的 Task 数量。
	TotalTrials            int     `json:"total_trials"`              // 保存本次 Eval Run 实际运行的 Trial 总数。
	EvaluatedTrials        int     `json:"evaluated_trials"`          // 保存完成任务判定的 Trial 总数。
	PassedTrials           int     `json:"passed_trials"`             // 保存本次 Eval Run 验收成功的 Trial 总数。
	FailedTrials           int     `json:"failed_trials"`             // 保存运行完成但没有满足 Task 要求的 Trial 总数。
	ErroredTrials          int     `json:"errored_trials"`            // 保存因运行错误而无法完成任务判定的 Trial 总数。
	AverageTaskSuccessRate float64 `json:"average_task_success_rate"` // 保存至少有一次有效判定的 Task 成功率平均值，取值范围为 0 到 1。
	AverageSteps           float64 `json:"average_steps"`             // 保存全部 Task 的平均 Step 数量。
	AverageToolCalls       float64 `json:"average_tool_calls"`        // 保存全部 Task 的平均工具调用数量。
	AverageRepeatedReads   float64 `json:"average_repeated_reads"`    // 保存全部 Task 的平均重复读取数量。
	TotalArtifacts         int     `json:"total_artifacts"`           // 保存本次 Eval Run 产生的 Artifact 总数。
	TotalArtifactBytes     int64   `json:"total_artifact_bytes"`      // 保存本次 Eval Run 的完整工具结果字节总数。
	TotalVisibleBytes      int64   `json:"total_visible_bytes"`       // 保存本次 Eval Run 进入模型上下文的原始结果字节总数。
	TotalVirtualizedBytes  int64   `json:"total_virtualized_bytes"`   // 保存本次 Eval Run 未进入上下文的结果字节总数。
	TotalInputTokens       int     `json:"total_input_tokens"`        // 保存本次 Eval Run 使用的输入 Token 总数。
	TotalOutputTokens      int     `json:"total_output_tokens"`       // 保存本次 Eval Run 使用的输出 Token 总数。
	TotalTokens            int     `json:"total_tokens"`              // 保存本次 Eval Run 使用的输入与输出 Token 总数。
	AverageWallTimeMillis  float64 `json:"average_wall_time_millis"`  // 保存全部 Task 的平均运行时间，单位为毫秒。
	StdDevSteps            float64 `json:"stddev_steps"`              // 保存全部任务的 Step 均值标准差。
	StdDevToolCalls        float64 `json:"stddev_tool_calls"`         // 保存全部任务的工具调用均值标准差。
	StdDevTokens           float64 `json:"stddev_tokens"`             // 保存全部任务的 Token 均值标准差。
	StdDevWallTimeMillis   float64 `json:"stddev_wall_time_millis"`   // 保存全部任务的耗时均值标准差（毫秒）。
	AverageContextTokens   float64 `json:"average_context_tokens"`    // 保存全部 Task 的投影 token 峰值平均。
	TotalCompactions       int     `json:"total_compactions"`         // 保存整轮 Eval 的压缩总次数。
	TotalHandoffs          int     `json:"total_handoffs"`            // 保存整轮 Eval 的交接总次数。
}

// Report 保存本次 Eval Run 的配置、每次 Trial 的结果和各级汇总数据。
type Report struct {
	SchemaVersion   int              `json:"schema_version"`             // 保存报告使用的字段格式版本号，当前版本为 2。
	RunID           string           `json:"run_id"`                     // 保存本次 Eval Run 的唯一编号。
	GeneratedAt     time.Time        `json:"generated_at"`               // 保存报告生成时的 UTC 时间。
	ConfigHash      string           `json:"config_hash"`                // 保存本次 Eval 配置计算出的 SHA-256，用于识别运行配置。
	Config          Config           `json:"config"`                     // 保存本次 Eval Run 实际使用的完整配置。
	Results         []TrialResult    `json:"results"`                    // 保存本次 Eval Run 中每次 Trial 的独立结果。
	TaskSummaries   []TaskSummary    `json:"task_summaries"`             // 保存按 Task 汇总的 Trial 结果。
	RunSummary      RunSummary       `json:"run_summary"`                // 保存本次 Eval Run 的总体结果。
	Label           string           `json:"label,omitempty"`            // 保存运行标签；用于对比报告的可读标识。
	FailureClusters []FailureCluster `json:"failure_clusters,omitempty"` // 保存失败与错误 Trial 的签名聚类。
}

// 定义函数类型ExecuteFunc: 定义Eval如何调用现有Agent，不复制Agent loop
type ExecuteFunc func(
	ctx context.Context,
	workspace string,
	task Task,
	sink harness.EventSink,
) (string, error)

// ProgressKind 表示一条评测进度信息对应 Trial 的哪个运行阶段。
type ProgressKind string

const (
	ProgressTrialStarted   ProgressKind = "trial.started"   // 表示 Runner 即将开始执行一个 Trial。
	ProgressTrialCompleted ProgressKind = "trial.completed" // 表示一个 Trial 已经得到成功或失败结果。
	ProgressTrialAborted   ProgressKind = "trial.aborted"   // 表示 Runner 因内部错误中止当前 Trial 和整轮评测。
)

// ProgressEvent 保存 CLI 展示一次 Trial 运行进度所需的信息。
type ProgressEvent struct {
	Kind     ProgressKind // 保存当前进度对应的运行阶段。
	Position int          // 保存当前 Trial 在整轮 Eval 中的位置，从 1 开始。
	Total    int          // 保存整轮 Eval 计划执行的 Trial 总数。
	TaskID   string       // 保存当前 Trial 所属的 Task ID。
	Trial    int          // 保存当前 Task 的 Trial 编号，从 1 开始。
	Result   TrialResult  // 保存已经完成的 Trial 结果；开始阶段使用零值。
	Error    string       // 保存导致 Trial 中止的内部错误；正常完成时为空。
}

// ProgressSink 接收 Runner 产生的实时进度信息；nil 表示调用方不需要进度输出。
type ProgressSink func(ProgressEvent)

// Runner：保存Eval Run所需的输出位置、配置和统一执行入口
type Runner struct {
	outputDir string       // 保存当前Eval Run输出路径
	runID     string       // 当前Eval Run的ID
	config    Config       // 配置参数
	execute   ExecuteFunc  // 执行单个Trial时调用现有harness.Agent.Run的函数
	progress  ProgressSink // 把 Trial 开始和结束状态实时交给 CLI 展示
}

// LoadCatalog函数：读取、解码、校验task，返回文件hash
func LoadCatalog(path string) (Catalog, string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Catalog{}, "", fmt.Errorf("read eval catalog: %w", err)
	}
	if len(raw) > maxCatalogBytes {
		return Catalog{}, "", fmt.Errorf("eval catalog exceeds %d bytes", maxCatalogBytes)
	}

	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()

	var catalog Catalog
	if err := decoder.Decode(&catalog); err != nil {
		return Catalog{}, "", fmt.Errorf("decode eval catalog: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return Catalog{}, "", fmt.Errorf("eval catalog contains trailing JSON values")
	}
	if catalog.SchemaVersion != catalogSchemaVersion {
		return Catalog{}, "", fmt.Errorf("unsupported catalog schema %d", catalog.SchemaVersion)
	}
	if len(catalog.Tasks) < 8 || len(catalog.Tasks) > maxCatalogTasks {
		return Catalog{}, "", fmt.Errorf("eval catalog must contain 8 to %d tasks", maxCatalogTasks)
	}

	// 创建一个用于记录已经见过的任务ID的集合
	// make函数参数1: map类型 参数2: 初始容量
	seenIDs := make(map[string]struct{}, len(catalog.Tasks))
	for index := range catalog.Tasks {
		task := &catalog.Tasks[index]
		if !validTaskID(task.ID) {
			return Catalog{}, "", fmt.Errorf("task %d has invalid id %q", index, task.ID)
		}
		if _, exists := seenIDs[task.ID]; exists {
			return Catalog{}, "", fmt.Errorf("duplicate task id %q", task.ID)
		}
		seenIDs[task.ID] = struct{}{}
		if strings.TrimSpace(task.Prompt) == "" {
			return Catalog{}, "", fmt.Errorf("task %q requires prompt", task.ID)
		}
		// Schema 3 起验收标准二选一：只读任务沿用 expected 三件套，
		// 写任务使用 verify_command 以环境最终状态判定。
		hasVerifyCommand := len(task.VerifyCommand) > 0
		hasExpectedFields := strings.TrimSpace(task.ExpectedTool) != "" &&
			len(task.ExpectedArguments) > 0 &&
			task.ExpectedOutputSubstring != ""
		if !hasVerifyCommand && !hasExpectedFields {
			return Catalog{}, "", fmt.Errorf(
				"task %q requires either expected_tool fields or verify_command", task.ID)
		}
		if hasVerifyCommand && strings.TrimSpace(task.VerifyCommand[0]) == "" {
			return Catalog{}, "", fmt.Errorf("task %q verify_command requires a program name", task.ID)
		}
		if hasExpectedFields {
			var expectedArguments map[string]any
			if err := json.Unmarshal(task.ExpectedArguments, &expectedArguments); err != nil || expectedArguments == nil {
				return Catalog{}, "", fmt.Errorf("task %q expected_arguments must be a JSON object", task.ID)
			}
		}
		// Demo 脚本必须至少一步且参数是合法 JSON 对象，保证离线评测确定可重放。
		if len(task.DemoScript) == 0 {
			return Catalog{}, "", fmt.Errorf("task %q requires at least one demo_script step", task.ID)
		}
		for stepIndex := range task.DemoScript {
			step := &task.DemoScript[stepIndex]
			if strings.TrimSpace(step.Tool) == "" {
				return Catalog{}, "", fmt.Errorf("task %q demo_script step %d requires tool", task.ID, stepIndex)
			}
			if len(step.Arguments) > 0 {
				var arguments map[string]any
				if err := json.Unmarshal(step.Arguments, &arguments); err != nil || arguments == nil {
					return Catalog{}, "", fmt.Errorf("task %q demo_script step %d arguments must be a JSON object", task.ID, stepIndex)
				}
			}
		}
		if len(task.Files) == 0 {
			return Catalog{}, "", fmt.Errorf("task %q requires at least one fixture file", task.ID)
		}
		seenPaths := make(map[string]string, len(task.Files))
		for name := range task.Files {
			if _, err := safeFixturePath("fixture-root", name); err != nil {
				return Catalog{}, "", fmt.Errorf("task %q: %w", task.ID, err)
			}
			cleanPath := filepath.Clean(filepath.FromSlash(name))
			pathKey := strings.ToLower(cleanPath)
			for existingKey, existingName := range seenPaths {
				if pathKey == existingKey {
					return Catalog{}, "", fmt.Errorf("task %q fixture paths %q and %q resolve to the same file", task.ID, existingName, name)
				}
				separator := string(filepath.Separator)
				if strings.HasPrefix(pathKey, existingKey+separator) || strings.HasPrefix(existingKey, pathKey+separator) {
					return Catalog{}, "", fmt.Errorf("task %q fixture paths %q and %q conflict as file and directory", task.ID, existingName, name)
				}
			}
			seenPaths[pathKey] = name
		}
		if task.MaxSteps <= 0 {
			task.MaxSteps = 4
		}
		if task.TimeoutSeconds <= 0 {
			task.TimeoutSeconds = 30
		}
	}
	digest := sha256.Sum256(raw)
	catalogHash := hex.EncodeToString(digest[:])
	catalog.loadedHash = catalogHash
	resolvedHash, err := hashCatalog(catalog)
	if err != nil {
		return Catalog{}, "", err
	}
	catalog.resolvedHash = resolvedHash
	return catalog, catalogHash, nil
}

// NewRunner 校验运行参数，并创建复用统一 Agent 入口的顺序执行器。
func NewRunner(outputDir, runID string, config Config, execute ExecuteFunc) (*Runner, error) {
	if strings.TrimSpace(outputDir) == "" || strings.TrimSpace(runID) == "" {
		return nil, fmt.Errorf("eval output directory and run id are required")
	}
	if config.Trials <= 0 {
		return nil, fmt.Errorf("eval trials must be positive")
	}
	if strings.TrimSpace(config.HarnessVersion) == "" {
		return nil, fmt.Errorf("eval harness version is required")
	}
	if strings.TrimSpace(config.Provider) == "" {
		return nil, fmt.Errorf("eval provider is required")
	}
	if strings.TrimSpace(config.Model) == "" {
		return nil, fmt.Errorf("eval model is required")
	}
	if strings.EqualFold(strings.TrimSpace(config.Provider), "bailian") && strings.TrimSpace(config.ProviderEndpoint) == "" {
		return nil, fmt.Errorf("eval provider endpoint is required for bailian")
	}
	if strings.TrimSpace(config.CatalogHash) == "" {
		return nil, fmt.Errorf("eval catalog hash is required")
	}
	if execute == nil {
		return nil, fmt.Errorf("eval execute function is required")
	}
	config.SchemaVersion = 1
	return &Runner{outputDir: outputDir, runID: runID, config: config, execute: execute}, nil
}

// SetProgressSink 为 Runner 设置实时进度接收函数，CLI 在调用 Run 前使用它开启终端进度输出。
func (r *Runner) SetProgressSink(progress ProgressSink) {
	r.progress = progress
}

// Run：依次运行全部Task/Trial，保存Trace，返回包含总体指标的report
func (r *Runner) Run(ctx context.Context, catalog Catalog) (Report, error) {
	if catalog.loadedHash == "" || catalog.loadedHash != r.config.CatalogHash {
		return Report{}, fmt.Errorf("eval catalog does not match configured catalog hash")
	}
	resolvedHash, err := hashCatalog(catalog)
	if err != nil {
		return Report{}, err
	}
	if catalog.resolvedHash == "" || resolvedHash != catalog.resolvedHash {
		return Report{}, fmt.Errorf("eval catalog changed after loading")
	}
	// 对本次eval的config做哈希，得到configHash
	configHash, err := hashConfig(r.config)
	// 配置序列化或哈希计算失败时，立即返回空报告和原错误
	if err != nil {
		return Report{}, err
	}
	// filepath.Join 生成当前 Eval 输出目录下的 traces 路径；MkdirAll 会连同缺失的父目录一起创建
	// 0o700 表示只有当前用户可以读取、写入和进入该目录
	if err := os.MkdirAll(filepath.Join(r.outputDir, "traces"), 0o700); err != nil {
		// %w 保留底层错误链，同时补充“创建 Eval 输出目录”这一上下文。
		return Report{}, fmt.Errorf("create eval output: %w", err)
	}

	report := Report{
		SchemaVersion: reportSchemaVersion,
		RunID:         r.runID,
		GeneratedAt:   time.Now().UTC(),
		ConfigHash:    configHash,
		Config:        r.config,
		Label:         r.config.Label,
		Results:       make([]TrialResult, 0, len(catalog.Tasks)*r.config.Trials),
	}

	totalTrials := len(catalog.Tasks) * r.config.Trials
	position := 0
	for trial := 1; trial <= r.config.Trials; trial++ {
		for _, task := range catalog.Tasks {
			if err := ctx.Err(); err != nil {
				return Report{}, err
			}
			position++
			r.emitProgress(ProgressEvent{
				Kind:     ProgressTrialStarted,
				Position: position,
				Total:    totalTrials,
				TaskID:   task.ID,
				Trial:    trial,
			})
			result, err := r.runTrial(ctx, task, trial, configHash)
			if err != nil {
				r.emitProgress(ProgressEvent{
					Kind:     ProgressTrialAborted,
					Position: position,
					Total:    totalTrials,
					TaskID:   task.ID,
					Trial:    trial,
					Error:    err.Error(),
				})
				return Report{}, err
			}
			report.Results = append(report.Results, result)
			r.emitProgress(ProgressEvent{
				Kind:     ProgressTrialCompleted,
				Position: position,
				Total:    totalTrials,
				TaskID:   task.ID,
				Trial:    trial,
				Result:   result,
			})
		}
	}
	report.TaskSummaries = summarizeTasks(report.Results)
	report.RunSummary = summarizeRun(report.TaskSummaries)
	report.FailureClusters = ClusterFailures(report.Results)
	return report, nil
}

// emitProgress 把进度交给调用方；没有设置接收函数时保持安静运行。
func (r *Runner) emitProgress(event ProgressEvent) {
	if r.progress != nil {
		r.progress(event)
	}
}

// runTrial: 创建隔离工作区， 运行现有Agent、保存事件并执行Outcome Grader
func (r *Runner) runTrial(
	ctx context.Context,
	task Task,
	trial int,
	configHash string,
) (TrialResult, error) {
	workspace, err := os.MkdirTemp("", "xcode-eval-"+task.ID+"-")
	if err != nil {
		return TrialResult{}, fmt.Errorf("create workspace for %s: %w", task.ID, err)
	}
	defer os.RemoveAll(workspace)
	if err := materializeTask(workspace, task); err != nil {
		return TrialResult{}, err
	}

	traceName := fmt.Sprintf("%s-trial-%02d.jsonl", task.ID, trial)
	tracePath := filepath.Join("traces", traceName)
	traceFile, err := os.OpenFile(
		filepath.Join(r.outputDir, tracePath),
		os.O_CREATE|os.O_EXCL|os.O_WRONLY|os.O_APPEND,
		0o600,
	)
	if err != nil {
		return TrialResult{}, fmt.Errorf("create trace for %s trial %d: %w", task.ID, trial, err)
	}

	recorder := eventstore.NewJSONLRecorder(traceFile, eventstore.TraceContext{
		RunID:      r.runID,
		SessionID:  fmt.Sprintf("%s/%s/trial-%02d", r.runID, task.ID, trial),
		TurnID:     fmt.Sprintf("%s/%s/trial-%02d/turn-1", r.runID, task.ID, trial),
		TaskID:     task.ID,
		Trial:      trial,
		ConfigHash: configHash,
		Provider:   r.config.Provider,
		Model:      r.config.Model,
	})
	var events []harness.Event
	sink := harness.EventSinkFunc(func(event harness.Event) {
		events = append(events, event)
		recorder.Emit(event)
	})

	trialContext, cancel := context.WithTimeout(ctx, time.Duration(task.TimeoutSeconds)*time.Second)
	started := time.Now()
	answer, runErr := r.execute(trialContext, workspace, task, sink)
	duration := time.Since(started)
	cancel()

	traceErr := recorder.Err()
	closeErr := traceFile.Close()
	metrics := collectMetrics(events)
	metrics.WallTimeMillis = duration.Milliseconds()

	result := TrialResult{
		TaskID:    task.ID,
		Trial:     trial,
		Outcome:   TrialError,
		Answer:    answer,
		TracePath: filepath.ToSlash(tracePath),
		Metrics:   metrics,
	}
	if recordErr := errors.Join(traceErr, closeErr); recordErr != nil {
		result.Error = recordErr.Error()
		return result, nil
	}
	// 受控停止（步数、预算、doom-loop）属于任务失败而不是运行错误：Agent 没能完成任务。
	if runErr != nil {
		if errors.Is(runErr, harness.ErrMaxSteps) ||
			errors.Is(runErr, harness.ErrBudget) ||
			errors.Is(runErr, harness.ErrDoomLoop) {
			result.Outcome = TrialFailed
		}
		result.Error = runErr.Error()
		result.StopReason = collectStopReason(events)
		return result, nil
	}
	// 验收判定：写任务用环境结果验证，只读任务沿用工具过程判定。
	var gradeErr error
	if task.VerifyCommand != nil {
		gradeErr = verifyWorkspaceOutcome(ctx, workspace, task)
	} else {
		gradeErr = gradeTaskOutcome(task, events)
	}
	result.StopReason = collectStopReason(events)
	if gradeErr != nil {
		result.Outcome = TrialFailed
		result.Error = gradeErr.Error()
		return result, nil
	}
	result.Outcome = TrialPassed
	return result, nil
}

// gradeTaskOutcome 检查 Agent 是否调用了 Task 指定的工具、使用了指定参数，并得到包含目标内容的工具结果。
func gradeTaskOutcome(task Task, events []harness.Event) error {
	expectedArguments := normalizedToolArguments(task.ExpectedTool, string(task.ExpectedArguments))
	matchingCalls := make(map[string]struct{})
	foundExpectedTool := false

	for _, event := range events {
		if event.Kind != harness.ToolStarted || event.Tool != task.ExpectedTool {
			continue
		}
		foundExpectedTool = true
		if normalizedToolArguments(event.Tool, event.Data) == expectedArguments {
			matchingCalls[event.ToolCallID] = struct{}{}
		}
	}

	if len(matchingCalls) == 0 {
		if foundExpectedTool {
			return fmt.Errorf("tool %q was called with arguments different from %s", task.ExpectedTool, expectedArguments)
		}
		return fmt.Errorf("expected tool %q was not called", task.ExpectedTool)
	}

	for _, event := range events {
		if event.Kind != harness.ToolCompleted || event.Tool != task.ExpectedTool {
			continue
		}
		if _, exists := matchingCalls[event.ToolCallID]; !exists {
			continue
		}
		if strings.Contains(event.Data, task.ExpectedOutputSubstring) {
			return nil
		}
	}

	return fmt.Errorf(
		"tool %q result does not contain expected substring %q",
		task.ExpectedTool,
		task.ExpectedOutputSubstring,
	)
}

// WriteReport: 把同一份Report写成机器可读JSON和人类可读Markdown
func WriteReport(outputDir string, report Report) error {
	encoded, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return fmt.Errorf("encode eval report: %w", err)
	}
	encoded = append(encoded, '\n')
	if err := os.WriteFile(filepath.Join(outputDir, "report.json"), encoded, 0o600); err != nil {
		return fmt.Errorf("write JSON report: %w", err)
	}

	var markdown strings.Builder
	fmt.Fprintln(&markdown, "# XCode Eval v0 Report")
	fmt.Fprintln(&markdown)
	fmt.Fprintf(&markdown, "- Run ID: `%s`\n", report.RunID)
	fmt.Fprintf(&markdown, "- Config hash: `%s`\n", report.ConfigHash)
	fmt.Fprintf(&markdown, "- Provider / model: `%s` / `%s`\n", report.Config.Provider, report.Config.Model)
	fmt.Fprintf(&markdown, "- Trials: `%d`\n", report.Config.Trials)
	fmt.Fprintf(&markdown, "- Tasks / trials: `%d / %d`\n", report.RunSummary.TotalTasks, report.RunSummary.TotalTrials)
	fmt.Fprintf(&markdown, "- Evaluated trials: `%d/%d`\n", report.RunSummary.EvaluatedTrials, report.RunSummary.TotalTrials)
	fmt.Fprintf(&markdown, "- Passed / failed / run errors: `%d / %d / %d`\n", report.RunSummary.PassedTrials, report.RunSummary.FailedTrials, report.RunSummary.ErroredTrials)
	fmt.Fprintf(&markdown, "- Average task success rate: `%.2f%%`\n", report.RunSummary.AverageTaskSuccessRate*100)
	fmt.Fprintf(&markdown, "- Average steps / tools / repeated reads: `%.2f / %.2f / %.2f`\n", report.RunSummary.AverageSteps, report.RunSummary.AverageToolCalls, report.RunSummary.AverageRepeatedReads)
	fmt.Fprintf(&markdown, "- Average context tokens / compactions / handoffs: `%.2f / %d / %d`\n", report.RunSummary.AverageContextTokens, report.RunSummary.TotalCompactions, report.RunSummary.TotalHandoffs)
	fmt.Fprintf(&markdown, "- Std devs (steps / tools / wall ms): `%.2f / %.2f / %.2f`\n", report.RunSummary.StdDevSteps, report.RunSummary.StdDevToolCalls, report.RunSummary.StdDevWallTimeMillis)
	if report.Label != "" {
		fmt.Fprintf(&markdown, "- Label: `%s`\n", report.Label)
	}
	fmt.Fprintf(
		&markdown,
		"- Artifacts / original bytes / visible bytes / virtualized bytes: `%d / %d / %d / %d`\n",
		report.RunSummary.TotalArtifacts,
		report.RunSummary.TotalArtifactBytes,
		report.RunSummary.TotalVisibleBytes,
		report.RunSummary.TotalVirtualizedBytes,
	)
	fmt.Fprintf(&markdown, "- Tokens input / output / total: `%d / %d / %d`\n", report.RunSummary.TotalInputTokens, report.RunSummary.TotalOutputTokens, report.RunSummary.TotalTokens)
	fmt.Fprintf(&markdown, "- Average wall time: `%.2f ms`\n", report.RunSummary.AverageWallTimeMillis)
	fmt.Fprintln(&markdown)
	fmt.Fprintln(&markdown, "## Task summaries")
	fmt.Fprintln(&markdown)
	fmt.Fprintln(&markdown, "| Task | Passed / Evaluated | Run Errors | Success | Avg Steps | Avg Tools | Avg Repeated | Avg Ctx | Comp | Hand | Tokens | Avg Wall ms |")
	fmt.Fprintln(&markdown, "|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|")
	for _, summary := range report.TaskSummaries {
		fmt.Fprintf(
			&markdown,
			"| %s | %d/%d | %d | %.2f%% | %.2f | %.2f | %.2f | %.2f | %d | %d | %d / %d / %d | %d | %.2f |\n",
			summary.TaskID,
			summary.PassedTrials,
			summary.EvaluatedTrials,
			summary.ErroredTrials,
			summary.SuccessRate*100,
			summary.AverageSteps,
			summary.AverageToolCalls,
			summary.AverageRepeatedReads,
			summary.AverageContextTokens,
			summary.TotalCompactions,
			summary.TotalHandoffs,
			summary.TotalArtifactBytes,
			summary.TotalVisibleBytes,
			summary.TotalVirtualizedBytes,
			summary.TotalTokens,
			summary.AverageWallTimeMillis,
		)
	}
	if len(report.FailureClusters) > 0 {
		fmt.Fprintln(&markdown)
		fmt.Fprintln(&markdown, "## Failure clusters")
		fmt.Fprintln(&markdown)
		fmt.Fprintln(&markdown, "| Cluster | Count | Trials | Sample |")
		fmt.Fprintln(&markdown, "|---|---:|---|---|")
		for _, cluster := range report.FailureClusters {
			fmt.Fprintf(&markdown, "| %s | %d | %s | %s |\n", cluster.Name, cluster.Count, strings.Join(cluster.TrialRefs, ", "), markdownCell(cluster.SampleError))
		}
	}
	fmt.Fprintln(&markdown)
	fmt.Fprintln(&markdown, "## Trial details")
	fmt.Fprintln(&markdown)
	fmt.Fprintln(&markdown, "| Task | Trial | Outcome | Stop | Steps | Tools | Repeated | Ctx | Comp | Hand | Artifact bytes | Visible bytes | Virtualized bytes | Tokens | Wall ms | Trace | Error |")
	fmt.Fprintln(&markdown, "|---|---:|:---:|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---|---|")
	for _, result := range report.Results {
		fmt.Fprintf(
			&markdown,
			"| %s | %d | %s | %s | %d | %d | %d | %d | %d | %d | %d | %d | %d | %d | %d | `%s` | %s |\n",
			result.TaskID,
			result.Trial,
			result.Outcome,
			result.StopReason,
			result.Metrics.Steps,
			result.Metrics.ToolCalls,
			result.Metrics.RepeatedReads,
			result.Metrics.MaxContextTokens,
			result.Metrics.Compactions,
			result.Metrics.Handoffs,
			result.Metrics.ArtifactBytes,
			result.Metrics.VisibleBytes,
			result.Metrics.VirtualizedBytes,
			result.Metrics.TotalTokens,
			result.Metrics.WallTimeMillis,
			result.TracePath,
			markdownCell(result.Error),
		)
	}
	if err := os.WriteFile(filepath.Join(outputDir, "report.md"), []byte(markdown.String()), 0o600); err != nil {
		return fmt.Errorf("write Markdown report: %w", err)
	}
	if err := writeReportHTML(outputDir, report); err != nil {
		return err
	}
	return nil
}

// reportHTMLTemplate 保存单文件 HTML 报告模板；表格结构与 Markdown 报告一一对应。
const reportHTMLTemplate = `<!DOCTYPE html>
<html lang="zh">
<head>
<meta charset="utf-8">
<title>XCode Eval Report {{.RunID}}</title>
<style>
body{font-family:sans-serif;margin:2rem;color:#222}
table{border-collapse:collapse;margin:1rem 0}
th,td{border:1px solid #ccc;padding:4px 10px;font-size:14px}
th{background:#f5f5f5}
.pass{color:#0a7d32}.fail{color:#b3261e}.err{color:#b36b00}
h2{border-bottom:1px solid #ddd;padding-bottom:4px}
</style>
</head>
<body>
<h1>XCode Eval Report</h1>
<p>Run <code>{{.RunID}}</code>{{with .Label}} · label <code>{{.}}</code>{{end}} · provider <code>{{.Config.Provider}}</code> · model <code>{{.Config.Model}}</code> · trials {{.Config.Trials}}</p>
<p>Passed {{.RunSummary.PassedTrials}}/{{.RunSummary.EvaluatedTrials}} evaluated · success {{printf "%.2f%%" (mul .RunSummary.AverageTaskSuccessRate 100)}}</p>
<h2>Task summaries</h2>
<table>
<tr><th>Task</th><th>Passed/Eval</th><th>Success</th><th>Steps</th><th>Tools</th><th>Tokens</th><th>Avg Ctx</th><th>Comp/Hand</th></tr>
{{range .TaskSummaries}}
<tr><td>{{.TaskID}}</td><td>{{.PassedTrials}}/{{.EvaluatedTrials}}</td><td>{{printf "%.0f%%" (mul .SuccessRate 100)}}</td>
<td>{{printf "%.2f±%.2f" .AverageSteps .StdDevSteps}}</td><td>{{printf "%.2f±%.2f" .AverageToolCalls .StdDevToolCalls}}</td>
<td>{{.TotalTokens}}</td><td>{{printf "%.0f" .AverageContextTokens}}</td><td>{{.TotalCompactions}}/{{.TotalHandoffs}}</td></tr>
{{end}}
</table>
{{if .FailureClusters}}
<h2>Failure clusters</h2>
<table>
<tr><th>Cluster</th><th>Count</th><th>Trials</th><th>Sample</th></tr>
{{range .FailureClusters}}
<tr><td>{{.Name}}</td><td>{{.Count}}</td><td>{{range .TrialRefs}}{{.}} {{end}}</td><td>{{.SampleError}}</td></tr>
{{end}}
</table>
{{end}}
<h2>Trials</h2>
<table>
<tr><th>Task</th><th>Trial</th><th>Outcome</th><th>Stop</th><th>Steps</th><th>Tools</th><th>Tokens</th><th>Wall ms</th><th>Error</th></tr>
{{range .Results}}
<tr><td>{{.TaskID}}</td><td>{{.Trial}}</td><td class="{{outcomeClass .Outcome}}">{{.Outcome}}</td><td>{{.StopReason}}</td>
<td>{{.Metrics.Steps}}</td><td>{{.Metrics.ToolCalls}}</td><td>{{.Metrics.TotalTokens}}</td><td>{{.Metrics.WallTimeMillis}}</td><td>{{.Error}}</td></tr>
{{end}}
</table>
</body>
</html>`

// writeReportHTML 用模板渲染单文件 HTML 报告。
func writeReportHTML(outputDir string, report Report) error {
	template, err := template.New("report").Funcs(template.FuncMap{
		"mul": func(value float64, factor float64) float64 { return value * factor },
		"outcomeClass": func(outcome TrialOutcome) string {
			switch outcome {
			case TrialPassed:
				return "pass"
			case TrialFailed:
				return "fail"
			default:
				return "err"
			}
		},
	}).Parse(reportHTMLTemplate)
	if err != nil {
		return fmt.Errorf("parse report template: %w", err)
	}
	file, err := os.OpenFile(filepath.Join(outputDir, "report.html"), os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("write HTML report: %w", err)
	}
	defer file.Close()
	if err := template.Execute(file, report); err != nil {
		return fmt.Errorf("execute report template: %w", err)
	}
	return nil
}

// materializeTask 把 Task 中声明的文件安全写入新建的隔离工作区。
func materializeTask(workspace string, task Task) error {
	for name, content := range task.Files {
		target, err := safeFixturePath(workspace, name)
		if err != nil {
			return fmt.Errorf("task %q: %w", task.ID, err)
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			return fmt.Errorf("create fixture directory: %w", err)
		}
		if err := os.WriteFile(target, []byte(content), 0o600); err != nil {
			return fmt.Errorf("write fixture %q: %w", name, err)
		}
	}
	return nil
}

// safeFixturePath 把 Catalog 中的相对路径限制在当前 Trial 工作区内部。
func safeFixturePath(workspace, name string) (string, error) {
	if strings.TrimSpace(name) == "" {
		return "", fmt.Errorf("fixture path is empty")
	}
	clean := filepath.Clean(filepath.FromSlash(name))
	if filepath.IsAbs(clean) || clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("fixture path %q escapes workspace", name)
	}
	return filepath.Join(workspace, clean), nil
}

// 工具函数validTaskID：判断TaskID是否有效，有乱码则无效
func validTaskID(id string) bool {
	if id == "" {
		return false
	}

	for _, character := range id {
		if character >= 'a' && character <= 'z' ||
			character >= 'A' && character <= 'Z' ||
			character >= '0' && character <= '9' ||
			character == '_' || character == '-' {
			continue
		}
		return false
	}

	return true
}

// 工具函数hashConfig：对Config做SHA-256
func hashConfig(config Config) (string, error) {
	// encoded是Config结构体序列化后的结果
	encoded, err := json.Marshal(config)
	if err != nil {
		return "", fmt.Errorf("encode eval config: %w", err)
	}
	digest := sha256.Sum256(encoded) // 对json序列做哈希运算
	return hex.EncodeToString(digest[:]), nil
}

// collectMetrics 从统一 Harness Event 计算 Step、工具、Artifact、重复读取和 Token 指标。
// 起重复读取直接由 ToolResult 的 RepeatedRead 判定，Eval 不再自行猜测两次参数是否表示同一内容。
func collectMetrics(events []harness.Event) Metrics {
	metrics := Metrics{}
	for _, event := range events {
		if event.Step > metrics.Steps {
			metrics.Steps = event.Step
		}
		switch event.Kind {
		case harness.ToolStarted:
			metrics.ToolCalls++
		case harness.ToolCompleted:
			if event.ArtifactID != "" {
				metrics.Artifacts++
				metrics.ArtifactBytes += int64(event.OriginalBytes)
				metrics.VisibleBytes += int64(event.VisibleBytes)
				if event.OriginalBytes > event.VisibleBytes {
					metrics.VirtualizedBytes += int64(event.OriginalBytes - event.VisibleBytes)
				}
			}
			if event.RepeatedRead {
				metrics.RepeatedReads++
			}
		case harness.ModelUsageEmitted:
			metrics.InputTokens += event.InputTokens
			metrics.OutputTokens += event.OutputTokens
			metrics.TotalTokens += event.TotalTokens
		case harness.ContextBuilt:
			if event.ContextTokens > metrics.MaxContextTokens {
				metrics.MaxContextTokens = event.ContextTokens
			}
		case harness.ContextCompacted:
			if event.Data == "handoff" {
				metrics.Handoffs++
			} else {
				metrics.Compactions++
			}
		}
	}
	return metrics
}

// normalizedToolArguments 把 JSON 工具参数转换成稳定格式，使字段顺序或空格不同的相同参数使用同一个统计键。
func normalizedToolArguments(tool, raw string) string {
	var value any
	if err := json.Unmarshal([]byte(raw), &value); err != nil {
		return raw
	}
	if tool == "read_file" {
		if arguments, ok := value.(map[string]any); ok {
			if path, ok := arguments["path"].(string); ok && path != "" {
				arguments["path"] = filepath.ToSlash(filepath.Clean(filepath.FromSlash(path)))
			}
		}
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return raw
	}
	return string(encoded)
}

// hashCatalog 计算 LoadCatalog 完成默认值处理后的 Catalog 内容哈希。
func hashCatalog(catalog Catalog) (string, error) {
	encoded, err := json.Marshal(catalog)
	if err != nil {
		return "", fmt.Errorf("encode resolved eval catalog: %w", err)
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

// summarizeTasks 按 TaskID 分组后一次性计算均值、方差与总量。
func summarizeTasks(results []TrialResult) []TaskSummary {
	grouped := make(map[string][]TrialResult)
	order := make([]string, 0)
	for _, result := range results {
		if _, exists := grouped[result.TaskID]; !exists {
			order = append(order, result.TaskID)
		}
		grouped[result.TaskID] = append(grouped[result.TaskID], result)
	}
	summaries := make([]TaskSummary, 0, len(order))
	for _, taskID := range order {
		trials := grouped[taskID]
		summary := TaskSummary{TaskID: taskID, TotalTrials: len(trials)}
		steps := make([]float64, 0, len(trials))
		toolCalls := make([]float64, 0, len(trials))
		tokens := make([]float64, 0, len(trials))
		wall := make([]float64, 0, len(trials))
		context := make([]float64, 0, len(trials))
		for _, result := range trials {
			switch result.Outcome {
			case TrialPassed:
				summary.PassedTrials++
				summary.EvaluatedTrials++
			case TrialFailed:
				summary.FailedTrials++
				summary.EvaluatedTrials++
			case TrialError:
				summary.ErroredTrials++
			}
			summary.TotalArtifacts += result.Metrics.Artifacts
			summary.TotalArtifactBytes += result.Metrics.ArtifactBytes
			summary.TotalVisibleBytes += result.Metrics.VisibleBytes
			summary.TotalVirtualizedBytes += result.Metrics.VirtualizedBytes
			summary.TotalInputTokens += result.Metrics.InputTokens
			summary.TotalOutputTokens += result.Metrics.OutputTokens
			summary.TotalTokens += result.Metrics.TotalTokens
			summary.TotalCompactions += result.Metrics.Compactions
			summary.TotalHandoffs += result.Metrics.Handoffs
			steps = append(steps, float64(result.Metrics.Steps))
			toolCalls = append(toolCalls, float64(result.Metrics.ToolCalls))
			tokens = append(tokens, float64(result.Metrics.TotalTokens))
			wall = append(wall, float64(result.Metrics.WallTimeMillis))
			context = append(context, float64(result.Metrics.MaxContextTokens))
		}
		if summary.EvaluatedTrials > 0 {
			summary.SuccessRate = float64(summary.PassedTrials) / float64(summary.EvaluatedTrials)
		}
		summary.AverageSteps = mean(steps)
		summary.AverageToolCalls = mean(toolCalls)
		summary.AverageRepeatedReads = mean(repeatedReads(trials))
		summary.AverageWallTimeMillis = mean(wall)
		summary.AverageContextTokens = mean(context)
		summary.StdDevSteps = stdDev(steps)
		summary.StdDevToolCalls = stdDev(toolCalls)
		summary.StdDevTokens = stdDev(tokens)
		summary.StdDevWallTimeMillis = stdDev(wall)
		summary.StdDevContextTokens = stdDev(context)
		summaries = append(summaries, summary)
	}
	return summaries
}

// mean 计算算术平均值；空切片返回 0。
func mean(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	total := 0.0
	for _, value := range values {
		total += value
	}
	return total / float64(len(values))
}

// stdDev 计算总体标准差；少于两个样本返回 0。
func stdDev(values []float64) float64 {
	if len(values) < 2 {
		return 0
	}
	average := mean(values)
	sumSquares := 0.0
	for _, value := range values {
		sumSquares += (value - average) * (value - average)
	}
	return math.Sqrt(sumSquares / float64(len(values)))
}

// repeatedReads 提取各 Trial 的重复读取数。
func repeatedReads(trials []TrialResult) []float64 {
	values := make([]float64, 0, len(trials))
	for _, result := range trials {
		values = append(values, float64(result.Metrics.RepeatedReads))
	}
	return values
}

// summarizeRun 根据每个 TaskSummary 计算整轮 Eval 的总体数量、平均成功率和平均过程指标。
func summarizeRun(taskSummaries []TaskSummary) RunSummary {
	summary := RunSummary{TotalTasks: len(taskSummaries)}
	for _, taskSummary := range taskSummaries {
		summary.TotalTrials += taskSummary.TotalTrials
		summary.EvaluatedTrials += taskSummary.EvaluatedTrials
		summary.PassedTrials += taskSummary.PassedTrials
		summary.FailedTrials += taskSummary.FailedTrials
		summary.ErroredTrials += taskSummary.ErroredTrials
		if taskSummary.EvaluatedTrials > 0 {
			summary.EvaluatedTasks++
			summary.AverageTaskSuccessRate += taskSummary.SuccessRate
		}
		summary.AverageSteps += taskSummary.AverageSteps
		summary.AverageToolCalls += taskSummary.AverageToolCalls
		summary.AverageRepeatedReads += taskSummary.AverageRepeatedReads
		summary.TotalArtifacts += taskSummary.TotalArtifacts
		summary.TotalArtifactBytes += taskSummary.TotalArtifactBytes
		summary.TotalVisibleBytes += taskSummary.TotalVisibleBytes
		summary.TotalVirtualizedBytes += taskSummary.TotalVirtualizedBytes
		summary.TotalInputTokens += taskSummary.TotalInputTokens
		summary.TotalOutputTokens += taskSummary.TotalOutputTokens
		summary.TotalTokens += taskSummary.TotalTokens
		summary.AverageWallTimeMillis += taskSummary.AverageWallTimeMillis
		summary.AverageContextTokens += taskSummary.AverageContextTokens
		summary.TotalCompactions += taskSummary.TotalCompactions
		summary.TotalHandoffs += taskSummary.TotalHandoffs
	}
	if summary.TotalTasks > 0 {
		count := float64(summary.TotalTasks)
		summary.AverageSteps /= count
		summary.AverageToolCalls /= count
		summary.AverageRepeatedReads /= count
		summary.AverageWallTimeMillis /= count
		summary.AverageContextTokens /= count
		summary.StdDevSteps = stdDev(taskAverages(taskSummaries, func(s TaskSummary) float64 { return s.AverageSteps }))
		summary.StdDevToolCalls = stdDev(taskAverages(taskSummaries, func(s TaskSummary) float64 { return s.AverageToolCalls }))
		summary.StdDevWallTimeMillis = stdDev(taskAverages(taskSummaries, func(s TaskSummary) float64 { return s.AverageWallTimeMillis }))
	}
	if summary.EvaluatedTasks > 0 {
		summary.AverageTaskSuccessRate /= float64(summary.EvaluatedTasks)
	}
	return summary
}

// markdownCell 把换行和表格分隔符转换成不会破坏 Markdown 表格的文本。
func markdownCell(value string) string {
	value = strings.ReplaceAll(value, "|", "\\|")
	value = strings.ReplaceAll(value, "\r", " ")
	return strings.ReplaceAll(value, "\n", " ")
}

// collectStopReason 从事件序列提取本次 Run 的 Harness 层停止原因；
// run.completed 直接携带原因，run.failed 在受控停止时携带，否则记为 failed。
func collectStopReason(events []harness.Event) string {
	for index := len(events) - 1; index >= 0; index-- {
		event := events[index]
		if event.Kind == harness.RunCompleted {
			if event.StopReason != "" {
				return string(event.StopReason)
			}
			return string(harness.StopCompleted)
		}
		if event.Kind == harness.RunFailed {
			if event.StopReason != "" {
				return string(event.StopReason)
			}
			return string(harness.StopFailed)
		}
	}
	return ""
}

// verifyWorkspaceOutcome 在 Trial 的隔离工作区内运行任务声明的验证命令，
// 用环境最终状态（退出码）判定任务是否真正完成，不参考 Agent 的自述。
func verifyWorkspaceOutcome(ctx context.Context, workspace string, task Task) error {
	verifyContext, cancel := context.WithTimeout(ctx, time.Duration(task.TimeoutSeconds)*time.Second)
	defer cancel()

	command := exec.CommandContext(verifyContext, task.VerifyCommand[0], task.VerifyCommand[1:]...)
	command.Dir = workspace
	command.Env = os.Environ()
	var output bytes.Buffer
	command.Stdout = &output
	command.Stderr = &output
	runErr := command.Run()
	if verifyContext.Err() != nil {
		return fmt.Errorf("verify command timed out after %ds", task.TimeoutSeconds)
	}
	if runErr != nil {
		return fmt.Errorf("verify command failed: %v\n%s", runErr, tailText(output.String(), 2048))
	}
	return nil
}

// tailText 截取文本末尾最多 limit 个字符，用于错误信息里保留验证命令的最后输出。
func tailText(value string, limit int) string {
	runes := []rune(value)
	if len(runes) <= limit {
		return strings.TrimSpace(value)
	}
	return strings.TrimSpace(string(runes[len(runes)-limit:]))
}

// taskAverages 按选择器提取各任务的均值，用于跨任务方差。
func taskAverages(summaries []TaskSummary, pick func(TaskSummary) float64) []float64 {
	values := make([]float64, 0, len(summaries))
	for _, summary := range summaries {
		values = append(values, pick(summary))
	}
	return values
}
