package command_test

import (
	"concave/internal/analysis"
	"strings"
	"testing"
)

func TestMacroWitnessThroughInspectionAndContractReview(t *testing.T) {
	fixture := contractFixture(t)
	path := "tests/identity.rs"
	source := "mod tests { proptest! { fn roundtrip(identity in any::<Identity>()) { prop_assert_eq!(identity, identity); } } }\n"
	fixture.Write(path, source)
	caseConfig := fixture.Config.Cases["authentication"]
	caseConfig.Witnesses = []string{path + "::tests::roundtrip"}
	fixture.Config.Cases["authentication"] = caseConfig
	fixture.Save()
	fixture.Commit()
	report := inspection(t, fixture, "--case", "authentication", "--contracts")
	if len(report.Findings) != 0 || len(report.Contracts.Witnesses) != 1 {
		t.Fatal(report.Findings, report.Contracts.Witnesses)
	}
	witness := report.Contracts.Witnesses[0]
	if witness.Resolution != "macro_input_found" || witness.Executed {
		t.Fatal(witness)
	}
	packet := contractReview(t, fixture, "--case", "authentication")
	if packet.Candidate.Contracts.Witnesses[0] != witness || packet.BehavioralParity != "not_established" {
		t.Fatal("review changed the meaning of macro discovery")
	}
	retained := false
	for _, input := range packet.Sources {
		if input.Path == path && input.Version == "candidate" && packet.Blobs[input.SHA256] == source {
			retained = true
		}
	}
	if !retained {
		t.Fatal("macro witness bytes absent from review")
	}
	code, text, stderr := run(fixture, "inspect", "queue", "--contracts", "--case", "authentication")
	if code != 0 || !strings.Contains(text, "macro_input_found; executed=false") {
		t.Fatal(code, text, stderr)
	}
	fixture.Write(path, strings.Replace(source, "fn roundtrip", "fn renamed", 1))
	changed := contractReview(t, fixture, "--case", "authentication")
	if changed.Candidate.Contracts.Witnesses[0].Resolution != "symbol_not_resolved" || changed.SnapshotSHA256 == packet.SnapshotSHA256 {
		t.Fatal("changed macro input escaped review identity")
	}
}

func TestMacroDefinitionWitnessThroughInspectionAndContractReview(t *testing.T) {
	fixture := contractFixture(t)
	path := "crates/core/serialization.rs"
	fixture.Write(path, "macro_rules! codegen { () => { fn generated() {} }; }\n")
	caseConfig := fixture.Config.Cases["authentication"]
	caseConfig.Witnesses = []string{path + "::codegen", path + "::generated"}
	fixture.Config.Cases["authentication"] = caseConfig
	fixture.Save()
	fixture.Commit()
	report := inspection(t, fixture, "--case", "authentication", "--contracts")
	packet := contractReview(t, fixture, "--case", "authentication")
	for _, witnesses := range [][]analysis.Witness{report.Contracts.Witnesses, packet.Candidate.Contracts.Witnesses} {
		if len(witnesses) != 2 || witnesses[0].Resolution != "symbol_found" || witnesses[1].Resolution != "symbol_not_resolved" || witnesses[0].Executed || witnesses[1].Executed {
			t.Fatal(witnesses)
		}
	}
}
