package analysis

import (
	"fmt"
	"sort"
	"strings"

	"concave/internal/policy"
	"concave/internal/repository"
)

// restorationCandidates projects a reviewed deletion onto the current upstream
// only through unchanged surrounding lines. Git supplies historical bytes and
// positional differences in batches, rather than a subprocess per match.
func restorationCandidates(r *repository.Repo, c *policy.Config, base string, contents map[string]sourcePair, history *[]HistoricalBlock) (map[string][]policy.RestoredHunk, error) {
	out := map[string][]policy.RestoredHunk{}
	retained := 0
	for _, rule := range c.Restorations {
		if err := r.Ancestor(rule.RemovedBy, base); err != nil {
			return nil, fmt.Errorf("restoration %s is not upstream history: %w", rule.ID, err)
		}
		parents, err := r.Git(nil, "rev-list", "--parents", "-n", "1", rule.RemovedBy)
		if err != nil {
			return nil, err
		}
		ids := strings.Fields(string(parents))
		if len(ids) != 2 {
			return nil, fmt.Errorf("restoration %s requires a removal commit with exactly one parent", rule.ID)
		}
		paths := []string{}
		for path := range contents {
			if repository.Matches(path, rule.Paths) && (!c.Exact(path) || history != nil) {
				paths = append(paths, path)
			}
		}
		if len(paths) == 0 {
			continue
		}
		sort.Strings(paths)
		removed, err := r.Diff(ids[1], rule.RemovedBy, paths)
		if err != nil {
			return nil, err
		}
		changes, err := r.Diff(rule.RemovedBy, base, paths)
		if err != nil {
			return nil, err
		}
		tree, err := r.Tree(rule.RemovedBy)
		if err != nil {
			return nil, err
		}
		for _, path := range paths {
			entry, ok := tree[path]
			if !ok || entry.Mode != "100644" && entry.Mode != "100755" {
				continue
			}
			if len(removed[path]) == 0 {
				continue
			}
			data, err := r.Blob(entry.OID)
			if err != nil {
				return nil, err
			}
			if !repository.Text(data) {
				continue
			}
			lines := repository.Lines(string(data))
			for _, h := range removed[path] {
				pureDeletion := h.After == "" && h.Before != "" && h.CurrentStart == h.CurrentEnd
				// Git represents deleting a final block and its preceding newline
				// as a replacement of the last surviving line. Permit only that
				// exact historical prefix, at EOF, with actual deleted content.
				joinedEOF := h.After != "" && !strings.Contains(h.After, "\n") &&
					h.CurrentEnd == len(lines) && h.CurrentEnd == h.CurrentStart+1 &&
					strings.HasPrefix(h.Before, h.After+"\n") && len(h.Before) > len(h.After)+1
				if !pureDeletion && !joinedEOF {
					continue
				}
				at, ok := unchangedAnchor(h.CurrentStart, len(lines), changes[path])
				if !ok && history == nil {
					continue
				}
				retained += len(h.Before)
				if retained > repository.MaxOutput {
					return nil, fmt.Errorf("historical restorations exceed source bound")
				}
				if history != nil {
					block := HistoricalBlock{Rule: rule.ID, Path: path, RemovedBy: rule.RemovedBy,
						SourceRevision: ids[1], Source: policy.Span{Start: h.Start, End: h.End},
						HistoricalText: h.Before, Status: "upstream_context_changed"}
					if ok {
						block.Upstream = &policy.Span{Start: at, End: at + len(repository.Lines(h.After))}
						block.Status = "no_single_hunk"
					}
					*history = append(*history, block)
				}
				if !ok || c.Exact(path) {
					continue
				}
				out[path] = append(out[path], policy.RestoredHunk{Rule: rule.ID, At: at, Before: h.After, Text: h.Before, RemovedBy: rule.RemovedBy, Source: policy.Span{Start: h.Start, End: h.End}})
			}
		}
	}
	return out, nil
}

// Two surrounding lines (or the file edge) must survive untouched. Insertion at
// that context boundary is also ambiguous. No substring search or fuzzy move can
// authorize historical code at a new owner.
func unchangedAnchor(at, lines int, changes []repository.Hunk) (int, bool) {
	lo, hi := max(0, at-2), min(lines, at+2)
	delta := 0
	for _, h := range changes {
		if h.Start == h.End {
			if h.Start >= lo && h.Start <= hi {
				return 0, false
			}
		} else if h.Start < hi && h.End > lo {
			return 0, false
		}
		if h.End <= lo {
			delta += (h.CurrentEnd - h.CurrentStart) - (h.End - h.Start)
		}
	}
	return at + delta, true
}
