package command_test

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"concave/internal/analysis"
	"concave/internal/command"
	"concave/internal/policy"
	"concave/internal/review"
	"concave/internal/testrepo"
)

func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == "--repo" {
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		os.Exit(command.Run(ctx, os.Args[1:], os.Stdout, os.Stderr))
	}
	os.Exit(m.Run())
}
func run(f *testrepo.Fixture, args ...string) (int, string, string) {
	var out, err bytes.Buffer
	n := command.Run(context.Background(), append([]string{"--repo", f.Root}, args...), &out, &err)
	return n, out.String(), err.String()
}
func TestCLIJSONExitAndPolicyChange(t *testing.T) {
	f := testrepo.New(t)
	n, out, err := run(f, "check", "--json")
	if n != 0 {
		t.Fatal(n, out, err)
	}
	f.Write("crates/core/lib.rs", "return;\n")
	n, out, err = run(f, "check", "--json")
	if n != 1 || !json.Valid([]byte(out)) {
		t.Fatal(n, out, err)
	}
	f.Config.PreserveExactly = nil
	f.Save()
	n, out, err = run(f, "check", "--json")
	if n != 1 || !strings.Contains(out, "policy_changed") {
		t.Fatal(n, out, err)
	}
}
func TestCLIHistoricalPolicyIsExplicit(t *testing.T) {
	f := testrepo.New(t)
	n, _, _ := run(f, "inspect", "--rev", f.Base)
	if n != 2 {
		t.Fatal(n)
	}
	n, out, err := run(f, "inspect", "--rev", f.Base, "--policy-from", "worktree", "--json")
	if n != 0 || !strings.Contains(out, "working policy for historical") {
		t.Fatal(n, out, err)
	}
	n, _, _ = run(f, "check", "--policy-from", "worktree")
	if n != 2 {
		t.Fatal("check allowed external policy")
	}
}
func TestCLIMissingSourceAndCase(t *testing.T) {
	f := testrepo.New(t)
	for _, args := range [][]string{{"inspect", "--case", "missing"}, {"check", "--rev", "not-a-ref"}, {"inspect", "--metric", "unknown"}, {"inspect", "no-such-path"}} {
		n, _, _ := run(f, args...)
		if n != 2 {
			t.Fatal(args, n)
		}
	}
}
func TestCLIListAndSavedReports(t *testing.T) {
	f := testrepo.New(t)
	n, out, err := run(f, "list", "--json")
	if n != 0 || !strings.Contains(out, "authentication") {
		t.Fatal(n, out, err)
	}
	dst := filepath.Join(f.Root, "target/report")
	n, out, err = run(f, "inspect", "--case", "authentication", "--rev", "HEAD", "--output", dst)
	if n != 0 {
		t.Fatal(n, out, err)
	}
	for _, name := range []string{"inspection.json", "report.md", "hunks.json", "selected.patch", "local.patch", "working.patch"} {
		if _, e := os.Stat(filepath.Join(dst, name)); e != nil {
			t.Fatal(e)
		}
	}
}
func TestCLIRealWriterAndRecovery(t *testing.T) {
	f := testrepo.New(t)
	land, e := os.ReadFile(filepath.Join("..", "..", "..", "..", "scripts", "land.py"))
	if e != nil {
		t.Fatal(e)
	}
	f.Write("scripts/land.py", string(land))
	f.Config.Writer = []string{"python3", "scripts/land.py", "--edit", "--"}
	f.Save()
	f.Write("crates/core/lib.rs", "changed\nfn second() {}\n")
	f.Commit()
	index := f.Git("ls-files", "--stage")
	dir := filepath.Join(f.Root, "target/report")
	n, out, err := run(f, "inspect", "--rev", "HEAD", "--case", "authentication", "--output", dir)
	if n != 0 {
		t.Fatal(n, out, err)
	}
	b, _ := os.ReadFile(filepath.Join(dir, "inspection.json"))
	rep := new(analysis.Report)
	if e = json.Unmarshal(b, rep); e != nil {
		t.Fatal(e)
	}
	planDir := filepath.Join(f.Root, "target/plan")
	n, out, err = run(f, "recover", "plan", filepath.Join(dir, "inspection.json"), "--hunk", rep.Hunks[0].ID, "--reason", "restore", "--output", planDir)
	if n != 0 {
		t.Fatal(n, out, err)
	}
	f.Write(".git/land/sync-fixture/state.json", `{"phase":"applying"}`)
	n, _, _ = run(f, "recover", "apply", filepath.Join(planDir, "plan.json"))
	if n == 0 {
		t.Fatal("ignored synchronization")
	}
	os.Remove(filepath.Join(f.Root, ".git/land/sync-fixture/state.json"))
	n, parentText, err := run(f, "review", "crates/core/lib.rs", "--json")
	if n != 0 {
		t.Fatal(n, parentText, err)
	}
	var parent review.Contribution
	if err := json.Unmarshal([]byte(parentText), &parent); err != nil {
		t.Fatal(err)
	}
	n, out, err = run(f, "recover", "apply", filepath.Join(planDir, "plan.json"), "--contribution", parent.ID, "--json")
	if n != 0 || !strings.Contains(out, parent.ID) {
		t.Fatal(n, out, err)
	}
	if index != f.Git("ls-files", "--stage") {
		t.Fatal("index changed")
	}
	n, _, _ = run(f, "recover", "apply", filepath.Join(planDir, "plan.json"))
	if n != 3 {
		t.Fatal("repeat must return stale", n)
	}
}
func TestAgainstDoesNotPermitUnreviewedExistingChanges(t *testing.T) {
	f := testrepo.New(t)
	f.Write("crates/core/lib.rs", "unreviewed\n")
	ref := f.Commit()
	n, out, err := run(f, "check", "--rev", ref, "--against", ref, "--json")
	if n != 1 || !strings.Contains(out, "unexplained_change") {
		t.Fatal(n, out, err)
	}
}
func TestCommittedDirtyPolicyCannotRelaxCheck(t *testing.T) {
	f := testrepo.New(t)
	f.Write("npm-packages/client/protocol.ts", "changed\n")
	ref := f.Commit()
	f.Config.PreserveExactly = nil
	f.Save()
	n, out, err := run(f, "check", "--rev", ref, "--json")
	if n != 1 || !strings.Contains(out, "exact_file_changed") {
		t.Fatal(n, out, err)
	}
}

func TestReviewQueueCLI(t *testing.T) {
	f := testrepo.New(t)
	f.Write("crates/core/lib.rs", "changed\nfn second() {}\n")
	rev := f.Commit()
	n, out, err := run(f, "inspect", "queue", "--rev", rev, "--json")
	if n != 0 {
		t.Fatal(n, out, err)
	}
	var q analysis.FindingQueue
	if e := json.Unmarshal([]byte(out), &q); e != nil {
		t.Fatal(e)
	}
	if len(q.Groups) != 1 || q.Complete || q.Revision != rev {
		t.Fatal(q)
	}
	id := q.Groups[0].ID
	f.Write("crates/core/lib.rs", "unrelated working change\n")
	n, out, err = run(f, "inspect", "queue", "--rev", rev, "--group", id)
	if n != 0 || !strings.Contains(out, "+ changed") || !strings.Contains(out, "hunk=") || strings.Contains(out, "unrelated working change") {
		t.Fatal(n, out, err)
	}
	n, _, _ = run(f, "inspect", "queue", "--group", id)
	if n != 2 {
		t.Fatal("stale group accepted", n)
	}
	n, _, _ = run(f, "inspect", "queue", "--top", "-1")
	if n != 2 {
		t.Fatal("negative limit accepted")
	}
	n, out, err = run(f, "inspect", "queue", "--family", "protocol", "--json")
	if n != 0 || !strings.Contains(out, `"source_review_complete": false`) {
		t.Fatal(n, out, err)
	}
}

func TestCLIExplicitLiteralTransform(t *testing.T) {
	f := testrepo.New(t)
	path := "npm-packages/convex/index.ts"
	f.Write("contract.md", "# Fixture\n")
	f.Config.Transforms = []policy.Transform{{ID: "fixture-value", Paths: []string{path}, Replacements: []policy.Replacement{{From: "value = 1", To: "value = 2"}}, Explanation: policy.Explanation{Why: "Use the reviewed fixture value.", Witnesses: []string{path}}}}
	f.Save()
	f.Write(path, "export const value = 2;\n")
	f.Commit()
	n, out, err := run(f, "list", "--json")
	if n != 0 || !strings.Contains(out, `"replacements"`) || !strings.Contains(out, "Use the reviewed fixture value") {
		t.Fatal(n, out, err)
	}
	n, out, err = run(f, "check", path, "--rev", "HEAD", "--json")
	if n != 0 || !strings.Contains(out, `"transformed"`) {
		t.Fatal(n, out, err)
	}
	f.Write(path, "export const value = 3;\n")
	n, out, err = run(f, "check", path, "--json")
	if n != 1 || !strings.Contains(out, `"source/covered"`) {
		t.Fatal(n, out, err)
	}
}

func TestCLIWorkingRecoveryThroughRealWriter(t *testing.T) {
	f := testrepo.New(t)
	land, e := os.ReadFile(filepath.Join("..", "..", "..", "..", "scripts", "land.py"))
	if e != nil {
		t.Fatal(e)
	}
	f.Write("scripts/land.py", string(land))
	f.Config.Writer = []string{"python3", "scripts/land.py", "--edit", "--"}
	f.Save()
	f.Commit()
	f.Write("crates/core/lib.rs", "working drift\nfn second() {}\npeer tail\n")
	index := f.Git("ls-files", "--stage")
	dir := filepath.Join(f.Root, "target/working-report")
	n, out, err := run(f, "inspect", "crates/core/lib.rs", "--output", dir)
	if n != 0 {
		t.Fatal(n, out, err)
	}
	b, e := os.ReadFile(filepath.Join(dir, "inspection.json"))
	if e != nil {
		t.Fatal(e)
	}
	var report analysis.Report
	if e = json.Unmarshal(b, &report); e != nil {
		t.Fatal(e)
	}
	if report.Revision != "" || !report.Files[0].RecoverySupported {
		t.Fatal(report)
	}
	planDir := filepath.Join(f.Root, "target/working-plan")
	n, out, err = run(f, "recover", "plan", filepath.Join(dir, "inspection.json"), "--hunk", report.Hunks[0].ID, "--reason", "restore only the selected working drift", "--output", planDir)
	if n != 0 {
		t.Fatal(n, out, err)
	}
	f.Write(".git/land/sync-fixture/state.json", `{"phase":"applying"}`)
	n, _, _ = run(f, "recover", "apply", filepath.Join(planDir, "plan.json"))
	if n == 0 {
		t.Fatal("ignored active synchronization")
	}
	if e = os.Remove(filepath.Join(f.Root, ".git/land/sync-fixture/state.json")); e != nil {
		t.Fatal(e)
	}
	n, out, err = run(f, "recover", "apply", filepath.Join(planDir, "plan.json"))
	if n != 0 {
		t.Fatal(n, out, err)
	}
	got, e := os.ReadFile(filepath.Join(f.Root, "crates/core/lib.rs"))
	if e != nil || string(got) != "fn first() {}\nfn second() {}\npeer tail\n" {
		t.Fatal(string(got), e)
	}
	if index != f.Git("ls-files", "--stage") {
		t.Fatal("index changed")
	}
	n, _, _ = run(f, "recover", "apply", filepath.Join(planDir, "plan.json"))
	if n != 3 {
		t.Fatal("repeat must return stale", n)
	}
}
