package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"xcode/internal/harness"
)

// This is execution evidence, not a correctness verdict. A completed model run
// can still have produced an incorrect patch; only an external verifier grades it.
type runRecord struct {
	SchemaVersion  int                         `json:"schema_version"`
	Status         string                      `json:"status"`
	StartedAt      time.Time                   `json:"started_at"`
	FinishedAt     time.Time                   `json:"finished_at"`
	WallTimeMillis int64                       `json:"wall_time_millis"`
	BinaryVersion  string                      `json:"binary_version"`
	Config         runRecordConfig             `json:"config"`
	ConfigHash     string                      `json:"config_hash"`
	PromptHash     string                      `json:"prompt_hash"`
	EventLog       string                      `json:"event_log,omitempty"`
	StopReason     harness.StopReason          `json:"stop_reason,omitempty"`
	Answer         string                      `json:"answer,omitempty"`
	Steps          int                         `json:"steps"`
	Approvals      int                         `json:"approval_requests"`
	Model          harness.ModelBudgetSnapshot `json:"model"`
	Error          string                      `json:"error,omitempty"`
}

type runRecordConfig struct {
	Workspace       string `json:"workspace"`
	Provider        string `json:"provider"`
	Model           string `json:"model"`
	Endpoint        string `json:"endpoint,omitempty"`
	Mode            string `json:"mode"`
	Sandbox         string `json:"sandbox"`
	SandboxImage    string `json:"sandbox_image"`
	ContextStrategy string `json:"context_strategy"`
	ContextBudget   int    `json:"context_budget"`
	MaxSteps        int    `json:"max_steps"`
	MaxTokens       int    `json:"max_tokens"`
	MaxModelCalls   int    `json:"max_model_calls"`
	ProviderRetries int    `json:"provider_retries"`
	Thinking        string `json:"thinking,omitempty"`
	ReasoningEffort string `json:"reasoning_effort,omitempty"`
	MaxOutputTokens int    `json:"max_output_tokens,omitempty"`
	DoomLoopLimit   int    `json:"doom_loop_limit"`
	Timeout         string `json:"timeout"`
	NoDelegate      bool   `json:"no_delegate"`
	NonInteractive  bool   `json:"non_interactive"`
	Isolated        bool   `json:"isolated"`
	ContractHash    string `json:"contract_hash,omitempty"`
	PolicyHash      string `json:"policy_hash,omitempty"`
	PreludeHash     string `json:"prelude_hash,omitempty"`
	Platform        string `json:"platform"`
	GoVersion       string `json:"go_version"`
}

func newRunRecord(config runRecordConfig) *runRecord {
	config.Workspace, _ = filepath.Abs(config.Workspace)
	config.Model = "demo"
	config.Platform, config.GoVersion = runtime.GOOS+"/"+runtime.GOARCH, runtime.Version()
	return &runRecord{SchemaVersion: 1, StartedAt: time.Now().UTC(), Config: config}
}

func (r *runRecord) finish(err error) {
	r.FinishedAt = time.Now().UTC()
	r.WallTimeMillis = r.FinishedAt.Sub(r.StartedAt).Milliseconds()
	r.Status = "completed"
	if err != nil {
		r.Status, r.Error = "error", err.Error()
		if r.StopReason == "" {
			r.StopReason = harness.StopFailed
		}
		switch {
		case errors.Is(err, harness.ErrBudget):
			r.Status, r.StopReason = "stopped", harness.StopBudget
		case errors.Is(err, harness.ErrMaxSteps):
			r.Status, r.StopReason = "stopped", harness.StopMaxSteps
		case errors.Is(err, harness.ErrDoomLoop):
			r.Status, r.StopReason = "stopped", harness.StopDoomLoop
		case errors.Is(err, context.DeadlineExceeded):
			r.Status = "timeout"
		case errors.Is(err, context.Canceled):
			r.Status = "cancelled"
		}
	}
	encoded, _ := json.Marshal(r.Config)
	r.ConfigHash = hashRunBytes(encoded)
}

func hashRunBytes(data []byte) string { sum := sha256.Sum256(data); return hex.EncodeToString(sum[:]) }

func safeEndpoint(value string) string {
	parsed, err := url.Parse(value)
	if err != nil {
		return "invalid"
	}
	parsed.User, parsed.RawQuery, parsed.Fragment = nil, "", ""
	return parsed.String()
}

func writeRunRecord(path string, record *runRecord) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("create run result: %w", err)
	}
	encoder := json.NewEncoder(file)
	encoder.SetIndent("", "  ")
	writeErr := encoder.Encode(record)
	return errors.Join(writeErr, file.Sync(), file.Close())
}
