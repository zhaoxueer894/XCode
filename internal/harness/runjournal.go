package harness

// 本文件实现轮次恢复日志（TurnLog）：append-only JSONL。
// Agent 在三个安全边界落盘记录：模型请求完成（assistant 消息）、工具开始、工具完成。
// 进程在任意位置崩溃后，恢复器读取最后一个未终止轮次段重建消息历史并对账副作用。
// TurnLog 只是恢复专用的持久化投影；Durable Transcript 的事实源仍是调用方的
// 消息历史（核心不变量 1 与 2 不受影响）。所有方法对 nil 接收器静默，
// 因此 Eval 与既有测试等不挂日志的路径保持 行为零变化。

import (
	"crypto/rand"   // 为 runID 与 turnID 生成随机后缀，避免同秒启动的编号冲突。
	"encoding/hex"  // 把随机字节转换成十六进制文本。
	"encoding/json" // 把每条记录编码成一行 JSON。
	"fmt"           // 构造读写错误。
	"os"            // 追加打开日志文件并写入。
	"path/filepath" // 创建日志父目录。
	"strings"       // 跳过空行。
	"sync"          // 保护写入与 turnID 状态。
	"time"          // 记录每个边界的 UTC 发生时间。
)

// TurnRecordKind 表示一条恢复日志记录属于哪个安全边界。
type TurnRecordKind string

const (
	TurnRecordStarted   TurnRecordKind = "turn.started"      // 轮次开始；携带本轮用户输入。
	TurnRecordAssistant TurnRecordKind = "assistant.message" // 模型请求完成边界；携带 assistant 消息增量。
	TurnRecordToolBegin TurnRecordKind = "tool.begin"        // 工具开始边界；副作用即将发生。
	TurnRecordToolEnd   TurnRecordKind = "tool.end"          // 工具完成边界；携带模型可见输出。
	TurnRecordCompleted TurnRecordKind = "turn.completed"    // 轮次正常完成的终止记录；恢复器据此认定无未完成轮次。
)

// turnLogSchemaVersion 是恢复日志的格式版本号；字段演进时递增。
const turnLogSchemaVersion = 1

// TurnRecord 是恢复日志 JSONL 中的一行，记录一次安全边界发生的持久化事实。
type TurnRecord struct {
	SchemaVersion int             `json:"schema_version"`         // 保存记录格式版本，读取方据此兼容旧文件。
	RunID         string          `json:"run_id"`                 // 保存产生该记录的进程运行编号；每次 CLI 启动生成一次。
	TurnID        string          `json:"turn_id,omitempty"`      // 保存当前轮次编号；同一轮次的全部记录共享。
	Kind          TurnRecordKind  `json:"kind"`                   // 保存记录对应的边界种类。
	Time          time.Time       `json:"time"`                   // 保存边界发生的 UTC 时间。
	Step          int             `json:"step,omitempty"`         // 保存模型迭代步数；恢复补执行使用 0。
	Tool          string          `json:"tool,omitempty"`         // 保存工具名称；工具边界记录使用。
	ToolCallID    string          `json:"tool_call_id,omitempty"` // 保存工具调用 ID；恢复对账用它与 journal 互查。
	Arguments     json.RawMessage `json:"arguments,omitempty"`    // 保存 tool.begin 的原始 JSON 参数；patch 对账需要。
	Message       *Message        `json:"message,omitempty"`      // 保存消息增量：started 的用户输入、assistant 消息或 tool 消息。
	Resumed       bool            `json:"resumed,omitempty"`      // 表示该轮次本身是否是一次恢复续跑。
}

// TurnLog 把轮次恢复记录追加写入单个 JSONL 文件。
// 与 executor.Journal 相同的容错取向：写入失败只记录首个错误供诊断，不中断 Agent 主流程。
type TurnLog struct {
	mu       sync.Mutex // 保护文件写入与 turnID 的推进。
	file     *os.File   // 保存以追加方式打开的日志文件。
	path     string     // 保存日志文件路径，供诊断说明。
	runID    string     // 保存本次进程运行编号。
	turnID   string     // 保存当前轮次编号；turn.started 时重新生成。
	firstErr error      // 保存首个写入错误；诊断用途，不中断主流程。
}

// NewTurnLog 以追加方式打开（或创建）恢复日志；目录自动创建，权限 0600。
func NewTurnLog(path string) (*TurnLog, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create turn log directory: %w", err)
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open turn log: %w", err)
	}
	return &TurnLog{file: file, path: path, runID: newBorderID("run-")}, nil
}

// AppendStarted 记录轮次开始边界；userInput 是本轮用户输入，resumed 表示这是否是一次恢复续跑。
func (l *TurnLog) AppendStarted(userInput string, resumed bool) {
	l.write(TurnRecord{Kind: TurnRecordStarted, Message: &Message{Role: "user", Content: userInput}, Resumed: resumed})
}

// AppendAssistant 记录模型请求完成边界：assistant 消息（含全部工具调用）已确定。
func (l *TurnLog) AppendAssistant(step int, message Message) {
	l.write(TurnRecord{Kind: TurnRecordAssistant, Step: step, Message: &message})
}

// AppendToolBegin 记录工具开始边界：该调用即将进入统一执行管线，副作用可能发生。
func (l *TurnLog) AppendToolBegin(step int, call ToolCall) {
	l.write(TurnRecord{Kind: TurnRecordToolBegin, Step: step, Tool: call.Name, ToolCallID: call.ID, Arguments: call.Arguments})
}

// AppendToolEnd 记录工具完成边界：模型可见输出已确定并进入消息历史。
func (l *TurnLog) AppendToolEnd(step int, call ToolCall, output string) {
	l.write(TurnRecord{
		Kind: TurnRecordToolEnd, Step: step, Tool: call.Name, ToolCallID: call.ID,
		Message: &Message{Role: "tool", Content: output, ToolCallID: call.ID},
	})
}

// AppendCompleted 记录轮次正常完成的终止记录；恢复器读到它即认为没有未完成轮次。
// 失败、受控停止与崩溃都不写终止记录——这些轮次保持"未终止"，正是 --resume 的恢复对象。
func (l *TurnLog) AppendCompleted(step int, answer string) {
	l.write(TurnRecord{Kind: TurnRecordCompleted, Step: step, Message: &Message{Role: "assistant", Content: answer}})
}

// write 补齐公共字段后把一条记录追加进日志；nil 接收器静默返回。
func (l *TurnLog) write(record TurnRecord) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	record.SchemaVersion = turnLogSchemaVersion
	record.RunID = l.runID
	record.TurnID = l.turnID
	record.Time = time.Now().UTC()
	if record.Kind == TurnRecordStarted {
		l.turnID = newBorderID("turn-")
		record.TurnID = l.turnID
	}
	encoded, err := json.Marshal(record)
	if err != nil {
		l.rememberError(err)
		return
	}
	if _, err := l.file.Write(append(encoded, '\n')); err != nil {
		l.rememberError(err)
	}
}

// rememberError 只保存首个写入错误供 Err 返回。
func (l *TurnLog) rememberError(err error) {
	if l.firstErr == nil {
		l.firstErr = err
	}
}

// Err 返回首个写入错误；全部成功或未挂载日志时为 nil。
func (l *TurnLog) Err() error {
	if l == nil {
		return nil
	}
	return l.firstErr
}

// Close 关闭日志文件。
func (l *TurnLog) Close() error {
	if l == nil || l.file == nil {
		return nil
	}
	return l.file.Close()
}

// ReadTurnLog 读取恢复日志的全部记录；文件不存在返回空切片而非错误。
func ReadTurnLog(path string) ([]TurnRecord, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read turn log: %w", err)
	}
	var records []TurnRecord
	for _, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var record TurnRecord
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			return nil, fmt.Errorf("decode turn log %s: %w", path, err)
		}
		records = append(records, record)
	}
	return records, nil
}

// newBorderID 生成"前缀+时间戳+随机后缀"形式的稳定编号，供 runID 与 turnID 使用。
func newBorderID(prefix string) string {
	nonce := make([]byte, 8)
	_, _ = rand.Read(nonce)
	return fmt.Sprintf("%s%s-%s", prefix, time.Now().UTC().Format("20060102T150405.000000000"), hex.EncodeToString(nonce))
}
