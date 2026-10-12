package workflow

import (
	"context"
	"fmt"
	"time"

	"concave/internal/continuation"
	"concave/internal/landing"
	"concave/internal/policy"
	"concave/internal/process"
	"concave/internal/repository"
	"concave/internal/review"
	"concave/internal/verification"
)

type Publication struct {
	landing.PublicationStatus
	Contribution string              `json:"contribution"`
	Next         continuation.Action `json:"next"`
}

// PublicationCommand describes a command over already selected content and
// evidence. Preparing it does not create or advance a contribution revision.
type PublicationCommand struct {
	Format       int                     `json:"format"`
	Kind         string                  `json:"kind"`
	Contribution string                  `json:"contribution"`
	Candidate    string                  `json:"candidate"`
	Artifact     string                  `json:"artifact"`
	Content      *review.SelectedContent `json:"content"`
	Command      []string                `json:"command"`
}

func (s *Service) PreparePublication(candidate *review.Contribution, message string) (*PublicationCommand, error) {
	if candidate.Candidate == nil || candidate.Verification == nil {
		return nil, fmt.Errorf("publication requires a verified candidate; select hunks and run concave verify first")
	}
	if message == "" {
		return nil, fmt.Errorf("publication requires a concrete landing message")
	}
	view, err := s.Repo.At(candidate.Candidate.Commit)
	if err != nil {
		return nil, err
	}
	defer view.Close()
	config, err := policy.Load(view, "")
	if err != nil {
		return nil, err
	}
	verdict, err := candidate.Check(view, config)
	if err != nil {
		return nil, err
	}
	if !verdict.Ready() {
		return nil, fmt.Errorf("candidate is not ready: %s", verdict.Next)
	}
	artifact, err := review.StoredPath(s.Repo, candidate)
	if err != nil {
		return nil, err
	}
	check, err := Command(view, config, candidate.RequiredChecks, artifact, Options{Execute: true, RequireReady: true})
	if err != nil {
		return nil, err
	}
	paths, err := CheckPaths(view, candidate.RequiredChecks)
	if err != nil {
		return nil, err
	}
	command := []string{"python3", "scripts/land.py", "-m", message, "--check", process.ShellCommand(check)}
	command = append(command, paths...)
	command = append(command, ContentArgs(candidate.Candidate.Content)...)
	return &PublicationCommand{Format: 3, Kind: "concave-publication-command", Contribution: candidate.Reference(), Candidate: candidate.Candidate.Commit, Artifact: artifact, Content: candidate.Candidate.Content, Command: command}, nil
}

// publicationID binds the exact selected delta, independently of verification
// revisions and the changing merge parent. land.py owns its durable receipt.
func publicationID(candidate *review.Contribution) (string, error) {
	if candidate.Candidate == nil || candidate.Candidate.Content == nil {
		return "", fmt.Errorf("publication requires selected review bytes")
	}
	content := candidate.Candidate.Content
	if err := content.Validate(); err != nil {
		return "", err
	}
	// This is the publisher's existing identity encoding. Derive it at this
	// boundary so a representation change cannot dispatch the same delta twice.
	baselines, modes, hashes := map[string]string{}, map[string]string{}, map[string]string{}
	for _, file := range content.Files {
		baselines[file.Path] = file.Baseline
		modes[file.Path] = "absent"
		if file.Change == review.WriteFile {
			modes[file.Path], hashes[file.Path] = file.Content.Mode, file.Content.SHA256
		}
	}
	return repository.JSONDigest([]any{content.Paths(), baselines, modes, hashes})
}

func (s *Service) Publication(candidate *review.Contribution) (*Publication, error) {
	id, err := publicationID(candidate)
	if err != nil {
		return nil, err
	}
	status, err := landing.Read(s.Repo, id)
	if err != nil {
		return nil, err
	}
	p := &Publication{PublicationStatus: *status, Contribution: candidate.Reference()}
	switch p.State {
	case landing.Unpublished:
		p.Next = continuation.Action{Kind: continuation.Publish, Message: "Publish the selected review through land.py.", Argv: []string{"concave", "review", "publish", candidate.Reference()}, Requires: []string{"--message <what changed and why>"}}
	case landing.Published:
		p.Next = continuation.Action{Kind: continuation.None, Message: "Published on origin/main; shared checkout unchanged."}
	case landing.Abandoned:
		p.Next = continuation.Action{Kind: continuation.Recapture, Message: "Capture the corrected source and select its contribution."}
	case landing.Pending, landing.Unconfirmed:
		p.Next = continuation.Action{Kind: continuation.Publish, Message: "Resume the frozen publication; land.py reconciles any unconfirmed push first.", Argv: []string{"concave", "review", "publish", candidate.Reference()}}
	case landing.Conflict:
		p.Next = continuation.Action{Kind: continuation.Publish, Message: "Resolve the files in the private conflict directory, then resume this exact contribution.", Argv: []string{"concave", "review", "publish", candidate.Reference(), "--resolve", p.Conflict}}
	}
	return p, nil
}

// Publish delegates capture, conflict recovery, dispatch and reconciliation to
// land.py. The deterministic receipt identity closes the wrapper's crash window.
func (s *Service) Publish(candidate *review.Contribution, message, resolution string) (*Publication, error) {
	p, err := s.Publication(candidate)
	if err != nil {
		return nil, err
	}
	if p.State == landing.Abandoned {
		return p, fmt.Errorf("publication was abandoned; capture corrected source")
	}
	if resolution == "" && p.State == landing.Conflict {
		return p, &landing.Failure{Code: 3, Err: fmt.Errorf("resolve the private conflict first: %s", p.Conflict)}
	}
	if resolution != "" && p.State != landing.Conflict {
		return p, fmt.Errorf("resolution requires a conflicted publication")
	}
	if p.State == landing.Published {
		return p, nil
	}
	command := p.PublicationStatus.Resume(resolution)
	if p.State == landing.Unpublished {
		prepared, err := s.PreparePublication(candidate, message)
		if err != nil {
			return p, err
		}
		id, err := publicationID(candidate)
		if err != nil {
			return p, err
		}
		command = append([]string{"python3", "scripts/land.py", "--publication-id", id}, prepared.Command[2:]...)
	}
	// A retry executes only the saved publisher request, never recaptures files.
	view, err := s.Repo.At(candidate.Candidate.Commit)
	if err != nil {
		return p, err
	}
	config, err := policy.Load(view, "")
	view.Close()
	if err != nil {
		return p, err
	}
	ctx, cancel := context.WithTimeout(s.Repo.Context, verification.Budget(config, candidate.RequiredChecks))
	defer cancel()
	out, runErr := process.RunOutput(ctx, s.Repo.Root, command, nil, repository.MaxOutput, s.Stderr, s.Stderr)
	// Cancellation cannot discard the publisher's durable outcome. This bounded
	// final read cannot dispatch, reconcile or start another check.
	finalContext, finish := context.WithTimeout(context.WithoutCancel(s.Repo.Context), 3*time.Second)
	defer finish()
	finalRepo := s.Repo.WithContext(finalContext)
	defer finalRepo.Close()
	p, err = (&Service{Repo: finalRepo, Stderr: s.Stderr}).Publication(candidate)
	if err != nil {
		return nil, err
	}
	if runErr != nil {
		if p.State == landing.Unpublished {
			// The publisher refused before taking the frozen bytes, for a reason
			// its message names, such as another capture owning these paths.
			p.Next.Message = "The publisher refused before publishing; address its message above, then publish again."
		}
		code := out.ExitCode
		if code < 1 || code > 255 {
			code = 2
		}
		return p, &landing.Failure{Code: code, Err: fmt.Errorf("publication %s: %w; receipt: %s", p.State, runErr, p.Receipt)}
	}
	if p.State != landing.Published {
		return p, fmt.Errorf("publisher returned without a confirmed receipt: %s", p.Receipt)
	}
	return p, nil
}
