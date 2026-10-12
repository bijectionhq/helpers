package exceptions_test

import (
	"testing"

	"concave/internal/analysis"
	"concave/internal/exceptions"
	"concave/internal/testrepo"
)

// rustfmt leaves two fences adjacent with a blank line between them, and Git
// reports their markers and new imports as one inserted hunk (the
// crates/database/src/token.rs layout). Each fence owns its part of it.
func TestAdjacentFencesShareOneInsertedHunk(t *testing.T) {
	f := testrepo.New(t)
	f.Write(path, "//! Tokens.\n\nuse std::sync::Arc;\n\nuse common::types::Timestamp;\nuse crate::reads::ReadSet;\n")
	f.Config.Upstream = f.Commit()
	f.Write("contract.md", "# Read\n")
	f.Save()
	f.Commit()
	f.Write(path, "//! Tokens.\n\n"+
		"// concave:begin token-expiry\n// why: Expiry is a duration.\n// witness: tests::expiry\nuse std::{\n    sync::Arc,\n    time::Duration,\n};\n\n// concave:end token-expiry\n\n"+
		"// concave:begin token-test-imports\n// why: Restored testing constructors.\n// witness: tests::imports\n#[cfg(test)]\nuse common::types::TabletIndexName;\nuse common::types::Timestamp;\n\n// concave:end token-test-imports\n"+
		"use crate::reads::ReadSet;\n")
	repo, config := f.Open()
	proposal, err := exceptions.Prepare(repo, config, exceptions.Record, []string{"token-expiry", "token-test-imports"}, exceptions.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if err := exceptions.Write(repo, config, proposal); err != nil {
		t.Fatal(err)
	}
	repo, config = f.Open()
	report, err := analysis.Analyze(repo, config, analysis.Options{Paths: []string{path}})
	if err != nil || len(report.Findings) != 0 || report.Files[0].Classification != "excepted" {
		t.Fatalf("%v %+v %+v", err, report.Findings, report.Hunks)
	}
}
