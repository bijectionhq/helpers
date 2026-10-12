package analysis

import (
	"sort"

	"concave/internal/repository"
)

// FindingFile measures only unresolved, located text hunks. OtherFindings keeps missing
// files, binary changes and unlocated annotation failures visible; zero text
// bytes never implies those findings have no source impact.
type FindingFile struct {
	Path          string   `json:"path"`
	Family        string   `json:"family"`
	Findings      int      `json:"findings"`
	TextHunks     int      `json:"text_hunks"`
	RemovedLines  int      `json:"removed_lines"`
	AddedLines    int      `json:"added_lines"`
	ChangedBytes  int      `json:"changed_bytes"`
	OtherFindings int      `json:"other_findings"`
	Kinds         []string `json:"kinds"`
	Groups        []string `json:"groups"`
}

type FindingFileView struct {
	FindingSummary
	Files []FindingFile `json:"files"`
}

// FindingFiles projects the complete edit queue without another source inspection or
// policy store. Groups remain stable drill-down IDs for the same snapshot/scope.
func FindingFiles(q *FindingQueue) *FindingFileView {
	view := &FindingFileView{FindingSummary: q.FindingSummary, Files: []FindingFile{}}
	view.Kind = "concave-finding-files"
	type entry struct {
		file   FindingFile
		hunks  map[string]bool
		groups map[string]bool
		kinds  map[string]bool
	}
	files := map[string]*entry{}
	for _, g := range q.Groups {
		for _, o := range g.Occurrences {
			e := files[o.Path]
			if e == nil {
				e = &entry{file: FindingFile{Path: o.Path, Family: o.Family}, hunks: map[string]bool{}, groups: map[string]bool{}, kinds: map[string]bool{}}
				files[o.Path] = e
			}
			e.file.Findings++
			e.groups[g.ID], e.kinds[g.Kind] = true, true
			if !g.TextHunk || o.Hunk == "" {
				e.file.OtherFindings++
				continue
			}
			if e.hunks[o.Hunk] {
				continue
			}
			e.hunks[o.Hunk] = true
			e.file.TextHunks++
			e.file.RemovedLines += len(repository.Lines(g.Before))
			e.file.AddedLines += len(repository.Lines(g.After))
			e.file.ChangedBytes += len(g.Before) + len(g.After)
		}
	}
	for _, e := range files {
		for id := range e.groups {
			e.file.Groups = append(e.file.Groups, id)
		}
		for kind := range e.kinds {
			e.file.Kinds = append(e.file.Kinds, kind)
		}
		sort.Strings(e.file.Groups)
		sort.Strings(e.file.Kinds)
		view.Files = append(view.Files, e.file)
	}
	sort.Slice(view.Files, func(i, j int) bool {
		a, b := view.Files[i], view.Files[j]
		// Unknown text size must not masquerade as an easy zero-byte edit.
		if (a.OtherFindings > 0) != (b.OtherFindings > 0) {
			return a.OtherFindings == 0
		}
		if a.TextHunks != b.TextHunks {
			return a.TextHunks < b.TextHunks
		}
		if a.ChangedBytes != b.ChangedBytes {
			return a.ChangedBytes < b.ChangedBytes
		}
		return a.Path < b.Path
	})
	return view
}
