package analysis

import (
	"concave/internal/policy"
	"concave/internal/repository"
	"testing"
)

func regroupingReport(hunks ...repository.Hunk) *Report {
	r := &Report{Upstream: "pinned", Hunks: hunks}
	for _, h := range hunks {
		r.Findings = append(r.Findings, policy.Finding{Path: h.Path, Line: h.CurrentStart + 1, Rule: "source/covered", Subject: policy.Subject(h.Before, h.After), Upstream: &policy.Span{Start: h.Start, End: h.End}})
	}
	return r
}

func TestCoveredRegroupingsPreserveExactSource(t *testing.T) {
	joined := regroupingReport(repository.Hunk{Path: "page.mdx", Start: 10, End: 13, CurrentStart: 40, Before: "link\n\nsnippet\n", After: "new link\n\nexpanded\nexample\n"})
	split := func() *Report {
		return regroupingReport(
			repository.Hunk{Path: "page.mdx", Start: 10, End: 11, CurrentStart: 90, Before: "link\n", After: "new link\n"},
			repository.Hunk{Path: "page.mdx", Start: 12, End: 13, CurrentStart: 92, Before: "snippet\n", After: "expanded\nexample\n"},
		)
	}
	for _, reverse := range []bool{false, true} {
		a, b := joined, split()
		if reverse {
			a, b = b, a
		}
		old, current := coveredRegroupings(a, b, nil)
		if len(old) != len(a.Findings) || len(current) != len(b.Findings) {
			t.Fatal("regrouped source lost", old, current)
		}
	}
	for _, change := range []struct {
		name  string
		apply func(*Report)
	}{
		{"replacement", func(r *Report) { r.Hunks[1].After = "different\nexample\n" }},
		{"upstream bytes", func(r *Report) { r.Hunks[1].Before = "different\n" }},
		{"upstream pin", func(r *Report) { r.Upstream = "other" }},
		{"path", func(r *Report) { r.Hunks[1].Path = "other.mdx"; r.Findings[1].Path = "other.mdx" }},
		{"rule", func(r *Report) { r.Findings[1].Rule = "source/exact" }},
		{"binding", func(r *Report) { r.Hunks[1].Approval = "approved" }},
		{"missing bytes", func(r *Report) { r.Hunks[1].Before = "" }},
	} {
		t.Run(change.name, func(t *testing.T) {
			r := split()
			change.apply(r)
			old, current := coveredRegroupings(joined, r, nil)
			if len(old) != 0 || len(current) != 0 {
				t.Fatal("changed obligation carried", old, current)
			}
		})
	}
}

func TestCoveredRegroupingsDoNotMoveInsertions(t *testing.T) {
	a := regroupingReport(repository.Hunk{Path: "a", Start: 1, End: 1, Before: "", After: "inserted\n"})
	b := regroupingReport(repository.Hunk{Path: "a", Start: 2, End: 2, Before: "", After: "inserted\n"})
	old, current := coveredRegroupings(a, b, nil)
	if len(old) != 0 || len(current) != 0 {
		t.Fatal("insertion moved", old, current)
	}
}

func TestCoveredRegroupingsAcrossUnchangedGap(t *testing.T) {
	before := regroupingReport(
		repository.Hunk{Path: "page", Start: 0, End: 1, Before: "command\n", After: "renamed\n"},
		repository.Hunk{Path: "page", Start: 3, End: 4, Before: "description\n", After: "new description\n```\n\nmore\n"},
	)
	// The first hunk was approved, rather than debt.
	before.Hunks[0].Approval = "product-names"
	before.Findings = before.Findings[1:]
	before.Files = []Row{{Path: "page", Upstream: &repository.State{OID: "source"}}}
	after := regroupingReport(
		repository.Hunk{Path: "page", Start: 0, End: 1, Before: "command\n", After: "renamed\n```\n\nnew description\n"},
		repository.Hunk{Path: "page", Start: 3, End: 4, Before: "description\n", After: "more\n"},
	)
	read := func(string) ([]byte, error) { return []byte("command\n```\n\ndescription\n"), nil }
	old, current := coveredRegroupings(before, after, read)
	if len(old) != 1 || len(current) != 2 {
		t.Fatal("realigned debt lost", old, current)
	}
	after.Hunks[1].After = "changed\n"
	old, current = coveredRegroupings(before, after, read)
	if len(old) != 0 || len(current) != 0 {
		t.Fatal("changed bytes carried", old, current)
	}
}
