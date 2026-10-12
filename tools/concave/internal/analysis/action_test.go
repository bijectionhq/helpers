package analysis

import (
	"reflect"
	"testing"

	"concave/internal/policy"
)

func TestCheckActionPreservesLocationPolicyAndRevision(t *testing.T) {
	for _, test := range []struct {
		finding policy.Finding
		want    []string
	}{
		{policy.Finding{Path: "src/auth.rs", Line: 84}, []string{"concave", "--repo", "/repo", "inspect", "src/auth.rs:84", "--contracts", "--rev", "revision"}},
		{policy.Finding{Path: policy.File, Rule: "policy/reviewed"}, []string{"concave", "--repo", "/repo", "rule", "explain", "policy/reviewed", "--rev", "revision"}},
	} {
		report := &Report{Findings: []policy.Finding{test.finding}, Revision: "revision"}
		if got := report.CheckAction("/repo", nil); !reflect.DeepEqual(got.Argv, test.want) {
			t.Fatal(got)
		}
	}
	if next := (&Report{}).CheckAction("/repo", nil); len(next.Argv) != 0 || next.Kind != "none" {
		t.Fatal(next)
	}
}

// A binding that no longer matches its working source leads to its refresh
// preview, ahead of the findings inside its span. An unfenced span binding, or
// a pinned revision, keeps inspection of the first finding.
func TestCheckActionRefreshesMismatchedBinding(t *testing.T) {
	config := &policy.Config{Exceptions: []policy.Exception{
		{ID: "fenced", Path: "src/a.rs", Fence: true},
		{ID: "asset", Path: "assets/logo.png", File: &policy.FileBinding{}},
		{ID: "span", Path: "config.json"},
	}}
	inspect := []string{"concave", "--repo", "/repo", "inspect", "src/a.rs:2", "--contracts"}
	for _, test := range []struct {
		finding  policy.Finding
		revision string
		want     []string
	}{
		{policy.Finding{Kind: "stale_exception", Path: "src/a.rs", Line: 9, Exception: "fenced"}, "", []string{"concave", "--repo", "/repo", "exception", "refresh", "fenced"}},
		{policy.Finding{Kind: "obsolete_exception", Path: "assets/logo.png", Exception: "asset"}, "", []string{"concave", "--repo", "/repo", "exception", "refresh", "asset", "--file"}},
		{policy.Finding{Kind: "stale_exception", Path: "config.json", Line: 3, Exception: "span"}, "", inspect},
		{policy.Finding{Kind: "stale_exception", Path: "src/a.rs", Line: 9, Exception: "fenced"}, "revision", append(inspect, "--rev", "revision")},
	} {
		report := &Report{Findings: []policy.Finding{{Kind: "unexplained_change", Path: "src/a.rs", Line: 2}, test.finding}, Revision: test.revision}
		if got := report.CheckAction("/repo", config); !reflect.DeepEqual(got.Argv, test.want) {
			t.Fatal(test.finding, got)
		}
	}
}
