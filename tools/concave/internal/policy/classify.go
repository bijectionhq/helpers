package policy

import (
	"fmt"
	"sort"
	"strings"

	"concave/internal/repository"
	"concave/internal/syntax"
)

// Classify reconstructs the permitted source from the pinned original. Fences
// are never blanket suppressions: both original and replacement are bound.
func (c *Config) Classify(path string, before, after []byte, hs []repository.Hunk) (string, []string, []Finding) {
	return (&Session{Config: c}).ClassifyRestorations(path, before, after, hs, nil, nil)
}

// Transformations apply first. An exception owns every transformation edit its
// original span encloses; an edit crossing that span has no single owner. An
// exception enclosing such edits binds the transformed original (Compose).
func (s *Session) ClassifyRestorations(path string, before, after []byte, hs []repository.Hunk, historical []RestoredHunk, transformed []TransformPlan) (string, []string, []Finding) {
	class, approved, findings, _ := s.ClassifyHunks(path, before, after, hs, historical, transformed)
	return class, approved, findings
}

// ClassifyHunks also names, for each hunk, the approval that covers it, or "".
func (s *Session) ClassifyHunks(path string, before, after []byte, hs []repository.Hunk, historical []RestoredHunk, transformed []TransformPlan) (string, []string, []Finding, []string) {
	c := s.Config
	var engines []*syntax.Engine
	if s.Syntax != nil {
		engines = append(engines, s.Syntax)
	}
	findings := []Finding{}
	approved := []string{}
	if c.Exact(path) {
		if string(before) == string(after) {
			return "preserved", approved, findings, make([]string, len(hs))
		}
		return "unexplained", approved, changeFindings("exact_file_changed", path, before, after, hs, "file must remain exactly upstream"), make([]string, len(hs))
	}
	fs, bad := s.Annotations(path, string(after))
	AbsorbBlankEdges(fs, string(after), hs)
	covered := map[int]string{}
	findings = append(findings, bad...)
	known := map[string]bool{}
	type edit struct {
		start, end int
		text       string
		owner      string
	}
	edits := []edit{}
	lines := repository.Lines(string(before))
	supplied := map[string][]repository.Hunk{}
	for _, plan := range transformed {
		supplied[plan.ID] = plan.Hunks
	}
	plans := []TransformPlan{}
	for _, t := range c.Transforms {
		if !t.Matches(path) {
			continue
		}
		updated, err := t.Apply(path, c.Manifest, string(before), engines...)
		if err != nil {
			findings = append(findings, Finding{Kind: "stale_transform", Path: path, Line: 1, Exception: t.ID, Message: err.Error()})
			continue
		}
		planned, ok := supplied[t.ID]
		if !ok {
			// The standalone classifier can verify a complete recipe without
			// derived hunks. Conservatively reserve the whole file in that case;
			// analysis always supplies exact batched Git edits for composition.
			planned = []repository.Hunk{{Start: 0, End: len(lines), Before: string(before), After: updated}}
		}
		plans = append(plans, TransformPlan{ID: t.ID, Hunks: planned})
	}
	composedAnchor := ""
	var composed Transformed
	if len(plans) > 0 {
		if result, err := TransformedLines(lines, plans); err == nil {
			composed = result
			composedAnchor = repository.Digest([]byte(strings.Join(result.Lines, "")))
		}
	}
	for _, x := range c.Exceptions {
		if x.Path != path || x.File != nil {
			continue
		}
		known[x.ID] = true
		fail := func(why string) {
			line, end := 1, 1
			if f, ok := fs[x.ID]; ok {
				line, end = f.Start+1, f.End
			} else {
				for _, h := range hs {
					if ConflictingSpans(Span{h.Start, h.End}, Span{x.Start, x.End}) {
						line, end = h.CurrentStart+1, max(h.CurrentEnd, h.CurrentStart+1)
						break
					}
				}
			}
			findings = append(findings, Finding{Kind: "stale_exception", Path: path, Line: line, EndLine: end, Exception: x.ID, Upstream: &Span{Start: x.Start, End: x.End}, Message: why})
		}
		if repository.Digest(before) != x.UpstreamSHA256 && (composedAnchor == "" || composedAnchor != x.UpstreamSHA256) || x.End > len(lines) {
			fail("upstream anchor changed")
			continue
		}
		replacement := ""
		found := false
		if x.Fence {
			f, ok := fs[x.ID]
			if ok && f.CodeSHA256() == x.ReplacementSHA256 {
				if len(hs) > 0 {
					span, err := AnnotationSpan(f, hs)
					if err != nil {
						fail(err.Error())
						continue
					}
					if span.Start != x.Start || span.End != x.End {
						fail(fmt.Sprintf("fence maps to upstream [%d,%d), recorded [%d,%d); review its source span before refreshing", span.Start, span.End, x.Start, x.End))
						continue
					}
				}
				if f.Code == strings.Join(lines[x.Start:x.End], "") {
					findings = append(findings, Finding{Kind: "obsolete_exception", Path: path, Line: f.Start + 1, EndLine: f.End, Exception: x.ID, Upstream: &Span{Start: x.Start, End: x.End}, Message: "code equals upstream; remove the annotation, then refresh the binding"})
					continue
				}
				if composedAnchor != "" && f.Code == composed.Span(lines, Span{x.Start, x.End}) {
					findings = append(findings, Finding{Kind: "obsolete_exception", Path: path, Line: f.Start + 1, EndLine: f.End, Exception: x.ID, Upstream: &Span{Start: x.Start, End: x.End}, Message: "code equals upstream after transformations; remove the annotation, then refresh the binding"})
					continue
				}
				replacement = f.Whole
				found = true
				for i, h := range hs {
					if h.CurrentStart >= f.Start && h.CurrentEnd <= f.End && h.Start >= x.Start && h.End <= x.End {
						covered[i] = x.ID
					}
				}
			}
		} else {
			for i, h := range hs {
				if h.Start == x.Start && h.End == x.End && repository.Digest([]byte(h.After)) == x.ReplacementSHA256 {
					replacement = h.After
					found = true
					covered[i] = x.ID
				}
			}
		}
		if !found {
			if SpanRestored(x, hs) {
				findings = append(findings, Finding{Kind: "obsolete_exception", Path: path, Line: 1, Exception: x.ID, Upstream: &Span{Start: x.Start, End: x.End}, Message: "original span is restored; refresh to remove its binding"})
				continue
			}
			fail("approved replacement or fence changed")
			continue
		}
		edits = append(edits, edit{x.Start, x.End, replacement, x.ID})
		approved = append(approved, x.ID)
	}
	for id, f := range fs {
		if !known[id] {
			findings = append(findings, Finding{Kind: "unknown_fence", Path: path, Line: f.Start + 1, EndLine: f.End, Exception: id, Message: "fence has no recorded exception; preview its bounded changes with exception record"})
		}
	}
	for i, h := range hs {
		if covered[i] != "" {
			continue
		}
		matches := []string{}
		for _, old := range historical {
			if old.Matches(h) {
				matches = append(matches, old.Rule)
			}
		}
		if len(matches) > 1 {
			findings = append(findings, Finding{Kind: "ambiguous_restoration", Rule: "history/restored", Path: path, Line: h.CurrentStart + 1, EndLine: h.CurrentEnd, Upstream: &Span{h.Start, h.End}, Subject: HunkSubject(before, h.Start, h.End, h.After), Message: "multiple historical rules cover this restoration; narrow their paths"})
			continue
		}
		if len(matches) == 1 {
			covered[i] = matches[0]
			edits = append(edits, edit{h.Start, h.End, h.After, matches[0]})
			approved = append(approved, matches[0])
		}
	}
	category := "excepted"
	for _, plan := range plans {
		conflict := ""
		kept := []repository.Hunk{}
		for _, h := range plan.Hunks {
			span := Span{h.Start, h.End}
			// Invalid/stale bindings reserve their original ownership too. A
			// recipe must never rewrite an exception's metadata, and an edit
			// crossing an exception's span has no single owner.
			enclosed := false
			for _, x := range c.Exceptions {
				if x.Path != path || x.File != nil || !ConflictingSpans(span, Span{x.Start, x.End}) {
					continue
				}
				if !Encloses(Span{x.Start, x.End}, h) {
					conflict = x.ID
					break
				}
				enclosed = true
			}
			if conflict == "" && !enclosed {
				for _, e := range edits {
					if ConflictingSpans(span, Span{e.start, e.end}) {
						conflict = e.owner
						break
					}
				}
			}
			if conflict != "" {
				break
			}
			if !enclosed {
				kept = append(kept, h)
			}
		}
		if conflict != "" {
			findings = append(findings, Finding{Kind: "stale_transform", Path: path, Line: 1, Exception: plan.ID, Message: "transformation crosses source owned by " + conflict + "; enclose its edit in that exception or narrow the recipe"})
			continue
		}
		for _, h := range kept {
			edits = append(edits, edit{h.Start, h.End, h.After, plan.ID})
		}
		// Git derives these edits from upstream plus the recipe. Comparing each
		// original span and both byte strings also recognizes anchored insertions
		// and deletions whose recipe includes context outside the changed hunk.
		for i, h := range hs {
			for _, permitted := range kept {
				if h.Start == permitted.Start && h.End == permitted.End && h.Before == permitted.Before && h.After == permitted.After {
					covered[i] = plan.ID
				}
			}
		}
		approved = append(approved, plan.ID)
		category = "transformed"
	}
	sort.Slice(edits, func(i, j int) bool { return edits[i].start > edits[j].start })
	expected := append([]string{}, lines...)
	for _, e := range edits {
		next := append([]string{}, expected[:e.start]...)
		next = append(next, e.text)
		next = append(next, expected[e.end:]...)
		expected = next
	}
	want := strings.Join(expected, "")
	sort.Strings(approved)
	unique := approved[:0]
	for _, id := range approved {
		if len(unique) == 0 || unique[len(unique)-1] != id {
			unique = append(unique, id)
		}
	}
	approved = unique
	sort.Slice(findings, func(i, j int) bool { return findings[i].Rule < findings[j].Rule })
	if want != string(after) {
		remaining := []repository.Hunk{}
		for i, h := range hs {
			if covered[i] == "" {
				remaining = append(remaining, h)
			}
		}
		findings = append(findings, changeFindings("unexplained_change", path, before, after, remaining, "source differs from upstream plus approved changes")...)
	}
	NormalizeFindings(findings)
	owners := make([]string, len(hs))
	for i := range hs {
		owners[i] = covered[i]
	}
	if len(findings) > 0 {
		return "unexplained", approved, findings, owners
	}
	if len(approved) == 0 {
		return "preserved", approved, findings, owners
	}
	return category, approved, findings, owners
}

// AbsorbBlankEdges lets each fence own the blank lines between a marker and
// the edge of a hunk that crosses that marker. A formatter separates a fenced
// item from its neighbors with blank lines on both sides, while upstream had
// one: whichever upstream blank line the diff aligns, an inserted blank line
// then sits just outside a marker. Only whitespace-only current lines inside
// the crossing hunk move into the fence, so the result does not depend on that
// alignment and never absorbs code. The binding still covers the fenced code.
func AbsorbBlankEdges(annotations map[string]Annotation, text string, hs []repository.Hunk) {
	lines := repository.Lines(text)
	blank := func(from, to int) bool {
		for i := from; i < to; i++ {
			if i < 0 || i >= len(lines) || strings.TrimSpace(lines[i]) != "" {
				return false
			}
		}
		return true
	}
	for id, a := range annotations {
		for _, h := range hs {
			if h.CurrentStart < a.Start && a.Start < h.CurrentEnd && blank(h.CurrentStart, a.Start) {
				a.Whole = strings.Join(lines[h.CurrentStart:a.Start], "") + a.Whole
				a.Start = h.CurrentStart
			}
			if h.CurrentStart < a.End && a.End < h.CurrentEnd && blank(a.End, h.CurrentEnd) {
				a.Whole += strings.Join(lines[a.End:h.CurrentEnd], "")
				a.End = h.CurrentEnd
			}
		}
		annotations[id] = a
	}
	// Two fences a formatter left adjacent, separated only by inserted blank
	// lines, share one inserted hunk: the earlier fence owns those blank lines,
	// so the fences meet exactly and SplitAtFences can divide the hunk there.
	ordered := make([]string, 0, len(annotations))
	for id := range annotations {
		ordered = append(ordered, id)
	}
	sort.Slice(ordered, func(i, j int) bool { return annotations[ordered[i]].Start < annotations[ordered[j]].Start })
	for i := 0; i+1 < len(ordered); i++ {
		a, b := annotations[ordered[i]], annotations[ordered[i+1]]
		if a.End >= b.Start || !blank(a.End, b.Start) {
			continue
		}
		for _, h := range hs {
			if h.Start == h.End && h.CurrentStart <= a.End && b.Start <= h.CurrentEnd {
				a.Whole += strings.Join(lines[a.End:b.Start], "")
				a.End = b.Start
				annotations[ordered[i]] = a
				break
			}
		}
	}
}

// SplitAtFences divides each inserted hunk (no original lines) at the fence
// boundaries inside it. Every piece is still an insertion at the same original
// position, so the division is exact; it lets adjacent fences, which a
// formatter can leave in one inserted hunk, each own their part. A hunk that
// also replaces original lines is never divided: its lines have no unambiguous
// owner, and the fences must be merged or separated by an unchanged line.
func SplitAtFences(annotations map[string]Annotation, text string, hs []repository.Hunk) []repository.Hunk {
	if len(annotations) == 0 {
		return hs
	}
	fences := map[string]Annotation{}
	for id, a := range annotations {
		fences[id] = a
	}
	AbsorbBlankEdges(fences, text, hs)
	bounds := []int{}
	for _, a := range fences {
		bounds = append(bounds, a.Start, a.End)
	}
	sort.Ints(bounds)
	lines := repository.Lines(text)
	out := make([]repository.Hunk, 0, len(hs))
	for _, h := range hs {
		if h.Start != h.End || h.BeforeMode != "" || h.AfterMode != "" || h.CurrentEnd > len(lines) {
			out = append(out, h)
			continue
		}
		from := h.CurrentStart
		for _, cut := range append(bounds, h.CurrentEnd) {
			if cut <= from || cut > h.CurrentEnd {
				continue
			}
			piece := h
			piece.CurrentStart, piece.CurrentEnd = from, cut
			piece.After = strings.Join(lines[from:cut], "")
			out = append(out, piece)
			from = cut
		}
	}
	return out
}

// AnnotationSpan maps both fence boundaries through the already measured hunks.
// A hunk crossing a fence boundary is ambiguous and must be split by its author.
func AnnotationSpan(a Annotation, hs []repository.Hunk) (Span, error) {
	boundary := func(pos int) (int, error) {
		delta := 0
		for _, h := range hs {
			if pos < h.CurrentStart {
				return pos + delta, nil
			}
			if pos == h.CurrentStart {
				return h.Start, nil
			}
			if pos < h.CurrentEnd {
				return 0, fmt.Errorf("change crosses fence boundary: %s:%d-%d (hunk %s, fence %s); place the marker outside this complete hunk, or, when the hunk joins two adjacent fences, merge them into one fence or keep an unchanged line between them", h.Path, h.CurrentStart+1, max(h.CurrentStart+1, h.CurrentEnd), h.ID, a.ID)
			}
			delta = h.End - h.CurrentEnd
		}
		return pos + delta, nil
	}
	start, e := boundary(a.Start)
	if e != nil {
		return Span{}, e
	}
	end, e := boundary(a.End)
	return Span{Start: start, End: end}, e
}
func SpanRestored(x Exception, hs []repository.Hunk) bool {
	for _, h := range hs {
		if ConflictingSpans(Span{h.Start, h.End}, Span{x.Start, x.End}) {
			return false
		}
	}
	return true
}

// HunkSubject names a divergence by content, not coordinates: the upstream lines
// it replaces, one upstream line on each side, and its replacement. Line shifts
// on either side keep it; rewriting the replacement or its upstream does not.
func HunkSubject(upstream []byte, start, end int, replacement string) string {
	lines := repository.Lines(string(upstream))
	before, after := "\x01", "\x01"
	if start > 0 && start <= len(lines) {
		before = lines[start-1]
	}
	if end < len(lines) {
		after = lines[end]
	}
	replaced := ""
	if start <= end && end <= len(lines) {
		replaced = strings.Join(lines[start:end], "")
	}
	return Subject(before, replaced, after, replacement)
}

func changeFindings(kind, path string, before, after []byte, hs []repository.Hunk, msg string) []Finding {
	out := []Finding{}
	for _, h := range hs {
		out = append(out, Finding{Kind: kind, Path: path, Line: h.CurrentStart + 1, EndLine: max(h.CurrentStart+1, h.CurrentEnd), Upstream: &Span{Start: h.Start, End: h.End}, Subject: HunkSubject(before, h.Start, h.End, h.After), Message: msg})
	}
	if len(out) == 0 {
		a, b := repository.Lines(string(before)), repository.Lines(string(after))
		start := 0
		for start < len(a) && start < len(b) && a[start] == b[start] {
			start++
		}
		ae, be := len(a), len(b)
		for ae > start && be > start && a[ae-1] == b[be-1] {
			ae--
			be--
		}
		out = append(out, Finding{Kind: kind, Path: path, Line: start + 1, EndLine: max(start+1, be), Upstream: &Span{Start: start, End: ae}, Subject: HunkSubject(before, start, ae, strings.Join(b[start:be], "")), Message: msg})
	}
	NormalizeFindings(out)
	return out
}
