package policy

import (
	"fmt"
	"sort"
	"strings"

	"concave/internal/repository"
)

// TransformationPlans derives a file's declared edits from its upstream bytes.
// Authoring and recovery share the same engine and composition as inspection.
func (s *Session) TransformationPlans(repo *repository.Repo, path string, before []byte) ([]TransformPlan, error) {
	plans := []TransformPlan{}
	for _, transform := range s.Config.Transforms {
		if !transform.Matches(path) {
			continue
		}
		want, err := transform.Apply(path, s.Config.Manifest, string(before), s.Syntax)
		if err != nil {
			return nil, fmt.Errorf("transformation %s: %w", transform.ID, err)
		}
		hunks, err := repo.DiffSources(map[string]repository.SourcePair{path: {Before: before, After: []byte(want)}})
		if err != nil {
			return nil, err
		}
		plans = append(plans, TransformPlan{ID: transform.ID, Hunks: hunks[path]})
	}
	return plans, nil
}

// TransformPlan is one transformation's edits over the pinned original, as
// hunks from upstream to that transformation's output.
type TransformPlan struct {
	ID    string
	Hunks []repository.Hunk
}

// Encloses reports whether an exception's original span owns a transformation
// edit outright: the edit lies inside the span, and an insertion is strictly
// inside, never at an edge where its order beside the exception is ambiguous.
// An edit that only overlaps the span has no single owner.
func Encloses(span Span, h repository.Hunk) bool {
	if h.Start == h.End {
		return h.Start > span.Start && h.Start < span.End
	}
	return h.Start >= span.Start && h.End <= span.End
}

// Composition is how an exception's original span meets the transformations
// of its file. Transformations apply first; an exception enclosing their edits
// explains only what remains, so its binding anchors the transformed original:
// Anchor digests the whole transformed file and Original is the span's
// transformed text. Without enclosed edits both describe the pinned upstream.
type Composition struct {
	Anchor   string
	Original string
	Enclosed []string
}

// Compose fails when a transformation edit crosses the span's boundary.
func Compose(before []byte, plans []TransformPlan, span Span) (Composition, error) {
	lines := repository.Lines(string(before))
	if span.Start < 0 || span.End < span.Start || span.End > len(lines) {
		return Composition{}, fmt.Errorf("span maps outside upstream source")
	}
	enclosed := map[string]bool{}
	for _, plan := range plans {
		for _, h := range plan.Hunks {
			if !ConflictingSpans(Span{h.Start, h.End}, span) {
				continue
			}
			if !Encloses(span, h) {
				return Composition{}, fmt.Errorf("transformation %s crosses the boundary of upstream lines %d..%d at %d..%d; move the fence to enclose that edit or leave it outside", plan.ID, span.Start+1, max(span.Start+1, span.End), h.Start+1, max(h.Start+1, h.End))
			}
			enclosed[plan.ID] = true
		}
	}
	if len(enclosed) == 0 {
		return Composition{Anchor: repository.Digest(before), Original: strings.Join(lines[span.Start:span.End], "")}, nil
	}
	transformed, err := TransformedLines(lines, plans)
	if err != nil {
		return Composition{}, err
	}
	c := Composition{Anchor: repository.Digest([]byte(strings.Join(transformed.Lines, ""))), Original: transformed.Span(lines, span)}
	for id := range enclosed {
		c.Enclosed = append(c.Enclosed, id)
	}
	sort.Strings(c.Enclosed)
	return c, nil
}

// Transformed is the original after every transformation edit, with the
// original-line edits that produced it.
type Transformed struct {
	Lines []string
	edits []repository.Hunk
}

// TransformedLines applies every plan's edits. Overlapping edits from two
// transformations have no single owner and fail.
func TransformedLines(lines []string, plans []TransformPlan) (Transformed, error) {
	edits := []repository.Hunk{}
	owners := []string{}
	for _, plan := range plans {
		for _, h := range plan.Hunks {
			for i, other := range edits {
				if ConflictingSpans(Span{h.Start, h.End}, Span{other.Start, other.End}) {
					return Transformed{}, fmt.Errorf("transformations %s and %s edit the same upstream lines", owners[i], plan.ID)
				}
			}
			edits = append(edits, h)
			owners = append(owners, plan.ID)
		}
	}
	sort.Slice(edits, func(i, j int) bool { return edits[i].Start < edits[j].Start })
	out := []string{}
	cursor := 0
	for _, h := range edits {
		if h.Start < cursor || h.End > len(lines) {
			return Transformed{}, fmt.Errorf("transformation edits exceed upstream source")
		}
		out = append(out, lines[cursor:h.Start]...)
		out = append(out, repository.Lines(h.After)...)
		cursor = h.End
	}
	out = append(out, lines[cursor:]...)
	return Transformed{Lines: out, edits: edits}, nil
}

// Span is the transformed text of an original span whose edits it encloses.
func (t Transformed) Span(lines []string, span Span) string {
	var out strings.Builder
	cursor := span.Start
	for _, h := range t.edits {
		if !Encloses(span, h) {
			continue
		}
		out.WriteString(strings.Join(lines[cursor:h.Start], ""))
		out.WriteString(h.After)
		cursor = h.End
	}
	out.WriteString(strings.Join(lines[cursor:span.End], ""))
	return out.String()
}
