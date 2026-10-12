package analysis

import (
	"concave/internal/policy"
	"testing"
)

func TestCompareScopesApprovalsAndLineMovement(t *testing.T) {
	a := Report{Format: 1, Kind: "concave-inspection", Upstream: "same", Files: []Row{{Path: "a", Approved: []string{"x"}}, {Path: "outside"}}, Findings: []policy.Finding{{Path: "a", Kind: "drift", Line: 1}, {Path: "a", Kind: "drift", Line: 2}, {Path: "outside", Kind: "drift"}}, Totals: Counts{Added: 20}}
	b := a
	b.Files = []Row{{Path: "a", Approved: []string{"y"}}}
	b.Findings = []policy.Finding{{Path: "a", Kind: "drift", Line: 40}}
	b.Totals.Added = 2
	b.PolicySHA256 = "new"
	c, err := Compare(&a, &b)
	if err != nil || !c.Comparable || c.ChangedLinesDelta != -18 || len(c.ResolvedFindings) != 1 || len(c.AddedFindings) != 0 || len(c.Files) != 2 {
		t.Fatal(c, err)
	}
	b.Upstream = "different"
	c, err = Compare(&a, &b)
	if err != nil || c.Comparable || len(c.ResolvedFindings) != 0 {
		t.Fatal(c, err)
	}
	b = a
	b.Scope.Paths = []string{"a"}
	c, err = Compare(&a, &b)
	if err != nil || c.Comparable {
		t.Fatal(c, err)
	}
}
