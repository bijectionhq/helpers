package analysis_test

import (
	"strings"
	"testing"

	"concave/internal/analysis"
	"concave/internal/policy"
	"concave/internal/testrepo"
)

const historyPrefix = "// top\n// stable one\n// stable two\n"
const historyTest = "#[cfg(test)]\nfn historical_test() {}\n"
const historySuffix = "fn first() {}\nfn second() {}\n// stable three\n// stable four\n// bottom\n"

func historyFixture(t *testing.T, upstreamChange func(string) string) *testrepo.Fixture {
	t.Helper()
	f := testrepo.New(t)
	f.Write("crates/core/lib.rs", historyPrefix+historyTest+historySuffix)
	f.Commit()
	f.Write("crates/core/lib.rs", historyPrefix+historySuffix)
	removal := f.Commit()
	f.Write("crates/core/lib.rs", upstreamChange(historyPrefix+historySuffix))
	// Ensure a descendant commit even when source is unchanged.
	f.Write("contract.md", "# Historical witnesses\n")
	f.Config.Upstream = f.Commit()
	f.Config.Restorations = []policy.Restoration{{ID: "historical-tests", RemovedBy: removal, Paths: []string{"crates/core/"}, Explanation: policy.Explanation{Why: "Retain the historical test witness.", Witnesses: []string{"crates/core/lib.rs::historical_test"}}}}
	f.Save()
	f.Commit()
	return f
}

func TestHistoricalRestorationRequiresExactBytesAndOwner(t *testing.T) {
	for _, name := range []string{"exact", "edited", "wrong-position", "extra-code", "other-file", "protected", "unrelated-drift", "invalid-reference", "duplicate-rule", "changed-context", "offset-change", "replaced-in-history"} {
		t.Run(name, func(t *testing.T) {
			change := func(s string) string { return s }
			if name == "changed-context" {
				change = func(s string) string { return strings.Replace(s, "fn first()", "fn new_owner()", 1) }
			}
			if name == "offset-change" {
				change = func(s string) string { return "// additional header\n" + s }
			}
			f := historyFixture(t, change)
			source := change(historyPrefix + historyTest + historySuffix)
			switch name {
			case "edited":
				source = strings.Replace(source, "historical_test", "different_test", 1)
			case "wrong-position":
				source = historyPrefix + "fn first() {}\n" + historyTest + strings.TrimPrefix(historySuffix, "fn first() {}\n")
			case "extra-code":
				source = historyPrefix + historyTest + "fn arbitrary_production() {}\n" + historySuffix
			case "other-file":
				f.Config.Restorations[0].Paths = []string{"npm-packages/"}
			case "protected":
				f.Config.PreserveExactly = append(f.Config.PreserveExactly, "crates/core/lib.rs")
			case "unrelated-drift":
				source = strings.Replace(source, "// bottom", "fn new_behavior() {}", 1)
			case "invalid-reference":
				f.Config.Restorations[0].Explanation.Witnesses = []string{"missing.rs::test"}
			case "duplicate-rule":
				x := f.Config.Restorations[0]
				x.ID = "overlapping-tests"
				f.Config.Restorations = append(f.Config.Restorations, x)
			case "replaced-in-history":
				// A removal plus replacement is not an approved pure deletion.
				f.Config.Restorations[0].RemovedBy = f.Config.Upstream
			}
			f.Save()
			f.Write("crates/core/lib.rs", source)
			r := scan(t, f, analysis.Options{Paths: []string{"crates/core/lib.rs"}})
			row := r.Files[0]
			want := "unexplained"
			if name == "exact" || name == "offset-change" {
				want = "excepted"
			}
			if row.Classification != want {
				t.Fatalf("%s: %+v %+v", name, row, r.Findings)
			}
			if want == "excepted" && (len(row.Approved) != 1 || row.Approved[0] != "historical-tests" || row.Explanations["historical-tests"].Why == "") {
				t.Fatal(row)
			}
			if want == "excepted" && (len(row.HistoricalMatches) != 1 || row.HistoricalMatches[0].RemovedBy != f.Config.Restorations[0].RemovedBy || row.HistoricalMatches[0].Source.Start != 3 || row.HistoricalMatches[0].Source.End != 5) {
				t.Fatal("missing historical evidence", row.HistoricalMatches)
			}
			if name == "unrelated-drift" && (len(row.Approved) != 1 || len(r.Findings) != 1) {
				t.Fatalf("lost partial approval: %+v %+v", row, r.Findings)
			}
		})
	}
}

func TestHistoricalDeletionAtFileEnd(t *testing.T) {
	f := testrepo.New(t)
	f.Write("crates/core/lib.rs", historyPrefix+historySuffix+historyTest)
	f.Commit()
	f.Write("crates/core/lib.rs", historyPrefix+historySuffix)
	removal := f.Commit()
	f.Config.Upstream = removal
	f.Config.Restorations = []policy.Restoration{{ID: "end-test", RemovedBy: removal, Paths: []string{"crates/core/lib.rs"}, Explanation: policy.Explanation{Why: "Restore the final historical witness.", Witnesses: []string{"crates/core/lib.rs"}}}}
	f.Save()
	f.Commit()
	f.Write("crates/core/lib.rs", historyPrefix+historySuffix+historyTest)
	r := scan(t, f, analysis.Options{Paths: []string{"crates/core/lib.rs"}})
	if r.Files[0].Classification != "excepted" {
		t.Fatal(r.Findings)
	}
	// Replacing an inherited line with the same historical bytes never counts
	// as restoring a pure insertion.
	f.Write("crates/core/lib.rs", historyPrefix+strings.TrimSuffix(historySuffix, "// bottom\n")+historyTest)
	r = scan(t, f, analysis.Options{Paths: []string{"crates/core/lib.rs"}})
	if r.Files[0].Classification != "unexplained" {
		t.Fatal(r.Findings)
	}
}

func TestHistoricalRuleMustBeUpstreamAndWellFormed(t *testing.T) {
	for _, name := range []string{"not-ancestor", "short-hash", "no-scope", "no-explanation", "root-commit"} {
		t.Run(name, func(t *testing.T) {
			f := historyFixture(t, func(s string) string { return s })
			switch name {
			case "not-ancestor":
				f.Config.Restorations[0].RemovedBy = f.Git("rev-parse", "HEAD")
			case "short-hash":
				f.Config.Restorations[0].RemovedBy = "HEAD"
			case "no-scope":
				f.Config.Restorations[0].Paths = nil
			case "no-explanation":
				f.Config.Restorations[0].Explanation.Why = ""
			case "root-commit":
				f.Config.Restorations[0].RemovedBy = f.Base
			}
			f.Save()
			if name == "short-hash" || name == "no-scope" || name == "no-explanation" {
				if f.Config.Validate() == nil {
					t.Fatal("invalid rule accepted")
				}
				return
			}
			r, c := f.Open()
			if _, err := analysis.Analyze(r, c, analysis.Options{Paths: []string{"crates/core/lib.rs"}}); err == nil {
				t.Fatal("invalid ancestry accepted")
			}
		})
	}
}

func TestCommittedHistoricalRuleIgnoresDirtyReferences(t *testing.T) {
	f := historyFixture(t, func(s string) string { return s })
	f.Write("crates/core/lib.rs", historyPrefix+historyTest+historySuffix)
	rev := f.Commit()
	f.Write("contract.md", "# Missing\n")
	r := scan(t, f, analysis.Options{Rev: rev, Paths: []string{"crates/core/lib.rs"}})
	if r.Files[0].Classification != "excepted" {
		t.Fatal(r.Findings)
	}
	r = scan(t, f, analysis.Options{Paths: []string{"crates/core/lib.rs"}})
	if len(r.Findings) != 0 {
		t.Fatal("documentation drift affected source parity", r.Findings)
	}
}
