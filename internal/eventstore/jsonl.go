// 把Agent运行过程中发生的事件写入JSONL文件
package eventstore

import (
	"encoding/json"
	"fmt"
	"io"
	"sync" // 保证顺序号和首个错误在并发调用下仍然一致
	"time"

	"xcode/internal/harness"
)

const traceSchemaVersion = 8 // JSONL格式版本号；Schema 8 新增 completion.gate 事件

// TraceContext结构体：定义一个Trial的信息
type TraceContext struct {
	RunID      string // 本次Eval Run的标识
	SessionID  string // 当前Trial对应的agent sessionID
	TurnID     string // 用户输入轮次ID
	TaskID     string // 任务ID
	Trial      int    // 同一task第几次运行
	ConfigHash string //
	Provider   string
	Model      string
}

// TraceRecord结构体：最终写入jsonl的一行所包含的字段信息
type TraceRecord struct {
	SchemaVersion int           `json:"schema_version"`      // 当前jsonl格式版本号
	Sequence      uint64        `json:"sequence"`            // 保存当前行在本 Trial JSONL 文件中的顺序号，编号从 1 开始。
	RecordID      string        `json:"record_id"`           // 保存当前 JSONL 行的唯一编号。
	ParentID      string        `json:"parent_id,omitempty"` // 保存上一行的 RecordID；第一行保存空值。
	Timestamp     time.Time     `json:"timestamp"`           // 保存当前 Agent 运行事件发生时的 UTC 时间。
	RunID         string        `json:"run_id"`              // 保存当前行所属 Eval Run 的唯一编号。
	SessionID     string        `json:"session_id"`          // 保存当前行所属完整 Agent 对话的编号。
	TurnID        string        `json:"turn_id"`             // 保存当前行所属用户输入及其处理过程的编号。
	TaskID        string        `json:"task_id"`             // 保存当前行所属 Task 的编号。
	Trial         int           `json:"trial"`               // 保存当前行所属的 Trial 编号。
	ConfigHash    string        `json:"config_hash"`         // 保存本次完整评测配置计算出的 SHA-256。
	Provider      string        `json:"provider"`            // 保存当前 Trial 使用的模型服务提供方名称。
	Model         string        `json:"model"`               // 保存当前 Trial 使用的具体模型名称。
	Event         harness.Event `json:"event"`               // 保存 Agent 刚才产生的 harness.Event。

}

// JSONL Recorder：负责把事件写入 JSONL 文件的对象
type JSONLRecorder struct {
	mu           sync.Mutex   // 用于保护sequence、parentID、err的互斥锁
	writer       io.Writer    // 接收jsonl内容的写入目标
	traceContext TraceContext //
	sequence     uint64
	parentID     string
	err          error
}

// 创建jsonlrecorder
func NewJSONLRecorder(writer io.Writer, traceContext TraceContext) *JSONLRecorder {
	return &JSONLRecorder{writer: writer, traceContext: traceContext}
}

// Emit收到一个harness.Event后补齐Task、Trial等信息，并写入一行JSON
func (r *JSONLRecorder) Emit(event harness.Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.err != nil {
		return
	}
	r.sequence++
	recordID := fmt.Sprintf("%s/event-%06d", r.traceContext.SessionID, r.sequence)
	record := TraceRecord{
		SchemaVersion: traceSchemaVersion,
		Sequence:      r.sequence,
		RecordID:      recordID,
		ParentID:      r.parentID,
		Timestamp:     event.Time,
		RunID:         r.traceContext.RunID,
		SessionID:     r.traceContext.SessionID,
		TurnID:        r.traceContext.TurnID,
		TaskID:        r.traceContext.TaskID,
		Trial:         r.traceContext.Trial,
		ConfigHash:    r.traceContext.ConfigHash,
		Provider:      r.traceContext.Provider,
		Model:         r.traceContext.Model,
		Event:         event,
	}
	encoded, err := json.Marshal(record)
	if err != nil {
		r.err = fmt.Errorf("encode trace record: %w", err)
		return
	}

	encoded = append(encoded, '\n')
	written, err := r.writer.Write(encoded)
	if err != nil {
		r.err = fmt.Errorf("write trace record: %w", err)
		return
	}
	if written != len(encoded) {
		r.err = io.ErrShortWrite
		return
	}
	r.parentID = recordID
}

func (r *JSONLRecorder) Err() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.err
}

var _ harness.EventSink = (*JSONLRecorder)(nil)
