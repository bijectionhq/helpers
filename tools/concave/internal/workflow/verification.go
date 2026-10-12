package workflow

import (
	"context"
	"time"

	"concave/internal/review"
)

// Verify retains the result even when execution is interrupted. Finalization has
// its own bounded context and cannot start another check.
func (s *Service) Verify(candidate *review.Contribution, directory string) (*review.Verdict, error) {
	child, verdict, err := s.executeCandidate(candidate, directory)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(s.Repo.Context), 3*time.Second)
	defer cancel()
	repo := s.Repo.WithContext(ctx)
	defer repo.Close()
	artifact, err := review.Store(repo, child)
	if err != nil {
		return nil, err
	}
	verdict.Artifact = artifact
	return verdict, nil
}
