package analysis

import (
	"sort"
	"strings"

	"concave/internal/policy"
	"concave/internal/repository"
)

// Diff alignment may split or join unchanged replacements when another part of
// a file changes. Compare connected replacements over the same upstream span;
// only exact bytes carry debt. Other rules and changed upstream pins keep their
// ordinary identities.
func coveredRegroupings(before, after *Report, readUpstream func(string) ([]byte, error)) (map[string]bool, map[string]bool) {
	old, current := map[string]bool{}, map[string]bool{}
	if before.Upstream != after.Upstream {
		return old, current
	}
	type entry struct {
		h         repository.Hunk
		finding   policy.Finding
		candidate bool
		eligible  bool
	}
	paths := map[string][]entry{}
	for i, report := range []*Report{before, after} {
		findings := map[string]map[policy.Span]policy.Finding{}
		for _, f := range report.Findings {
			if f.Rule != "source/covered" || f.Exception != "" || f.Upstream == nil {
				continue
			}
			if findings[f.Path] == nil {
				findings[f.Path] = map[policy.Span]policy.Finding{}
			}
			findings[f.Path][*f.Upstream] = f
		}
		for _, h := range report.Hunks {
			f, ok := findings[h.Path][policy.Span{Start: h.Start, End: h.End}]
			eligible := ok && h.Approval == "" && f.Line == h.CurrentStart+1
			// An approved transformation can share an alignment region with debt,
			// but cannot itself manufacture a carried finding.
			paths[h.Path] = append(paths[h.Path], entry{h, f, i == 1, eligible || !ok && h.Approval != ""})
		}
	}
	for path, entries := range paths {
		sort.SliceStable(entries, func(i, j int) bool { return entries[i].h.Start < entries[j].h.Start })
		var source []string
		if readUpstream != nil {
			for _, file := range before.Files {
				if file.Path == path && file.Upstream != nil {
					data, err := readUpstream(file.Upstream.OID)
					if err == nil {
						source = repository.Lines(string(data))
					}
					break
				}
			}
		}
		for at := 0; at < len(entries); {
			start, end, next := entries[at].h.Start, entries[at].h.End, at
			for next < len(entries) && entries[next].h.Start <= end {
				end = max(end, entries[next].h.End)
				next++
			}
			firstEnd := next
			for {
				group := entries[at:next]
				upstream := make([]string, end-start)
				if start >= 0 && end <= len(source) {
					copy(upstream, source[start:end])
				}
				valid := true
				var sides [2][]repository.Hunk
				for _, e := range group {
					if !e.eligible || e.h.BeforeMode != "" || e.h.AfterMode != "" {
						valid = false
						break
					}
					lines := repository.Lines(e.h.Before)
					if len(lines) != e.h.End-e.h.Start {
						valid = false
						break
					}
					for i, line := range lines {
						index := e.h.Start - start + i
						if upstream[index] != "" && upstream[index] != line {
							valid = false
						}
						upstream[index] = line
					}
					side := 0
					if e.candidate {
						side = 1
					}
					sides[side] = append(sides[side], e.h)
				}
				for _, line := range upstream {
					if line == "" {
						valid = false
					}
				}
				if !valid || len(sides[0]) == 0 || len(sides[1]) == 0 {
					break
				}
				replacement := func(hunks []repository.Hunk) (string, bool) {
					var text strings.Builder
					cursor := start
					for _, h := range hunks {
						if h.Start < cursor {
							return "", false
						}
						text.WriteString(strings.Join(upstream[cursor-start:h.Start-start], ""))
						text.WriteString(h.After)
						cursor = h.End
					}
					text.WriteString(strings.Join(upstream[cursor-start:], ""))
					return text.String(), true
				}
				left, lok := replacement(sides[0])
				right, rok := replacement(sides[1])
				if !lok || !rok {
					break
				}
				if left == right {
					for _, e := range group {
						if e.finding.Rule == "" {
							continue
						}
						if e.candidate {
							current[e.finding.Identity()] = true
						} else {
							old[e.finding.Identity()] = true
						}
					}
					firstEnd = next
					break
				}
				// A true byte difference cannot be repaired by a later hunk.
				// A prefix can: alignment may move text across an unchanged gap.
				if next == len(entries) || !(strings.HasPrefix(left, right) || strings.HasPrefix(right, left)) {
					break
				}
				end = entries[next].h.End
				for next < len(entries) && entries[next].h.Start <= end {
					end = max(end, entries[next].h.End)
					next++
				}
			}
			at = firstEnd
		}
	}
	return old, current
}
