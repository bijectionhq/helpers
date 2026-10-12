package command_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"concave/internal/analysis"
	"concave/internal/exceptions"
	"concave/internal/policy"
	"concave/internal/review"
	"concave/internal/testrepo"
)

func TestAnnotationPreviewWritesOnlySelectedSourceAndBindings(t *testing.T) {
	f := testrepo.New(t)
	f.Config.Writer = []string{"env"}
	f.Save()
	f.Commit()
	source := "fn first() { required(); }\nfn second() {}\n\nfn peer() {}\n"
	f.Write("crates/core/lib.rs", source)
	code, text, stderr := run(f, "inspect", "crates/core/lib.rs", "--json")
	if code != 0 {
		t.Fatal(code, text, stderr)
	}
	var report analysis.Report
	if err := json.Unmarshal([]byte(text), &report); err != nil {
		t.Fatal(err)
	}
	code, parentText, stderr := run(f, "review", "crates/core/lib.rs", "--json")
	if code != 0 {
		t.Fatal(code, parentText, stderr)
	}
	var parent review.Contribution
	if err := json.Unmarshal([]byte(parentText), &parent); err != nil {
		t.Fatal(err)
	}
	args := []string{"exception", "record", "required", "--annotate", "--path", "crates/core/lib.rs", "--hunk", report.Hunks[0].ID, "--contribution", parent.ID, "--why", "Preserve the required safety check.", "--witness", "crates/core/lib.rs", "--json"}
	code, text, stderr = run(f, args...)
	if code != 0 {
		t.Fatal(code, text, stderr)
	}
	var proposal exceptions.Proposal
	if err := json.Unmarshal([]byte(text), &proposal); err != nil {
		t.Fatal(err)
	}
	f.Write("target/proposal.json", text)
	before, _ := os.ReadFile(filepath.Join(f.Root, policy.File))
	raw, _ := os.ReadFile(filepath.Join(f.Root, "crates/core/lib.rs"))
	if string(raw) != source {
		t.Fatal("preview mutated source")
	}
	// Writer-active exercises the same checked write without launching a binary.
	code, text, stderr = run(f, append(args, "--write", "--expect", proposal.ProposalSHA256, "--writer-active")...)
	if code != 0 {
		t.Fatal(code, text, stderr)
	}
	var child review.Contribution
	if err := json.Unmarshal([]byte(text), &child); err != nil || child.Parent != parent.ID || len(child.Selected) != 0 || len(child.Approvals) != 1 {
		t.Fatal("linked review lost", err, text)
	}
	after, _ := os.ReadFile(filepath.Join(f.Root, policy.File))
	if string(before) != string(after) {
		t.Fatal("record changed human policy")
	}
	raw, _ = os.ReadFile(filepath.Join(f.Root, "crates/core/lib.rs"))
	if !strings.Contains(string(raw), "concave:end required\nfn second() {}\n\nfn peer() {}\n") {
		t.Fatal(string(raw))
	}
	_, cfg := f.Open()
	if len(cfg.Exceptions) != 1 || !cfg.Exceptions[0].Fence {
		t.Fatal(cfg.Exceptions)
	}
	code, text, stderr = run(f, "exception", "explain", "required", "--json")
	if code != 0 || !strings.Contains(text, `"status": "valid"`) {
		t.Fatal(code, text, stderr)
	}
	proposalPath := filepath.Join(f.Root, "target/proposal.json")
	code, text, stderr = run(f, "review", "crates/core/lib.rs", "--from", proposalPath, "--json")
	if code != 0 {
		t.Fatal(code, text, stderr)
	}
	var captured review.Contribution
	if err := json.Unmarshal([]byte(text), &captured); err != nil || len(captured.Approvals) != 1 {
		t.Fatal(err, text)
	}
	code, _, stderr = run(f, "review", "packaging/facade.ts", "--from", proposalPath)
	if code != 2 || !strings.Contains(stderr, "outside review scope") {
		t.Fatal(code, stderr)
	}
	f.Write("crates/core/lib.rs", strings.Replace(string(raw), "required();", "changed();", 1))
	code, _, stderr = run(f, "review", "crates/core/lib.rs", "--from", proposalPath)
	if code != 3 || !strings.Contains(stderr, "recorded source no longer valid") {
		t.Fatal(code, stderr)
	}
}

func TestAnnotationRejectsStaleSourceBeforeEitherWrite(t *testing.T) {
	f := testrepo.New(t)
	f.Write("crates/core/lib.rs", "fn first() { local(); }\nfn second() {}\n")
	r, c := f.Open()
	report, err := analysis.Analyze(r, c, analysis.Options{Paths: []string{"crates/core/lib.rs"}})
	if err != nil {
		t.Fatal(err)
	}
	p, err := exceptions.Prepare(r, c, exceptions.Record, []string{"guard"}, exceptions.Options{Annotate: true, Path: "crates/core/lib.rs", Hunks: []string{report.Hunks[0].ID}, Metadata: policy.Explanation{Why: "Preserve safety", Witnesses: []string{"crates/core/lib.rs"}}})
	if err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(filepath.Join(f.Root, policy.BindingPath("crates/core/lib.rs")))
	f.Write("crates/core/lib.rs", "peer edit\n")
	if err := exceptions.Write(r, c, p); err == nil {
		t.Fatal("stale annotation accepted")
	}
	after, _ := os.ReadFile(filepath.Join(f.Root, policy.BindingPath("crates/core/lib.rs")))
	if string(before) != string(after) {
		t.Fatal("partial approval write")
	}
}
