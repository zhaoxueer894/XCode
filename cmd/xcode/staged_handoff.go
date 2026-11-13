package main

import (
	"context"
	"fmt"
	"os/exec"
	"strings"

	"xcode/internal/harness"
	"xcode/internal/workspace"
)

const plannerHandoffPrompt = "\nFinish with a short plain-text paragraph for the developer: likely cause, relevant file or function, and the next edit or check. Use at most four sentences. Hand off as soon as the developer has enough to start; you do not need to finish debugging."

func validateWorkerHandoff(role, text string) error {
	text = strings.TrimSpace(text)
	if text == "" {
		return fmt.Errorf("empty %s handoff", role)
	}
	normalized := strings.ReplaceAll(text, "｜", "|")
	if strings.Contains(normalized, "|DSML|") || strings.Contains(normalized, "<tool_call") {
		return fmt.Errorf("tool call markup appeared in the %s handoff", role)
	}
	return nil
}

func stagedCandidatePatch(ctx context.Context, root string) (func(context.Context) (bool, error), error) {
	view, err := workspace.Open(root)
	if err != nil {
		return nil, err
	}
	git := func(ctx context.Context, args ...string) ([]byte, error) {
		return exec.CommandContext(ctx, "git", append([]string{"-C", root}, args...)...).Output()
	}
	head, err := git(ctx, "rev-parse", "--verify", "HEAD")
	if err != nil {
		return nil, fmt.Errorf("staged review requires a Git baseline: %w", err)
	}
	baseline := strings.TrimSpace(string(head))
	return func(ctx context.Context) (bool, error) {
		tracked, err := git(ctx, "diff", "--name-only", "-z", "--no-renames", "--no-ext-diff", baseline, "--")
		if err != nil {
			return false, fmt.Errorf("read tracked changes: %w", err)
		}
		untracked, err := git(ctx, "ls-files", "--others", "--exclude-standard", "-z", "--")
		if err != nil {
			return false, fmt.Errorf("read new files: %w", err)
		}
		for _, path := range strings.Split(string(append(tracked, untracked...)), "\x00") {
			if path != "" && !view.IsHardExcluded(path) && harness.IsProductionPath(path) {
				return true, nil
			}
		}
		return false, nil
	}, nil
}
