package analysis

import (
	"encoding/json"
	"sort"

	"concave/internal/policy"
	"concave/internal/repository"
)

type FindingOccurrence struct {
	Path      string       `json:"path"`
	Family    string       `json:"family"`
	Line      int          `json:"line,omitempty"`
	EndLine   int          `json:"end_line,omitempty"`
	Upstream  *policy.Span `json:"upstream,omitempty"`
	Hunk      string       `json:"hunk,omitempty"`
	Exception string       `json:"exception,omitempty"`
}

// FindingGroup contains byte-identical edits under the same finding kind and rule.
// Identical edits are review candidates, never evidence of identical meaning.
type FindingGroup struct {
	ID          string              `json:"id"`
	Rule        string              `json:"rule"`
	Kind        string              `json:"kind"`
	Message     string              `json:"message"`
	TextHunk    bool                `json:"text_hunk"`
	Before      string              `json:"before"`
	After       string              `json:"after"`
	Occurrences []FindingOccurrence `json:"occurrences"`
}

type FindingFamily struct {
	ID         string `json:"id"`
	Exact      int    `json:"exact_files"`
	Approved   int    `json:"approved_files"`
	Unresolved int    `json:"unresolved_files"`
	Findings   int    `json:"findings"`
}

// FindingSummary is shared by the edit-group and file views of the same inspection.
// Neither view changes the findings, scope or completion requirements.
type FindingSummary struct {
	Format          int             `json:"format"`
	Kind            string          `json:"kind"`
	Upstream        string          `json:"upstream"`
	Revision        string          `json:"revision"`
	Head            string          `json:"head"`
	PolicySHA256    string          `json:"policy_sha256"`
	SnapshotSHA256  string          `json:"snapshot_sha256"`
	Scope           OptionsJSON     `json:"scope"`
	Coverage        Coverage        `json:"coverage"`
	Exact           int             `json:"exact_files"`
	Approved        int             `json:"approved_files"`
	Unresolved      int             `json:"unresolved_files"`
	FindingCount    int             `json:"findings"`
	UnresolvedHunks int             `json:"unresolved_text_hunks"`
	Families        []FindingFamily `json:"families"`
	Complete        bool            `json:"source_review_complete"`
}

type FindingQueue struct {
	FindingSummary
	Groups        []FindingGroup `json:"groups"`
	SelectedGroup string         `json:"selected_group,omitempty"`
}

func BuildFindingQueue(r *Report) *FindingQueue {
	q := &FindingQueue{FindingSummary: FindingSummary{Format: 1, Kind: "concave-finding-queue", Upstream: r.Upstream, Revision: r.Revision, Head: r.Head,
		PolicySHA256: r.PolicySHA256, SnapshotSHA256: r.SnapshotSHA256, Scope: r.Scope, Coverage: r.Coverage,
		FindingCount: len(r.Findings), Families: []FindingFamily{}}, Groups: []FindingGroup{}}
	families := map[string]*FindingFamily{}
	fileFamilies := map[string]string{}
	for _, row := range r.Files {
		fileFamilies[row.Path] = row.Family
		if row.Upstream == nil {
			continue
		}
		f := families[row.Family]
		if f == nil {
			f = &FindingFamily{ID: row.Family}
			families[row.Family] = f
		}
		switch row.Classification {
		case "preserved":
			f.Exact++
			q.Exact++
		case "transformed", "excepted", "derived":
			f.Approved++
			q.Approved++
		default:
			f.Unresolved++
			q.Unresolved++
		}
	}
	type location struct {
		path       string
		start, end int
	}
	hunks := map[location]repository.Hunk{}
	for _, h := range r.Hunks {
		hunks[location{h.Path, h.Start, h.End}] = h
	}
	groups := map[string]*FindingGroup{}
	unresolvedHunks := map[string]bool{}
	for _, f := range r.Findings {
		o := FindingOccurrence{Path: f.Path, Family: fileFamilies[f.Path], Line: f.Line, EndLine: f.EndLine, Upstream: f.Upstream, Exception: f.Exception}
		g := FindingGroup{Rule: f.Rule, Kind: f.Kind, Message: f.Message, Occurrences: []FindingOccurrence{}}
		if f.Upstream != nil {
			if h, ok := hunks[location{f.Path, f.Upstream.Start, f.Upstream.End}]; ok {
				g.TextHunk, g.Before, g.After = true, h.Before, h.After
				o.Hunk = h.ID
				unresolvedHunks[h.ID] = true
			}
		}
		// Marshal unambiguous fields, including whether empty bytes mean an
		// empty textual edit or a non-text finding. Paths and offsets deliberately
		// do not affect the identity of a repeated edit.
		key, _ := json.Marshal([]any{g.Rule, g.Kind, g.Message, g.TextHunk, g.Before, g.After})
		g.ID = repository.Digest(key)[:20]
		if groups[g.ID] == nil {
			groups[g.ID] = &g
		}
		groups[g.ID].Occurrences = append(groups[g.ID].Occurrences, o)
		if family := families[o.Family]; family != nil {
			family.Findings++
		}
	}
	q.UnresolvedHunks = len(unresolvedHunks)
	for _, g := range groups {
		sort.Slice(g.Occurrences, func(i, j int) bool {
			a, b := g.Occurrences[i], g.Occurrences[j]
			if a.Path != b.Path {
				return a.Path < b.Path
			}
			if a.Line != b.Line {
				return a.Line < b.Line
			}
			if a.Hunk != b.Hunk {
				return a.Hunk < b.Hunk
			}
			return a.Exception < b.Exception
		})
		q.Groups = append(q.Groups, *g)
	}
	sort.Slice(q.Groups, func(i, j int) bool {
		a, b := q.Groups[i], q.Groups[j]
		if len(a.Occurrences) != len(b.Occurrences) {
			return len(a.Occurrences) > len(b.Occurrences)
		}
		return a.ID < b.ID
	})
	for _, f := range families {
		q.Families = append(q.Families, *f)
	}
	sort.Slice(q.Families, func(i, j int) bool { return q.Families[i].ID < q.Families[j].ID })
	q.Complete = q.Coverage.Scope == "repository" && q.Coverage.Uninspected == 0 && q.Unresolved == 0 && q.FindingCount == 0
	return q
}
