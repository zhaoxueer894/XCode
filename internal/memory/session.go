package memory

// 本文件实现分层记忆的持久部分：L1 会话笔记（自动写入）与 L2 项目记忆（人工编写）。
// L3 用户记忆按设计推迟；每条 L1 记录带创建时间与类型，不保存原始聊天。

import (
	"encoding/json" // 会话笔记的 JSON 存取。
	"errors"        // 识别笔记文件不存在。
	"fmt"           // 构造路径与读写错误。
	"os"            // 建目录与读写文件。
	"path/filepath" // 拼接项目记忆路径。
	"sync"          // 保护并发追加。
	"time"          // 记录创建时间。
)

// Note 保存一条 L1 会话记忆。
type Note struct {
	CreatedAt time.Time `json:"created_at"` // 保存笔记写入时间（UTC）。
	Kind      string    `json:"kind"`       // 保存笔记类型：handoff / compaction / manual。
	Content   string    `json:"content"`    // 保存笔记正文；是事实与决策，不是原始聊天。
}

// SessionStore 把 L1 笔记追加保存为一个 JSON 数组文件。
type SessionStore struct {
	mu   sync.Mutex // 保护并发写入。
	path string     // 保存笔记文件绝对或相对路径。
}

// NewSessionStore 创建会话笔记存储；path 通常是 <workspace>/.xcode-state/memory/session-notes.json。
func NewSessionStore(path string) *SessionStore {
	return &SessionStore{path: path}
}

// RecordHandoff 追加一条 handoff 笔记；目录不存在自动创建，文件权限 0600。
func (s *SessionStore) RecordHandoff(content string) error {
	return s.appendNote(Note{CreatedAt: time.Now().UTC(), Kind: "handoff", Content: content})
}

// appendNote 是唯一的写入口：读取现有数组、追加、整体重写。
func (s *SessionStore) appendNote(note Note) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return fmt.Errorf("create memory directory: %w", err)
	}
	notes, err := s.listUnlocked()
	if err != nil {
		return err
	}
	notes = append(notes, note)
	encoded, err := json.MarshalIndent(notes, "", "  ")
	if err != nil {
		return fmt.Errorf("encode session notes: %w", err)
	}
	if err := os.WriteFile(s.path, encoded, 0o600); err != nil {
		return fmt.Errorf("write session notes: %w", err)
	}
	return nil
}

// List 返回全部笔记；文件不存在视为空。
func (s *SessionStore) List() ([]Note, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.listUnlocked()
}

// listUnlocked 读取并解码笔记文件；调用方必须已持有锁。
func (s *SessionStore) listUnlocked() ([]Note, error) {
	data, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read session notes: %w", err)
	}
	var notes []Note
	if err := json.Unmarshal(data, &notes); err != nil {
		return nil, fmt.Errorf("decode session notes: %w", err)
	}
	return notes, nil
}

// ProjectMemoryPath 返回 L2 项目记忆文件的约定路径。
func ProjectMemoryPath(root string) string {
	return filepath.Join(root, ".xcode", "memory.md")
}

// LoadProjectMemory 读取 L2 项目记忆；文件不存在返回空字符串，其他读取错误向上返回。
// 该文件由人工编写，运行时只读注入，Agent 不会修改它。
func LoadProjectMemory(root string) (string, error) {
	data, err := os.ReadFile(ProjectMemoryPath(root))
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("read project memory: %w", err)
	}
	return string(data), nil
}
