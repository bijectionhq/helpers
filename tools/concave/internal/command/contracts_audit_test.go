package command_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"concave/internal/analysis"
	"concave/internal/artifact"
	"concave/internal/policy"
	"concave/internal/testrepo"
)

func inspection(t *testing.T, f *testrepo.Fixture, args ...string) analysis.Report {
	t.Helper()
	n, out, stderr := run(f, append([]string{"inspect", "--json"}, args...)...)
	if n != 0 {
		t.Fatal(n, out, stderr)
	}
	var report analysis.Report
	if err := json.Unmarshal([]byte(out), &report); err != nil {
		t.Fatal(err)
	}
	return report
}

func contractReview(t *testing.T, f *testrepo.Fixture, args ...string) analysis.ContractReview {
	t.Helper()
	n, out, stderr := run(f, append([]string{"inspect", "queue", "--contracts", "--json"}, args...)...)
	if n != 0 {
		t.Fatal(n, out, stderr)
	}
	var packet analysis.ContractReview
	if err := json.Unmarshal([]byte(out), &packet); err != nil {
		t.Fatal(err)
	}
	return packet
}

func TestContractAuditFingerprintsIncludeReferencesAndCommittedScope(t *testing.T) {
	f := contractFixture(t)
	a := inspection(t, f, "--contracts", "crates/core/lib.rs")
	f.Write("contracts/auth.rs", "// concave:invariant authentication\n// Changed obligation.\nfn require_audience() {}\n")
	b := inspection(t, f, "--contracts", "crates/core/lib.rs")
	if a.SnapshotSHA256 == b.SnapshotSHA256 {
		t.Fatal("contract edit absent from inspection identity")
	}
	a = inspection(t, f, "--rev", "HEAD", "crates/core/lib.rs")
	b = inspection(t, f, "--rev", "HEAD", "npm-packages/client/protocol.ts")
	if a.SnapshotSHA256 == b.SnapshotSHA256 {
		t.Fatal("committed scopes share identity")
	}
}

func TestContractAuditRepositoryRequirementIsEnforced(t *testing.T) {
	f := contractFixture(t)
	f.Config.Contract = "contracts/auth.rs#absent"
	f.Save()
	f.Commit()
	n, out, stderr := run(f, "check", "npm-packages/client/protocol.ts", "--json")
	if n != 1 || !strings.Contains(out, "invalid_contract") {
		t.Fatal(n, out, stderr)
	}
}

func TestContractAuditWitnessEngineUnavailable(t *testing.T) {
	f := contractFixture(t)
	f.Config.Families[0].Contracts = nil
	k := f.Config.Cases["authentication"]
	k.Witnesses = []string{"npm-packages/client/protocol.ts::Request"}
	k.Surfaces = []policy.Surface{{ID: "protocol", Purpose: "witness discovery", Paths: []string{"npm-packages/client/protocol.ts"}}}
	f.Config.Cases["authentication"] = k
	f.Save()
	t.Setenv("CONCAVE_AST_GREP", filepath.Join(f.Root, "missing-engine"))
	n, _, stderr := run(f, "inspect", "--contracts", "--case", "authentication", "--json")
	if n != 2 {
		t.Fatal("missing engine hidden as unresolved symbol", n, stderr)
	}
}

func TestWikiChangesDoNotAffectSourceObligations(t *testing.T) {
	f := contractFixture(t)
	before := inspection(t, f, "--contracts", "crates/core/lib.rs")
	for _, operation := range []string{"edit", "remove"} {
		if operation == "edit" {
			f.Write("contract.md", "# Renamed documentation\nMalleable wiki prose.\n")
		} else if err := os.Remove(filepath.Join(f.Root, "contract.md")); err != nil {
			t.Fatal(err)
		}
		after := inspection(t, f, "--contracts", "crates/core/lib.rs")
		if before.SnapshotSHA256 != after.SnapshotSHA256 || len(after.Findings) != 0 {
			t.Fatal("wiki became a lint input", after.Findings)
		}
		n, out, err := run(f, "check", "crates/core/lib.rs", "--json")
		if n != 0 {
			t.Fatal(n, out, err)
		}
		p := contractReview(t, f, "crates/core/lib.rs")
		for _, source := range p.Sources {
			if source.Path == "contract.md" {
				t.Fatal("wiki entered review obligations")
			}
		}
	}
}

func TestContractAuditReviewDeletedNativeFile(t *testing.T) {
	f := contractFixture(t)
	f.Config.Families[0].Contracts = []string{"contracts/auth.rs#authentication"}
	f.Save()
	f.Commit()
	if err := os.Remove(filepath.Join(f.Root, "crates/core/adapter.rs")); err != nil {
		t.Fatal(err)
	}
	f.Commit()
	p := contractReview(t, f, "--against", "HEAD~1", "crates/core/adapter.rs")
	found := false
	for _, c := range p.Changes {
		if c.Kind == "obligation_removed" {
			found = true
		}
	}
	if !found {
		t.Fatal("deleted native obligation omitted", p.Changes)
	}
}

func TestContractAuditReviewWitnessChangesAndUntrackedConsumer(t *testing.T) {
	f := contractFixture(t)
	f.Write("tests/witness.rs", "fn verify() { assert!(true); }\n")
	k := f.Config.Cases["authentication"]
	k.Witnesses = []string{"tests/witness.rs::verify"}
	f.Config.Cases["authentication"] = k
	f.Save()
	f.Commit()
	f.Write("tests/witness.rs", "fn verify() {}\n")
	f.Write("crates/core/new_consumer.rs", "fn caller() { authenticate(); }\n")
	p := contractReview(t, f, "--case", "authentication")
	witness, consumer := false, false
	for _, c := range p.Changes {
		if c.Kind == "witness_source_changed" && c.Path == "tests/witness.rs" {
			witness = true
		}
	}
	for _, c := range p.Consumers {
		if c.Path == "crates/core/new_consumer.rs" {
			consumer = true
		}
	}
	if !witness || !consumer {
		t.Fatal("missing changed witness or new consumer", witness, consumer)
	}
}

func TestContractAuditReviewNewCaseAndScopeRemoval(t *testing.T) {
	f := contractFixture(t)
	f.Config.Cases["new-case"] = policy.Case{Surfaces: []policy.Surface{{ID: "core", Purpose: "review", Paths: []string{"crates/core/"}}}}
	f.Config.Families[0].Contracts = nil
	f.Save()
	p := contractReview(t, f, "--case", "new-case")
	found := false
	for _, c := range p.Changes {
		if c.Kind == "obligation_removed" && c.Reference == "contracts/auth.rs#authentication" {
			found = true
		}
	}
	if !found {
		t.Fatal("removed source requirement omitted", p.Changes)
	}
}

func TestContractAuditPacketRetainsAbsentReferencesAndExactAssets(t *testing.T) {
	f := contractFixture(t)
	f.Write("crates/core/asset.json", "{\"known\":true}\n")
	f.Config.Upstream = f.Commit()
	k := f.Config.Cases["authentication"]
	k.Witnesses = []string{"tests/missing.rs"}
	f.Config.Cases["authentication"] = k
	f.Save()
	f.Commit()
	p := contractReview(t, f, "--case", "authentication")
	absent, asset := false, false
	for _, s := range p.Sources {
		if s.Version != "candidate" {
			continue
		}
		if s.Path == "tests/missing.rs" && s.Unavailable != "" {
			absent = true
		}
		if s.Path == "crates/core/asset.json" && p.Blobs[s.SHA256] == "{\"known\":true}\n" {
			asset = true
		}
	}
	if !absent || !asset {
		t.Fatal("missing source identity", absent, asset)
	}
}

func TestContractAuditScopeNarrowingAndCommittedIsolation(t *testing.T) {
	f := contractFixture(t)
	k := f.Config.Cases["authentication"]
	k.Surfaces = []policy.Surface{{ID: "core", Purpose: "before", Paths: []string{"crates/core/"}}}
	f.Config.Cases["authentication"] = k
	f.Save()
	old := f.Commit()
	k.Surfaces = []policy.Surface{{ID: "package", Purpose: "after", Paths: []string{"packaging/"}}}
	f.Config.Cases["authentication"] = k
	f.Save()
	candidate := f.Commit()
	f.Write("crates/core/adapter.rs", "malformed concave:invariant dirty work\n")
	p := contractReview(t, f, "--case", "authentication", "--against", old, "--rev", candidate)
	found := false
	for _, file := range p.Candidate.Contracts.Files {
		if file.Path == "crates/core/adapter.rs" && len(file.Invariants) == 1 {
			found = true
		}
	}
	if !found {
		t.Fatal("narrowed case hid old source or read dirty source")
	}
	for _, finding := range p.Candidate.Findings {
		if finding.Kind == "invalid_contract" {
			t.Fatal(finding)
		}
	}
}

func TestContractAuditSavedPacket(t *testing.T) {
	f := contractFixture(t)
	dir := filepath.Join(f.Root, "target/review")
	n, out, stderr := run(f, "inspect", "queue", "--contracts", "crates/core/", "--output", dir, "--json")
	if n != 0 {
		t.Fatal(n, out, stderr)
	}
	data, err := os.ReadFile(filepath.Join(dir, "context.json"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != out {
		t.Fatal("saved and printed context disagree")
	}
	n, _, _ = run(f, "inspect", "queue", "--contracts", "crates/core/", "--output", dir)
	if n != 2 {
		t.Fatal("existing review output overwritten", n)
	}
}

func TestContractAuditFullReviewKeepsRepositoryCoverage(t *testing.T) {
	f := contractFixture(t)
	p := contractReview(t, f)
	if p.Candidate.Coverage.Scope != "repository" || p.Candidate.Coverage.Uninspected != 0 {
		t.Fatal(p.Candidate.Coverage)
	}
}

func TestWikiCannotBeDeclaredAsAnObligation(t *testing.T) {
	for _, scope := range []string{"repository", "family", "case"} {
		t.Run(scope, func(t *testing.T) {
			f := contractFixture(t)
			switch scope {
			case "repository":
				f.Config.Contract = "contract.md#authentication"
			case "family":
				f.Config.Families[0].Contracts = []string{"contract.md#authentication"}
			case "case":
				k := f.Config.Cases["authentication"]
				k.Contracts = []string{"contract.md#authentication"}
				f.Config.Cases["authentication"] = k
			}
			if err := f.Config.Validate(); err == nil {
				t.Fatal("wiki became an obligation")
			}
		})
	}
	f := contractFixture(t)
	if n, _, _ := run(f, "exception", "record", "x", "--contract", "contract.md"); n != 2 {
		t.Fatal("retired --contract flag accepted", n)
	}
	var explanation policy.Explanation
	if err := artifact.Decode([]byte(`{"why":"Required behavior","contract":"contract.md","witnesses":["source.rs"]}`), &explanation); err == nil {
		t.Fatal("retired explanation field accepted")
	}
}
