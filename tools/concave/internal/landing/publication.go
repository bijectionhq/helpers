// Package landing consumes the publisher's versioned protocol. Only land.py
// owns publication state, private conflict files, dispatch and reconciliation.
package landing

import (
	"fmt"

	"concave/internal/artifact"
	"concave/internal/process"
	"concave/internal/repository"
)

type State string

const (
	Unpublished State = "unpublished"
	Pending     State = "pending"
	Unconfirmed State = "unconfirmed"
	Published   State = "published"
	Abandoned   State = "abandoned"
	Conflict    State = "conflict"
)

type PublicationStatus struct {
	Format   int    `json:"format"`
	Kind     string `json:"kind"`
	ID       string `json:"id"`
	State    State  `json:"state"`
	Receipt  string `json:"receipt"`
	Commit   string `json:"commit,omitempty"`
	Conflict string `json:"conflict,omitempty"`
}

func Read(repo *repository.Repo, id string) (*PublicationStatus, error) {
	result, err := process.Run(repo.Context, repo.Root, []string{"python3", "scripts/land.py", "--publication-status", id}, nil, 64<<10)
	if err != nil {
		return nil, fmt.Errorf("publication status: %w", err)
	}
	var status PublicationStatus
	if err := artifact.Decode(result.Stdout, &status); err != nil {
		return nil, err
	}
	if err := status.validate(id); err != nil {
		return nil, err
	}
	return &status, nil
}

func (s PublicationStatus) validate(id string) error {
	if s.Format != 1 || s.Kind != "land-publication" || s.ID != id || s.Receipt == "" {
		return fmt.Errorf("invalid publisher status envelope")
	}
	switch s.State {
	case Unpublished, Pending, Unconfirmed, Abandoned, Published, Conflict:
	default:
		return fmt.Errorf("unknown publisher state %q", s.State)
	}
	if (s.State == Published) != (s.Commit != "") || (s.State == Conflict) != (s.Conflict != "") {
		return fmt.Errorf("inconsistent publisher state %q", s.State)
	}
	return nil
}

// Resume delegates validation of the resolution and its identity to its owner.
func (s PublicationStatus) Resume(resolution string) []string {
	target := s.Receipt
	if resolution != "" {
		target = resolution
	}
	return []string{"python3", "scripts/land.py", "--continue", target, "--publication-id", s.ID}
}

type Failure struct {
	Code int
	Err  error
}

func (e *Failure) Error() string { return e.Err.Error() }
func (e *Failure) Unwrap() error { return e.Err }
func (e *Failure) ExitCode() int { return e.Code }

func (s PublicationStatus) Headline() string {
	switch s.State {
	case Pending:
		return "Resume publication"
	case Unconfirmed:
		return "Publication unconfirmed"
	case Published:
		return "Published"
	case Conflict:
		return "Resolve conflict"
	case Abandoned:
		return "Publication abandoned"
	default:
		return "Not published"
	}
}
