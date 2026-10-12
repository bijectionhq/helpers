package analysis_test

import (
	"reflect"
	"strings"
	"testing"

	"concave/internal/analysis"
	"concave/internal/policy"
	"concave/internal/repository"
	"concave/internal/testrepo"
)

func TestHistoryComparisonNeverApprovesAdaptations(t *testing.T) {
	for _, name := range []string{"exact", "adapted", "extra-code", "absent", "changed-context", "protected", "other-scope"} {
		t.Run(name, func(t *testing.T) {
			change := func(s string) string { return s }
			if name == "changed-context" {
				change = func(s string) string { return strings.Replace(s, "fn first()", "fn new_owner()", 1) }
			}
			f := historyFixture(t, change)
			source := change(historyPrefix + historyTest + historySuffix)
			want := "exact_bytes"
			switch name {
			case "adapted":
				source = strings.Replace(source, "historical_test", "adapted_test", 1)
				want = "adapted"
			case "extra-code":
				source = strings.Replace(source, historyTest, historyTest+"fn unrelated_production() {}\n", 1)
				want = "adapted"
			case "absent":
				source = change(historyPrefix + historySuffix)
				want = "no_single_hunk"
			case "changed-context":
				want = "upstream_context_changed"
			case "protected":
				f.Config.PreserveExactly = append(f.Config.PreserveExactly, "crates/core/lib.rs")
			case "other-scope":
				f.Config.Restorations[0].Paths = []string{"npm-packages/"}
			}
			f.Save()
			f.Write("crates/core/lib.rs", source)
			plain := scan(t, f, analysis.Options{Paths: []string{"crates/core/lib.rs"}})
			got := scan(t, f, analysis.Options{Paths: []string{"crates/core/lib.rs"}, History: true})
			if !reflect.DeepEqual(plain.Findings, got.Findings) || !reflect.DeepEqual(plain.Totals, got.Totals) || plain.Files[0].Classification != got.Files[0].Classification {
				t.Fatalf("diagnosis changed enforcement: plain=%+v history=%+v", plain.Findings, got.Findings)
			}
			if len(plain.History) != 0 {
				t.Fatal("history must be opt-in")
			}
			if name == "other-scope" {
				if len(got.History) != 0 {
					t.Fatal(got.History)
				}
				return
			}
			if len(got.History) != 1 {
				t.Fatal(got.History)
			}
			b := got.History[0]
			if b.Status != want || b.HistoricalText != historyTest || b.Source.Start != 3 || b.Source.End != 5 || b.SourceRevision != f.Git("rev-parse", b.RemovedBy+"^") {
				t.Fatalf("wrong provenance or comparison: %+v", b)
			}
			if want == "adapted" {
				if b.Selected == nil || b.Selected.Hunk == "" || len(b.Changes) != 1 || len(got.Findings) == 0 {
					t.Fatal(b, got.Findings)
				}
				if b.Changes[0].Before == b.Changes[0].After {
					t.Fatal("empty adaptation")
				}
			} else if len(b.Changes) != 0 {
				t.Fatal(b)
			}
			if want == "upstream_context_changed" && (b.Upstream != nil || b.Selected != nil) {
				t.Fatal("unsafe pairing", b)
			}
			if want == "no_single_hunk" && b.Selected != nil {
				t.Fatal("invented block", b)
			}
			if name == "protected" && !has(got, "exact_file_changed") {
				t.Fatal("diagnosis waived exact policy")
			}
		})
	}
}

func TestHistoryUsesSelectedSnapshotAndReconstructsAdaptation(t *testing.T) {
	f := historyFixture(t, func(s string) string { return s })
	adapted := strings.Replace(historyTest, "historical_test", "current_test", 1)
	f.Write("crates/core/lib.rs", historyPrefix+adapted+historySuffix)
	rev := f.Commit()
	f.Write("crates/core/lib.rs", historyPrefix+historyTest+historySuffix)
	r := scan(t, f, analysis.Options{Rev: rev, Paths: []string{"crates/core/lib.rs"}, History: true})
	b := r.History[0]
	if b.Status != "adapted" || b.Selected.Text != adapted {
		t.Fatal(b)
	}
	lines := repository.Lines(b.HistoricalText)
	var rebuilt strings.Builder
	pos := 0
	for _, h := range b.Changes {
		rebuilt.WriteString(strings.Join(lines[pos:h.Start], ""))
		rebuilt.WriteString(h.After)
		pos = h.End
	}
	rebuilt.WriteString(strings.Join(lines[pos:], ""))
	if rebuilt.String() != adapted {
		t.Fatal("comparison cannot reconstruct selected bytes")
	}
}

func TestHistoryIncludesJoinedEOFAndReportsMissingComparison(t *testing.T) {
	f := testrepo.New(t)
	retained := historyPrefix + historySuffix + "// tail"
	f.Write("crates/core/lib.rs", retained+"\n"+historyTest)
	f.Commit()
	f.Write("crates/core/lib.rs", retained)
	removal := f.Commit()
	f.Config.Upstream = removal
	f.Config.PreserveExactly = nil
	f.Config.Restorations = []policy.Restoration{{ID: "tail", RemovedBy: removal, Paths: []string{"crates/core/lib.rs"}, Explanation: policy.Explanation{Why: "Retain the deleted witness.", Witnesses: []string{"crates/core/lib.rs"}}}}
	f.Save()
	f.Commit()
	f.Write("crates/core/lib.rs", retained+"\n"+strings.Replace(historyTest, "historical_test", "updated_test", 1))
	r := scan(t, f, analysis.Options{Paths: []string{"crates/core/lib.rs"}, History: true})
	if len(r.History) != 1 || r.History[0].HistoricalText != "// tail\n"+historyTest || r.History[0].Status != "adapted" || len(r.Findings) == 0 {
		t.Fatal(r.History, r.Findings)
	}
	// Replacing surrounding upstream lines is not an exact same-span insertion.
	f.Write("crates/core/lib.rs", historyPrefix+"fn replacement() {}\n")
	r = scan(t, f, analysis.Options{Paths: []string{"crates/core/lib.rs"}, History: true})
	if r.History[0].Status != "no_single_hunk" || r.History[0].Selected != nil {
		t.Fatal(r.History)
	}
}

func TestExplicitHistoryPairDiagnosesChangedContextWithoutApproval(t *testing.T) {
	change := func(s string) string { return strings.Replace(s, "fn first()", "fn new_owner()", 1) }
	f := historyFixture(t, change)
	f.Write("crates/core/lib.rs", change(historyPrefix+strings.Replace(historyTest, "historical_test", "current_test", 1)+historySuffix))
	plain := scan(t, f, analysis.Options{Paths: []string{"crates/core/"}, History: true})
	pair := analysis.HistoryPair{Block: plain.History[0].ID, Hunk: plain.Hunks[0].ID}
	if len(pair.Block) != 20 || plain.History[0].Upstream != nil {
		t.Fatal(plain.History)
	}
	got := scan(t, f, analysis.Options{Paths: []string{"crates/core/"}, History: true, HistoryPairs: []analysis.HistoryPair{pair}})
	b := got.History[0]
	if b.Pairing != "explicit_selection" || b.Status != "adapted" || b.Selected.Hunk != pair.Hunk || b.Upstream != nil || len(b.Changes) != 1 {
		t.Fatal(b)
	}
	if !reflect.DeepEqual(got.Findings, plain.Findings) || !reflect.DeepEqual(got.Totals, plain.Totals) || got.Files[0].Classification != plain.Files[0].Classification || len(got.Findings) == 0 {
		t.Fatal("explicit pairing changed enforcement")
	}
	if len(got.Scope.HistoryPairs) != 1 || got.SnapshotSHA256 == plain.SnapshotSHA256 {
		t.Fatal("selection missing from identity")
	}
	comparison, err := analysis.Compare(plain, got)
	if err != nil || !comparison.Comparable || len(comparison.Files) != 0 || len(comparison.AddedFindings) != 0 || len(comparison.ResolvedFindings) != 0 || !reflect.DeepEqual(comparison.BasisChanges, []string{"history"}) {
		t.Fatal("diagnostic pairing changed source comparison", comparison, err)
	}
	repo, config := f.Open()
	defer repo.Close()
	for _, tc := range []struct {
		name    string
		history bool
		pairs   []analysis.HistoryPair
	}{
		{"requires-history", false, []analysis.HistoryPair{pair}},
		{"unknown-block", true, []analysis.HistoryPair{{Block: "unknown", Hunk: pair.Hunk}}},
		{"unknown-hunk", true, []analysis.HistoryPair{{Block: pair.Block, Hunk: "unknown"}}},
		{"duplicate", true, []analysis.HistoryPair{pair, pair}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := analysis.Analyze(repo, config, analysis.Options{Paths: []string{"crates/core/"}, History: tc.history, HistoryPairs: tc.pairs}); err == nil {
				t.Fatal("invalid pair accepted")
			}
		})
	}
	f.Write("crates/core/lib.rs", change(historyPrefix+historyTest+historySuffix))
	next := scan(t, f, analysis.Options{Paths: []string{"crates/core/"}, History: true})
	if next.History[0].ID != pair.Block {
		t.Fatal("historical identity depends on current source")
	}
	if _, err := analysis.Analyze(repo, config, analysis.Options{Paths: []string{"crates/core/"}, History: true, HistoryPairs: []analysis.HistoryPair{pair}}); err == nil {
		t.Fatal("outdated hunk accepted")
	}
	pair.Hunk = next.Hunks[0].ID
	exact := scan(t, f, analysis.Options{Paths: []string{"crates/core/"}, History: true, HistoryPairs: []analysis.HistoryPair{pair}})
	if exact.History[0].Status != "exact_bytes" || len(exact.Findings) == 0 {
		t.Fatal("explicit exact bytes waived changed context")
	}
}
