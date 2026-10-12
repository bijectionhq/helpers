package analysis_test

import (
	"strings"
	"testing"

	"concave/internal/analysis"
	"concave/internal/policy"
	"concave/internal/testrepo"
)

func TestHistoricalRestorationAfterUnterminatedFinalLine(t *testing.T) {
	const comment = "// Historical test follows"
	for _, name := range []string{"exact", "edited-test", "edited-comment", "wrong-position", "changed-upstream-context", "replacement-in-history", "newline-only", "unrelated-drift", "protected", "duplicate-rule"} {
		t.Run(name, func(t *testing.T) {
			f := testrepo.New(t)
			original := historyPrefix + historySuffix + comment + "\n" + historyTest
			if name == "newline-only" {
				original = historyPrefix + historySuffix + comment + "\n"
			}
			f.Write("crates/core/lib.rs", original)
			f.Commit()
			retained := historyPrefix + historySuffix + comment
			if name == "replacement-in-history" {
				retained += " changed"
			}
			f.Write("crates/core/lib.rs", retained)
			removal := f.Commit()
			if name == "changed-upstream-context" {
				retained = strings.Replace(retained, "// bottom", "// edited bottom", 1)
				f.Write("crates/core/lib.rs", retained)
				f.Commit()
				original = strings.Replace(original, "// bottom", "// edited bottom", 1)
			}
			f.Config.Upstream = f.Git("rev-parse", "HEAD")
			f.Config.Restorations = []policy.Restoration{{ID: "tail-test", RemovedBy: removal, Paths: []string{"crates/core/lib.rs"}, Explanation: policy.Explanation{Why: "Retain the exact historical tail.", Witnesses: []string{"crates/core/lib.rs"}}}}
			switch name {
			case "edited-test":
				original = strings.Replace(original, "historical_test", "other_test", 1)
			case "edited-comment":
				original = strings.Replace(original, comment, comment+" changed", 1)
			case "wrong-position":
				original = historyPrefix + historyTest + historySuffix + comment
			case "unrelated-drift":
				original = strings.Replace(original, "// top", "fn new_behavior() {}", 1)
			case "protected":
				f.Config.PreserveExactly = append(f.Config.PreserveExactly, "crates/core/lib.rs")
			case "duplicate-rule":
				other := f.Config.Restorations[0]
				other.ID = "overlapping-tail"
				f.Config.Restorations = append(f.Config.Restorations, other)
			}
			f.Save()
			f.Commit()
			f.Write("crates/core/lib.rs", original)
			r := scan(t, f, analysis.Options{Paths: []string{"crates/core/lib.rs"}})
			want := "unexplained"
			if name == "exact" {
				want = "excepted"
			}
			if r.Files[0].Classification != want {
				t.Fatal(r.Files, r.Findings)
			}
			if name == "exact" && (len(r.Files[0].HistoricalMatches) != 1 || r.Files[0].HistoricalMatches[0].Source.Start != 8 || r.Files[0].HistoricalMatches[0].Source.End != 11) {
				t.Fatal("missing exact historical tail evidence", r.Files[0].HistoricalMatches)
			}
			if name == "unrelated-drift" && (len(r.Files[0].Approved) != 1 || len(r.Findings) != 1) {
				t.Fatal("unrelated change was absorbed or partial approval lost", r.Files, r.Findings)
			}
		})
	}
}
