package command_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"concave/internal/analysis"
	"concave/internal/process"
	"concave/internal/testrepo"
)

func TestCheckFeedbackKeepsScopeAndConcreteContinuation(t *testing.T) {
	f := testrepo.New(t)
	code, text, stderr := run(f, "check", "--changed")
	if code != 0 || !strings.Contains(text, "No changed files.") || strings.Contains(text, "{") {
		t.Fatal(code, text, stderr)
	}
	code, text, stderr = run(f, "check", "--changed", "--rdjson")
	var diagnostic struct {
		Diagnostics []any `json:"diagnostics"`
	}
	if code != 0 || json.Unmarshal([]byte(text), &diagnostic) != nil || len(diagnostic.Diagnostics) != 0 || strings.Contains(text, "changed_paths") {
		t.Fatal(code, text, stderr)
	}
	directory := filepath.Join(f.Root, "target/empty-check")
	code, text, stderr = run(f, "check", "--changed", "--json", "--output", directory)
	var empty analysis.Report
	if code != 0 || json.Unmarshal([]byte(text), &empty) != nil || empty.Kind != "concave-inspection" || empty.Format != 1 || empty.Status != "passed" || empty.Coverage.Inspected != 0 || empty.Coverage.Uninspected == 0 || empty.Next == nil {
		t.Fatal(code, text, stderr)
	}
	saved, err := os.ReadFile(filepath.Join(directory, "inspection.json"))
	var persisted analysis.Report
	if err != nil || json.Unmarshal(saved, &persisted) != nil || persisted.SnapshotSHA256 != empty.SnapshotSHA256 {
		t.Fatal("empty check not saved", err, string(saved))
	}
	f.Write("crates/core/lib.rs", "fn first() { drift(); }\nfn second() {}\n")
	code, text, stderr = run(f, "check", "crates/core/lib.rs")
	for _, want := range []string{"Source failed", "1 file inspected", "uninspected", "Behavioral checks not run", "source/covered", "Next:", f.Root, "inspect", "--contracts"} {
		if code != 1 || !strings.Contains(text, want) {
			t.Fatal(want, code, text, stderr)
		}
	}
	code, text, stderr = run(f, "check", "crates/core/lib.rs", "--json")
	var report analysis.Report
	if code != 1 || json.Unmarshal([]byte(text), &report) != nil || report.Metric != nil {
		t.Fatal("scoped check computed an unused metric", code, text, stderr)
	}
	if report.Next == nil || report.Next.Kind != "inspect" {
		t.Fatal("JSON omitted continuation", text)
	}
	_, rendered, _ := run(f, "check", "crates/core/lib.rs")
	if !strings.Contains(rendered, process.ShellCommand(report.Next.Argv)) {
		t.Fatal("text and JSON actions disagree", rendered, report.Next)
	}
	code, text, stderr = run(f, report.Next.Argv[1:]...)
	if code != 0 || !strings.Contains(text, "Behavioral obligations") {
		t.Fatal("next action was not executable", code, text, stderr)
	}
	// A changed policy expands enforcement to the complete inventory.
	f.Config.Families[0].Title += " (edited)"
	f.Save()
	code, text, stderr = run(f, "check", "--changed", "--json")
	if code != 1 || !strings.Contains(text, "policy_changed") || !strings.Contains(text, `"path": "npm-packages/client/protocol.ts"`) {
		t.Fatal("policy expansion omitted the full audit", code, text, stderr)
	}
}

func TestReviewRefusesUnsupportedChangeBeforePreparingCandidate(t *testing.T) {
	fixture := testrepo.New(t)
	fixture.Write("packaging/binary.bin", "original\x00bytes")
	fixture.Commit()
	fixture.Write("packaging/binary.bin", "changed\x00bytes")
	code, text, stderr := run(fixture, "review", "packaging/binary.bin")
	if code != 2 || !strings.Contains(stderr, "bounded regular UTF-8 source") || strings.Contains(text, "Select") {
		t.Fatal(code, text, stderr)
	}
	code, text, stderr = run(fixture, "review", "packaging/facade.ts")
	if code != 0 || !strings.Contains(text, "nothing to prepare") {
		t.Fatal(code, text, stderr)
	}
}

func TestChangedScopeAcceptsKnownForkOwnedDeletionWithoutHidingBadSelectors(t *testing.T) {
	fixture := testrepo.New(t)
	path := "packaging/retired.go"
	fixture.Write(path, "package retired\n")
	base := fixture.Commit()
	if err := os.Remove(filepath.Join(fixture.Root, path)); err != nil {
		t.Fatal(err)
	}
	for _, committed := range []bool{false, true} {
		args := []string{"check", "--changed", "--against", base, "--json"}
		if committed {
			args = append(args, "--rev", fixture.Commit())
		}
		code, text, stderr := run(fixture, args...)
		var report analysis.Report
		if code != 0 || json.Unmarshal([]byte(text), &report) != nil || len(report.Unmatched) != 0 || len(report.Scope.Paths) != 1 || report.Scope.Paths[0] != path || report.BehavioralParity != "not_established" {
			t.Fatal(code, text, stderr)
		}
	}
	if code, text, stderr := run(fixture, "check", path, "--json"); code != 2 {
		t.Fatal("arbitrary missing selector was accepted", code, text, stderr)
	}
}
