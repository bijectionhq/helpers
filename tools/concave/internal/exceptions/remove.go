package exceptions

import (
	"fmt"
	"slices"

	"concave/internal/analysis"
	"concave/internal/policy"
	"concave/internal/repository"
)

// prepareRemove retires selected bindings explicitly, for example when a
// derived entry or a restoration supersedes them. A fenced binding whose fence
// is still in source is refused: the fence owns its explanation, and removing
// only the binding would leave an unknown fence. The preview names every
// finding the removal leaves on each binding's span; publication refuses a
// retirement whose divergence remains unexplained.
func prepareRemove(repo *repository.Repo, config *policy.Config, ids []string, options Options) (*Proposal, error) {
	if options.Path != "" || options.File || len(options.Hunks) != 0 || len(options.Replace) != 0 || options.Metadata.Why != "" || len(options.Metadata.Witnesses) != 0 || options.Metadata.RemoveWhen != "" || len(options.Metadata.Preserves) != 0 {
		return nil, fmt.Errorf("remove takes only binding IDs")
	}
	ids = slices.Clone(ids)
	slices.Sort(ids)
	removed := map[string]policy.Exception{}
	paths := []string{}
	for index, id := range ids {
		if index > 0 && ids[index-1] == id {
			return nil, fmt.Errorf("duplicate exception ID %q", id)
		}
		binding := lookup(config, id)
		if binding == nil {
			return nil, fmt.Errorf("unknown exception %q", id)
		}
		removed[id] = *binding
		paths = append(paths, binding.Path)
	}
	slices.Sort(paths)
	paths = slices.Compact(paths)
	proposal := &Proposal{Format: ProposalFormat, Kind: "concave-exception-proposal", Status: Preview, Action: Remove, Changes: []*Change{}, Observed: repository.Inputs{}}
	session := policy.NewSession(repo, config)
	for _, path := range paths {
		state, source, err := repo.Snapshot(path)
		if err != nil {
			return nil, err
		}
		if err := proposal.Observed.Observe(path, state); err != nil {
			return nil, err
		}
		if state == nil || state.Kind != "file" || !repository.Text(source) || !policy.SupportsAnnotations(path) {
			continue
		}
		annotations, invalid := session.Annotations(path, string(source))
		if len(invalid) > 0 {
			return nil, fmt.Errorf("fix the invalid annotation at %s:%d before removing bindings: %s", path, invalid[0].Line, invalid[0].Message)
		}
		for _, id := range ids {
			binding := removed[id]
			if binding.Path != path || !binding.Fence {
				continue
			}
			if annotation, fenced := annotations[id]; fenced {
				return nil, fmt.Errorf("exception %s is still fenced at %s:%d; delete its concave:begin/end markers from source first (keeping or restoring the code), or run `concave exception refresh %s` if the fenced code already equals upstream", id, path, annotation.Start+1, id)
			}
		}
	}
	next := *config
	next.Exceptions = []policy.Exception{}
	for _, binding := range config.Exceptions {
		if _, ok := removed[binding.ID]; !ok {
			next.Exceptions = append(next.Exceptions, binding)
		}
	}
	report, err := analysis.Analyze(repo, &next, analysis.Options{Paths: paths})
	if err != nil {
		return nil, err
	}
	for _, row := range report.Files {
		if state, ok := proposal.Observed[row.Path]; ok && !repository.Same(state, row.Selected) {
			return nil, &repository.Stale{Message: "input changed: " + row.Path}
		}
	}
	for _, id := range ids {
		binding := removed[id]
		change := &Change{Action: Remove, ID: id, Previous: []policy.Exception{binding}, Hunks: []string{}, Remaining: []policy.Finding{}}
		if !binding.Fence {
			explanation := binding.Explanation()
			change.Explanation = &explanation
		}
		for _, finding := range report.Findings {
			if finding.Path != binding.Path {
				continue
			}
			if binding.File == nil && (finding.Upstream == nil || !policy.ConflictingSpans(*finding.Upstream, policy.Span{Start: binding.Start, End: binding.End})) {
				continue
			}
			change.Remaining = append(change.Remaining, finding)
		}
		proposal.Changes = append(proposal.Changes, change)
	}
	return finish(config, proposal)
}
