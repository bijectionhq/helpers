package command

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"concave/internal/coverage"
	"concave/internal/exceptions"
	"concave/internal/output"
	"concave/internal/policy"
	"concave/internal/repository"
)

// registerWitnessCoverage adds `verify witness`: opt-in coverage evidence that
// a fence's test witnesses execute the fenced code.
func (a *app) registerWitnessCoverage(group *cobra.Command) {
	var keep, mutate bool
	var timeout, runTimeout time.Duration
	witness := &cobra.Command{Use: "witness <fence-id>...", Args: cobra.MinimumNArgs(1), Annotations: map[string]string{"timeout": "policy"}, Short: "Run each test witness of the named fences with line coverage, or with --mutate against the fence's original, and record the outcome", RunE: func(cmd *cobra.Command, args []string) error {
		if timeout <= 0 || runTimeout <= 0 {
			return fmt.Errorf("witness deadlines must be positive")
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
		session := policy.NewSession(repo, config)
		requests := []coverage.Request{}
		mutated, anchors := map[string][]byte{}, map[string]string{}
		var upstream map[string]repository.Entry
		for _, id := range args {
			var binding *policy.Exception
			for i := range config.Exceptions {
				if config.Exceptions[i].ID == id {
					binding = &config.Exceptions[i]
				}
			}
			if binding == nil || !binding.Fence {
				return fmt.Errorf("%s is not a recorded fence", id)
			}
			_, source, err := repo.Snapshot(binding.Path)
			if err != nil || source == nil {
				return fmt.Errorf("%s: cannot read %s: %v", id, binding.Path, err)
			}
			annotations, bad := session.Annotations(binding.Path, string(source))
			fence, ok := annotations[id]
			if len(bad) > 0 || !ok {
				return fmt.Errorf("%s: fence is not valid in %s", id, binding.Path)
			}
			if mutate {
				if upstream == nil {
					if upstream, err = repo.Tree(config.Upstream); err != nil {
						return err
					}
				}
				entry, ok := upstream[config.UpstreamPath(binding.Path)]
				if !ok {
					return fmt.Errorf("%s: %s has no upstream file", id, binding.Path)
				}
				before, err := repo.Blob(entry.OID)
				if err != nil {
					return err
				}
				original, err := exceptions.Original(repo, config, before, *binding)
				if err != nil {
					return err
				}
				lines := repository.Lines(string(source))
				anchors[id] = coverage.BindingAnchor(binding.UpstreamSHA256, binding.Start, binding.End)
				mutated[id] = []byte(strings.Join(lines[:fence.Start], "") + original + strings.Join(lines[fence.End:], ""))
			}
			for _, w := range fence.Explanation.Witnesses {
				path, symbol, err := policy.WitnessTarget(binding.Path, w)
				if err != nil {
					return err
				}
				_, witnessSource, err := repo.Snapshot(path)
				if err != nil || witnessSource == nil {
					return fmt.Errorf("%s: cannot read witness %s: %v", id, path, err)
				}
				requests = append(requests, coverage.Request{Fence: id, Path: binding.Path, Source: source, CodeStart: fence.CodeStart, CodeEnd: fence.CodeEnd, Witness: w, WitnessPath: path, Symbol: symbol, WitnessSource: witnessSource})
			}
		}
		// Coverage builds are large: one private scratch per invocation,
		// shared by its witnesses and removed afterwards unless kept.
		scratch, err := os.MkdirTemp("", "concave-coverage-")
		if err != nil {
			return err
		}
		if keep {
			fmt.Fprintln(cmd.ErrOrStderr(), "Keeping coverage scratch:", scratch)
		} else {
			defer os.RemoveAll(scratch)
		}
		receipts := []*coverage.Receipt{}
		failed := false
		for _, request := range requests {
			options := coverage.Options{Root: repo.Root, Scratch: scratch, Timeout: timeout}
			var receipt *coverage.Receipt
			if mutate {
				receipt, err = coverage.Mutate(repo.Context, options, coverage.Mutation{Request: request, Anchor: anchors[request.Fence], Mutated: mutated[request.Fence]}, runTimeout)
			} else {
				receipt, err = coverage.Run(repo.Context, options, request)
			}
			if err != nil {
				return err
			}
			receipts = append(receipts, receipt)
			failed = failed || receipt.Outcome != coverage.Executed && receipt.Outcome != coverage.Witnessed
			if !a.jsonMode {
				if mutate {
					fmt.Fprintf(a.out, "%s %s: %s (%.0f s)\n", request.Fence, request.Witness, receipt.Outcome, receipt.Seconds)
				} else {
					fmt.Fprintf(a.out, "%s %s: %s (%d of %d instrumented fenced lines ran, %.0f s)\n", request.Fence, request.Witness, receipt.Outcome, len(receipt.Lines), receipt.Instrumented, receipt.Seconds)
				}
				if receipt.Problem != "" {
					fmt.Fprintf(a.out, "  %s\n", receipt.Problem)
				}
				fmt.Fprintln(a.out, "  Historical observation; candidate verification establishes current evidence.")
				fmt.Fprintf(a.out, "  receipt %s\n", filepath.Join(coverage.Directory(repo.Root), receipt.Key+".json"))
			}
		}
		if a.jsonMode {
			if err := output.JSON(a.out, map[string]any{"format": 1, "kind": "concave-witness-coverage-run", "receipts": receipts}); err != nil {
				return err
			}
		}
		if failed {
			return &Exit{Code: 1}
		}
		return nil
	}}
	witness.Flags().BoolVar(&keep, "keep-scratch", false, "keep the private coverage build directory")
	witness.Flags().DurationVar(&timeout, "timeout", 30*time.Minute, "deadline for each witness, including its builds")
	witness.Flags().BoolVar(&mutate, "mutate", false, "replace each fence with what it explains away in a private copy and require the witness to pass before and fail after")
	witness.Flags().DurationVar(&runTimeout, "run-timeout", 5*time.Minute, "with --mutate, deadline for each test run; exceeding it is inconclusive")
	group.AddCommand(witness)
}
