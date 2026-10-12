package analysis_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"concave/internal/analysis"
	"concave/internal/testrepo"
)

func scan(t *testing.T, f *testrepo.Fixture, o analysis.Options) *analysis.Report {
	t.Helper()
	r, c := f.Open()
	v, e := analysis.Analyze(r, c, o)
	if e != nil {
		t.Fatal(e)
	}
	return v
}
func has(r *analysis.Report, kind string) bool {
	for _, f := range r.Findings {
		if f.Kind == kind {
			return true
		}
	}
	return false
}
func TestSourceAccounting(t *testing.T) {
	tests := []struct {
		name                     string
		change                   func(*testrepo.Fixture)
		removed, added, newFiles int
		kind                     string
	}{
		{"exact", func(f *testrepo.Fixture) {}, 0, 0, 0, ""},
		{"addition", func(f *testrepo.Fixture) { f.Write("crates/core/lib.rs", "fn first() {}\nfn second() {}\nreturn;\n") }, 0, 1, 0, "unexplained_change"},
		{"replacement", func(f *testrepo.Fixture) { f.Write("crates/core/lib.rs", "fn replacement() {}\nfn second() {}\n") }, 1, 1, 0, "unexplained_change"},
		{"deletion", func(f *testrepo.Fixture) { os.Remove(filepath.Join(f.Root, "crates/core/lib.rs")) }, 2, 0, 0, "missing_file"},
		{"rename", func(f *testrepo.Fixture) {
			os.Rename(filepath.Join(f.Root, "crates/core/lib.rs"), filepath.Join(f.Root, "crates/core/moved.rs"))
			f.Commit()
		}, 2, 0, 1, "missing_file"},
		{"new files give no credit", func(f *testrepo.Fixture) { f.Write("crates/core/new.rs", "extension\n"); f.Commit() }, 0, 0, 1, ""},
		{"tests remain source", func(f *testrepo.Fixture) { f.Write("crates/core/lib.rs", "#[cfg(test)]\nfn test() {}\n") }, 2, 2, 0, "unexplained_change"},
		{"non test cfg", func(f *testrepo.Fixture) { f.Write("crates/core/lib.rs", "#[cfg(not(test))]\nfn production() {}\n") }, 2, 2, 0, "unexplained_change"},
		{"protected addition", func(f *testrepo.Fixture) {
			f.Write("npm-packages/client/protocol.ts", "export type Request = string;\nextra\n")
		}, 0, 1, 0, "exact_file_changed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := testrepo.New(t)
			tt.change(f)
			r := scan(t, f, analysis.Options{})
			if r.Totals.Inherited != 4 || r.Totals.Removed != tt.removed || r.Totals.Added != tt.added || r.Totals.New != tt.newFiles {
				t.Fatalf("counts: %+v", r.Totals)
			}
			if tt.kind != "" && !has(r, tt.kind) {
				t.Fatalf("missing %s: %+v", tt.kind, r.Findings)
			}
			if tt.name == "rename" {
				for _, v := range r.Files {
					if v.Status == "missing" && len(v.IdenticalNewPaths) != 1 {
						t.Fatal(v)
					}
				}
			}
			if tt.name == "exact" && r.Families["packages"].Inherited != 1 {
				t.Fatal(r.Families)
			}
		})
	}
}
func TestCommittedSnapshotIgnoresDirtyFiles(t *testing.T) {
	f := testrepo.New(t)
	rev := f.Git("rev-parse", "HEAD")
	f.Write("crates/core/lib.rs", "dirty\n")
	f.Write("crates/core/untracked.rs", "untracked\n")
	r := scan(t, f, analysis.Options{Rev: rev})
	if r.Totals.Exact != 4 || r.Totals.New != 0 {
		t.Fatal(r.Totals)
	}
}
func TestDirtyAttributesCannotHideDiff(t *testing.T) {
	f := testrepo.New(t)
	f.Write("crates/core/lib.rs", "changed\n")
	rev := f.Commit()
	f.Write(".gitattributes", "*.rs -diff\n")
	r := scan(t, f, analysis.Options{Rev: rev})
	if r.Totals.Removed != 2 || r.Totals.Binary != 0 {
		t.Fatal(r.Totals)
	}
}
func TestUnusualPathsAndNoFinalNewline(t *testing.T) {
	f := testrepo.New(t)
	p := "crates/core/a b\t雪\n.rs"
	f.Write(p, "old")
	f.Config.PreserveExactly = nil
	f.Config.Upstream = f.Commit()
	f.Save()
	f.Commit()
	f.Write(p, "new")
	r := scan(t, f, analysis.Options{Paths: []string{p}})
	if r.Totals.Removed != 1 || r.Totals.Added != 1 {
		t.Fatal(r.Totals)
	}
}
func TestBinaryAndModes(t *testing.T) {
	for _, kind := range []string{"binary", "mode", "symlink", "oversize"} {
		t.Run(kind, func(t *testing.T) {
			f := testrepo.New(t)
			p := filepath.Join(f.Root, "crates/core/lib.rs")
			switch kind {
			case "binary":
				f.Write("crates/core/lib.rs", "\x00binary")
			case "mode":
				os.Chmod(p, 0700)
			case "symlink":
				os.Remove(p)
				os.Symlink("../../outside", p)
			case "oversize":
				f.Write("crates/core/lib.rs", strings.Repeat("a", 4<<20+1))
			}
			r := scan(t, f, analysis.Options{})
			if kind == "mode" {
				if !has(r, "mode_or_type_changed") {
					t.Fatal(r.Findings)
				}
			} else if !has(r, "unsupported_change") {
				t.Fatal(r.Findings)
			}
		})
	}
}
func TestInvalidCoverage(t *testing.T) {
	for _, kind := range []string{"unmapped", "protected missing", "empty", "unknown case", "unknown family"} {
		t.Run(kind, func(t *testing.T) {
			f := testrepo.New(t)
			o := analysis.Options{}
			switch kind {
			case "unmapped":
				f.Config.Families = f.Config.Families[1:]
			case "protected missing":
				f.Config.PreserveExactly = append(f.Config.PreserveExactly, "crates/missing.rs")
			case "empty":
				o.Paths = []string{"nowhere/"}
			case "unknown case":
				o.Case = "missing"
			case "unknown family":
				o.Family = "missing"
			}
			f.Save()
			r, c := f.Open()
			if _, e := analysis.Analyze(r, c, o); e == nil {
				t.Fatal("accepted invalid coverage")
			}
		})
	}
}
func TestDiagnosisSnapshotsProbesAndUntracked(t *testing.T) {
	f := testrepo.New(t)
	selected := f.Git("rev-parse", "HEAD")
	f.Write("crates/core/lib.rs", "committed\n")
	f.Commit()
	f.Write("crates/core/lib.rs", "working\n")
	f.Write("crates/core/untracked.rs", "do not read\n")
	r := scan(t, f, analysis.Options{Rev: selected, Case: "authentication"})
	if len(r.Untracked) != 1 || len(r.Probes) == 0 || len(r.ReviewPatches) != 3 {
		t.Fatalf("diagnosis: %+v", r)
	}
	for _, v := range r.Files {
		if v.Path == "crates/core/lib.rs" && (v.Selected.OID == v.Head.OID || v.Head.OID == v.Working.OID) {
			t.Fatal("snapshots collapsed")
		}
	}
}
func TestMissingCaseSelectorVisible(t *testing.T) {
	f := testrepo.New(t)
	c := f.Config.Cases["authentication"]
	c.Surfaces[0].Paths = append(c.Surfaces[0].Paths, "crates/missing.rs")
	f.Config.Cases["authentication"] = c
	f.Save()
	r := scan(t, f, analysis.Options{Case: "authentication"})
	if !has(r, "unmatched_selector") {
		t.Fatal(r.Findings)
	}
}
func TestHistoricalMetricsRemainSeparate(t *testing.T) {
	f := testrepo.New(t)
	f.Write("crates/core/lib.rs", "fn first() {}\nfn second() {}\n#[cfg(test)]\nmod tests {\n fn x() {}\n}\n")
	r := scan(t, f, analysis.Options{Metric: "non-test"})
	if r.Metric.Total != 0 || r.Totals.Added != 4 || !has(r, "unexplained_change") {
		t.Fatalf("metrics: %+v %+v", r.Metric, r.Totals)
	}
}
func TestCfgAttrDoesNotHideFollowingProduction(t *testing.T) {
	lines := analysis.RustTestLines("#[cfg_attr(any(test, feature = \"testing\"), derive(X))]\nstruct Production;\n#[test]\nfn witness() { let x = r#\"}\"#; }\nfn production() {}\n", false)
	if !lines[1] || lines[2] || !lines[3] || !lines[4] || lines[5] {
		t.Fatal(lines)
	}
	old := analysis.RustTestLines("#[test]\nfn witness() {}\n", true)
	if len(old) != 0 {
		t.Fatal(old)
	}
}
func TestBoundaryOutsideRootsIsVisible(t *testing.T) {
	f := testrepo.New(t)
	f.Write("Cargo.toml", "old\n")
	f.Config.Upstream = f.Commit()
	f.Config.Boundaries = []string{"Cargo.toml"}
	f.Save()
	f.Commit()
	f.Write("Cargo.toml", "patched\n")
	r := scan(t, f, analysis.Options{})
	found := false
	for _, row := range r.Files {
		if row.Path == "Cargo.toml" {
			found = true
		}
	}
	if !found {
		t.Fatal("root boundary omitted")
	}
}

func TestFullCoverageCannotExcludeInheritedFiles(t *testing.T) {
	f := testrepo.New(t)
	f.Write("scripts/inherited.sh", "original\n")
	f.Write("assets/icon.bin", "\x00original")
	f.Config.Upstream = f.Commit()
	f.Save()
	f.Commit()
	f.Write("scripts/inherited.sh", "changed\n")
	f.Write("assets/icon.bin", "\x00changed")
	r := scan(t, f, analysis.Options{})
	if r.Coverage.Scope != "repository" || r.Coverage.Inspected != 8 || r.Coverage.Uninspected != 0 || r.Coverage.OutsideConfigured != 5 {
		t.Fatal(r.Coverage)
	}
	if !has(r, "unsupported_change") || !has(r, "unexplained_change") {
		t.Fatal(r.Findings)
	}
	for _, row := range r.Files {
		if row.Path == "scripts/inherited.sh" && row.Classification != "unexplained" {
			t.Fatal(row)
		}
	}
	selected := scan(t, f, analysis.Options{Family: "engine"})
	if selected.Coverage.Scope != "selected" || selected.Coverage.Inspected != 1 || selected.Coverage.Uninspected != 7 {
		t.Fatal(selected.Coverage)
	}
}
func TestCompensatingEditsStillFail(t *testing.T) {
	f := testrepo.New(t)
	f.Write("crates/core/lib.rs", "earlier drift\n")
	f.Commit()
	f.Write("crates/core/lib.rs", "fn first() {}\nfn second() {}\n")
	f.Write("npm-packages/convex/index.ts", "return;\n")
	r := scan(t, f, analysis.Options{})
	if !has(r, "unexplained_change") {
		t.Fatal("restoration paid for regression")
	}
}

func TestNonTestMetricIsInformational(t *testing.T) {
	f := testrepo.New(t)
	f.Write("crates/core/lib.rs", "changed\n")
	r := scan(t, f, analysis.Options{Metric: "non-test"})
	if r.Metric == nil || r.Metric.Total != 3 || len(r.Metric.Files) == 0 || r.Metric.Files[0].Path != "crates/core/lib.rs" || r.Metric.Files[0].Divergence != 3 {
		t.Fatal(r.Metric)
	}
	for _, finding := range r.Findings {
		if finding.Path == "concave.json" {
			t.Fatal("a measurement produced a finding", finding)
		}
	}
}
