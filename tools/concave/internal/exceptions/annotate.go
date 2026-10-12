package exceptions

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"concave/internal/analysis"
	"concave/internal/contracts"
	"concave/internal/policy"
	"concave/internal/recovery"
	"concave/internal/repository"
)

func prepareAnnotated(repo *repository.Repo, config *policy.Config, action Action, ids []string, o Options) (*Proposal, error) {
	if action != Record || len(ids) != 1 || o.File || len(o.Replace) > 0 || !policy.SupportsAnnotations(o.Path) || len(o.Hunks) == 0 {
		return nil, fmt.Errorf("--annotate requires record, one new ID, a supported --path and explicit --hunk selections")
	}
	id := ids[0]
	if !contracts.ID.MatchString(id) || lookup(config, id) != nil {
		return nil, fmt.Errorf("annotation needs a new valid exception ID")
	}
	if err := o.Metadata.Validate(); err != nil {
		return nil, err
	}
	if config.Exact(o.Path) {
		return nil, fmt.Errorf("exact-preservation files cannot have exceptions")
	}
	report, err := analysis.Analyze(repo, config, analysis.Options{Paths: []string{o.Path}, Diagnose: true})
	if err != nil {
		return nil, err
	}
	if len(report.Files) != 1 || report.Files[0].Upstream == nil {
		return nil, fmt.Errorf("annotation needs one inherited file")
	}
	state, source, err := repo.Snapshot(o.Path)
	if err != nil {
		return nil, err
	}
	if state == nil || !repository.Same(state, report.Files[0].Selected) {
		return nil, &repository.Stale{Message: "annotation source changed"}
	}
	before, err := repo.Blob(report.Files[0].Upstream.OID)
	if err != nil {
		return nil, err
	}
	selected := map[string]bool{}
	for _, h := range o.Hunks {
		if selected[h] {
			return nil, fmt.Errorf("duplicate hunk")
		}
		selected[h] = true
	}
	start, end, upStart, upEnd := len(repository.Lines(string(source))), 0, len(repository.Lines(string(before))), 0
	found := 0
	for _, h := range report.Hunks {
		if selected[h.ID] {
			found++
			start = min(start, h.CurrentStart)
			end = max(end, h.CurrentEnd)
			upStart = min(upStart, h.Start)
			upEnd = max(upEnd, h.End)
		}
	}
	if found != len(selected) {
		return nil, &repository.Stale{Message: "annotation hunk selection changed; inspect again"}
	}
	for _, h := range report.Hunks {
		if h.CurrentStart <= end && h.CurrentEnd >= start && !selected[h.ID] {
			return nil, fmt.Errorf("fence would absorb unselected hunk %s; explicitly select it or choose a separate fence", h.ID)
		}
	}
	session := policy.NewSession(repo, config)
	annotations, bad := session.Annotations(o.Path, string(source))
	if len(bad) > 0 {
		return nil, fmt.Errorf("fix existing invalid fences before annotation")
	}
	for _, a := range annotations {
		if a.Start <= end && a.End >= start {
			return nil, fmt.Errorf("new fence overlaps %s; edit and refresh that fence", a.ID)
		}
	}
	lines := repository.Lines(string(source))
	eol := "\n"
	if strings.Contains(string(source), "\r\n") {
		eol = "\r\n"
	}
	prefix := "//"
	if filepath.Ext(o.Path) == ".toml" {
		prefix = "#"
	}
	indent := ""
	if start < len(lines) {
		indent = lines[start][:len(lines[start])-len(strings.TrimLeft(lines[start], " \t"))]
	}
	comment := func(s string) string { return indent + prefix + " " + s + eol }
	document := filepath.Ext(o.Path) == ".mdx" || filepath.Ext(o.Path) == ".md"
	if document {
		// Frontmatter is YAML: its fences are '#' comment lines around whole fields.
		facts, err := session.Documents.Parse(o.Path, string(source))
		if err != nil {
			return nil, err
		}
		if first, last, ok := facts.FrontmatterLines(string(source)); ok && start < last && end > first {
			if start < first || end > last {
				return nil, fmt.Errorf("select frontmatter and page body hunks in separate fences")
			}
			document, prefix = false, "#"
		}
	}
	opening, closing := "{/*", "*/}"
	if filepath.Ext(o.Path) == ".md" {
		opening, closing = "<!--", "-->"
	}
	if document {
		comment = func(s string) string { return indent + s + eol }
	}
	var fence strings.Builder
	if document {
		fence.WriteString(indent + opening + " ")
	}
	fence.WriteString(comment("concave:begin " + id))
	field := func(key, value string) {
		parts := strings.Split(strings.ReplaceAll(value, "\r\n", "\n"), "\n")
		for i, part := range parts {
			if i == 0 {
				part = key + ": " + part
			}
			fence.WriteString(comment(part))
		}
	}
	field("why", o.Metadata.Why)
	for _, ref := range o.Metadata.Preserves {
		field("preserves", ref)
	}
	for _, ref := range o.Metadata.Witnesses {
		field("witness", ref)
	}
	if o.Metadata.RemoveWhen != "" {
		field("remove-when", o.Metadata.RemoveWhen)
	}
	if document {
		fence.WriteString(indent + closing + eol + eol)
	} else {
		fence.WriteString(indent + prefix + eol)
	}
	code := strings.Join(lines[start:end], "")
	fence.WriteString(code)
	if code != "" && !strings.HasSuffix(code, "\n") {
		fence.WriteString(eol)
	}
	if document {
		fence.WriteString(indent + opening + " concave:end " + id + " " + closing + eol)
	} else {
		fence.WriteString(comment("concave:end " + id))
	}
	nextSource := []byte(strings.Join(lines[:start], "") + fence.String() + strings.Join(lines[end:], ""))
	annotations, bad = session.Annotations(o.Path, string(nextSource))
	if len(bad) > 0 {
		return nil, fmt.Errorf("proposed annotation is invalid: %s", bad[0].Message)
	}
	if _, ok := annotations[id]; !ok {
		return nil, fmt.Errorf("proposed fence is inside a literal or block comment; move outside that syntax")
	}
	hs, err := repo.DiffSources(map[string]repository.SourcePair{o.Path: {Before: before, After: nextSource}})
	if err != nil {
		return nil, err
	}
	hs[o.Path] = repository.Identify(o.Path, before, nextSource, policy.SplitAtFences(annotations, string(nextSource), hs[o.Path]))
	policy.AbsorbBlankEdges(annotations, string(nextSource), hs[o.Path])
	a := annotations[id]
	span, err := policy.AnnotationSpan(a, hs[o.Path])
	if err != nil {
		return nil, err
	}
	if span.Start != upStart || span.End != upEnd {
		return nil, fmt.Errorf("proposed fence expands upstream scope; select the complete neighboring hunk explicitly")
	}
	binding := &policy.Exception{ID: id, Path: o.Path, Start: span.Start, End: span.End, ReplacementSHA256: a.CodeSHA256(), Fence: true}
	composition, err := composeBinding(repo, config, before, binding)
	if err != nil {
		return nil, err
	}
	change := &Change{ID: id, Action: Record, Next: binding, Explanation: &a.Explanation, Hunks: append([]string{}, o.Hunks...), SourceDiff: string(repository.Patch(o.Path, []repository.Hunk{{Start: span.Start, End: span.End, Before: composition.Original, After: a.Whole, CurrentStart: a.Start, CurrentEnd: a.End}})), observed: repository.Inputs{o.Path: state}}
	sort.Strings(change.Hunks)
	if err := observeReferences(repo, change, o.Path, a.Explanation); err != nil {
		return nil, err
	}
	p := &Proposal{Format: ProposalFormat, Kind: "concave-exception-proposal", Status: Preview, Action: Record, Changes: []*Change{change}, Observed: change.observed, SourcePatch: string(recovery.SinglePatch(o.Path, source, nextSource))}
	change.observed = nil
	if err := p.Observed.Verify(repo, repository.SourceInputs); err != nil {
		return nil, err
	}
	return finish(config, p)
}
