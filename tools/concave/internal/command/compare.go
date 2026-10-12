package command

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"concave/internal/analysis"
	"concave/internal/artifact"
	"concave/internal/output"
)

func (a *app) registerCompare(root *cobra.Command) {
	var parity analysis.ParityOptions
	command := &cobra.Command{Use: "compare (<before.json> <after.json> | --baseline <rev> --candidate <rev>)", Short: "Decide a candidate's parity against its baseline, or describe two saved inspections", Annotations: map[string]string{"timeout": "10m"}, RunE: func(cmd *cobra.Command, args []string) error {
		if parity.Baseline != "" || parity.Candidate != "" || parity.BaselineTool != "" {
			if len(args) != 0 || parity.Baseline == "" || parity.Candidate == "" {
				return fmt.Errorf("--baseline and --candidate take no saved inspections")
			}
			return a.parity(parity)
		}
		if len(args) != 2 {
			return fmt.Errorf("compare requires two saved inspections, or --baseline and --candidate revisions")
		}
		var before, after analysis.Report
		if _, err := artifact.Read(args[0], &before); err != nil {
			return err
		}
		if _, err := artifact.Read(args[1], &after); err != nil {
			return err
		}
		v, err := analysis.Compare(&before, &after)
		if err != nil {
			return err
		}
		if a.jsonMode {
			return output.JSON(a.out, v)
		}
		fmt.Fprintf(a.out, "Inherited changed lines: %d → %d (%+d)\n", v.Before.Added+v.Before.Removed, v.After.Added+v.After.Removed, v.ChangedLinesDelta)
		fmt.Fprintf(a.out, "Comparable scope: %t; basis changes: %v\n", v.Comparable, v.BasisChanges)
		fmt.Fprintf(a.out, "Findings: %d added; %d resolved in the common file scope\n", len(v.AddedFindings), len(v.ResolvedFindings))
		for _, o := range v.Outcomes {
			if o.AnnotationOnly {
				fmt.Fprintf(a.out, "%s: annotation-only source change\n", o.Path)
			}
			fmt.Fprintf(a.out, "%s: %d original lines restored; %d former replacement lines removed; new approvals %v; %d unresolved findings\n", o.Path, o.RestoredOriginalLines, o.RemovedForkLines, o.NewApprovals, o.UnresolvedFindings)
		}
		for _, f := range v.Files {
			old, new := "absent", "absent"
			if f.Before != nil {
				old = f.Before.Classification
			}
			if f.After != nil {
				new = f.After.Classification
			}
			fmt.Fprintf(a.out, "%s: %s → %s\n", f.Path, old, new)
			if f.Before != nil && f.After != nil {
				fmt.Fprintf(a.out, "  changed lines %d → %d; approvals %v → %v\n", f.Before.Added+f.Before.Removed, f.After.Added+f.After.Removed, f.Before.Approved, f.After.Approved)
			}
		}
		if !v.Comparable {
			fmt.Fprintln(a.out, "Different comparison bases: totals are descriptive; no findings classified as resolved.")
		}
		return nil
	}}
	command.Flags().StringVar(&parity.Baseline, "baseline", "", "published revision the candidate builds on")
	command.Flags().StringVar(&parity.Candidate, "candidate", "", "revision to decide")
	command.Flags().StringVar(&parity.BaselineTool, "baseline-tool", "", "Concave built from the baseline's own source, when it differs from this one")
	root.AddCommand(command)
}

// parity prints the one publication verdict. Failure exits 1 after printing it.
func (a *app) parity(options analysis.ParityOptions) error {
	repo, err := a.open()
	if err != nil {
		return err
	}
	defer repo.Close()
	v, err := analysis.CheckParity(repo, options)
	if err != nil {
		return err
	}
	if a.jsonMode {
		if err := output.JSON(a.out, v); err != nil {
			return err
		}
	} else {
		scope := fmt.Sprintf("%d changed path(s)", len(v.Scope.Paths))
		if v.Scope.Repository {
			scope = "the repository (" + strings.Join(v.Scope.Reasons, ", ") + " changed)"
		}
		fmt.Fprintf(a.out, "Parity %s: %s against %s over %s\n", v.Status, short(v.Candidate), short(v.Baseline), scope)
		fmt.Fprintf(a.out, "%d regression(s), %d coverage loss(es); %d finding(s) carried, %d resolved, %d discovered debt still unapproved\n",
			len(v.Regressions), len(v.CoverageLoss), v.Carried, v.Resolved, len(v.DiscoveredDebt))
		for _, f := range v.Regressions {
			location := f.Path
			if f.Line > 0 {
				location = fmt.Sprintf("%s:%d", f.Path, f.Line)
			}
			fmt.Fprintf(a.out, "regression %s: %s: %s\n", location, f.Rule, f.Message)
		}
		for _, loss := range v.CoverageLoss {
			fmt.Fprintf(a.out, "coverage loss %s: %s\n", loss.Path, loss.Reason)
		}
		fmt.Fprintf(a.out, "Next: %s\n", v.Next.Message)
		if len(v.Next.Argv) > 0 {
			fmt.Fprintf(a.out, "  %s\n", strings.Join(v.Next.Argv, " "))
		}
	}
	if v.Status != "passed" {
		return &Exit{Code: 1}
	}
	return nil
}

func short(revision string) string {
	if len(revision) > 12 {
		return revision[:12]
	}
	return revision
}
