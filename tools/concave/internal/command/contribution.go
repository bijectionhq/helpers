package command

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"concave/internal/output"
	"concave/internal/policy"
	"concave/internal/process"
	"concave/internal/repository"
	"concave/internal/review"
)

func (a *app) registerContribution(root *cobra.Command) {
	var against string
	var hunks []string
	var origins []string
	var selectInteractive bool
	c := &cobra.Command{Use: "review <path ... | contribution>", Args: cobra.MinimumNArgs(1), Annotations: map[string]string{"timeout": "policy"}, Short: "Capture source changes as a contribution, select hunks and show its verdict", RunE: func(cmd *cobra.Command, paths []string) error {
		repo, err := a.open()
		if err != nil {
			return err
		}
		defer repo.Close()
		machine, stop := context.WithTimeout(repo.Context, 2*time.Minute)
		defer stop()
		repo = repo.WithContext(machine)
		defer repo.Close()
		var config *policy.Config
		if selectInteractive && (len(hunks) != 0 || a.jsonMode) {
			return fmt.Errorf("--select cannot be combined with --hunk or --json")
		}
		var r *review.Contribution
		reference := len(paths) == 1 && review.IsReference(paths[0])
		if reference {
			if against != "" || len(origins) != 0 {
				return fmt.Errorf("--against and --from apply to source capture")
			}
			r, err = review.Read(repo, paths[0])
		} else {
			config, err = policy.Load(repo, "")
			if err != nil {
				return err
			}
			r, err = a.workflow(repo).Capture(config, paths, against, origins)
			if err == nil {
				_, err = review.Store(repo, r)
			}
		}
		if err != nil {
			return err
		}
		if len(hunks) == 0 && !selectInteractive {
			return a.printContribution(repo, r)
		}
		if r.Candidate != nil {
			return fmt.Errorf("selection is frozen; capture the paths again to change it")
		}
		if config == nil {
			config, err = policy.Load(repo, "")
			if err != nil {
				return err
			}
		}
		if err := r.Validate(repo, config); err != nil {
			return err
		}
		if selectInteractive {
			hunks, err = terminalSelection(a.ctx, r.Hunks)
			if err != nil {
				return err
			}
		}
		// Human reading time has no machine deadline. Revalidate before preparation.
		machine, stop = context.WithTimeout(a.ctx, 2*time.Minute)
		defer stop()
		repo = repo.WithContext(machine)
		defer repo.Close()
		if r.Origin == "" {
			r.Origin = r.ID
		}
		r.Parent, r.Selected = r.ID, hunks
		if err := r.Seal(); err != nil {
			return err
		}
		return a.prepareContribution(repo, config, r)
	}}
	c.Flags().StringVar(&against, "against", "", "baseline commit (default: HEAD); upstream stays pinned")
	c.Flags().StringArrayVar(&hunks, "hunk", nil, "hunk ID from this capture to select (repeatable)")
	c.Flags().BoolVar(&selectInteractive, "select", false, "select files or individual hunks, then confirm the exact selection")
	c.Flags().StringArrayVar(&origins, "from", nil, "attach and validate an applied recovery plan or recorded exception proposal")
	root.AddCommand(c)
	var message string
	export := &cobra.Command{Use: "export <contribution>", Args: cobra.ExactArgs(1), Short: "Print the publication command for a verified contribution without publishing", RunE: func(cmd *cobra.Command, args []string) error {
		repo, err := a.open()
		if err != nil {
			return err
		}
		defer repo.Close()
		candidate, err := review.Read(repo, args[0])
		if err != nil {
			return err
		}
		publication, err := a.workflow(repo).PreparePublication(candidate, message)
		if err != nil {
			return err
		}
		if a.jsonMode {
			return output.JSON(a.out, publication)
		}
		fmt.Fprintln(a.out, "Contribution:", publication.Contribution)
		fmt.Fprintln(a.out, "Publication command:", process.ShellCommand(publication.Command))
		return nil
	}}
	export.Flags().StringVarP(&message, "message", "m", "", "concrete landing message")

	export.MarkFlagRequired("message")
	c.AddCommand(export)
	a.registerCandidateEvaluation(c)
	a.registerPublication(c)
}

func (a *app) saveContribution(repo *repository.Repo, r *review.Contribution) error {
	_, err := review.Store(repo, r)
	if err != nil {
		return err
	}
	return a.printContribution(repo, r)
}

func (a *app) printContribution(repo *repository.Repo, r *review.Contribution) error {
	if a.jsonMode {
		return output.JSON(a.out, r)
	}
	verdict, err := a.workflow(repo).Inspect(r)
	if verdict == nil {
		return err
	}
	if printErr := a.printVerdict(verdict); printErr != nil {
		return printErr
	}
	if verdict.Outcome == review.NoChanges {
		return err
	}
	fmt.Fprintf(a.out, "\n%d changes; %d selected; %d required checks\n", len(r.Hunks), len(r.Selected), len(r.RequiredChecks))
	for index, h := range r.Hunks {
		if index >= 25 {
			fmt.Fprintf(a.out, "  … %d more hunks; concave review %s --json shows every source byte\n", len(r.Hunks)-index, r.ID)
			break
		}
		fmt.Fprintf(a.out, "  %s %s:%d\n", h.ID, h.Path, h.CurrentStart+1)
		lines := strings.Split(string(repository.Patch(h.Path, []repository.Hunk{h})), "\n")
		if len(lines) > 80 {
			lines = append(lines[:80], "… hunk display shortened; --json shows every byte")
		}
		fmt.Fprintln(a.out, strings.Join(lines, "\n"))
	}
	output.Contracts(a.out, r.Context.Candidate.Contracts, 12)
	return err
}

func (a *app) inspectContribution(repo *repository.Repo, r *review.Contribution) error {
	v, err := a.workflow(repo).Inspect(r)
	if err != nil {
		if v != nil {
			if printErr := a.printVerdict(v); printErr != nil {
				return printErr
			}
		}
		return err
	}
	if err := a.printVerdict(v); err != nil {
		return err
	}
	if !v.Passed() {
		return &Exit{Code: 1}
	}
	return nil
}

func (a *app) verifyContribution(ref, dir string) error {
	repo, err := a.open()
	if err != nil {
		return err
	}
	defer repo.Close()
	r, err := review.Read(repo, ref)
	if err != nil {
		return err
	}
	if r.Candidate == nil {
		// A capture without a selection verifies every hunk it shows; review
		// --hunk or --select narrows it first.
		if len(r.Hunks) == 0 {
			return fmt.Errorf("no source changes in this capture; nothing to verify")
		}
		config, err := policy.Load(repo, "")
		if err != nil {
			return err
		}
		if err := r.Validate(repo, config); err != nil {
			return err
		}
		selection := *r
		if selection.Origin == "" {
			selection.Origin = selection.ID
		}
		selection.Parent, selection.Selected = selection.ID, nil
		for _, hunk := range r.Hunks {
			selection.Selected = append(selection.Selected, hunk.ID)
		}
		if err := selection.Seal(); err != nil {
			return err
		}
		if r, err = a.workflow(repo).Prepare(config, &selection); err != nil {
			return err
		}
		if _, err := review.Store(repo, r); err != nil {
			return err
		}
	}
	verdict, err := a.workflow(repo).Verify(r, dir)
	if err != nil {
		return err
	}
	if err := a.printVerdict(verdict); err != nil {
		return err
	}
	if !verdict.Ready() {
		return &Exit{Code: 1}
	}
	return nil
}

func (a *app) printVerdict(v *review.Verdict) error {
	// Rendering binds relative actions to the caller's repository, never a removed
	// candidate checkout. It does not mutate the saved verdict or action.
	copy := *v
	root := a.actionRoot
	if root == "" {
		root = absolute(a.repoPath)
	}
	copy.Next = copy.Next.InRepository(root)

	v = &copy
	if a.jsonMode {
		return output.JSON(a.out, v)
	}
	fmt.Fprintln(a.out, v.Outcome.Headline())
	fmt.Fprintln(a.out, "Contribution:", v.Contribution)
	if v.Outcome == review.NoChanges {
		output.Next(a.out, v.Next)
		return nil
	}
	fmt.Fprintf(a.out, "Source: %s within selected scope\nVerification: %s\n", v.Source, v.Verification)
	for _, located := range v.Locations {
		f := located.Finding
		fmt.Fprintf(a.out, "  %s:%d [%s] %s (%s)\n", f.Path, f.Line, f.Rule, f.Message, located.Scope)
	}
	for _, name := range v.MissingChecks {
		fmt.Fprintln(a.out, "  missing check:", name)
	}
	for _, witness := range v.UnmappedWitnesses {
		fmt.Fprintln(a.out, "  unmapped witness:", witness)
	}
	for _, check := range v.Checks {
		output.CheckResult(a.out, check)
	}

	if v.Witnesses == "none_declared" {
		fmt.Fprintln(a.out, "No behavioral witnesses are declared for this contribution.")
	}
	fmt.Fprintf(a.out, "%d findings outside selection; %d inherited files uninspected.\n", len(v.Outside), v.Uninspected)
	output.Next(a.out, v.Next)
	return nil
}
