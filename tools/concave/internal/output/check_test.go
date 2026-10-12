package output

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"concave/internal/analysis"
	"concave/internal/policy"
	"concave/internal/review"
	"concave/internal/verification"
)

func TestFailureFeedbackNamesCaseAndBoundsRunnerOutput(t *testing.T) {
	dir := t.TempDir()
	log := strings.Repeat("x", 8192) + "\nassertion: expected refusal\n"
	if err := os.WriteFile(filepath.Join(dir, "stdout.log"), []byte(log), 0600); err != nil {
		t.Fatal(err)
	}
	check := review.CheckOutcome{Name: "auth", Status: verification.Failed, Directory: dir, Results: verification.Results{Failed: 1, Executed: 1, Cases: []verification.Case{{ID: "auth::reject_wrong_audience", Status: "failed"}}}, Problems: []verification.Problem{{Code: "command_failed", Phase: "execution", Message: "exit status 1"}}}
	var text bytes.Buffer
	CheckResult(&text, check)
	for _, want := range []string{"auth::reject_wrong_audience", "assertion: expected refusal", filepath.Join(dir, "stdout.log"), filepath.Join(dir, "stderr.log")} {
		if !strings.Contains(text.String(), want) {
			t.Fatal(want, text.String())
		}
	}
	if len(text.String()) > 4096 {
		t.Fatal("unbounded output", text.Len())
	}
}

func TestFailureFeedbackPrefersCaseDiagnosticToUnrelatedTail(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "stdout.log"), []byte("unrelated final runner event"), 0600); err != nil {
		t.Fatal(err)
	}
	check := review.CheckOutcome{Name: "auth", Directory: dir, ExecutionStarted: true, Results: verification.Results{Failed: 1, Executed: 1, Cases: []verification.Case{{ID: "auth::rejects", Status: "failed", Diagnostic: "expected refusal"}}}, Problems: []verification.Problem{{Code: "tests_failed", Phase: "execution", Message: "1 tests failed"}}}
	var out bytes.Buffer
	CheckResult(&out, check)
	if !strings.Contains(out.String(), "expected refusal") || strings.Contains(out.String(), "unrelated final runner event") || strings.Contains(out.String(), "did not start") {
		t.Fatal(out.String())
	}
}

// Differences inside a binding that no longer matches its source fold into
// one line beneath it; findings outside its span or file stay their own.
func TestCheckTextFoldsFindingsInsideAMismatchedBinding(t *testing.T) {
	covered := func(path string, line int) policy.Finding {
		return policy.Finding{Kind: "unexplained_change", Rule: "source/covered", Path: path, Line: line, Message: "source differs"}
	}
	report := &analysis.Report{Findings: []policy.Finding{
		{Kind: "stale_exception", Rule: "exception/valid", Path: "a.rs", Line: 10, EndLine: 40, Exception: "native", Message: "fence changed"},
		covered("a.rs", 10), covered("a.rs", 20), covered("a.rs", 40), covered("a.rs", 41), covered("b.rs", 12),
	}}
	var out bytes.Buffer
	CheckText(&out, report, 0)
	text := out.String()
	for _, want := range []string{"fence changed (native)", "+ 3 source/covered findings inside this binding, lines 10-40", "41  source/covered", "12  source/covered"} {
		if !strings.Contains(text, want) {
			t.Fatal(want, "\n", text)
		}
	}
	if strings.Count(text, "source differs") != 2 {
		t.Fatal("folded findings repeated\n", text)
	}
	// --top counts displayed lines; the remainder counts every hidden finding.
	out.Reset()
	CheckText(&out, report, 1)
	if !strings.Contains(out.String(), "… 2 more findings") {
		t.Fatal(out.String())
	}
}
