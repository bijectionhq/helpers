package command_test

import (
	"concave/internal/analysis"
	"concave/internal/testrepo"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func contractFixture(t *testing.T) *testrepo.Fixture {
	t.Helper()
	configured := os.Getenv("CONCAVE_AST_GREP")
	if configured == "" {
		configured = "../../../../target/concave/engine/0.45.3/ast-grep"
	}
	binary, err := filepath.Abs(configured)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("CONCAVE_AST_GREP", binary)
	f := testrepo.New(t)
	f.Write("contract.md", "# Authentication\nInformative documentation.\n")
	f.Write("contracts/auth.rs", "// concave:invariant authentication\n// Reject incompatible audience claims.\nfn require_audience() {}\n")
	f.Write("crates/core/adapter.rs", "// concave:invariant audience-binding\n// Reject incompatible audiences.\nfn authenticate() {}\n")
	f.Config.Families[0].Contracts = []string{"contracts/auth.rs#authentication", "crates/core/adapter.rs#audience-binding"}
	f.Save()
	f.Commit()
	return f
}
func TestInspectLocationAndContractReferences(t *testing.T) {
	f := contractFixture(t)
	n, out, err := run(f, "inspect", "crates/core/adapter.rs:3", "--json")
	if n != 0 || !strings.Contains(out, "audience-binding") || !strings.Contains(out, `"scope": "family:engine"`) {
		t.Fatal(n, out, err)
	}
	for _, location := range []string{"crates/core/adapter.rs:90", "crates/core/adapter.rs:3:900", "crates/core/adapter.rs:0"} {
		if n, _, _ := run(f, "inspect", location); n != 2 {
			t.Fatal(location, n)
		}
	}
	f.Write("crates/core/adapter.rs", "// concave:invariant audience-binding\nfn authenticate() {}\n")
	n, out, err = run(f, "check", "crates/core/adapter.rs", "--json")
	if n != 1 || !strings.Contains(out, "invalid_contract") {
		t.Fatal(n, out, err)
	}
}

func TestRustTypeWitnessThroughInspectionAndContractReview(t *testing.T) {
	fixture := contractFixture(t)
	path := "crates/core/snapshot.rs"
	source := "struct Snapshot;\nimpl Snapshot { fn new() {} }\nimpl Read for Snapshot { fn read() {} }\n"
	fixture.Write(path, source)
	caseConfig := fixture.Config.Cases["authentication"]
	caseConfig.Witnesses = []string{path + "::Snapshot", path + "::Snapshot::read"}
	fixture.Config.Cases["authentication"] = caseConfig
	fixture.Save()
	fixture.Commit()
	report := inspection(t, fixture, "--case", "authentication", "--contracts")
	if len(report.Findings) != 0 || len(report.Contracts.Witnesses) != 2 {
		t.Fatal(report.Findings, report.Contracts.Witnesses)
	}
	for _, witness := range report.Contracts.Witnesses {
		if witness.Resolution != "symbol_found" || witness.Executed {
			t.Fatal(witness)
		}
	}
	packet := contractReview(t, fixture, "--case", "authentication")
	if len(packet.Candidate.Contracts.Witnesses) != 2 || packet.BehavioralParity != "not_established" {
		t.Fatal(packet.Candidate.Contracts.Witnesses, packet.BehavioralParity)
	}
	for i, witness := range report.Contracts.Witnesses {
		if packet.Candidate.Contracts.Witnesses[i] != witness {
			t.Fatal("review changed witness resolution", packet.Candidate.Contracts.Witnesses)
		}
	}
	fixture.Write(path, strings.TrimPrefix(source, "struct Snapshot;\n"))
	changed := contractReview(t, fixture, "--case", "authentication")
	if changed.Candidate.Contracts.Witnesses[0].Resolution != "symbol_not_resolved" || changed.Candidate.Contracts.Witnesses[1].Resolution != "symbol_found" || changed.SnapshotSHA256 == packet.SnapshotSHA256 {
		t.Fatal("implementation invented a type definition", changed.Candidate.Contracts.Witnesses)
	}
}
func TestExplicitUntrackedObligationAndMissingReference(t *testing.T) {
	f := contractFixture(t)
	f.Write("crates/core/new.rs", "// concave:invariant local\n// Must be discoverable before committing.\nfn new_owner() {}\n")
	n, out, err := run(f, "inspect", "crates/core/new.rs:3", "--json")
	if n != 0 || !strings.Contains(out, "local") {
		t.Fatal(n, out, err)
	}
	f.Config.Families[0].Contracts = []string{"crates/core/adapter.rs#absent"}
	f.Save()
	n, out, err = run(f, "check", "crates/core/lib.rs", "--json")
	if n != 1 || !strings.Contains(out, "does not exist") {
		t.Fatal(n, out, err)
	}
}
func TestPreservesDoesNotAuthorizeSourceChanges(t *testing.T) {
	f := contractFixture(t)
	f.Write("crates/core/lib.rs", "// concave:begin audience\n// why: Adapt the identity.\n// preserves: crates/core/adapter.rs#audience-binding\n// witness: crates/core/adapter.rs::authenticate\nfn changed() {}\n// concave:end audience\nfn second() {}\n")
	n, out, err := run(f, "inspect", "crates/core/lib.rs:5", "--json")
	if n != 0 || !strings.Contains(out, "unknown_fence") || !strings.Contains(out, "symbol_found") || !strings.Contains(out, `"executed": false`) {
		t.Fatal(n, out, err)
	}
	n, out, err = run(f, "exception", "record", "audience", "--path", "crates/core/lib.rs", "--json")
	if n != 0 || !strings.Contains(out, "audience-binding") {
		t.Fatal(n, out, err)
	}
	f.Write("crates/core/adapter.rs", "fn authenticate() {}\n")
	if n, _, _ := run(f, "exception", "record", "audience", "--path", "crates/core/lib.rs", "--json"); n != 2 {
		t.Fatal("record accepted missing invariant", n)
	}
}

func TestContractReviewComparesWholeObligationsAndConsumers(t *testing.T) {
	f := contractFixture(t)
	f.Write("crates/core/caller.rs", "// concave:invariant caller-safety\n// Admit only authenticated sessions.\nfn caller() { authenticate(); }\n")
	f.Commit()
	f.Write("crates/core/adapter.rs", "// concave:invariant audience-binding\n// Accept every audience.\nfn replacement() {}\n")
	n, out, err := run(f, "inspect", "queue", "--contracts", "--against", "HEAD", "crates/core/", "--json")
	if n != 0 {
		t.Fatal(n, out, err)
	}
	for _, want := range []string{"obligation_changed", "obligation_reattached", "source_changed", "Reject incompatible audiences.", "Accept every audience.", "caller-safety", "unresolved_lexical_candidate", "ready_for_review", "not_established"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %s: %s", want, out)
		}
	}
	var packet analysis.ContractReview
	if err := json.Unmarshal([]byte(out), &packet); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, consumer := range packet.Consumers {
		if consumer.Path != "crates/core/caller.rs" {
			continue
		}
		for _, scope := range consumer.Scopes {
			if scope.Reference == "contracts/auth.rs#authentication" && scope.Scope == "family:engine" && strings.Contains(packet.ConsumerDefinitions[scope.Reference].Text, "Reject incompatible") {
				found = true
			}
		}
	}
	if !found {
		t.Fatal("caller did not retain its family obligation", packet.Consumers)
	}
	f.Write("crates/core/adapter.rs", "fn authenticate() {}\n")
	n, out, err = run(f, "inspect", "queue", "--contracts", "crates/core/", "--json")
	if n != 0 || !strings.Contains(out, "obligation_removed") || !strings.Contains(out, "invalid_contract") {
		t.Fatal(n, out, err)
	}
}
func TestContractReviewNewFilesAndBoundedConsumers(t *testing.T) {
	f := contractFixture(t)
	f.Config.Families[0].Contracts = []string{"contracts/auth.rs#authentication"}
	f.Write("crates/core/adapter.rs", "fn unrelated() {}\n")
	f.Save()
	f.Write("crates/core/consumer.rs", "fn callers() {\n"+strings.Repeat("authenticate();\n", 80)+"}\n")
	f.Commit()
	f.Write("crates/core/new.rs", "// concave:invariant new-law\n// Reject incompatible audiences.\nfn authenticate() {}\n")
	n, out, err := run(f, "inspect", "queue", "--contracts", "crates/core/new.rs", "--json")
	if n != 0 || !strings.Contains(out, "obligation_added") || !strings.Contains(out, `"omitted_references": 16`) {
		t.Fatal(n, out, err)
	}
}
