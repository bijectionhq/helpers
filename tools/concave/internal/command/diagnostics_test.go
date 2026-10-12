package command_test

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"concave/internal/analysis"
	"concave/internal/policy"
	"concave/internal/testrepo"
)

type diagnosticResult struct {
	Diagnostics []struct {
		Message  string `json:"message"`
		Severity string `json:"severity"`
		Location struct {
			Path  string `json:"path"`
			Range *struct {
				Start struct {
					Line int `json:"line"`
				} `json:"start"`
				End struct {
					Line int `json:"line"`
				} `json:"end"`
			} `json:"range"`
		} `json:"location"`
		Code struct {
			Value string `json:"value"`
		} `json:"code"`
	} `json:"diagnostics"`
	Source struct {
		Name string `json:"name"`
	} `json:"source"`
}

func TestRDJSONPreservesEveryFindingAndExitStatus(t *testing.T) {
	f := testrepo.New(t)
	f.Write("crates/core/lib.rs", "fn changed() {}\nfn second() {}\n")
	f.Config.Families[0].Title += " (edited)"
	f.Save() // Includes an unlocated policy finding.
	n, original, err := run(f, "check", "--json")
	if n != 1 {
		t.Fatal(n, original, err)
	}
	var report analysis.Report
	if err := json.Unmarshal([]byte(original), &report); err != nil {
		t.Fatal(err)
	}
	n, out, err := run(f, "check", "--rdjson", "--top", "1")
	if n != 1 {
		t.Fatal(n, out, err)
	}
	var result diagnosticResult
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Diagnostics) != len(report.Findings) || len(result.Diagnostics) < 2 || result.Source.Name != "concave" {
		t.Fatal(out)
	}
	for i, finding := range report.Findings {
		d := result.Diagnostics[i]
		if d.Code.Value != finding.Rule || d.Location.Path != finding.Path || d.Severity != "ERROR" || !strings.Contains(d.Message, finding.Message) {
			t.Fatal(d, finding)
		}
		if finding.Line == 0 {
			if d.Location.Range != nil {
				t.Fatal("invented source location", d)
			}
		} else if d.Location.Range == nil || d.Location.Range.Start.Line != finding.Line || d.Location.Range.End.Line < finding.Line {
			t.Fatal("lost source location", d)
		}
	}
	for _, args := range [][]string{{"check", "--json", "--rdjson"}, {"check", "--top", "-1"}} {
		n, out, err := run(f, args...)
		if n != 2 || out != "" {
			t.Fatal(args, n, out, err)
		}
	}
	n, out, err = run(f, "check", "no-file.rs", "--rdjson")
	if n != 2 {
		t.Fatal("missing input passed", n, out, err)
	}
}

func TestRDJSONCleanSourceAndLocatedAnnotation(t *testing.T) {
	f := contractFixture(t)
	n, out, err := run(f, "check", "crates/core/lib.rs", "--rdjson")
	if n != 0 || !strings.Contains(out, `"diagnostics": []`) {
		t.Fatal(n, out, err)
	}
	f.Write("crates/core/native.rs", "fn first() {}\n// concave:invariant empty\nfn owner() {}\n")
	n, out, err = run(f, "check", "crates/core/native.rs", "--rdjson")
	if n != 1 {
		t.Fatal(n, out, err)
	}
	var d diagnosticResult
	if err := json.Unmarshal([]byte(out), &d); err != nil {
		t.Fatal(err)
	}
	if len(d.Diagnostics) != 1 || d.Diagnostics[0].Location.Range == nil || d.Diagnostics[0].Location.Range.Start.Line != 2 {
		t.Fatal(out)
	}
}

func TestRuleExplanationIsActionableAndDoesNotRequireEngine(t *testing.T) {
	f := structuralFixture(t)
	t.Setenv("CONCAVE_AST_GREP", filepath.Join(f.Root, "missing-engine"))
	for _, r := range policy.Rules {
		if r.Help == "" {
			t.Fatal("enforcement rule has no guidance", r.ID)
		}
		n, out, err := run(f, "rule", "explain", r.ID, "--repo", filepath.Join(f.Root, "missing-repository"), "--json")
		if n != 0 || !strings.Contains(out, r.Help) {
			t.Fatal(r.ID, n, out, err)
		}
	}
	n, out, err := run(f, "rule", "explain", "public-import")
	if n != 0 || !strings.Contains(out, "have not been verified") || !strings.Contains(out, "2 fixtures") {
		t.Fatal(n, out, err)
	}
	f.Config.Transforms[0].Explanation.Why = "Different working explanation."
	f.Save()
	n, out, err = run(f, "rule", "explain", "public-import", "--rev", "HEAD", "--json")
	if n != 0 || strings.Contains(out, "Different working") || !strings.Contains(out, "Adapt the public import only.") {
		t.Fatal(n, out, err)
	}
	for _, args := range [][]string{{"rule", "explain", "missing"}, {"rule", "explain", "source/exact", "--rev", "HEAD"}} {
		if n, out, err := run(f, args...); n != 2 {
			t.Fatal(n, out, err)
		}
	}
}
