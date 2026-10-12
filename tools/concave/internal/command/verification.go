package command

import (
	"context"
	"fmt"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	"concave/internal/artifact"
	"concave/internal/output"
	"concave/internal/policy"
	"concave/internal/review"
	"concave/internal/verification"
)

func (a *app) registerVerification(root *cobra.Command) {
	var candidateOutput string
	group := &cobra.Command{Use: "verify <contribution>", Args: cobra.ExactArgs(1), Annotations: map[string]string{"timeout": "policy"}, Short: "Run a contribution's required checks on its candidate in a disposable checkout", RunE: func(cmd *cobra.Command, args []string) error {
		return a.verifyContribution(args[0], candidateOutput)
	}}
	group.Flags().StringVar(&candidateOutput, "output", "", "new ignored or external directory for logs and receipts")
	group.AddCommand(&cobra.Command{Use: "list", Args: cobra.NoArgs, Short: "List the named checks declared in concave.json", RunE: func(cmd *cobra.Command, args []string) error {
		repo, err := a.open()
		if err != nil {
			return err
		}
		defer repo.Close()
		config, err := policy.Load(repo, "")
		if err != nil {
			return err
		}
		if a.jsonMode {
			return output.JSON(a.out, config.Checks)
		}
		for _, n := range verification.Names(config) {
			v := config.Checks[n]
			fmt.Fprintf(a.out, "%s (%s, %ds) %v\n", n, v.Directory, v.TimeoutSeconds, v.Command)
		}
		return nil
	}})
	var dir string
	run := &cobra.Command{Use: "run <check>", Args: cobra.ExactArgs(1), Annotations: map[string]string{"timeout": "policy"}, Short: "Run one named check in the working tree and save its receipt", RunE: func(cmd *cobra.Command, args []string) error {
		repo, err := a.open()
		if err != nil {
			return err
		}
		defer repo.Close()
		config, err := policy.Load(repo, "")
		if err != nil {
			return err
		}
		ctx, cancel := context.WithTimeout(repo.Context, verification.Budget(config, args))
		defer cancel()
		repo = repo.WithContext(ctx)
		defer repo.Close()
		if dir == "" {
			dir = filepath.Join(repo.Root, "target/concave/checks", fmt.Sprint(time.Now().UnixNano()))
		}
		receipt, err := verification.Run(repo, config, args[0], dir)
		if err != nil {
			return err
		}
		if a.jsonMode {
			err = output.JSON(a.out, receipt)
		} else {
			output.CheckResult(a.out, review.CheckOutcome{Name: receipt.Check, Status: receipt.Status, Results: receipt.Results, ExecutionStarted: receipt.ExecutionStarted, Problems: receipt.Problems, Directory: dir})
			fmt.Fprintln(a.out, "Receipt:", filepath.Join(dir, "receipt.json"))

		}
		if err != nil {
			return err
		}
		if receipt.Status == verification.Stale {
			return &Exit{Code: 3}
		}
		if receipt.Status != verification.Passed {
			return &Exit{Code: 1}
		}
		return nil
	}}
	run.Flags().StringVar(&dir, "output", "", "new ignored or external directory for logs and the receipt")
	group.AddCommand(run)
	a.registerWitnessCoverage(group)
	group.AddCommand(&cobra.Command{Use: "check <receipt.json>", Args: cobra.ExactArgs(1), Short: "Validate a saved receipt and its declared inputs without rerunning the check", RunE: func(cmd *cobra.Command, args []string) error {
		var receipt verification.Receipt
		if _, err := artifact.Read(args[0], &receipt); err != nil {
			return err
		}
		repo, err := a.open()
		if err != nil {
			return err
		}
		defer repo.Close()
		config, err := policy.Load(repo, "")
		if err != nil {
			return err
		}
		if err = verification.ValidateRecorded(repo, config, &receipt); err != nil {
			return &Exit{Code: 3, Err: err}
		}
		if err = verification.ValidateLogs(filepath.Dir(absolute(args[0])), &receipt); err != nil {
			return &Exit{Code: 3, Err: err}
		}
		return output.JSON(a.out, map[string]any{"status": "current", "check": receipt.Check, "evidence_sha256": receipt.EvidenceSHA256})
	}})
	root.AddCommand(group)
}
