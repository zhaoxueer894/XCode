package workspace

// 本文件记录当前 Context Epoch 已经向模型展示过的文件范围。

import (
	"strings" // 匹配同一文件的全部行范围资源。
	"sync"    // 保护多个工具调用对 ReadSet 的并发访问。
)

// ReadRecord 保存一个规范文件范围最近一次进入模型上下文时的身份。
type ReadRecord struct {
	Resource     string // 保存规范路径和实际行范围组成的资源名称。
	ContentHash  string // 保存读取时完整文件内容的 SHA-256。
	ArtifactID   string // 保存该范围完整结果所在的 Artifact ID。
	VisibleEpoch uint64 // 保存该范围最后一次进入模型上下文的 Epoch。
}

// ReadSet 保存资源名称到最近一次读取记录的映射。
type ReadSet struct {
	mu      sync.Mutex            // 保护 records 的读取和更新。
	records map[string]ReadRecord // 保存当前 Agent Run 已读取的文件范围。
}

// NewReadSet 创建空的读取记录集合。
func NewReadSet() *ReadSet {
	return &ReadSet{records: make(map[string]ReadRecord)}
}

// InvalidatePath 在文件被修改或删除后移除它的全部读取记录，使后续读取重新提供正文。
func (s *ReadSet) InvalidatePath(relative string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	prefix := relative + "#"
	for key := range s.records {
		if key == relative || strings.HasPrefix(key, prefix) {
			delete(s.records, key)
		}
	}
}

// Observe 记录一次读取；同资源、同内容、同 Epoch 已存在时返回 true。
func (s *ReadSet) Observe(record ReadRecord) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	previous, exists := s.records[record.Resource]
	repeated := exists &&
		previous.ContentHash == record.ContentHash &&
		previous.VisibleEpoch == record.VisibleEpoch
	s.records[record.Resource] = record
	return repeated
}
