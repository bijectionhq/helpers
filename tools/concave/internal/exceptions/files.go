package exceptions

import (
	"fmt"

	"concave/internal/analysis"
	"concave/internal/policy"
	"concave/internal/repository"
)

func prepareFile(repo *repository.Repo, config *policy.Config, action Action, id string, options Options, old *policy.Exception, report *analysis.Report, row *analysis.Row) (*Change, error) {
	if len(options.Hunks) != 0 || len(options.Replace) != 0 {
		return nil, fmt.Errorf("file bindings take a path, not source hunks or replacement exception IDs")
	}
	if old != nil && old.File == nil {
		return nil, fmt.Errorf("a source span cannot be widened to a file binding")
	}
	if row == nil || row.Upstream == nil || row.Upstream.Kind != "file" {
		return nil, fmt.Errorf("file binding requires a bounded inherited regular file")
	}
	if config.Exact(row.Path) {
		return nil, fmt.Errorf("exact-preservation files cannot have exceptions")
	}
	for _, transform := range config.Transforms {
		if transform.Matches(row.Path) {
			return nil, fmt.Errorf("transformed files cannot have file exceptions")
		}
	}
	state, after, err := repo.Snapshot(row.Path)
	if err != nil {
		return nil, err
	}
	if !repository.Same(state, row.Selected) {
		return nil, &repository.Stale{Message: "source changed; inspect again"}
	}
	before, err := repo.Blob(row.Upstream.OID)
	if err != nil {
		return nil, err
	}
	change := &Change{Action: action, ID: id, Previous: []policy.Exception{}, Hunks: []string{}, observed: repository.Inputs{row.Path: state}}
	if old != nil {
		change.Previous = append(change.Previous, *old)
	}
	if policy.FileState(row.Upstream).Matches(state) {
		if old == nil {
			return nil, fmt.Errorf("file equals upstream; no exception is needed")
		}
		change.Action = Remove
		return change, nil
	}
	if err := policy.FileEligible(row.Upstream, state, before, after); err != nil {
		return nil, err
	}
	binding := &policy.FileBinding{Upstream: *policy.FileState(row.Upstream), Replacement: policy.FileState(state)}
	if old != nil && !options.File && (old.File.Upstream != binding.Upstream || (old.File.Replacement == nil) != (binding.Replacement == nil)) {
		return nil, fmt.Errorf("upstream identity or retirement state changed; preview with explicit --file")
	}
	metadata := options.Metadata
	if old != nil && metadata.Why == "" && len(metadata.Witnesses) == 0 && metadata.RemoveWhen == "" && len(metadata.Preserves) == 0 {
		metadata = old.Explanation()
	}
	if err := metadata.Validate(); err != nil {
		return nil, err
	}
	if err := observeReferences(repo, change, row.Path, metadata); err != nil {
		return nil, err
	}
	change.Next = &policy.Exception{ID: id, Path: row.Path, File: binding, Reason: metadata.Why, Witnesses: metadata.Witnesses, RemoveWhen: metadata.RemoveWhen, Preserves: metadata.Preserves}
	change.Explanation = &metadata
	change.SourceDiff = fmt.Sprintf("file %s\nupstream mode=%s sha256=%s\n", row.Path, binding.Upstream.Mode, binding.Upstream.SHA256)
	if binding.Replacement == nil {
		change.SourceDiff += "selected: absent (retirement)\n"
		// Text retirement can be reviewed directly, without a second source
		// authoring format. Binary data is never printed into a report.
		for _, h := range report.Hunks {
			if h.Path == row.Path {
				change.SourceDiff += string(repository.Patch(row.Path, []repository.Hunk{h}))
			}
		}
	} else {
		change.SourceDiff += fmt.Sprintf("selected mode=%s sha256=%s\n", binding.Replacement.Mode, binding.Replacement.SHA256)
	}
	return change, nil
}
