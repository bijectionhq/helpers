package workflow

import (
	"concave/internal/policy"
	"concave/internal/review"
)

// Inspect projects saved evidence against its selected source. It writes no
// artifacts, starts no verification, and never probes or installs a toolchain.
func (s *Service) Inspect(r *review.Contribution) (*review.Verdict, error) {
	repo := s.Repo
	if r.Candidate != nil {
		view, err := repo.At(r.Candidate.Commit)
		if err != nil {
			return nil, err
		}
		defer view.Close()
		repo = view
	}
	config, err := policy.Load(repo, "")
	if err != nil {
		return nil, err
	}
	return r.Check(repo, config)
}
