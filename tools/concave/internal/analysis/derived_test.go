package analysis_test

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"concave/internal/analysis"
	"concave/internal/policy"
	"concave/internal/repository"
	"concave/internal/testrepo"
)

// rebase makes the current tree, including newly written inherited files, the
// pinned upstream for the rest of the fixture.
func rebase(f *testrepo.Fixture) {
	f.Config.Upstream = f.Commit()
	f.Save()
	f.Commit()
}

func row(t *testing.T, r *analysis.Report, path string) analysis.Row {
	t.Helper()
	for _, v := range r.Files {
		if v.Path == path {
			return v
		}
	}
	t.Fatalf("no row for %s", path)
	return analysis.Row{}
}

func findingsFor(r *analysis.Report, path string) []policy.Finding {
	out := []policy.Finding{}
	for _, f := range r.Findings {
		if f.Path == path {
			out = append(out, f)
		}
	}
	return out
}

func TestOversizedFileEqualToUpstreamIsExact(t *testing.T) {
	const video = "npm-packages/docs/tutorial.mp4"
	content := strings.Repeat("\x00frame", repository.MaxBytes/6+16)
	f := testrepo.New(t)
	f.Write(video, content)
	rebase(f)
	for _, tt := range []struct {
		name    string
		options analysis.Options
	}{
		{"working", analysis.Options{}},
		{"working-selected", analysis.Options{Paths: []string{video}}},
		{"working-history", analysis.Options{History: true}},
		{"revision", analysis.Options{Rev: "HEAD"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			r := scan(t, f, tt.options)
			if v := row(t, r, video); v.Classification != "preserved" || v.Status != "identical" {
				t.Fatalf("%+v", v)
			}
			if fs := findingsFor(r, video); len(fs) != 0 {
				t.Fatal(fs)
			}
		})
	}
	t.Run("changed", func(t *testing.T) {
		f.Write(video, content+"x")
		r := scan(t, f, analysis.Options{})
		if !has(r, "unsupported_change") || row(t, r, video).Classification != "unexplained" {
			t.Fatal(r.Findings)
		}
	})
}

func derivedFixture(t *testing.T) *testrepo.Fixture {
	t.Helper()
	f := testrepo.New(t)
	f.Write("crates/core/generated.json", "{\"title\": \"upstream\"}\n")
	rebase(f)
	f.Config.Checks = map[string]policy.CheckDefinition{"fresh": {Command: []string{"true"}, Directory: ".", Inputs: []string{"crates/core/generated.json", "crates/core/lib.rs"}, Toolchain: []policy.ToolCheck{{Command: []string{"true"}}}, TimeoutSeconds: 1, Results: "command"}}
	f.Config.Derived = []policy.Derived{{ID: "generated-contract", Paths: []string{"crates/core/generated.json"}, Sources: []string{"crates/core/lib.rs"}, Generator: "generate-contract", Check: "fresh", Explanation: policy.Explanation{Why: "Serialized from the declarations in lib.rs.", Witnesses: []string{"crates/core/lib.rs"}}}}
	f.Save()
	f.Write("crates/core/generated.json", "{\"title\": \"fork\"}\n")
	f.Commit()
	return f
}

func TestDerivedOutputIsClassifiedByItsDeclaration(t *testing.T) {
	f := derivedFixture(t)
	for _, rev := range []string{"", "HEAD"} {
		r := scan(t, f, analysis.Options{Rev: rev})
		v := row(t, r, "crates/core/generated.json")
		if v.Classification != "derived" || !slices.Equal(v.Approved, []string{"generated-contract"}) || v.Derived == nil || v.Derived.Check != "fresh" {
			t.Fatalf("%+v", v)
		}
		if _, ok := v.Explanations["generated-contract"]; !ok || v.Added != 1 || v.Removed != 1 {
			t.Fatalf("%+v", v)
		}
		if fs := findingsFor(r, "crates/core/generated.json"); len(fs) != 0 {
			t.Fatal(fs)
		}
		if r.BehavioralParity != "not_established" || !strings.Contains(strings.Join(r.Limits, "\n"), "freshness") {
			t.Fatal(r.Limits)
		}
	}
	r, c := f.Open()
	report, err := analysis.Check(r, c, analysis.CheckOptions{Rev: "HEAD", Paths: []string{"crates/core/generated.json"}})
	if err != nil || report.Status != "passed" {
		t.Fatal(err, report.Findings)
	}
	for _, tt := range []struct {
		paths []string
		want  bool
	}{
		{[]string{"crates/core/generated.json"}, true},
		{[]string{"crates/core/lib.rs"}, true},
		{[]string{"npm-packages/convex/index.ts"}, false},
	} {
		got := false
		for _, check := range c.RequiredChecks(tt.paths, nil).Checks {
			got = got || check.Name == "fresh"
		}
		if got != tt.want {
			t.Fatal(tt.paths, got)
		}
	}
}

func TestDerivedOutputKeepsOtherFindings(t *testing.T) {
	t.Run("missing", func(t *testing.T) {
		f := derivedFixture(t)
		os.Remove(filepath.Join(f.Root, "crates/core/generated.json"))
		r := scan(t, f, analysis.Options{})
		if !has(r, "missing_file") || row(t, r, "crates/core/generated.json").Classification != "unexplained" {
			t.Fatal(r.Findings)
		}
	})
	t.Run("mode", func(t *testing.T) {
		f := derivedFixture(t)
		os.Chmod(filepath.Join(f.Root, "crates/core/generated.json"), 0700)
		r := scan(t, f, analysis.Options{})
		if !has(r, "mode_or_type_changed") || row(t, r, "crates/core/generated.json").Classification != "unexplained" {
			t.Fatal(r.Findings)
		}
	})
	t.Run("witness", func(t *testing.T) {
		f := derivedFixture(t)
		f.Config.Derived[0].Explanation.Witnesses = []string{"crates/core/absent.rs"}
		f.Save()
		r := scan(t, f, analysis.Options{})
		fs := findingsFor(r, "crates/core/generated.json")
		if len(fs) != 1 || fs[0].Kind != "invalid_reference" || fs[0].Rule != "derived/declared" || row(t, r, "crates/core/generated.json").Classification != "unexplained" {
			t.Fatal(fs)
		}
	})
}

func TestDerivedPathsAndSourcesMustExist(t *testing.T) {
	for name, change := range map[string]func(*policy.Derived){
		"absent-output": func(d *policy.Derived) { d.Paths = []string{"crates/core/generated.json", "crates/core/absent.json"} },
		"absent-source": func(d *policy.Derived) { d.Sources = []string{"crates/absent/"} },
	} {
		t.Run(name, func(t *testing.T) {
			f := derivedFixture(t)
			change(&f.Config.Derived[0])
			f.Config.Checks["fresh"] = policy.CheckDefinition{Command: []string{"true"}, Directory: ".", Inputs: []string{"crates/"}, Toolchain: []policy.ToolCheck{{Command: []string{"true"}}}, TimeoutSeconds: 1, Results: "command"}
			f.Save()
			r, c := f.Open()
			if _, err := analysis.Analyze(r, c, analysis.Options{}); err == nil || !strings.Contains(err.Error(), "derived generated-contract") {
				t.Fatal(err)
			}
		})
	}
}
