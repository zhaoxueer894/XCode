package contextmgr

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"xcode/internal/harness"
)

func TestBoundedKeepsInstructionsWholeToolExchangesAndEvidence(t *testing.T) {
	history := []harness.Message{{Role: "user", Content: "project rules: preserve public API"}, {Role: "user", Content: "complete original issue; do not lose this task"}}
	for i := 0; i < 12; i++ {
		a, b := fmt.Sprintf("a%d", i), fmt.Sprintf("b%d", i)
		name, args := "read_file", json.RawMessage(`{"path":"source.go"}`)
		if i == 0 {
			name, args = "apply_patch", json.RawMessage(`{"path":"source.go","operation":"update"}`)
		}
		history = append(history, harness.Message{Role: "assistant", ReasoningContent: "continuation-state", ToolCalls: []harness.ToolCall{{ID: a, Name: name, Arguments: args}, {ID: b, Name: "read_file"}}},
			harness.Message{Role: "tool", ToolCallID: a, Content: "[resource=source.go artifact_id=sha256:evidence]\n" + strings.Repeat("source code ", 400)},
			harness.Message{Role: "tool", ToolCallID: b, Content: strings.Repeat("other source ", 400)})
	}
	history[2].ReasoningContent = "Hypothesis: validate numeric options before assigning the decoded value."
	// The newest externalized analysis must survive even when its source reads
	// no longer fit; filenames alone are insufficient working memory.
	history[2+8*3].ReasoningContent = "Current finding: the missing validation is in numeric decoding; next make a minimal fix."
	before, _ := json.Marshal(history)
	manager, _ := NewManager(StrategyBounded, 2500, nil)
	projected := manager.Project(context.Background(), history)
	if !projected.Compacted || projected.Tokens >= EstimateMessagesTokens(history) {
		t.Fatal("history did not shrink")
	}
	if !reflect.DeepEqual(projected.Messages[:2], history[:2]) {
		t.Fatal("lost instructions or actual task behind project prelude")
	}
	pending := map[string]bool{}
	for _, message := range projected.Messages {
		if message.Role == "assistant" && len(message.ToolCalls) > 0 {
			if len(pending) != 0 || message.ReasoningContent != "continuation-state" {
				t.Fatal("split a thinking/tool exchange")
			}
			for _, call := range message.ToolCalls {
				pending[call.ID] = true
			}
		}
		if message.Role == "tool" {
			if !pending[message.ToolCallID] {
				t.Fatalf("orphan tool %s", message.ToolCallID)
			}
			delete(pending, message.ToolCallID)
		}
	}
	if len(pending) != 0 {
		t.Fatal("lost tool responses")
	}
	all := ""
	for _, message := range projected.Messages {
		all += message.Content
	}
	if !strings.Contains(all, "Attempted edit: update source.go") || !strings.Contains(all, "sha256:evidence") {
		t.Fatal("lost edit or retrieval evidence")
	}
	if !strings.Contains(all, "Current finding: the missing validation") {
		t.Fatal("lost working analysis during compaction")
	}
	after, _ := json.Marshal(history)
	if string(before) != string(after) {
		t.Fatal("mutated durable history")
	}
}
