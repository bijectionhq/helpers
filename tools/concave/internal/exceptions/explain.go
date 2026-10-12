package exceptions

import (
	"errors"
	"fmt"
	"slices"

	"concave/internal/analysis"
	"concave/internal/policy"
	"concave/internal/repository"
)

// Explain diagnoses an explicit bounded selection with one analysis and one
// history search per source. No partial result escapes a failed selection.
func Explain(repo *repository.Repo, config *policy.Config, ids []string, rev string) ([]*Explanation, error) {
	if len(ids) == 0 || len(ids) > MaxChanges {
		return nil, fmt.Errorf("select 1..%d exception IDs", MaxChanges)
	}
	ids = slices.Clone(ids)
	slices.Sort(ids)
	bindings := make([]policy.Exception, 0, len(ids))
	paths := make([]string, 0, len(ids))
	for i, id := range ids {
		if i > 0 && ids[i-1] == id {
			return nil, fmt.Errorf("duplicate exception ID %q", id)
		}
		binding := lookup(config, id)
		if binding == nil {
			return nil, fmt.Errorf("unknown exception %q", id)
		}
		bindings = append(bindings, *binding)
		paths = append(paths, binding.Path)
	}
	slices.Sort(paths)
	report, err := analysis.Analyze(repo, config, analysis.Options{Rev: rev, Paths: slices.Compact(paths)})
	if err != nil {
		return nil, err
	}
	snapshot, err := repository.OpenSnapshot(repo, report.Revision)
	if err != nil {
		return nil, err
	}
	if report.Revision == "" && snapshot.Head != report.Head {
		return nil, &repository.Stale{Message: "input changed: HEAD advanced"}
	}
	for _, row := range report.Files {
		snapshot.Inputs[row.Path] = row.Selected
	}
	historyHead := report.Revision
	if historyHead == "" {
		historyHead = report.Head
	}
	explanations := make([]*Explanation, 0, len(bindings))
	var stale []policy.Exception
	for _, binding := range bindings {
		explanation := explainBinding(binding, report)
		explanations = append(explanations, explanation)
		if explanation.Status != "valid" {
			stale = append(stale, binding)
		}
	}
	history := newSourceHistory(repo, historyHead, stale)
	total := 0
	for _, explanation := range explanations {
		binding := explanation.Binding
		if explanation.Status != "valid" {
			var content []byte
			if binding.Fence {
				content, err = snapshot.Read(binding.Path)
				var unavailable *repository.Unavailable
				if err != nil && !errors.As(err, &unavailable) {
					return nil, err
				}
			}
			explanation.PreviousSource, err = history.previous(binding, content)
			if err != nil {
				return nil, err
			}
			total += len(explanation.PreviousSource.Diff)
			if total > repository.MaxOutput {
				return nil, fmt.Errorf("source explanation exceeds size bound")
			}
		}
	}
	if _, err := snapshot.Verify(); err != nil {
		return nil, err
	}
	current, err := policy.Load(repo, report.Revision)
	if err != nil {
		return nil, err
	}
	if !report.PolicyCurrent(config, current) {
		return nil, &repository.Stale{Message: "input changed: " + policy.File}
	}
	return explanations, nil
}

func explainBinding(binding policy.Exception, report *analysis.Report) *Explanation {
	explanation := &Explanation{Format: 1, ID: binding.ID, Status: "stale", Source: report.Revision, Binding: binding, Findings: []policy.Finding{}, BehavioralParity: "not_established"}
	if report.Revision == "" {
		explanation.Source = "working"
	}
	for _, row := range report.Files {
		if row.Path != binding.Path {
			continue
		}
		explanation.FileClassification = row.Classification
		if metadata, ok := row.Explanations[binding.ID]; ok {
			explanation.Explanation = &metadata
		}
		for _, known := range row.Approved {
			if known == binding.ID {
				explanation.Status = "valid"
			}
		}
	}
	for _, finding := range report.Findings {
		if finding.Exception == binding.ID {
			explanation.Findings = append(explanation.Findings, finding)
			explanation.Status = "stale"
			if finding.Kind == "obsolete_exception" {
				explanation.Status = "obsolete"
			}
		}
	}
	return explanation
}
