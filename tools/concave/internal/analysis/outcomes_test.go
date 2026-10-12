package analysis_test

import (
	"concave/internal/analysis"
	"concave/internal/policy"
	"concave/internal/testrepo"
	"testing"
)

func TestOutcomeDistinguishesRestorationFromAnnotationApproval(t *testing.T) {
	f := testrepo.New(t)
	f.Write("crates/core/lib.rs", "fn first() { local(); }\nfn second() {}\n")
	r, c := f.Open()
	before, err := analysis.Analyze(r, c, analysis.Options{Paths: []string{"crates/core/lib.rs"}})
	if err != nil {
		t.Fatal(err)
	}
	f.Write("crates/core/lib.rs", "fn first() {}\nfn second() {}\n")
	after, err := analysis.Analyze(r, c, analysis.Options{Paths: []string{"crates/core/lib.rs"}})
	if err != nil {
		t.Fatal(err)
	}
	comparison, err := analysis.Compare(before, after)
	if err != nil || len(comparison.Outcomes) != 1 || comparison.Outcomes[0].RestoredOriginalLines != 1 {
		t.Fatal(comparison, err)
	}
	f.Write("crates/core/lib.rs", "// concave:begin local\n// why: preserve safety\n// witness: crates/core/lib.rs\n//\nfn first() { local(); }\n// concave:end local\nfn second() {}\n")
	after, err = analysis.Analyze(r, c, analysis.Options{Paths: []string{"crates/core/lib.rs"}})
	if err != nil {
		t.Fatal(err)
	}
	comparison, err = analysis.Compare(before, after)
	if err != nil || !comparison.Outcomes[0].AnnotationOnly || comparison.Outcomes[0].RestoredOriginalLines != 0 {
		t.Fatal(comparison, err)
	}
	// Literal comment examples stay executable bytes in this comparison.
	a, ok := policy.AnnotationFree("sample.rs", "let x = \"// concave:begin literal\";\n")
	if !ok || a != "let x = \"// concave:begin literal\";\n" {
		t.Fatal(a, ok)
	}
}
