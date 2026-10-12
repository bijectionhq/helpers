package output

import (
	"bytes"
	"concave/internal/analysis"
	"concave/internal/repository"
	"strings"
	"testing"
)

func TestLocationInspectionKeepsOnlyContainingHunks(t *testing.T) {
	r := &analysis.Report{Contracts: &analysis.ContractContext{Location: &analysis.Location{Path: "core.rs", Line: 10}}, Hunks: []repository.Hunk{
		{ID: "unrelated", Path: "core.rs", CurrentStart: 0, CurrentEnd: 2},
		{ID: "containing", Path: "core.rs", CurrentStart: 9, CurrentEnd: 11},
		{ID: "other-file", Path: "other.rs", CurrentStart: 9, CurrentEnd: 11},
	}}
	var out bytes.Buffer
	Text(&out, r, 1)
	if strings.Contains(out.String(), "unrelated") || strings.Contains(out.String(), "other-file") || !strings.Contains(out.String(), "hunk containing") {
		t.Fatal(out.String())
	}
	if len(r.Hunks) != 3 {
		t.Fatal("rendering changed saved evidence")
	}
}

func TestCaseDiagnosticIsBoundedWithoutLosingFirstAction(t *testing.T) {
	full := "Regenerate: just reference\n" + strings.Repeat("runner detail\n", 400)
	got := shortDiagnostic(full)
	if !strings.HasPrefix(got, "Regenerate: just reference") || !strings.Contains(got, "full diagnostic") || len(got) > 2200 {
		t.Fatal(got)
	}
	if got := shortDiagnostic(strings.Repeat("x", 5000)); len(got) > 2200 {
		t.Fatal(len(got))
	}
}

func TestTAPFeedbackLeadsWithAssertionAndKeepsPlainText(t *testing.T) {
	const tap = "  duration_ms: 10\n  failureType: 'testCodeFailure'\n  error: |-\n    Regenerate: just reference\n    stale pages\n  stack: |-\n    internals\n"
	if got := checkDiagnostic("node-test-tap", tap); got != "Regenerate: just reference\nstale pages" {
		t.Fatal(got)
	}
	if got := checkDiagnostic("go-test-json", tap); got != tap {
		t.Fatal("altered another runner", got)
	}
	if got := checkDiagnostic("node-test-tap", "plain failure"); got != "plain failure" {
		t.Fatal(got)
	}
}

func TestFileReferenceHasNoExecutableWitnessClaim(t *testing.T) {
	var out bytes.Buffer
	witnessReference(&out, "src/core.rs", "tests/README.md")
	witnessReference(&out, "src/core.rs", "tests/check.rs::reject")
	for _, want := range []string{"source reference: tests/README.md", "no executable test", "witness: tests/check.rs::reject (not executed)"} {
		if !strings.Contains(out.String(), want) {
			t.Fatal(out.String())
		}
	}
}
