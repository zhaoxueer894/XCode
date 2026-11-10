package main

import (
	"testing"

	"xcode/internal/config"
)

// TestClassifierKeyPrecedence: dedicated key > bailian fallback > env.
func TestClassifierKeyPrecedence(t *testing.T) {
	t.Setenv("DASHSCOPE_API_KEY", "env-key")
	if got := classifierKey(config.Config{TypedClassifierAPIKey: "own", BailianAPIKey: "bailian"}); got != "own" {
		t.Fatalf("got %q, want own", got)
	}
	if got := classifierKey(config.Config{BailianAPIKey: "bailian"}); got != "bailian" {
		t.Fatalf("got %q, want bailian fallback", got)
	}
	if got := classifierKey(config.Config{}); got != "env-key" {
		t.Fatalf("got %q, want env fallback", got)
	}
}
