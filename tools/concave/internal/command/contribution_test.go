package command_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"concave/internal/analysis"
	"concave/internal/policy"
	"concave/internal/review"
	"concave/internal/testrepo"
)

func TestContributionCaptureCheckAndStaleness(t *testing.T) {
	f := testrepo.New(t)
	code, text, stderr := run(f, "review", "crates/core/lib.rs", "--json")
	if code != 0 {
		t.Fatal(code, text, stderr)
	}
	var r review.Contribution
	if err := json.Unmarshal([]byte(text), &r); err != nil {
		t.Fatal(err)
	}
	code, text, stderr = run(f, "review", "status", r.ID[:10])
	if code != 0 {
		t.Fatal(code, text, stderr)
	}
	f.Write("peer", "unrelated")
	f.Commit()
	code, text, stderr = run(f, "review", "status", r.ID)
	if code != 0 {
		t.Fatal(code, text, stderr)
	}
	f.Write("crates/core/lib.rs", "later edit\n")
	code, _, stderr = run(f, "review", "status", r.ID)
	if code != 3 || !strings.Contains(stderr, "input changed") {
		t.Fatal(code, stderr)
	}
}

func TestContributionRejectsNewScopeMember(t *testing.T) {
	f := testrepo.New(t)
	code, text, stderr := run(f, "review", "crates/core/", "--json")
	if code != 0 {
		t.Fatal(code, text, stderr)
	}
	var r review.Contribution
	if err := json.Unmarshal([]byte(text), &r); err != nil {
		t.Fatal(err)
	}
	f.Write("crates/core/new.rs", "fn new() {}\n")
	code, _, stderr = run(f, "review", "status", r.ID)
	if code != 3 || !strings.Contains(stderr, "scope membership changed") {
		t.Fatal(code, stderr)
	}
}

func TestContributionDoesNotOfferDependencyEditsAsSource(t *testing.T) {
	f := testrepo.New(t)
	f.Config.Families[0].Title += " (edited)"
	f.Save()
	f.Write("crates/core/lib.rs", "fn first() { changed(); }\nfn second() {}\n")
	code, text, stderr := run(f, "review", "crates/core/lib.rs", "--json")
	if code != 0 {
		t.Fatal(code, text, stderr)
	}
	var r review.Contribution
	if err := json.Unmarshal([]byte(text), &r); err != nil {
		t.Fatal(err)
	}
	if len(r.Hunks) != 1 || r.Hunks[0].Path != "crates/core/lib.rs" || r.Inputs[policy.File] == nil {
		t.Fatal("dependency must be bound without becoming a contribution", r.Hunks, r.Inputs)
	}
}

func TestContributionRunsMappedWitnessAndKeepsParentImmutable(t *testing.T) {
	f := testrepo.New(t)
	f.Config.Cases["authentication"] = policy.Case{Surfaces: []policy.Surface{{ID: "core", Purpose: "test", Paths: []string{"crates/core/"}}}}
	f.Config.Checks = map[string]policy.CheckDefinition{"local": {Command: []string{"sh", "-c", `printf '%s\n' '{"Action":"pass","Package":"pkg","Test":"TestRead"}'`}, Directory: ".", Inputs: []string{"crates/core/"}, Toolchain: []policy.ToolCheck{{Command: []string{"sh", "-c", "printf v1"}, Expected: "v1"}}, TimeoutSeconds: 5, Results: "go-test-json", Witnesses: map[string][]string{"crates/core/lib.rs": {"pkg::TestRead"}}}}
	f.Config.Exceptions = []policy.Exception{}
	f.Save()
	f.Commit()
	// A path witness needs no parser and still requires explicit execution mapping.
	f.Config.Transforms = []policy.Transform{{ID: "first", Paths: []string{"crates/core/lib.rs"}, Replacements: []policy.Replacement{{From: "fn first() {}", To: "fn first() { changed(); }"}}, Explanation: policy.Explanation{Why: "fixture transformation", Witnesses: []string{"crates/core/lib.rs"}}}}
	f.Save()
	f.Commit()
	f.Write("crates/core/lib.rs", "fn first() { changed(); }\nfn second() {}\n")
	code, text, stderr := run(f, "review", "crates/core/lib.rs", "--json")
	if code != 0 {
		t.Fatal(code, text, stderr)
	}
	var r review.Contribution
	if err := json.Unmarshal([]byte(text), &r); err != nil {
		t.Fatal(err)
	}
	if len(r.RequiredChecks) != 1 {
		t.Fatal(r.RequiredChecks, r.UnmappedWitnesses)
	}
	parentPath := filepath.Join(f.Root, "target/concave/reviews/objects", r.ID[2:10], r.ID+".json")
	parent, _ := os.ReadFile(parentPath)
	code, _, stderr = run(f, "review", "status", r.ID)
	if code != 1 {
		t.Fatal(code, stderr)
	}
	// Verifying the capture prepares every shown change through the publisher,
	// which this fixture lacks; the failure leaves the capture as it was.
	code, _, stderr = run(f, "verify", r.ID, "--json")
	if code != 2 || !strings.Contains(stderr, "landing baselines") {
		t.Fatal(code, stderr)
	}

	after, _ := os.ReadFile(parentPath)
	if string(parent) != string(after) {
		t.Fatal("parent rewritten")
	}
}

func TestChangedScopeIncludesDeletionAndPolicyExpansion(t *testing.T) {
	f := testrepo.New(t)
	f.Write("crates/core/lib.rs", "old divergence\n")
	f.Commit()
	code, text, stderr := run(f, "check", "--changed", "--json")
	var empty analysis.Report
	if code != 0 || json.Unmarshal([]byte(text), &empty) != nil || empty.Kind != "concave-inspection" || len(empty.Files) != 0 || empty.Coverage.Inspected != 0 || empty.Coverage.Uninspected == 0 {
		t.Fatal(code, text, stderr)
	}
	if err := os.Remove(filepath.Join(f.Root, "npm-packages/client/protocol.ts")); err != nil {
		t.Fatal(err)
	}
	code, text, stderr = run(f, "check", "--changed", "--json")
	if code != 1 || !strings.Contains(text, "missing") {
		t.Fatal(code, text, stderr)
	}
	f.Config.Families[0].Title += " (edited)"
	f.Save()
	code, text, stderr = run(f, "check", "--changed", "--json")
	if code != 1 || !strings.Contains(text, "crates/core/lib.rs") {
		t.Fatal(code, text, stderr)
	}
}

func TestReviewCannotRemoveItsOwnWitnessDuty(t *testing.T) {
	for _, remove := range []bool{false, true} {
		t.Run(map[bool]string{false: "case", true: "mapping"}[remove], func(t *testing.T) {
			f := testrepo.New(t)
			f.Config.Checks = map[string]policy.CheckDefinition{"local": {Command: []string{"sh", "-c", "true"}, Directory: ".", Inputs: []string{"crates/core/"}, Toolchain: []policy.ToolCheck{{Command: []string{"sh", "-c", "printf v1"}, Expected: "v1"}}, TimeoutSeconds: 5, Results: "go-test-json", Witnesses: map[string][]string{"crates/core/lib.rs": {"pkg::old", "pkg::retained"}}}}
			f.Save()
			f.Commit()
			if remove {
				delete(f.Config.Checks, "local")
			} else {
				check := f.Config.Checks["local"]
				check.Witnesses["crates/core/lib.rs"] = []string{"pkg::retained"}
				f.Config.Checks["local"] = check
			}
			f.Save()
			code, text, stderr := run(f, "review", policy.File, "--json")
			if code != 0 {
				t.Fatal(code, text, stderr)
			}
			var r review.Contribution
			if err := json.Unmarshal([]byte(text), &r); err != nil {
				t.Fatal(err)
			}
			code, text, stderr = run(f, "review", "status", r.ID, "--json")
			if code != 1 || !strings.Contains(text, "policy_witness_weakened") {
				t.Fatal(code, text, stderr)
			}
		})
	}
}

// A converted document's upstream bytes live at its declared upstream path; a
// review of the converted page reads them there.
func TestReviewCapturesACorrespondedDocument(t *testing.T) {
	f := testrepo.New(t)
	f.Documents()
	f.Write("npm-packages/docs/docs/guide.mdx", "# Guide\n\nOriginal text.\n")
	f.Config.Upstream = f.Commit()
	f.Config.Roots = append(f.Config.Roots, "apps")
	f.Config.Families = append(f.Config.Families, policy.Family{ID: "docs", Title: "Docs", Paths: []string{"apps/docs/"}})
	f.Config.Documents = &policy.Documents{Paths: []string{"apps/docs/"}, Correspondences: []policy.Correspondence{{Path: "apps/docs/guide.mdx", UpstreamPath: "npm-packages/docs/docs/guide.mdx"}}}
	f.Save()
	f.Write("apps/docs/guide.mdx", "# Guide\n\nOriginal text.\n")
	f.Commit()
	f.Write("apps/docs/guide.mdx", "# Guide\n\nEdited text.\n")
	code, text, stderr := run(f, "review", "apps/docs/guide.mdx", "--json")
	var r review.Contribution
	if code != 0 || json.Unmarshal([]byte(text), &r) != nil || len(r.Hunks) != 1 {
		t.Fatal(code, text, stderr)
	}
}
