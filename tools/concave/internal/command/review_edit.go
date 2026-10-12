package command

import (
	"concave/internal/analysis"
	"concave/internal/policy"
	"concave/internal/repository"
	"concave/internal/review"
	"fmt"
)

func editCapture(repo *repository.Repo, config *policy.Config, ref string, paths []string) (*review.Contribution, error) {
	if ref == "" {
		return nil, nil
	}
	parent, err := review.Read(repo, ref)
	if err != nil {
		return nil, err
	}
	if parent.Candidate != nil {
		return nil, fmt.Errorf("source edits require a capture without a candidate; capture the working scope first")
	}
	if err := parent.Validate(repo, config); err != nil {
		return nil, err
	}
	for _, path := range paths {
		if !repository.Matches(path, parent.Context.Candidate.Scope.Paths) {
			return nil, fmt.Errorf("edit path outside the contribution scope: %s", path)
		}
	}
	return parent, nil
}

func editedCapture(repo *repository.Repo, parent *review.Contribution) (*review.Contribution, error) {
	config, err := policy.Load(repo, "")
	if err != nil {
		return nil, err
	}
	packet, err := analysis.ReviewContracts(repo, config, analysis.Options{Paths: parent.Context.Candidate.Scope.Paths}, parent.Context.Against, nil)
	if err != nil {
		return nil, err
	}
	child, err := review.Compose(repo, config, packet, nil)
	if err != nil {
		return nil, err
	}
	child.Parent = parent.ID
	// Changed hunk identities require selection again, never broaden a previous
	// selection by carrying over a path-level approval.
	return child, nil
}
