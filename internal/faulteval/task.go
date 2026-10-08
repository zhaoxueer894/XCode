// Package faulteval 实现 长任务故障评测：在受控故障点截断运行，
// 按 reference / durable / decision-gated 三种策略恢复续跑，并以成功率、恢复成功率、
// 提前完成率、关键事实保留率、重复副作用率和输入 Token 六项指标产出可从 Trace 重算的报告。
package faulteval

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath" // 夹具路径规范化与越界校验。
	"strings"
)

const (
	faultCatalogSchema = 1       // 任务目录格式版本号。
	maxFaultTasks      = 8       // 目录最多 8 个任务（规格要求 6–8）。
	minFaultTasks      = 6       // 目录最少 6 个任务。
	maxFaultCatalogSz  = 2 << 20 // 目录文件大小上限，与常规评测一致。
)

// Strategy 表示一次故障评测 Trial 使用的恢复策略。
type Strategy string

const (
	// StrategyReference 复现 Anthropic 公开的 Compaction + 自维护进展要点模式：
	// 故障后不恢复，从零重跑同一任务；上下文用模型摘要压缩。
	StrategyReference Strategy = "reference"
	// StrategyDurable 在 reference 之上加入可恢复状态（TurnLog + journal + 恢复续跑）。
	StrategyDurable Strategy = "durable"
	// StrategyDecisionGated 在 durable 之上把上下文压缩换为决策门控（可另挂 shadow 决策器）。
	StrategyDecisionGated Strategy = "decision-gated"
)

// FaultTask 定义一个长尾故障评测任务。
type FaultTask struct {
	ID              string            `json:"id"`                       // 保存任务唯一标识。
	Prompt          string            `json:"prompt"`                   // 保存交给模型的任务输入。
	Files           map[string]string `json:"files"`                    // 保存初始工作区夹具：相对路径 → 内容。
	MaxSteps        int               `json:"max_steps"`                // 保存每阶段允许的最大模型迭代数；0 使用默认 10。
	TimeoutSeconds  int               `json:"timeout_seconds"`          // 保存单阶段超时秒数；0 使用默认 90。
	VerifyCommand   []string          `json:"verify_command,omitempty"` // 保存环境结果验证命令；存在时以退出码判定成功。
	CriticalFacts   []string          `json:"critical_facts,omitempty"` // 保存最终答案必须包含的关键事实；用于关键事实保留率。
	FaultAfterTools int               `json:"fault_after_tools"`        // 保存故障注入点：第 N 个 tool.completed 后截断运行；0 表示不注入。
	DemoScript      []FaultStep       `json:"demo_script"`              // 保存离线确定性脚本；真实 Provider 忽略。
	Gate            bool              `json:"gate,omitempty"`           // 表示是否为该任务挂载完成门禁契约（提前完成率指标）。
}

// FaultStep 保存脚本的一个工具调用步骤；语义与常规评测的 DemoStep 一致。
type FaultStep struct {
	Tool      string          `json:"tool"`                // 保存工具名称。
	Arguments json.RawMessage `json:"arguments,omitempty"` // 保存完整 JSON 参数。
}

// FaultCatalog 保存一组故障评测任务。
type FaultCatalog struct {
	SchemaVersion int         `json:"schema_version"` // 保存目录格式版本号。
	Tasks         []FaultTask `json:"tasks"`          // 保存全部任务。
	loadedHash    string      // 保存加载时原始文件哈希（私有，防篡改校验用）。
	resolvedHash  string      // 保存默认值处理后内容哈希（私有）。
}

// FaultConfig 保存一次故障评测运行的身份与规模配置。
type FaultConfig struct {
	SchemaVersion           int    `json:"schema_version"`                      // 保存配置格式版本号。
	HarnessVersion          string `json:"harness_version"`                     // 保存可执行文件哈希，纳入身份。
	Provider                string `json:"provider"`                            // 保存模型提供方。
	Model                   string `json:"model"`                               // 保存具体模型名。
	ProviderEndpoint        string `json:"provider_endpoint,omitempty"`         // 保存百炼端点；进入配置身份。
	Trials                  int    `json:"trials"`                              // 保存每任务每策略的 Trial 数（规格要求至少 3）。
	ContextBudget           int    `json:"context_budget,omitempty"`            // 保存投影预算；小预算用于在长任务上触发压缩。
	Label                   string `json:"label,omitempty"`                     // 保存运行标签。
	TypedShadow             bool   `json:"typed_shadow,omitempty"`              // 表示是否启用 shadow 决策器对照（Jev 模式的自研实现；TypeSafe Jev 产品未接入）。
	TypedClassifier         string `json:"typed_classifier,omitempty"`          // 保存 shadow 分类后端：local（聊天模型）或 jev（TypeSafe systemone 真实端点）；空值视为 local。
	TypedClassifierEndpoint string `json:"typed_classifier_endpoint,omitempty"` // 保存 local 分类器使用的端点；空值表示复用主模型。
	TypedApplied            bool   `json:"typed_applied,omitempty"`             // 表示分类器是否真正拍板（生效决策器）；false 时仅 shadow 对照。
	TypedClassifierModel    string `json:"typed_classifier_model,omitempty"`    // 保存 local 分类器使用的模型名；空值表示复用主模型。
	CatalogHash             string `json:"catalog_hash"`                        // 保存任务目录哈希。
}

// LoadFaultCatalog 读取、解码并校验故障任务目录，返回目录与其内容哈希。
func LoadFaultCatalog(path string) (FaultCatalog, string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return FaultCatalog{}, "", fmt.Errorf("read fault catalog: %w", err)
	}
	if len(raw) > maxFaultCatalogSz {
		return FaultCatalog{}, "", fmt.Errorf("fault catalog exceeds %d bytes", maxFaultCatalogSz)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var catalog FaultCatalog
	if err := decoder.Decode(&catalog); err != nil {
		return FaultCatalog{}, "", fmt.Errorf("decode fault catalog: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return FaultCatalog{}, "", fmt.Errorf("fault catalog contains trailing JSON values")
	}
	if catalog.SchemaVersion != faultCatalogSchema {
		return FaultCatalog{}, "", fmt.Errorf("unsupported fault catalog schema %d", catalog.SchemaVersion)
	}
	if len(catalog.Tasks) < minFaultTasks || len(catalog.Tasks) > maxFaultTasks {
		return FaultCatalog{}, "", fmt.Errorf("fault catalog must contain %d to %d tasks", minFaultTasks, maxFaultTasks)
	}
	seen := make(map[string]struct{}, len(catalog.Tasks))
	for index := range catalog.Tasks {
		task := &catalog.Tasks[index]
		if strings.TrimSpace(task.ID) == "" {
			return FaultCatalog{}, "", fmt.Errorf("fault task %d requires an id", index)
		}
		if _, exists := seen[task.ID]; exists {
			return FaultCatalog{}, "", fmt.Errorf("duplicate fault task id %q", task.ID)
		}
		seen[task.ID] = struct{}{}
		if strings.TrimSpace(task.Prompt) == "" {
			return FaultCatalog{}, "", fmt.Errorf("fault task %q requires prompt", task.ID)
		}
		if len(task.Files) == 0 {
			return FaultCatalog{}, "", fmt.Errorf("fault task %q requires fixture files", task.ID)
		}
		if len(task.VerifyCommand) == 0 && len(task.CriticalFacts) == 0 {
			return FaultCatalog{}, "", fmt.Errorf(
				"fault task %q requires verify_command or critical_facts for grading", task.ID)
		}
		if len(task.VerifyCommand) > 0 && strings.TrimSpace(task.VerifyCommand[0]) == "" {
			return FaultCatalog{}, "", fmt.Errorf("fault task %q verify_command requires a program name", task.ID)
		}
		if len(task.DemoScript) == 0 {
			return FaultCatalog{}, "", fmt.Errorf("fault task %q requires a demo_script", task.ID)
		}
		if task.MaxSteps <= 0 {
			task.MaxSteps = 10
		}
		if task.TimeoutSeconds <= 0 {
			task.TimeoutSeconds = 90
		}
		if task.FaultAfterTools < 0 || task.FaultAfterTools >= len(task.DemoScript) {
			return FaultCatalog{}, "", fmt.Errorf(
				"fault task %q fault_after_tools must be within [0, %d)", task.ID, len(task.DemoScript))
		}
	}
	digest := sha256.Sum256(raw)
	hash := hex.EncodeToString(digest[:])
	catalog.loadedHash = hash
	resolved, err := hashFaultCatalog(catalog)
	if err != nil {
		return FaultCatalog{}, "", err
	}
	catalog.resolvedHash = resolved
	return catalog, hash, nil
}

// MatchesRunHash 校验目录与运行配置中的哈希一致，防止运行中途目录被改。
func (c FaultCatalog) MatchesRunHash(configured string) error {
	if c.loadedHash == "" || c.loadedHash != configured {
		return fmt.Errorf("fault catalog does not match configured catalog hash")
	}
	resolved, err := hashFaultCatalog(c)
	if err != nil {
		return err
	}
	if c.resolvedHash == "" || resolved != c.resolvedHash {
		return fmt.Errorf("fault catalog changed after loading")
	}
	return nil
}

// hashFaultCatalog 计算默认值处理后的目录内容哈希，使身份不随格式空白漂移。
func hashFaultCatalog(c FaultCatalog) (string, error) {
	for index := range c.Tasks {
		if c.Tasks[index].MaxSteps <= 0 {
			c.Tasks[index].MaxSteps = 10
		}
		if c.Tasks[index].TimeoutSeconds <= 0 {
			c.Tasks[index].TimeoutSeconds = 90
		}
	}
	encoded, err := json.Marshal(c)
	if err != nil {
		return "", fmt.Errorf("hash fault catalog: %w", err)
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

// hashFaultConfig 计算运行配置哈希，纳入报告身份。
func hashFaultConfig(config FaultConfig) (string, error) {
	config.SchemaVersion = 1
	encoded, err := json.Marshal(config)
	if err != nil {
		return "", fmt.Errorf("hash fault config: %w", err)
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

// materializeFaultTask 把任务夹具写入隔离工作区；路径安全与常规评测同规则。
func materializeFaultTask(workspace string, task FaultTask) error {
	for name, content := range task.Files {
		// 拒绝空路径、绝对路径、盘符与父目录逃逸；Clean 后斜杠统一，便于哈希与展示。
		clean := filepath.Clean(filepath.FromSlash(name))
		if strings.TrimSpace(name) == "" || filepath.IsAbs(clean) || filepath.VolumeName(clean) != "" ||
			clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
			return fmt.Errorf("unsafe fixture path %q", name)
		}
		abs := filepath.Join(workspace, clean)
		if err := os.MkdirAll(filepath.Dir(abs), 0o700); err != nil {
			return fmt.Errorf("create fixture directory for %q: %w", name, err)
		}
		if err := os.WriteFile(abs, []byte(content), 0o600); err != nil {
			return fmt.Errorf("write fixture %q: %w", name, err)
		}
	}
	return nil
}
