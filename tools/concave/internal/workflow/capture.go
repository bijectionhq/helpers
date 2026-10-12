package workflow

import (
	"encoding/json"
	"fmt"

	"concave/internal/analysis"
	"concave/internal/artifact"
	"concave/internal/exceptions"
	"concave/internal/policy"
	"concave/internal/process"
	"concave/internal/recovery"
	"concave/internal/repository"
	"concave/internal/review"
)

// Capture freezes changes and validates the provenance attached to this scope.
func (s *Service) Capture(config *policy.Config, paths []string, against string, origins []string) (*review.Contribution, error) {
	repo := s.Repo
	finish := process.Phase(repo.Context, "source", "capture changes and contracts")
	packet, err := analysis.ReviewContracts(repo, config, analysis.Options{Paths: paths}, against, nil)
	finish()
	if err != nil {
		return nil, err
	}
	r, err := review.Compose(repo, config, packet, nil)
	if err != nil {
		return nil, err
	}
	for _, path := range origins {
		var header map[string]json.RawMessage
		raw, err := artifact.Read(path, &header)
		if err != nil {
			return nil, err
		}
		var kind string
		if err := json.Unmarshal(header["kind"], &kind); err != nil {
			return nil, err
		}
		switch kind {
		case "concave-recovery-plan":
			var p recovery.Plan
			if err := artifact.Decode(raw, &p); err != nil {
				return nil, err
			}
			if err := recovery.ValidateApplied(repo, config, &p); err != nil {
				return nil, err
			}
			for path := range p.Files {
				if !repository.Matches(path, packet.Candidate.Scope.Paths) {
					return nil, fmt.Errorf("recovery path is outside review scope: %s", path)
				}
			}
			r.Recoveries = append(r.Recoveries, p)
		case "concave-exception-proposal":
			var p exceptions.Proposal
			if err := artifact.Decode(raw, &p); err != nil {
				return nil, err
			}
			if err := exceptions.ValidateRecorded(repo, config, &p); err != nil {
				return nil, err
			}
			for _, change := range p.Changes {
				bindings := append([]policy.Exception{}, change.Previous...)
				if change.Next != nil {
					bindings = append(bindings, *change.Next)
				}
				for _, binding := range bindings {
					if !repository.Matches(binding.Path, packet.Candidate.Scope.Paths) {
						return nil, fmt.Errorf("approval path is outside review scope: %s", binding.Path)
					}
				}
			}
			r.Approvals = append(r.Approvals, p)
		default:
			return nil, fmt.Errorf("--from needs a recovery plan or exception proposal")
		}
	}
	return r, nil
}
