package workflow

import (
	"strings"

	"concave/internal/analysis"
	"concave/internal/policy"
	"concave/internal/process"
	"concave/internal/repository"
	"concave/internal/review"
)

// composeCandidate creates a source revision only when selected content is first
// applied, or its integration tree changes. Verification owns separate children.
func composeCandidate(source *repository.Repo, config *policy.Config, seed *review.Contribution, parent string) (*review.Contribution, error) {
	head, err := source.Resolve("HEAD")
	if err != nil {
		return nil, err
	}
	baseline := seed.Candidate.Parent
	if head != seed.Candidate.Commit {
		baseline, err = source.Resolve("HEAD^")
		if err != nil {
			return nil, err
		}
	}
	finish := process.Phase(source.Context, "source", "evaluate the exact candidate's contracts")
	packet, err := analysis.ReviewContracts(source, config, analysis.Options{Paths: seed.Candidate.Content.Paths()}, baseline, nil)
	finish()
	if err != nil {
		return nil, err
	}
	candidate, err := review.Compose(source, config, packet, nil)
	if err != nil {
		return nil, err
	}
	candidate.Context.Candidate.Revision = head
	candidate.Parent, candidate.Origin = parent, seed.Origin
	for _, hunk := range candidate.Hunks {
		candidate.Selected = append(candidate.Selected, hunk.ID)
	}
	tree, err := source.Git(nil, "rev-parse", head+"^{tree}")
	if err != nil {
		return nil, err
	}
	candidate.Candidate = &review.Candidate{Commit: head, Tree: strings.TrimSpace(string(tree)), Parent: baseline, Content: seed.Candidate.Content}
	if err := candidate.Seal(); err != nil {
		return nil, err
	}
	return candidate, nil
}
