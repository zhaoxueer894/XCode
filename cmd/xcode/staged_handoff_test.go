package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestWorkerHandoffRejectsToolMarkup(t *testing.T) {
	if err := validateWorkerHandoff("developer", "<｜｜DSML｜｜ calls>"); err == nil {
		t.Fatal("tool markup accepted as developer output")
	}
	if err := validateWorkerHandoff("planner", " "); err == nil {
		t.Fatal("empty planner handoff accepted")
	}
	if err := validateWorkerHandoff("planner", "The comparator in source.go mishandles unknown types. Correct that case and run the comparison test."); err != nil {
		t.Fatal(err)
	}
}

func TestStagedCandidatePatchRequiresActualProductionChanges(t *testing.T) {
	root := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		out, err := exec.Command("git", append([]string{"-C", root}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	write := func(path, content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(root, path), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	git("init", "-q")
	write("source.go", "package fixture\n")
	git("add", "source.go")
	git("-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "-qm", "baseline")
	candidate, err := stagedCandidatePatch(t.Context(), root)
	if err != nil {
		t.Fatal(err)
	}
	check := func(want bool) {
		t.Helper()
		got, err := candidate(t.Context())
		if got != want || err != nil {
			t.Fatalf("candidate = %v, %v, want %v", got, err, want)
		}
	}
	check(false)
	write("source_test.go", "package fixture\n")
	check(false)
	write("source.go", "package fixture\nconst Fix = true\n")
	git("add", "source.go")
	check(true)
	write("source.go", "package fixture\n")
	check(false)
}
