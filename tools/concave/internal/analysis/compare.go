package analysis

import (
	"fmt"
	"reflect"
	"sort"

	"concave/internal/policy"
	"concave/internal/repository"
)

type FileChange struct {
	Path   string `json:"path"`
	Before *Row   `json:"before"`
	After  *Row   `json:"after"`
}
type CoverageLoss struct {
	Path   string `json:"path"`
	Reason string `json:"reason"`
}

// Comparison describes two saved inspections. It decides nothing: publication
// parity is CheckParity's verdict over revisions.
type Comparison struct {
	Format            int              `json:"format"`
	Kind              string           `json:"kind"`
	Comparable        bool             `json:"comparable"`
	BasisChanges      []string         `json:"basis_changes"`
	Before            Counts           `json:"before"`
	After             Counts           `json:"after"`
	ChangedLinesDelta int              `json:"changed_lines_delta"`
	Files             []FileChange     `json:"files"`
	AddedFindings     []policy.Finding `json:"added_findings"`
	ResolvedFindings  []policy.Finding `json:"resolved_findings"`
	BehavioralParity  string           `json:"behavioral_parity"`
	Outcomes          []Outcome        `json:"outcomes"`
}

// Compare never interprets a finding disappearing outside the common file scope
// as a resolution. Findings compare by identity, rule and obligation, never by
// wording or lines; duplicate occurrences are compared as a multiset.
func Compare(before, after *Report) (*Comparison, error) {
	for _, r := range []*Report{before, after} {
		if r.Format != 1 || r.Kind != "concave-inspection" {
			return nil, fmt.Errorf("compare requires two Concave inspections")
		}
	}
	c := &Comparison{Format: 1, Kind: "concave-comparison", Comparable: true, BasisChanges: []string{}, Before: before.Totals, After: after.Totals, Files: []FileChange{}, AddedFindings: []policy.Finding{}, ResolvedFindings: []policy.Finding{}, BehavioralParity: "not_established"}
	for _, pair := range []struct {
		name         string
		a, b         any
		incompatible bool
	}{
		{"upstream", before.Upstream, after.Upstream, true}, {"scope", comparisonScope(before.Scope), comparisonScope(after.Scope), true},
		{"history", []any{before.Scope.History, before.Scope.HistoryPairs}, []any{after.Scope.History, after.Scope.HistoryPairs}, false},
		{"coverage_scope", before.Coverage.Scope, after.Coverage.Scope, true},
		{"policy", before.PolicySHA256, after.PolicySHA256, false}, {"policy_source", before.PolicySource, after.PolicySource, false},
		{"document_engine", before.DocumentEngine, after.DocumentEngine, true},
		{"document_correspondence", documentCorrespondenceBasis(before), documentCorrespondenceBasis(after), true},
		{"syntax_engine", before.SyntaxEngine, after.SyntaxEngine, true}, {"head", before.Head, after.Head, false},
	} {
		if !reflect.DeepEqual(pair.a, pair.b) {
			c.BasisChanges = append(c.BasisChanges, pair.name)
			if pair.incompatible {
				c.Comparable = false
			}
		}
	}
	c.ChangedLinesDelta = after.Totals.Added + after.Totals.Removed - before.Totals.Added - before.Totals.Removed
	old, next := map[string]*Row{}, map[string]*Row{}
	paths := map[string]bool{}
	for i := range before.Files {
		r := &before.Files[i]
		if old[r.Path] != nil {
			return nil, fmt.Errorf("duplicate before path %s", r.Path)
		}
		old[r.Path] = r
		paths[r.Path] = true
	}
	for i := range after.Files {
		r := &after.Files[i]
		if next[r.Path] != nil {
			return nil, fmt.Errorf("duplicate after path %s", r.Path)
		}
		next[r.Path] = r
		paths[r.Path] = true
	}
	keys := []string{}
	for p := range paths {
		keys = append(keys, p)
	}
	sort.Strings(keys)
	for _, p := range keys {
		a, b := old[p], next[p]
		// HEAD and working observations are provenance, not selected-source changes.
		view := func(r *Row) any {
			if r == nil {
				return nil
			}
			return []any{comparisonSource(r.Selected), comparisonSource(r.Upstream), r.Status, r.Classification, r.Added, r.Removed, r.Approved, r.Explanations}
		}
		if !reflect.DeepEqual(view(a), view(b)) {
			c.Files = append(c.Files, FileChange{p, a, b})
		}
	}
	difference := func(a, b []policy.Finding) []policy.Finding {
		out := []policy.Finding{}
		for _, f := range difference(a, b, policy.Finding.Identity) {
			if f.Path == "" || old[f.Path] != nil && next[f.Path] != nil {
				out = append(out, f)
			}
		}
		return out
	}
	if c.Comparable {
		c.AddedFindings = difference(after.Findings, before.Findings)
		c.ResolvedFindings = difference(before.Findings, after.Findings)
		c.Outcomes = outcomes(before, after, c.Files)
	}
	return c, nil
}

// Historical diagnostics do not change the inspected source or enforcement scope.
func comparisonScope(scope OptionsJSON) OptionsJSON {
	scope.History, scope.HistoryPairs = false, nil
	return scope
}

// Regular-file identity is the same whether inspection read the bytes or used
// its unchanged-blob shortcut. Preserve full evidence for unsupported states.
// This projection belongs to report comparison; repository.Same stays strict.
func comparisonSource(state *repository.State) any {
	if state != nil && state.OID != "" && (state.Kind == "file" || state.Kind == "blob") && (state.Mode == "100644" || state.Mode == "100755") {
		return repository.Entry{Kind: "blob", Mode: state.Mode, OID: state.OID}
	}
	return state
}

func documentCorrespondenceBasis(report *Report) map[string]string {
	result := map[string]string{}
	for _, row := range report.Files {
		if row.UpstreamPath != "" {
			result[row.Path] = row.UpstreamPath
		}
	}
	return result
}
