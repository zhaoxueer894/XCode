package eventstore

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"xcode/internal/harness"
)

// TestJSONLRecorderWritesOrderedRecords 验证 Recorder 会写入连续顺序号和正确的父子关系。
func TestJSONLRecorderWritesOrderedRecords(t *testing.T) {
	t.Parallel()

	var output bytes.Buffer
	recorder := NewJSONLRecorder(&output, TraceContext{
		RunID:      "run-1",
		SessionID:  "run-1/task-1/trial-01",
		TurnID:     "run-1/task-1/trial-01/turn-1",
		TaskID:     "task-1",
		Trial:      1,
		ConfigHash: "config-hash",
		Provider:   "demo",
		Model:      "demo",
	})
	firstTime := time.Date(2026, 8, 18, 1, 2, 3, 0, time.UTC)
	recorder.Emit(harness.Event{Time: firstTime, Kind: harness.RunStarted})
	recorder.Emit(harness.Event{Time: firstTime.Add(time.Second), Kind: harness.RunCompleted, Step: 1})
	if err := recorder.Err(); err != nil {
		t.Fatalf("recorder error: %v", err)
	}

	records := decodeTraceRecords(t, output.String())
	if len(records) != 2 {
		t.Fatalf("record count = %d, want 2", len(records))
	}
	if records[0].Sequence != 1 || records[0].RecordID != "run-1/task-1/trial-01/event-000001" || records[0].ParentID != "" {
		t.Fatalf("first record identity = %#v", records[0])
	}
	if records[1].Sequence != 2 || records[1].ParentID != records[0].RecordID {
		t.Fatalf("second record chain = %#v", records[1])
	}
	if records[0].Timestamp != firstTime || records[0].Event.Kind != harness.RunStarted {
		t.Fatalf("first record event = %#v", records[0])
	}
	if records[1].RunID != "run-1" || records[1].TaskID != "task-1" || records[1].Trial != 1 || records[1].ConfigHash != "config-hash" {
		t.Fatalf("trace context = %#v", records[1])
	}
}

// TestJSONLRecorderSerializesConcurrentEmit 验证并发 Emit 仍会形成无缺口的顺序链。
func TestJSONLRecorderSerializesConcurrentEmit(t *testing.T) {
	t.Parallel()

	var output bytes.Buffer
	recorder := NewJSONLRecorder(&output, TraceContext{SessionID: "session"})
	const eventCount = 32
	var waitGroup sync.WaitGroup
	waitGroup.Add(eventCount)
	for index := 0; index < eventCount; index++ {
		go func(step int) {
			defer waitGroup.Done()
			recorder.Emit(harness.Event{Time: time.Now().UTC(), Kind: harness.ModelCompleted, Step: step})
		}(index + 1)
	}
	waitGroup.Wait()
	if err := recorder.Err(); err != nil {
		t.Fatalf("recorder error: %v", err)
	}

	records := decodeTraceRecords(t, output.String())
	if len(records) != eventCount {
		t.Fatalf("record count = %d, want %d", len(records), eventCount)
	}
	for index, record := range records {
		wantSequence := uint64(index + 1)
		if record.Sequence != wantSequence {
			t.Fatalf("record %d sequence = %d, want %d", index, record.Sequence, wantSequence)
		}
		if index > 0 && record.ParentID != records[index-1].RecordID {
			t.Fatalf("record %d parent = %q, want %q", index, record.ParentID, records[index-1].RecordID)
		}
	}
}

// TestJSONLRecorderPreservesFirstWriteError 验证首次写入失败后 Recorder 保存该错误并停止继续写入。
func TestJSONLRecorderPreservesFirstWriteError(t *testing.T) {
	t.Parallel()

	wantErr := errors.New("disk unavailable")
	writer := &errorWriter{err: wantErr}
	recorder := NewJSONLRecorder(writer, TraceContext{SessionID: "session"})
	recorder.Emit(harness.Event{Kind: harness.RunStarted})
	recorder.Emit(harness.Event{Kind: harness.RunCompleted})

	if !errors.Is(recorder.Err(), wantErr) {
		t.Fatalf("recorder error = %v, want wrapped %v", recorder.Err(), wantErr)
	}
	if writer.calls != 1 {
		t.Fatalf("writer calls = %d, want 1", writer.calls)
	}
}

// TestJSONLRecorderDetectsShortWrite 验证 Writer 少写字节时返回 io.ErrShortWrite。
func TestJSONLRecorderDetectsShortWrite(t *testing.T) {
	t.Parallel()

	recorder := NewJSONLRecorder(shortWriter{}, TraceContext{SessionID: "session"})
	recorder.Emit(harness.Event{Kind: harness.RunStarted})
	if !errors.Is(recorder.Err(), io.ErrShortWrite) {
		t.Fatalf("recorder error = %v, want %v", recorder.Err(), io.ErrShortWrite)
	}
}

type errorWriter struct {
	err   error
	calls int
}

func (w *errorWriter) Write(_ []byte) (int, error) {
	w.calls++
	return 0, w.err
}

type shortWriter struct{}

func (shortWriter) Write(data []byte) (int, error) {
	return len(data) - 1, nil
}

func decodeTraceRecords(t *testing.T, raw string) []TraceRecord {
	t.Helper()

	lines := strings.Split(strings.TrimSpace(raw), "\n")
	records := make([]TraceRecord, 0, len(lines))
	for lineNumber, line := range lines {
		var record TraceRecord
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatalf("decode line %d: %v", lineNumber+1, err)
		}
		records = append(records, record)
	}
	return records
}
