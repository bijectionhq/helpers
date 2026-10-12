// Package workflow owns the contribution lifecycle over the existing landing owner.
package workflow

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"concave/internal/artifact"
	"concave/internal/environment"
	"concave/internal/policy"
	"concave/internal/process"
	"concave/internal/repository"
	"concave/internal/review"
	"concave/internal/syntax"
	"concave/internal/verification"
)

// Options names execution choices at the workflow boundary.
type Options struct {
	Output       string
	Execute      bool
	RequireReady bool
}
type Service struct {
	Repo   *repository.Repo
	Stderr io.Writer
}

// The completion pointer is written last. The immutable revision owns all content.
type evaluationResult struct {
	Revision  string `json:"revision"`
	Candidate string `json:"candidate,omitempty"`
}

func ContentArgs(content *review.SelectedContent) []string {
	args := []string{}
	for _, file := range content.Files {
		args = append(args, "--content-base", file.Path+"="+file.Baseline)
		if file.Change == review.DeleteFile {
			args = append(args, "--delete", file.Path)
		} else {
			args = append(args, "--content", file.Path+"="+file.Content.Artifact, "--content-sha256", file.Path+"="+file.Content.SHA256, "--content-mode", file.Path+"="+file.Content.Mode)
		}
	}
	return append(append(args, "--"), content.Paths()...)
}

func Command(repo *repository.Repo, config *policy.Config, checks []string, ref string, options Options) ([]string, error) {
	binary, err := os.Executable()
	if err != nil {
		return nil, err
	}
	engine, err := syntax.New(repo.Context, repo.Root).Executable()
	if err != nil {
		return nil, err
	}
	env := []string{"env", "CONCAVE_AST_GREP=" + engine, "CONCAVE_REVIEW_ROOT=" + repo.Root}
	cache := os.Getenv("CONCAVE_CACHE_ROOT")
	if cache == "" {
		cache = repo.Root
	}
	env = append(env, "CONCAVE_CACHE_ROOT="+cache)
	needsNode := false
	for _, name := range checks {
		check, ok := config.Checks[name]
		if !ok {
			return nil, fmt.Errorf("unknown required check %q", name)
		}
		needsNode = needsNode || check.Environment != ""
		for _, tool := range check.Toolchain {
			needsNode = needsNode || len(tool.Command) > 0 && tool.Command[0] == "node" && tool.VersionFile == ".nvmrc"
		}
	}
	if needsNode {
		node, err := environment.Node(repo)
		if err == nil {
			env = append(env, "CONCAVE_NODE="+node, "PATH="+filepath.Dir(node)+string(os.PathListSeparator)+os.Getenv("PATH"))
		}
	}
	// Reuse the shared warm Cargo cache; land.py alone owns disposable checkouts.
	for _, name := range checks {
		check := config.Checks[name]
		if check.Results == "rust-test-json" {
			target := os.Getenv("CARGO_TARGET_DIR")
			if target == "" {
				target = filepath.Join(repo.Root, "target")
			}
			target, err = filepath.Abs(target)
			if err != nil {
				return nil, err
			}
			env = append(env, "CARGO_TARGET_DIR="+target)
			break
		}
	}
	args := append(env, binary, "--repo", ".", "review", "evaluate", ref)
	if options.Output != "" {
		args = append(args, "--output", options.Output)
	}
	if options.Execute {
		args = append(args, "--execute")
	}
	if options.RequireReady {
		args = append(args, "--require-ready")
	}
	return args, nil
}

func (s *Service) Prepare(config *policy.Config, r *review.Contribution) (*review.Contribution, error) {
	repo := s.Repo
	finish := process.Phase(repo.Context, "candidate", "prepare the selected review through land.py")
	defer finish()
	dir := filepath.Join(repo.Root, "target/concave/candidates", fmt.Sprint(time.Now().UnixNano()))
	exported, err := review.SelectContent(repo, config, r, dir)
	if err != nil {
		return nil, err
	}
	args := append([]string{"python3", "scripts/land.py", "--prepare"}, ContentArgs(exported)...)
	result, err := process.Run(repo.Context, repo.Root, args, nil, repository.MaxOutput)
	if err != nil {
		return nil, fmt.Errorf("prepare candidate: %w", err)
	}
	var prepared struct {
		Format int      `json:"format"`
		Kind   string   `json:"kind"`
		Commit string   `json:"commit"`
		Tree   string   `json:"tree"`
		Parent string   `json:"parent"`
		Paths  []string `json:"paths"`
	}
	if err := artifact.Decode(result.Stdout, &prepared); err != nil {
		return nil, err
	}
	if prepared.Format != 1 || prepared.Kind != "land-candidate" || prepared.Commit == "" || prepared.Tree == "" {
		return nil, fmt.Errorf("invalid land candidate")
	}
	r.Candidate = &review.Candidate{Commit: prepared.Commit, Tree: prepared.Tree, Parent: prepared.Parent, Content: exported}
	r.Verification = nil
	view, err := repo.At(prepared.Commit)
	if err != nil {
		return nil, err
	}
	defer view.Close()
	candidateConfig, err := policy.Load(view, "")
	if err != nil {
		return nil, err
	}
	return composeCandidate(view, candidateConfig, r, r.Parent)
}

// executeCandidate runs one verification through the existing landing owner.
func (s *Service) executeCandidate(r *review.Contribution, destination string) (*review.Contribution, *review.Verdict, error) {
	repo := s.Repo
	if r.Candidate == nil {
		return nil, nil, fmt.Errorf("exact candidate required")
	}
	ref, err := review.Store(repo, r)
	if err != nil {
		return nil, nil, err
	}
	if destination == "" {
		destination = filepath.Join(repo.Root, "target/concave/candidates", fmt.Sprint(time.Now().UnixNano()))
	}
	dir, err := repo.OutputDirectory(destination)
	if err != nil {
		return nil, nil, err
	}
	preparationFailure := func(cause error) (*review.Contribution, *review.Verdict, error) {
		if err := os.WriteFile(filepath.Join(dir, "evaluation.stderr.log"), []byte(cause.Error()+"\n"), 0600); err != nil {
			return nil, nil, err
		}
		return nil, nil, fmt.Errorf("candidate preparation: %w; retained logs: %s", cause, dir)
	}
	view, err := repo.At(r.Candidate.Commit)
	if err != nil {
		return preparationFailure(err)
	}
	defer view.Close()
	config, err := policy.Load(view, "")
	if err != nil {
		return preparationFailure(err)
	}
	args, err := Command(view, config, r.RequiredChecks, ref, Options{Output: dir, Execute: true})
	if err != nil {
		return preparationFailure(err)
	}
	materialization, err := CheckPaths(view, r.RequiredChecks)
	if err != nil {
		return preparationFailure(err)
	}
	ctx, cancel := context.WithTimeout(repo.Context, verification.Budget(config, r.RequiredChecks))
	defer cancel()
	finish := process.Phase(ctx, "checkout", "verify the saved candidate through land.py")
	landing := []string{"python3", "scripts/land.py", "--check-candidate", r.Candidate.Commit, "--check", process.ShellCommand(args)}
	landing = append(landing, materialization...)
	out, err := process.RunStreaming(ctx, repo.Root, landing, nil, repository.MaxOutput, s.Stderr)
	finish()
	// Cancellation may arrive after the process exits. Reading its retained
	// result always gets a bounded context independent of further cancellation.
	finalize, stop := context.WithTimeout(context.WithoutCancel(repo.Context), 3*time.Second)
	defer stop()
	repo = repo.WithContext(finalize)
	defer repo.Close()
	for name, data := range map[string][]byte{"evaluation.stdout.log": out.Stdout, "evaluation.stderr.log": out.Stderr} {
		if writeErr := os.WriteFile(filepath.Join(dir, name), data, 0600); writeErr != nil {
			return nil, nil, writeErr
		}
	}
	if err != nil && (ctx.Err() == nil || !regularFile(filepath.Join(dir, "result.json"))) {
		detail := strings.TrimSpace(string(out.Stderr))
		if len(detail) > 4096 {
			detail = detail[len(detail)-4096:]
		}
		if ctx.Err() != nil {
			// Interrupted before any verdict: nothing was recorded, so the same
			// command starts it again.
			return nil, nil, fmt.Errorf("candidate evaluation interrupted before a verdict: %w\n%s\nRetained logs: %s\nRetry: %s", err, detail, dir,
				process.ShellCommand([]string{"concave", "--repo", repo.Root, "verify", r.Reference()}))
		}
		return nil, nil, fmt.Errorf("candidate evaluation: %w\n%s\nRetained logs: %s", err, detail, dir)
	}
	var result evaluationResult
	if _, err := artifact.Read(filepath.Join(dir, "result.json"), &result); err != nil {
		return nil, nil, err
	}
	if len(result.Revision) != 66 || !review.IsReference(result.Revision) {
		return nil, nil, fmt.Errorf("candidate evaluation returned an invalid revision ID")
	}
	// The immutable file, not result.json, is the content-addressed authority.
	record, err := review.Read(repo, filepath.Join(dir, result.Revision+".json"))
	if err != nil {
		return nil, nil, err
	}
	if record.ID != result.Revision {
		return nil, nil, fmt.Errorf("candidate evaluation revision ID mismatch")
	}
	if result.Candidate != "" {
		if len(result.Candidate) != 66 || !review.IsReference(result.Candidate) || record.Verification != nil && record.Verification.Candidate != result.Candidate {
			return nil, nil, fmt.Errorf("candidate evaluation returned an invalid candidate identity")
		}
		candidate, err := review.Read(repo, filepath.Join(dir, result.Candidate+".json"))
		if err != nil {
			return nil, nil, err
		}
		if _, err := review.Store(repo, candidate); err != nil {
			return nil, nil, err
		}
	}
	var verdict review.Verdict
	if _, err := artifact.Read(filepath.Join(dir, "verdict.json"), &verdict); err != nil {
		return nil, nil, err
	}
	return record, &verdict, nil
}

// EvaluateContents is shared by immutable source inspection and land.py-owned execution.
func (s *Service) EvaluateContents(seed *review.Contribution, options Options) (*review.Contribution, *review.Verdict, error) {
	repo := s.Repo
	if seed == nil || seed.Candidate == nil || seed.Context == nil || seed.Context.Candidate == nil {
		return nil, nil, fmt.Errorf("evaluation requires an exact candidate with source context")
	}
	execute, destination := options.Execute, options.Output
	source, err := repo.At("HEAD")
	if err != nil {
		return nil, nil, err
	}
	defer source.Close()
	config, err := policy.Load(source, "")
	if err != nil {
		return nil, nil, err
	}
	head, err := repo.Resolve("HEAD")
	if err != nil {
		return nil, nil, err
	}
	r := seed
	createdCandidate := ""
	// One session prepares each environment once, for source evaluation and
	// check execution alike.
	session := environment.New(repo, config, destination)
	if head != seed.Candidate.Commit || seed.Context.Candidate.Revision != head {
		// Evaluating a merged tree parses its documents, whose parser this
		// checkout installs only through its environment.
		if config.Documents != nil && config.Documents.Environment != "" {
			if _, err := session.Prepare(config.Documents.Environment); err != nil {
				return nil, nil, err
			}
		}
		r, err = composeCandidate(source, config, seed, seed.ID)
		if err != nil {
			return nil, nil, err
		}
		if _, err := review.Write(repo, r, destination); err != nil {
			return nil, nil, err
		}
		createdCandidate = r.ID
	}
	previous := map[string]review.CheckEvidence{}
	for _, e := range seed.Evidence() {
		if e.Receipt != nil {
			previous[e.Receipt.Check] = e
		}
	}
	initial, err := r.Check(source, config)
	if err != nil {
		return nil, nil, err
	}
	if execute {
		inputs, err := CheckInputs(source, config, r.RequiredChecks)
		if err != nil {
			return nil, nil, err
		}
		if err := inputs.Verify(repo, repository.SourceInputs); err != nil {
			return nil, nil, fmt.Errorf("candidate materialization does not contain required inputs: %w", err)
		}
	}
	execute = execute && initial.Source == review.SourcePassed && len(r.UnmappedWitnesses) == 0
	if execute || seed.Verification != nil {
		ctx, cancel := context.WithTimeout(repo.Context, verification.Budget(config, r.RequiredChecks))
		defer cancel()
		repo = repo.WithContext(ctx)
		defer repo.Close()
		candidateID := r.ID
		if r.Verification != nil {
			candidateID = r.Verification.Candidate
		}
		child := *r
		child.Parent = r.ID
		child.Verification = &review.Verification{Candidate: candidateID}
		r = &child
		for _, name := range r.RequiredChecks {
			old := previous[name]
			decision := verification.ReuseDecision{State: "retained", Reason: "saved source-bound evidence; tool admission is required before execution reuse"}
			if execute {
				decision = verification.Reuse(repo, config, name, old.Directory, old.Receipt)
			}
			evidence := old
			if execute && decision.State != "reusable" {
				directory := filepath.Join(destination, name)
				receipt, err := verification.RunPrepared(repo, config, session, name, directory)
				if err != nil {
					return nil, nil, err
				}
				evidence = review.CheckEvidence{Mode: review.EvidenceExecuted, Reason: decision.Reason, Directory: directory, Receipt: receipt}
			} else if old.Receipt != nil {
				evidence.Mode = review.EvidenceRetained
				if execute {
					evidence.Mode = review.EvidenceReused
					process.Phase(repo.Context, "reuse", name+": current evidence")()
				}
				evidence.Reason = decision.Reason
			}
			if evidence.Receipt != nil {
				r.Verification.Evidence = append(r.Verification.Evidence, evidence)
				if evidence.Receipt.Status != verification.Passed {
					break
				}
			}
		}
	}
	return finalizeEvaluation(repo, source, config, r, destination, createdCandidate)
}

func finalizeEvaluation(repo, source *repository.Repo, config *policy.Config, r *review.Contribution, destination, createdCandidate string) (*review.Contribution, *review.Verdict, error) {
	// Detach before finalization, even if execution has not been cancelled yet:
	// cancellation can arrive during artifact writes or source-only validation.
	// This bounded phase cannot admit another check or prepare an environment.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(repo.Context), 3*time.Second)
	defer cancel()
	repo = repo.WithContext(ctx)
	defer repo.Close()
	source = source.WithContext(ctx)
	defer source.Close()
	finish := process.Phase(ctx, "finalize", "retain contribution and verdict")
	defer finish()
	if _, err := review.Write(repo, r, destination); err != nil {
		return nil, nil, err
	}
	verdict, err := r.Check(source, config)
	if err != nil {
		return nil, nil, err
	}
	verdictBytes, err := json.MarshalIndent(verdict, "", "  ")
	if err != nil {
		return nil, nil, err
	}
	if err := os.WriteFile(filepath.Join(destination, "verdict.json"), verdictBytes, 0600); err != nil {
		return nil, nil, err
	}
	result, err := json.Marshal(evaluationResult{Revision: r.ID, Candidate: createdCandidate})
	if err != nil {
		return nil, nil, err
	}
	if err := os.WriteFile(filepath.Join(destination, "result.json"), result, 0600); err != nil {
		return nil, nil, err
	}
	return r, verdict, nil
}

func regularFile(path string) bool {
	info, err := os.Lstat(path)
	return err == nil && info.Mode().IsRegular()
}
