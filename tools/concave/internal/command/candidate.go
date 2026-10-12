package command

import (
	"concave/internal/continuation"
	"concave/internal/policy"
	"concave/internal/repository"
	"concave/internal/review"
	"concave/internal/workflow"
	"fmt"
	"github.com/spf13/cobra"
	"os"
	"path/filepath"
	"time"
)

func (a *app) workflow(repo *repository.Repo) *workflow.Service {
	return &workflow.Service{Repo: repo, Stderr: a.errout}
}
func (a *app) prepareContribution(repo *repository.Repo, config *policy.Config, contribution *review.Contribution) error {
	child, err := a.workflow(repo).Prepare(config, contribution)
	if err != nil {
		return err
	}
	return a.saveContribution(repo, child)
}

func (a *app) registerCandidateEvaluation(parent *cobra.Command) {
	var destination string
	var execute, ready bool
	cmd := &cobra.Command{Use: "evaluate <artifact>", Args: cobra.ExactArgs(1), Hidden: true, Annotations: map[string]string{"timeout": "policy"}, RunE: func(cmd *cobra.Command, args []string) error {
		repo, err := a.open()
		if err != nil {
			return err
		}
		defer repo.Close()
		seed, err := review.Read(repo, args[0])
		if err != nil {
			return err
		}
		if seed.Candidate == nil || !filepath.IsAbs(args[0]) {
			return fmt.Errorf("candidate evaluation requires an absolute candidate artifact")
		}
		// This operation must run inside land.py scratch, never the mixed checkout.
		if os.Getenv("LAND_CANDIDATE_CHECK") != "1" {
			return fmt.Errorf("candidate evaluation must run through land.py")
		}
		a.actionRoot = os.Getenv("CONCAVE_REVIEW_ROOT")
		if !filepath.IsAbs(a.actionRoot) {
			return fmt.Errorf("candidate evaluation requires its source repository for continuations")
		}
		if destination == "" {
			destination = filepath.Join(filepath.Dir(args[0]), "runs", fmt.Sprint(time.Now().UnixNano()))
			if err := os.MkdirAll(destination, 0700); err != nil {
				return err
			}
		}
		_, verdict, err := a.workflow(repo).EvaluateContents(seed, workflow.Options{Execute: execute, Output: destination})
		if err != nil {
			return err
		}
		if ready && verdict.Ready() {
			// This run is the publisher's gate: publishing is already its caller's
			// step, not a next action for the reader.
			gate := *verdict
			gate.Next = continuation.Action{Kind: continuation.None, Message: "Publication gate passed; the publisher continues."}
			verdict = &gate
		}
		if err := a.printVerdict(verdict); err != nil {
			return err
		}
		if ready && !verdict.Ready() {
			return &Exit{Code: 1}
		}
		return nil
	}}
	cmd.Flags().StringVar(&destination, "output", "", "external retained artifact directory")
	cmd.Flags().BoolVar(&execute, "execute", false, "execute missing or nonreusable witnesses")
	cmd.Flags().BoolVar(&ready, "require-ready", false, "require accepted source and current required witnesses")
	parent.AddCommand(cmd)
}
