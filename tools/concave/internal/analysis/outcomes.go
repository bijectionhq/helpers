package analysis

import (
	"sort"

	"concave/internal/policy"
)

type Outcome struct {
	Path                  string   `json:"path"`
	RestoredOriginalLines int      `json:"restored_original_lines"`
	RemovedForkLines      int      `json:"removed_fork_lines"`
	NewApprovals          []string `json:"new_approvals"`
	UnresolvedFindings    int      `json:"unresolved_findings"`
	AnnotationOnly        bool     `json:"annotation_only"`
}

// Count restoration only for complete formerly divergent spans with no remaining
// edit against the same upstream blob. New approval never implies restoration.
func outcomes(before, after *Report, changes []FileChange) []Outcome {
	result := []Outcome{}
	for _, change := range changes {
		a, b := change.Before, change.After
		if a == nil || b == nil {
			continue
		}
		out := Outcome{Path: change.Path, NewApprovals: []string{}}
		out.AnnotationOnly = a.AnnotationFreeSHA256 != "" && a.AnnotationFreeSHA256 == b.AnnotationFreeSHA256 && a.Selected != nil && b.Selected != nil && a.Selected.OID != b.Selected.OID
		old := map[string]bool{}
		for _, id := range a.Approved {
			old[id] = true
		}
		for _, id := range b.Approved {
			if !old[id] {
				out.NewApprovals = append(out.NewApprovals, id)
			}
		}
		sort.Strings(out.NewApprovals)
		if a.Upstream != nil && b.Upstream != nil && a.Upstream.OID == b.Upstream.OID && b.Selected != nil && (b.Selected.Kind == "file" || b.Selected.Kind == "blob") {
			for _, h := range before.Hunks {
				if h.Path != change.Path {
					continue
				}
				restored := true
				for _, next := range after.Hunks {
					if next.Path == h.Path && policy.ConflictingSpans(policy.Span{Start: h.Start, End: h.End}, policy.Span{Start: next.Start, End: next.End}) {
						restored = false
						break
					}
				}
				if restored {
					out.RestoredOriginalLines += h.End - h.Start
					out.RemovedForkLines += h.CurrentEnd - h.CurrentStart
				}
			}
		}
		for _, f := range after.Findings {
			if f.Path == change.Path {
				out.UnresolvedFindings++
			}
		}
		result = append(result, out)
	}
	return result
}
